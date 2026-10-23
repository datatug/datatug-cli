package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/sneat-co/sneat-go-core/twiliosms"
	"github.com/spf13/cobra"
)

func addWatchDaemonFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.Bool("coordinate", false, "Run the DataTug Watch Coordinator in the foreground")
	f.Bool("run", false, "Run the DataTug Watch Runner in the foreground (outbound HTTP only)")
	f.String("watch-config", "", "Deployment-owned Watch JSON configuration file")
	f.String("coordinator", "", "Explicit Coordinator endpoint override (otherwise project manifest coordination.endpoint)")
	f.String("namespace", "", "Require this namespace to match the project manifest and deployment")
	f.String("project", "", "Runner project directory override")
	f.String("env", "", "Runner environment override")
	cmd.RunE = watchDaemonAction
}

func watchDaemonAction(cmd *cobra.Command, _ []string) error {
	coordinate, _ := cmd.Flags().GetBool("coordinate")
	run, _ := cmd.Flags().GetBool("run")
	if !coordinate && !run {
		return cmd.Help()
	}
	configPath, _ := cmd.Flags().GetString("watch-config")
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return errors.New("Watch daemon configuration location unavailable")
		}
		configPath = filepath.Join(home, ".datatug", "watch-daemon.json")
	}
	cfg, err := watchdaemon.LoadConfig(configPath)
	if err != nil {
		return err
	}
	cfg.Limits, err = cfg.Limits.Resolve()
	if err != nil {
		return err
	}
	projectDir, _ := cmd.Flags().GetString("project")
	if projectDir != "" {
		cfg.Runner.ProjectDir = projectDir
	}
	environment, _ := cmd.Flags().GetString("env")
	if environment != "" {
		cfg.Runner.Environment = environment
	}
	endpoint, _ := cmd.Flags().GetString("coordinator")
	namespace, _ := cmd.Flags().GetString("namespace")
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	slog.SetDefault(slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), nil)))
	var runner *watchdaemon.Runner
	if run {
		projectID, manifestEndpoint, manifestNamespace, err := watchProjectCoordination(cfg.Runner.ProjectDir)
		if err != nil {
			return err
		}
		endpoint, err = resolveWatchCoordinator(manifestEndpoint, cfg.Runner.CoordinatorEndpoint, endpoint)
		if err != nil {
			return err
		}
		if namespace != "" && namespace != manifestNamespace || cfg.Namespace != "" && cfg.Namespace != manifestNamespace {
			return errors.New("Watch namespace differs from project manifest")
		}
		cfg.Namespace = manifestNamespace
		if watchdaemon.ValidateEndpoint(endpoint) != nil {
			return errors.New("Watch Coordinator endpoint is invalid")
		}
		if cfg.Runner.Environment == "" || cfg.Runner.Principal == "" {
			return errors.New("Watch runner requires an explicit environment and local principal")
		}
		cleanup, err := configureWatchExecution(cfg, projectID)
		if err != nil {
			return err
		}
		defer cleanup()
		token := os.Getenv(cfg.Runner.TokenEnv)
		if len(token) < 32 {
			return errors.New("Watch runner token is unavailable")
		}
		runner = &watchdaemon.Runner{Client: watchdaemon.Client{Endpoint: endpoint, Token: token}, Executor: watchdaemon.ExecutorFunc(func(ctx context.Context, claim watch.Claim) (watch.Completion, error) {
			d := claim.Definition
			if d.Namespace != cfg.Namespace || d.Project != projectID || d.Environment != cfg.Runner.Environment {
				return watch.Completion{}, watch.ErrForbidden
			}
			start := time.Now()
			// Attempt/fence is part of the local operation ID: an expired attempt
			// never reuses an uncertain prior source execution journal.
			op := "watch-" + claim.RunID + fmt.Sprintf("-%d", claim.Fence)
			run, fingerprint, err := endpoints.ExecuteWatchCheck(ctx, d.Project, d.Environment, d.CheckID, d.CheckVersion, d.SourceFingerprint, op, d.Inputs)
			completion := watch.Completion{ExecutionStatus: run.ExecutionStatus, Outcome: run.Outcome, Severity: run.Severity, DurationMS: time.Since(start).Milliseconds(), CheckRunRef: run.ID, SourceFingerprint: fingerprint}
			if errors.Is(err, detective.ErrConflict) {
				completion.ExecutionStatus = "error"
				completion.Outcome = "unknown"
				completion.FailureCode = "SOURCE_CHANGED"
				return completion, nil
			}
			return completion, err
		})}
	}
	if namespace != "" && namespace != cfg.Namespace {
		return errors.New("Watch deployment namespace mismatch")
	}
	var store *watch.Store
	var httpServer *http.Server
	errCh := make(chan error, 3)
	var workers sync.WaitGroup
	startWorker := func(fn func() error) { workers.Add(1); go func() { defer workers.Done(); errCh <- fn() }() }
	if coordinate {
		if cfg.Namespace == "" {
			return errors.New("Watch coordinator namespace is required")
		}
		auth, err := watchdaemon.NewAuthenticator(cfg.Credentials)
		if err != nil {
			return err
		}
		for _, c := range cfg.Credentials {
			if c.Namespace != cfg.Namespace {
				return errors.New("Watch credential namespace differs from coordinator namespace")
			}
		}
		dsn := os.Getenv(cfg.Storage.DALGOConnectionEnv)
		if dsn == "" {
			return errors.New("Watch DALGO storage connection environment variable is unavailable")
		}
		paid := map[string]bool{}
		for _, c := range cfg.Credentials {
			if c.Kind == "user" && c.Tier == "paid" && c.AccountID != "" {
				paid[c.AccountID] = true
			} else if c.Kind == "user" && c.Tier == "paid" {
				paid[c.ID] = true
			}
		}
		store, err = watch.NewSQLiteStore(dsn, watch.Config{Namespace: cfg.Namespace, IsPaidAccount: func(account string) bool { return paid[account] }, EmailDailyLimit: cfg.Limits.EmailPerDay, EmailMinuteLimit: cfg.Limits.EmailPerMinute, SMSDailyLimit: cfg.Limits.SMSPerDay, SMSMinuteLimit: cfg.Limits.SMSPerMinute, TelegramDailyLimit: cfg.Limits.TelegramPerDay, TelegramMinuteLimit: cfg.Limits.TelegramPerMinute, MaxWatchesPerUser: cfg.Limits.WatchesPerUser, MinIntervalSeconds: cfg.Limits.MinIntervalSeconds})
		if err != nil {
			return errors.New("Watch transactional coordinator storage could not be opened")
		}
		defer store.Close()
		service := &watchdaemon.Server{Store: store, Auth: auth, AllowedOrigins: cfg.AllowedOrigins, PublicUIURL: cfg.PublicUIURL, ProjectUIURLs: cfg.ProjectUIURLs, Limits: cfg.Limits, TelegramRouteID: cfg.Notifications.TelegramRouteID, TelegramCallbackSecret: os.Getenv(cfg.Notifications.TelegramCallbackSecretEnv)}
		if cfg.Notifications.EmailSink != "" {
			service.Email = &watchdaemon.FileEmailSink{Path: cfg.Notifications.EmailSink}
		} else if cfg.Notifications.SMTP != nil {
			service.Email = watchdaemon.SMTPSender{Config: *cfg.Notifications.SMTP}
		}
		if cfg.Notifications.SMSSink != "" {
			service.SMS = watchdaemon.FileSMSSink{Sink: &watchdaemon.FileEmailSink{Path: cfg.Notifications.SMSSink}}
		} else if cfg.Notifications.Twilio {
			credentials, ok := twiliosms.CredentialsFromEnv(os.Getenv)
			if !ok {
				return errors.New("Watch Twilio deployment credentials unavailable")
			}
			service.SMS = watchdaemon.TwilioSMSSender{Credentials: credentials}
		}
		if cfg.Notifications.TelegramSink != "" {
			service.Telegram = watchdaemon.FileTelegramSink{Sink: &watchdaemon.FileEmailSink{Path: cfg.Notifications.TelegramSink}}
		} else if cfg.Notifications.TelegramGatewayURL != "" {
			if watchdaemon.ValidateEndpoint(cfg.Notifications.TelegramGatewayURL) != nil {
				return errors.New("Watch Telegram gateway URL invalid")
			}
			service.Telegram = watchdaemon.GatewayTelegramSender{Endpoint: cfg.Notifications.TelegramGatewayURL, TokenEnv: cfg.Notifications.TelegramGatewayTokenEnv}
		} else if cfg.Notifications.TelegramTokenEnv != "" {
			service.Telegram = watchdaemon.BotTelegramSender{TokenEnv: cfg.Notifications.TelegramTokenEnv}
		}
		listen := cfg.Listen
		if listen == "" {
			listen = "127.0.0.1:8990"
		}
		host, _, err := net.SplitHostPort(listen)
		if err != nil {
			return errors.New("Watch listen address is invalid")
		}
		ip := net.ParseIP(host)
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
			if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return errors.New("Watch non-loopback listener requires TLS certificate and key")
			}
		}
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			return errors.New("Watch Coordinator listener is unavailable")
		}
		httpServer = &http.Server{Handler: service, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
		startWorker(func() error {
			if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
				return httpServer.ServeTLS(listener, cfg.TLSCertFile, cfg.TLSKeyFile)
			} else {
				return httpServer.Serve(listener)
			}
		})
		startWorker(func() error { return service.RunCoordinator(ctx, time.Second) })
		slog.Info("watch_coordinator_started", "namespace", cfg.Namespace, "roles", map[string]bool{"coordinate": true, "run": run})
	}
	if runner != nil {
		startWorker(func() error { return runner.Run(ctx) })
		slog.Info("watch_runner_started", "namespace", cfg.Namespace)
	}
	select {
	case <-ctx.Done():
		err = nil
	case err = <-errCh:
		if errors.Is(err, context.Canceled) || errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		cancel()
	}
	if httpServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if shutdownErr := httpServer.Shutdown(shutdownCtx); shutdownErr != nil && err == nil {
			err = errors.New("Watch Coordinator shutdown timed out")
		}
	}
	finished := make(chan struct{})
	go func() { workers.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		if err == nil {
			err = errors.New("Watch daemon worker shutdown timed out")
		}
	}
	slog.Info("watch_daemon_stopped", "namespace", cfg.Namespace)
	return err
}

