package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/storage"
)

var watchFixtureOrdinal atomic.Int64

func watchCommandFixture(t *testing.T) (watchdaemon.Config, string) {
	t.Helper()
	noPoliciesFallbackDir(t)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	id := fmtWatchID(watchFixtureOrdinal.Add(1))
	endpoint := "http://127.0.0.1:8990"
	body, _ := json.Marshal(map[string]any{"id": id, "access": "private", "created": map[string]string{"at": "2026-10-23T10:00:00Z"}, "coordination": map[string]string{"endpoint": endpoint, "namespace": "northwind"}})
	if err := os.WriteFile(filepath.Join(project, storage.ProjectSummaryFileName), body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WATCH_COMMAND_USER", strings.Repeat("user-token", 8))
	t.Setenv("WATCH_COMMAND_RUNNER", strings.Repeat("runner-token", 8))
	t.Setenv("WATCH_COMMAND_DB", filepath.Join(root, "state.sqlite"))
	cfg := watchdaemon.Config{Namespace: "northwind", Listen: "127.0.0.1:0", Credentials: []watchdaemon.Credential{{TokenEnv: "WATCH_COMMAND_USER", Kind: "user", ID: "alice", Namespace: "northwind", Project: id, Environment: "training", Tier: "paid", Email: "alice@example.test"}, {TokenEnv: "WATCH_COMMAND_RUNNER", Kind: "runner", ID: "runner", Namespace: "northwind", Project: id, Environment: "training"}}}
	cfg.Storage.DALGOConnectionEnv = "WATCH_COMMAND_DB"
	cfg.Runner.TokenEnv = "WATCH_COMMAND_RUNNER"
	cfg.Runner.CoordinatorEndpoint = endpoint
	cfg.Runner.ProjectDir = project
	cfg.Runner.Environment = "training"
	cfg.Runner.Principal = "local-owner"
	cfg.Runner.EvidenceDir = filepath.Join(root, "evidence")
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() {
		storage.NewDatatugStore = previousStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
		_ = api.CloseExecutionEvidence()
	})
	logger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(logger) })
	return cfg, id
}
func fmtWatchID(n int64) string { return "watch-command-" + strconv.FormatInt(n, 10) }
func invokeWatchCommand(t *testing.T, ctx context.Context, cfg watchdaemon.Config, args ...string) error {
	t.Helper()
	file := filepath.Join(t.TempDir(), "daemon.json")
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, body, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := daemonCommand()
	cmd.SetContext(ctx)
	cmd.SetErr(io.Discard)
	cmd.SetOut(io.Discard)
	if err = cmd.Flags().Parse(append([]string{"--watch-config", file}, args...)); err != nil {
		t.Fatal(err)
	}
	return watchDaemonAction(cmd, nil)
}

func TestWatchDaemonRejectsUnsafeDeploymentConfiguration(t *testing.T) {
	for _, scenario := range []string{"limits", "namespace", "namespace-flag", "credential", "credential-namespace", "storage-missing", "storage-invalid", "twilio-missing", "listen-invalid", "listen-public", "listen-failed"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, _ := watchCommandFixture(t)
			args := []string{"--coordinate"}
			switch scenario {
			case "limits":
				cfg.Limits.SMSPerDay = 101
			case "namespace":
				cfg.Namespace = ""
			case "namespace-flag":
				args = append(args, "--namespace", "other")
			case "credential":
				cfg.Credentials = nil
			case "credential-namespace":
				cfg.Credentials[0].Namespace = "other"
			case "storage-missing":
				cfg.Storage.DALGOConnectionEnv = "WATCH_COMMAND_MISSING"
			case "storage-invalid":
				t.Setenv("WATCH_COMMAND_DB", "postgres://secret@db/private")
			case "twilio-missing":
				cfg.Notifications.Twilio = true
				t.Setenv("TWILIO_ACCOUNT_SID", "")
			case "listen-invalid":
				cfg.Listen = "bad-address"
			case "listen-public":
				cfg.Listen = "0.0.0.0:8990"
			case "listen-failed":
				original := watchListen
				defer func() { watchListen = original }()
				watchListen = func(string, string) (net.Listener, error) { return nil, errors.New("bind unavailable") }
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := invokeWatchCommand(t, ctx, cfg, args...); err == nil {
				t.Fatal("unsafe startup accepted", scenario)
			}
		})
	}
}

