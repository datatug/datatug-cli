package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/executionstore"
	"github.com/datatug/datatug-cli/pkg/incidentstore"
	"github.com/datatug/datatug-cli/pkg/privateadmission"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/incidents"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// This public envelope is deliberately independent of CheckRun: the latter
// includes private definitions, query/evidence references and input names.
type checksRunSummary struct {
	SchemaVersion  string            `json:"schemaVersion"`
	Status         string            `json:"status"`
	ExitCode       int               `json:"exitCode"`
	Checks         []checksRunResult `json:"checks"`
	DeepLink       string            `json:"deepLink"`
	DeepLinkReason string            `json:"deepLinkReason"`
}
type checksRunResult struct {
	Index           int    `json:"index"`
	Status          string `json:"status"`
	ExecutionStatus string `json:"executionStatus"`
	Outcome         string `json:"outcome"`
	Severity        string `json:"severity"`
	DurationMS      int64  `json:"durationMs"`
}
type checksRunOptions struct {
	project, environment, evidence, warnings, format string
	checks                                           []string
	owner                                            bool
	timeout                                          time.Duration
}

func checksRunCommand() *cobra.Command {
	o := &checksRunOptions{}
	cmd := &cobra.Command{Use: "run", Short: "Run trusted local Checks once and emit a safe JSON summary", SilenceUsage: true, SilenceErrors: true}
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) != 0 {
			return writeChecksRunSummary(cmd, newChecksRunSummary("configuration_error", 2))
		}
		return nil
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		return writeChecksRunSummary(cmd, newChecksRunSummary("configuration_error", 2))
	})
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return runChecksCommand(cmd, *o) }
	f := cmd.Flags()
	f.StringVarP(&o.project, "project", "p", "", "Trusted local project directory")
	f.StringVar(&o.environment, "env", "", "Explicit environment")
	f.StringArrayVar(&o.checks, "check", nil, "Check ID (repeatable, maximum 100)")
	f.BoolVar(&o.owner, "local-owner", false, "Explicitly consent to unrestricted local project-owner execution (no secured policies)")
	f.StringVar(&o.evidence, "evidence-dir", "", "Existing private absolute directory outside the project (0700)")
	f.DurationVar(&o.timeout, "timeout", 30*time.Second, "Deadline for the entire invocation (maximum 1 hour)")
	f.StringVar(&o.warnings, "warnings", "fail", "Business warning handling: fail or ignore")
	f.StringVar(&o.format, "format", "json", "Summary format (json)")
	return cmd
}
func newChecksRunSummary(status string, code int) checksRunSummary {
	return checksRunSummary{SchemaVersion: "datatug.checks.run/v1", Status: status, ExitCode: code, Checks: []checksRunResult{}, DeepLink: "https://datatug.app", DeepLinkReason: "hosted_result_reference_unsupported"}
}
func writeChecksRunSummary(cmd *cobra.Command, summary checksRunSummary) error {
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(summary); err != nil {
		return Exit("checks run: summary output unavailable", 4)
	}
	if summary.ExitCode != 0 {
		return Exit("checks run: "+summary.Status, summary.ExitCode)
	}
	return nil
}