func resolveWatchCoordinator(manifestEndpoint, trustedEndpoint, explicitOverride string) (string, error) {
	if explicitOverride != "" {
		if watchdaemon.ValidateEndpoint(explicitOverride) != nil {
			return "", errors.New("Watch Coordinator override is invalid")
		}
		return explicitOverride, nil
	}
	if watchdaemon.ValidateEndpoint(trustedEndpoint) != nil || strings.TrimRight(manifestEndpoint, "/") != strings.TrimRight(trustedEndpoint, "/") {
		return "", errors.New("Watch manifest Coordinator differs from the deployment trusted endpoint; configure runner.coordinatorEndpoint or explicitly pass --coordinator")
	}
	return manifestEndpoint, nil
}

func watchProjectCoordination(projectDir string) (id, endpoint, namespace string, err error) {
	if projectDir == "" {
		return "", "", "", errors.New("Watch runner project directory is required")
	}
	project, err := filestore.LoadProjectFile(projectDir)
	if err != nil {
		return "", "", "", errors.New("Watch runner project is unavailable")
	}
	body, err := os.ReadFile(filepath.Join(projectDir, storage.ProjectSummaryFileName))
	if err != nil || len(body) > 1024*1024 {
		return "", "", "", errors.New("Watch project manifest unavailable")
	}
	var manifest struct {
		Coordination struct{ Endpoint, Namespace string } `json:"coordination"`
	}
	if json.Unmarshal(body, &manifest) != nil || manifest.Coordination.Namespace == "" || watchdaemon.ValidateEndpoint(manifest.Coordination.Endpoint) != nil {
		return "", "", "", errors.New("Watch project requires coordination endpoint and namespace")
	}
	return project.ID, manifest.Coordination.Endpoint, manifest.Coordination.Namespace, nil
}

