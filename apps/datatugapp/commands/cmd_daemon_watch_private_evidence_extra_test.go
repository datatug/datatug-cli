package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
)

func TestWatchPrivateRunnerFailsClosedOnUnsafeExecutionEvidence(t *testing.T) {
	cfg, projectID := watchCommandFixture(t)
	home := t.TempDir()
	cfg.Runner.EvidenceDir = ""
	cfg.Runner.PrivateExecutionEvidence = true

	oldHome, oldRunner, oldExecute := watchUserHomeDir, watchRunRunner, watchExecuteCheck
	t.Cleanup(func() {
		watchUserHomeDir = oldHome
		watchRunRunner = oldRunner
		watchExecuteCheck = oldExecute
		_ = api.CloseExecutionEvidence()
	})
	watchUserHomeDir = func() (string, error) { return home, nil }
	var executeArgs []string
	watchExecuteCheck = func(_ context.Context, project, environment, checkID, checkVersion, fingerprint, operationID string, _ map[string]detective.InputValue) (detective.CheckRun, string, error) {
		executeArgs = []string{project, environment, checkID, checkVersion, fingerprint, operationID}
		return detective.CheckRun{ID: "run", ExecutionStatus: "completed", Outcome: "pass", Severity: "info"}, "source-fingerprint", nil
	}
	var completion watch.Completion
	var executorErr error
	var runnerSetupErr error
	watchRunRunner = func(ctx context.Context, runner *watchdaemon.Runner) error {
		evidenceDir := filepath.Join(home, ".datatug", "watch-evidence")
		repository, _, err := privateExecutionEvidencePaths(cfg.Runner.ProjectDir, evidenceDir)
		if err != nil {
			runnerSetupErr = err
			return err
		}
		if err = os.Symlink(cfg.Runner.ProjectDir, filepath.Join(repository, "redirect")); err != nil {
			runnerSetupErr = err
			return err
		}
		completion, executorErr = runner.Executor.Execute(ctx, watch.Claim{
			RunID: "run", Fence: 7,
			Definition: watch.Definition{
				Scope:   watch.Scope{Namespace: "northwind", Project: projectID, Environment: "training"},
				CheckID: "pass", CheckVersion: "1", SourceFingerprint: "source-fingerprint",
			},
		})
		return context.Canceled
	}
	if err := invokeWatchCommand(t, context.Background(), cfg, "--run"); err != nil {
		t.Fatalf("private runner command failed before executor result: %v", err)
	}
	if runnerSetupErr != nil {
		t.Fatalf("prepare private runner evidence: %v", runnerSetupErr)
	}
	wantArgs := []string{projectID, "training", "pass", "1", "source-fingerprint", "watch-run-7"}
	if strings.Join(executeArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("private runner execution args = %q; want %q", executeArgs, wantArgs)
	}
	if executorErr == nil || executorErr.Error() != "watch private execution evidence unavailable" || strings.Contains(executorErr.Error(), cfg.Runner.ProjectDir) {
		t.Fatalf("unsafe evidence execution error = %v; want constant private-evidence denial", executorErr)
	}
	if completion.Outcome != "" {
		t.Fatalf("unsafe evidence returned completion %+v; want no published outcome", completion)
	}
}

func TestWatchPrivateEvidenceConfigurationRejectsSymlinkedRoutes(t *testing.T) {
	t.Run("evidence-base-symlink", func(t *testing.T) {
		cfg, projectID := watchCommandFixture(t)
		cfg.Runner.PrivateExecutionEvidence = true
		outside := t.TempDir()
		if err := os.Symlink(outside, cfg.Runner.EvidenceDir); err != nil {
			t.Fatal(err)
		}
		if _, err := configureWatchExecution(cfg, projectID); err == nil || !strings.Contains(err.Error(), "watch private execution evidence unavailable") {
			t.Fatalf("symlinked evidence base error = %v; want private-evidence rejection", err)
		}
	})

	t.Run("repository-route-symlink", func(t *testing.T) {
		cfg, projectID := watchCommandFixture(t)
		cfg.Runner.PrivateExecutionEvidence = true
		if err := os.Mkdir(cfg.Runner.EvidenceDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(cfg.Runner.ProjectDir, filepath.Join(cfg.Runner.EvidenceDir, "check-executions")); err != nil {
			t.Fatal(err)
		}
		if _, err := configureWatchExecution(cfg, projectID); err == nil || !strings.Contains(err.Error(), "watch private execution evidence unavailable") {
			t.Fatalf("symlinked repository route error = %v; want private-evidence rejection", err)
		}
	})
}

func TestWatchPrivateEvidenceHomeFailureStopsRunnerStartup(t *testing.T) {
	cfg, _ := watchCommandFixture(t)
	cfg.Runner.EvidenceDir = ""
	cfg.Runner.PrivateExecutionEvidence = true
	previous := watchUserHomeDir
	t.Cleanup(func() { watchUserHomeDir = previous })
	watchUserHomeDir = func() (string, error) { return "", errors.New("private home detail") }
	err := invokeWatchCommand(t, context.Background(), cfg, "--run")
	if err == nil || err.Error() != "watch evidence location unavailable" || strings.Contains(err.Error(), "private home detail") {
		t.Fatalf("runner with unavailable private evidence home = %v; want constant location error", err)
	}
}

func TestWatchPrivateEvidencePathFailureStopsRunnerStartup(t *testing.T) {
	cfg, _ := watchCommandFixture(t)
	cfg.Runner.PrivateExecutionEvidence = true
	previous := watchPrivateEvidencePaths
	t.Cleanup(func() { watchPrivateEvidencePaths = previous })
	watchPrivateEvidencePaths = func(string, string) (string, string, error) {
		return "", "", errors.New("private physical path detail")
	}
	if err := invokeWatchCommand(t, context.Background(), cfg, "--run"); err == nil || err.Error() != "watch private execution evidence unavailable" {
		t.Fatalf("private path failure = %v; want sanitized startup refusal", err)
	}
}

func TestWatchPrivateEvidenceMkdirFailureStopsConfiguration(t *testing.T) {
	cfg, projectID := watchCommandFixture(t)
	cfg.Runner.PrivateExecutionEvidence = true
	previous := watchPrivateMkdirAll
	t.Cleanup(func() { watchPrivateMkdirAll = previous; _ = api.CloseExecutionEvidence() })
	watchPrivateMkdirAll = func(string, os.FileMode) error { return errors.New("private mkdir detail") }
	if _, err := configureWatchExecution(cfg, projectID); err == nil || err.Error() != "watch private execution evidence unavailable" {
		t.Fatalf("private mkdir failure = %v; want sanitized startup refusal", err)
	}
}