func runChecksCommand(cmd *cobra.Command, o checksRunOptions) error {
	// Library diagnostics can contain database names or driver errors. This
	// process-local command emits only its allowlisted summary, including stderr.
	previousLog, previousSlog, previousFlags := log.Writer(), slog.Default(), log.Flags()
	log.SetOutput(io.Discard)
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() {
		slog.SetDefault(previousSlog)
		log.SetOutput(previousLog)
		log.SetFlags(previousFlags)
	}()
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	summary := newChecksRunSummary("pass", 0)
	finish := func(status string, code int) error {
		summary.Status = status
		summary.ExitCode = code
		return writeChecksRunSummary(cmd, summary)
	}
	if !o.owner {
		return finish("access_denied", 5)
	}
	if o.project == "" || o.environment == "" || api.ValidateIdentifier("environment", o.environment) != nil || len(o.checks) == 0 || len(o.checks) > 100 || o.format != "json" || (o.warnings != "fail" && o.warnings != "ignore") || o.timeout <= 0 || o.timeout > time.Hour || !filepath.IsAbs(o.evidence) {
		return finish("configuration_error", 2)
	}
	for _, id := range o.checks {
		if api.ValidateIdentifier("check", id) != nil {
			return finish("configuration_error", 2)
		}
	}
	project, err := filestore.LoadProjectFile(o.project)
	if err != nil {
		return finish("configuration_error", 2)
	}
	if api.ValidateProjectIdentifier("project", project.ID) != nil {
		return finish("configuration_error", 2)
	}
	// resolveServeSession may use project, environment or home policy files.
	// Presence of any secured profile must fail before local execution setup.
	session, err := resolveServeSession(o.project, serveFlags{as: "local-owner"})
	if err != nil {
		return finish("configuration_error", 2)
	}
	if !session.Unrestricted {
		return finish("access_denied", 5)
	}
	info, err := os.Lstat(o.evidence)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return finish("configuration_error", 2)
	}
	// Existing LocalStore verifies physical path disjointness, including aliases.
	store, err := detective.NewLocalStoreContext(ctx, project.ID, o.project, o.evidence)
	if err != nil {
		if ctx.Err() != nil {
			status, code := checksRunFailure(ctx, err)
			return finish(status, code)
		}
		return finish("configuration_error", 2)
	}
	if ctx.Err() != nil {
		status, code := checksRunFailure(ctx, ctx.Err())
		return finish(status, code)
	}
	env, err := filestore.NewProjectStore(project.ID, o.project).LoadEnvironment(ctx, o.environment)
	if err != nil || env == nil {
		if ctx.Err() != nil {
			status, code := checksRunFailure(ctx, err)
			return finish(status, code)
		}
		return finish("configuration_error", 2)
	}
	// Preflight all requested definitions before dispatching any source work.
	versions := make([]string, len(o.checks))
	for i, id := range o.checks {
		check, e := store.GetCheck(ctx, id)
		if e != nil {
			status, code := checksRunFailure(ctx, e)
			if code == 4 {
				status, code = "configuration_error", 2
			}
			return finish(status, code)
		}
		if check.ID != id || check.Version < 1 || len(check.Inputs) > 0 {
			return finish("configuration_error", 2)
		}
		versions[i] = strconv.FormatInt(check.Version, 10)
	}
	previousStore := storage.NewDatatugStore
	defer func() {
		storage.NewDatatugStore = previousStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	}()
	cfg := watchdaemon.Config{}
	cfg.Runner.ProjectDir = o.project
	cfg.Runner.Environment = o.environment
	cfg.Runner.Principal = "local-owner"
	cfg.Runner.EvidenceDir = o.evidence
	// Consent above is independent of no-policies fallback or principal name.
	cleanup, err := configureWatchExecution(cfg, project.ID)
	if err != nil {
		_ = api.CloseExecutionEvidence()
		return finish("configuration_error", 2)
	}
	defer cleanup()
	// Watch defaults place immutable execution metadata in the project. Local
	// automation must also route that metadata privately so a read-only project
	// remains unchanged; Record:true and the existing receipt engine are retained.
	repository, err := configurePrivateExecutionEvidence(project.ID, o.project, o.evidence)
	if err != nil {
		return finish("configuration_error", 2)
	}
	for i, id := range o.checks {
		if ctx.Err() != nil {
			status, code := checksRunFailure(ctx, ctx.Err())
			return finish(status, code)
		}
		start := time.Now()
		run, _, runErr := endpoints.ExecuteWatchCheck(ctx, project.ID, o.environment, id, versions[i], "", "checks-"+uuid.NewString(), nil)
		// Repository records normally use Git-friendly modes. The inaccessible
		// private ancestor protects publication; tighten all completed metadata
		// before exposing an outcome, including failed/cancelled runs.
		if privacyErr := securePrivateExecutionTree(repository); privacyErr != nil {
			runErr = privacyErr
		}
		status, code := classifyChecksRun(ctx, run, runErr, o.warnings)
		result := checksRunResult{Index: i + 1, Status: status, ExecutionStatus: "error", Outcome: "unknown", DurationMS: time.Since(start).Milliseconds()}
		if runErr == nil {
			result.ExecutionStatus = checksEnum(run.ExecutionStatus, "completed", "error", "skipped", "pending", "running")
			result.Outcome = checksEnum(run.Outcome, "pass", "fail", "unknown")
			result.Severity = checksEnum(run.Severity, "critical", "error", "warning", "info")
		}
		summary.Checks = append(summary.Checks, result)
		// Execute all requested Checks for business outcomes; stop on an uncertain
		// source result, configuration failure, cancellation or timeout. No retries.
		if code > 1 {
			return finish(status, code)
		}
		if code > summary.ExitCode || checksStatusPriority(status) > checksStatusPriority(summary.Status) {
			summary.Status, summary.ExitCode = status, code
		} else if status == "warning" && summary.Status == "pass" {
			summary.Status = status
		}
	}
	return writeChecksRunSummary(cmd, summary)
}
func checksEnum(value string, allowed ...string) string {
	for _, item := range allowed {
		if value == item {
			return value
		}
	}
	return ""
}
func checksRunFailure(ctx context.Context, err error) (string, int) {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return "timeout", 124
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return "cancelled", 130
	case errors.Is(err, detective.ErrNotFound):
		return "not_found", 3
	case errors.Is(err, detective.ErrInvalid), errors.Is(err, detective.ErrConflict):
		return "configuration_error", 2
	default:
		return "error", 4
	}
}
func classifyChecksRun(ctx context.Context, run detective.CheckRun, err error, warnings string) (string, int) {
	if err != nil || ctx.Err() != nil {
		return checksRunFailure(ctx, err)
	}
	if run.ExecutionStatus != "completed" {
		return "error", 4
	}
	if run.Outcome == "unknown" {
		return "unknown", 1
	}
	if run.Outcome == "pass" {
		return "pass", 0
	}
	if run.Outcome == "fail" {
		if run.Severity == "warning" || run.Severity == "info" {
			if warnings == "ignore" {
				return "warning", 0
			}
			return "warning", 1
		}
		return "fail", 1
	}
	return "error", 4
}