func configureWatchExecution(cfg watchdaemon.Config, projectID string) (func(), error) {
	session, err := resolveServeSession(cfg.Runner.ProjectDir, serveFlags{as: cfg.Runner.Principal})
	if err != nil {
		return nil, err
	}
	// Detective deliberately has no secured project-asset grant yet. Preserve
	// its existing fail-closed boundary instead of impersonating a local owner.
	if !session.Unrestricted {
		return nil, errors.New("Watch Check assets require explicit local owner mode; secured project asset grants are unavailable")
	}
	paths := map[string]string{projectID: cfg.Runner.ProjectDir}
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	filestore.SetProjectPath(projectID, cfg.Runner.ProjectDir)
	evidence := cfg.Runner.EvidenceDir
	if evidence == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, errors.New("Watch evidence location unavailable")
		}
		evidence = filepath.Join(home, ".datatug", "watch-evidence")
	}
	api.ConfigureSecureSession(session, paths, api.Capabilities{EvidencePrivateDir: evidence, AllowOpaqueSQL: cfg.Runner.AllowOpaqueSQL})
	if err = api.ConfigureExecutionEvidence(paths, nil, executionstore.Options{PrivateDir: evidence}); err != nil {
		return nil, errors.New("Watch execution evidence unavailable")
	}
	return func() { _ = api.CloseExecutionEvidence() }, nil
}