func TestWatchDaemonDefaultConfigurationAndHelp(t *testing.T) {
	cmd := daemonCommand()
	cmd.SetContext(context.Background())
	cmd.SetOut(io.Discard)
	if err := watchDaemonAction(cmd, nil); err != nil {
		t.Fatal(err)
	}
	original := watchUserHomeDir
	defer func() { watchUserHomeDir = original }()
	watchUserHomeDir = func() (string, error) { return "", errors.New("home unavailable") }
	cmd = daemonCommand()
	cmd.SetContext(context.Background())
	_ = cmd.Flags().Set("coordinate", "true")
	if watchDaemonAction(cmd, nil) == nil {
		t.Fatal("missing home accepted")
	}
	home := t.TempDir()
	watchUserHomeDir = func() (string, error) { return home, nil }
	if err := os.Mkdir(filepath.Join(home, ".datatug"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".datatug", "watch-daemon.json"), []byte(`{"namespace":"northwind"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if watchDaemonAction(cmd, nil) == nil {
		t.Fatal("empty credential startup accepted")
	}
	_ = cmd.Flags().Set("watch-config", filepath.Join(home, "missing"))
	if watchDaemonAction(cmd, nil) == nil {
		t.Fatal("missing config accepted")
	}
}

func TestWatchDaemonRunnerAdmissionAndScope(t *testing.T) {
	for _, scenario := range []string{"missing-project", "untrusted", "namespace", "principal", "configure", "token", "resolver-invalid"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, _ := watchCommandFixture(t)
			args := []string{"--run"}
			switch scenario {
			case "missing-project":
				cfg.Runner.ProjectDir = ""
			case "untrusted":
				cfg.Runner.CoordinatorEndpoint = "https://other.example"
			case "namespace":
				args = append(args, "--namespace", "other")
			case "principal":
				cfg.Runner.Principal = ""
			case "configure":
				original := watchConfigureExecution
				defer func() { watchConfigureExecution = original }()
				watchConfigureExecution = func(watchdaemon.Config, string) (func(), error) { return nil, errors.New("source unavailable") }
			case "token":
				cfg.Runner.TokenEnv = "MISSING_WATCH_TOKEN"
			case "resolver-invalid":
				original := watchResolveCoordinator
				defer func() { watchResolveCoordinator = original }()
				watchResolveCoordinator = func(string, string, string) (string, error) { return "http://public.example", nil }
			}
			if err := invokeWatchCommand(t, context.Background(), cfg, args...); err == nil {
				t.Fatal("unsafe runner started")
			}
		})
	}
	cfg, id := watchCommandFixture(t)
	oldRunner, oldExecute := watchRunRunner, watchExecuteCheck
	defer func() { watchRunRunner, watchExecuteCheck = oldRunner, oldExecute }()
	watchExecuteCheck = func(context.Context, string, string, string, string, string, string, map[string]detective.InputValue) (detective.CheckRun, string, error) {
		return detective.CheckRun{ID: "receipt", ExecutionStatus: "completed", Outcome: "pass"}, "fingerprint", nil
	}
	watchRunRunner = func(ctx context.Context, r *watchdaemon.Runner) error {
		claim := watch.Claim{RunID: "run", Fence: 1, Definition: watch.Definition{Scope: watch.Scope{Namespace: "northwind", Project: id, Environment: "training"}, CheckID: "check", CheckVersion: "1"}}
		result, err := r.Executor.Execute(ctx, claim)
		if err != nil || result.Outcome != "pass" {
			t.Error("Check result mapping", result, err)
		}
		claim.Definition.Environment = "prod"
		if _, err = r.Executor.Execute(ctx, claim); !errors.Is(err, watch.ErrForbidden) {
			t.Error("foreign environment reached source")
		}
		claim.Definition.Environment = "training"
		watchExecuteCheck = func(context.Context, string, string, string, string, string, string, map[string]detective.InputValue) (detective.CheckRun, string, error) {
			return detective.CheckRun{}, "changed", detective.ErrConflict
		}
		result, err = r.Executor.Execute(ctx, claim)
		if err != nil || result.Outcome != "unknown" || result.FailureCode != "SOURCE_CHANGED" {
			t.Error("changed source reset baseline", result, err)
		}
		return context.Canceled
	}
	if err := invokeWatchCommand(t, context.Background(), cfg, "--run", "--project", cfg.Runner.ProjectDir, "--env", "training"); err != nil {
		t.Fatal(err)
	}
}

func TestWatchDaemonCoordinatorProvidersAndShutdown(t *testing.T) {
	for _, provider := range []string{"sink", "smtp", "twilio", "tls-failure", "shutdown-failure", "default-listen"} {
		t.Run(provider, func(t *testing.T) {
			cfg, _ := watchCommandFixture(t)
			switch provider {
			case "sink":
				cfg.Notifications.EmailSink = filepath.Join(t.TempDir(), "email")
				cfg.Notifications.SMSSink = filepath.Join(t.TempDir(), "sms")
			case "smtp":
				cfg.Notifications.SMTP = &watchdaemon.SMTPConfig{Address: "127.0.0.1:1", From: "watch@example.test"}
			case "twilio":
				cfg.Notifications.Twilio = true
				t.Setenv("TWILIO_ACCOUNT_SID", "fake")
				t.Setenv("TWILIO_AUTH_TOKEN", "fake")
				t.Setenv("TWILIO_FROM_NUMBER", "+353123456789")
				cfg.Credentials[0].AccountID = "paid-account"
			case "tls-failure":
				cfg.TLSCertFile = "missing.pem"
				cfg.TLSKeyFile = "missing-key.pem"
			case "default-listen":
				cfg.Listen = ""
			}
			originalListen := watchListen
			defer func() { watchListen = originalListen }()
			ready := make(chan string, 1)
			watchListen = func(network, address string) (net.Listener, error) {
				l, err := net.Listen(network, "127.0.0.1:0")
				if err == nil {
					ready <- l.Addr().String()
				}
				return l, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- invokeWatchCommand(t, ctx, cfg, "--coordinate") }()
			addr := <-ready
			if provider == "tls-failure" {
				if err := <-done; err == nil {
					t.Fatal("invalid TLS certificate accepted")
				}
				return
			}
			deadline := time.Now().Add(time.Second)
			for {
				response, err := http.Get("http://" + addr + "/health")
				if err == nil {
					response.Body.Close()
					if response.StatusCode == 200 {
						break
					}
				}
				if time.Now().After(deadline) {
					t.Fatal("coordinator did not become ready")
				}
				time.Sleep(time.Millisecond)
			}
			if provider == "shutdown-failure" {
				old := watchShutdownHTTP
				defer func() { watchShutdownHTTP = old }()
				watchShutdownHTTP = func(s *http.Server, c context.Context) error { _ = old(s, c); return errors.New("shutdown timeout") }
			}
			cancel()
			err := <-done
			if provider == "shutdown-failure" {
				if err == nil {
					t.Fatal("failed shutdown hidden")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWatchDaemonWorkerShutdownTimeoutIsVisible(t *testing.T) {
	cfg, _ := watchCommandFixture(t)
	oldRunner, oldTimeout := watchRunRunner, watchWorkerShutdownTimeout
	defer func() { watchRunRunner, watchWorkerShutdownTimeout = oldRunner, oldTimeout }()
	watchWorkerShutdownTimeout = time.Millisecond
	started, release := make(chan struct{}), make(chan struct{})
	watchRunRunner = func(context.Context, *watchdaemon.Runner) error { close(started); <-release; return nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- invokeWatchCommand(t, ctx, cfg, "--run") }()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("stuck worker shutdown hidden")
	}
	close(release)
}