// Equal exit codes must not let an earlier ignored warning hide later unknown
// or business-false outcomes. Hard failures stop execution before this reducer.
func checksStatusPriority(status string) int {
	switch status {
	case "unknown":
		return 4
	case "fail":
		return 3
	case "warning":
		return 2
	case "pass":
		return 1
	default:
		return 5
	}
}

// The physical checkout hash prevents different projects sharing an evidence
// base from sharing receipts or snapshots, even when their logical IDs match.
func configurePrivateExecutionEvidence(projectID, projectDir, privateDir string) (string, error) {
	repository, snapshots, err := privateExecutionEvidencePaths(projectDir, privateDir)
	if err != nil {
		return "", err
	}
	scope := filepath.Dir(repository)
	container := filepath.Dir(scope)
	for _, dir := range []string{container, scope, repository, snapshots} {
		if err = os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, e := os.Lstat(dir)
		if e != nil {
			return "", e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("unsafe private execution directory")
		}
	}
	route := incidentstore.ConfiguredStore{StoreID: projectID, Kind: incidents.StoreLocationProjectRepository, Project: &incidents.ProjectRef{StoreID: projectID, ProjectID: projectID}, Repository: repository}
	err = api.ConfigureExecutionEvidence(map[string]string{projectID: projectDir}, []incidentstore.ConfiguredStore{route}, executionstore.Options{PrivateDir: snapshots})
	return repository, err
}

func securePrivateExecutionTree(repository string) (result error) {
	root, err := os.OpenRoot(repository)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, root.Close()) }()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := root.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("unsafe private execution entry")
		}
		mode := os.FileMode(0600)
		if info.IsDir() {
			mode = 0700
		}
		return root.Chmod(path, mode)
	})
}

func privateExecutionEvidencePaths(projectDir, privateDir string) (repository, snapshots string, err error) {
	physical, err := privateadmission.PhysicalDirectory(projectDir)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256([]byte(physical))
	scope := filepath.Join(privateDir, "check-executions", hex.EncodeToString(digest[:16]))
	return filepath.Join(scope, "repository"), filepath.Join(scope, "snapshots"), nil
}
