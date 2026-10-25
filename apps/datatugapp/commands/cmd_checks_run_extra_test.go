package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/spf13/cobra"
)

type checksSummaryOutputFailure struct{}

func (checksSummaryOutputFailure) Write([]byte) (int, error) {
	return 0, errors.New("private output detail")
}

func runChecksWithOptions(t *testing.T, ctx context.Context, options checksRunOptions) (checksRunSummary, error) {
	t.Helper()
	cmd := checksRunCommand()
	var output, diagnostics strings.Builder
	cmd.SetOut(&output)
	cmd.SetErr(&diagnostics)
	cmd.SetContext(ctx)
	err := runChecksCommand(cmd, options)
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected command diagnostics: %q", diagnostics.String())
	}
	var summary checksRunSummary
	if decodeErr := json.Unmarshal([]byte(output.String()), &summary); decodeErr != nil {
		t.Fatalf("invalid checks summary %q: %v", output.String(), decodeErr)
	}
	return summary, err
}

func TestChecksRunCommandDeniesWithoutExplicitOwnerConsent(t *testing.T) {
	summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{})
	var exit ExitCoder
	if !errors.As(err, &exit) || exit.ExitCode() != 5 || summary.Status != "access_denied" || summary.ExitCode != 5 {
		t.Fatalf("ownerless checks run = %+v, %v; want access denied with exit 5", summary, err)
	}
}

func TestChecksRunSummaryOutputFailureIsSanitized(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(checksSummaryOutputFailure{})
	err := writeChecksRunSummary(cmd, newChecksRunSummary("pass", 0))
	var exit ExitCoder
	if !errors.As(err, &exit) || exit.ExitCode() != 4 || strings.Contains(err.Error(), "private output detail") {
		t.Fatalf("summary output failure = %v; want sanitized exit 4", err)
	}
}

func TestChecksRunCommandRejectsMissingProjectEnvironmentAndEvidence(t *testing.T) {
	t.Run("missing-project", func(t *testing.T) {
		private := t.TempDir()
		missing := filepath.Join(t.TempDir(), "missing-project")
		summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
			project: missing, environment: "local", evidence: private, checks: []string{"pass"}, owner: true,
			timeout: time.Second, warnings: "fail", format: "json",
		})
		var exit ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || summary.Status != "configuration_error" {
			t.Fatalf("missing project = %+v, %v; want configuration error", summary, err)
		}
		if strings.Contains(err.Error(), missing) {
			t.Fatalf("missing project path leaked in error: %v", err)
		}
	})

	t.Run("missing-evidence-directory", func(t *testing.T) {
		project, _ := checksDemo(t)
		missingEvidence := filepath.Join(t.TempDir(), "missing-evidence")
		summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
			project: project, environment: "local", evidence: missingEvidence, checks: []string{"pass"}, owner: true,
			timeout: time.Second, warnings: "fail", format: "json",
		})
		var exit ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || summary.Status != "configuration_error" {
			t.Fatalf("missing evidence directory = %+v, %v; want configuration error", summary, err)
		}
	})

	t.Run("missing-environment", func(t *testing.T) {
		project, private := checksDemo(t)
		summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
			project: project, environment: "absent", evidence: private, checks: []string{"pass"}, owner: true,
			timeout: time.Second, warnings: "fail", format: "json",
		})
		var exit ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || summary.Status != "configuration_error" {
			t.Fatalf("missing environment = %+v, %v; want configuration error", summary, err)
		}
	})
}

func TestChecksRunRejectsInvalidProjectAndPolicyDocuments(t *testing.T) {
	t.Run("invalid-project-id", func(t *testing.T) {
		project, private := checksDemo(t)
		manifestPath := filepath.Join(project, "datatug-project.json")
		body, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		if err = json.Unmarshal(body, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest["id"] = "invalid/project"
		body, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(manifestPath, body, 0600); err != nil {
			t.Fatal(err)
		}
		summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
			project: project, environment: "local", evidence: private, checks: []string{"pass"}, owner: true,
			timeout: time.Second, warnings: "fail", format: "json",
		})
		var exit ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || summary.Status != "configuration_error" {
			t.Fatalf("invalid project id = %+v, %v; want configuration error", summary, err)
		}
	})

	t.Run("malformed-policy", func(t *testing.T) {
		project, private := checksDemo(t)
		policyDir := filepath.Join(project, "policies")
		if err := os.Mkdir(policyDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(policyDir, "policy.yaml"), []byte("apiVersion: ["), 0600); err != nil {
			t.Fatal(err)
		}
		summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
			project: project, environment: "local", evidence: private, checks: []string{"pass"}, owner: true,
			timeout: time.Second, warnings: "fail", format: "json",
		})
		var exit ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || summary.Status != "configuration_error" {
			t.Fatalf("malformed policy = %+v, %v; want configuration error", summary, err)
		}
	})

	t.Run("permissive-evidence-mode", func(t *testing.T) {
		project, private := checksDemo(t)
		if err := os.Chmod(private, 0755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(private, 0700); err != nil {
				t.Error(err)
			}
		})
		summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
			project: project, environment: "local", evidence: private, checks: []string{"pass"}, owner: true,
			timeout: time.Second, warnings: "fail", format: "json",
		})
		var exit ExitCoder
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || summary.Status != "configuration_error" {
			t.Fatalf("permissive evidence mode = %+v, %v; want configuration error", summary, err)
		}
	})
}

func TestChecksRunPreflightsCheckDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch func(string) string
	}{
		{"mismatched-id", func(body string) string { return strings.Replace(body, `"id": "pass"`, `"id": "other"`, 1) }},
		{"with-inputs", func(body string) string {
			return strings.Replace(body, `"inputs": []`, `"inputs": [{"id":"private-input"}]`, 1)
		}},
		{"malformed-definition", func(string) string { return "{" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, private := checksDemo(t)
			checkPath := filepath.Join(project, "checks", "pass.json")
			body, err := os.ReadFile(checkPath)
			if err != nil {
				t.Fatal(err)
			}
			updated := tc.patch(string(body))
			if updated == string(body) {
				t.Fatal("test Check definition was not changed")
			}
			if err = os.WriteFile(checkPath, []byte(updated), 0600); err != nil {
				t.Fatal(err)
			}
			summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
				project: project, environment: "local", evidence: private, checks: []string{"pass"}, owner: true,
				timeout: time.Second, warnings: "fail", format: "json",
			})
			var exit ExitCoder
			if !errors.As(err, &exit) || exit.ExitCode() != 2 || summary.Status != "configuration_error" {
				t.Fatalf("unsafe Check definition = %+v, %v; want preflight configuration failure", summary, err)
			}
		})
	}
}

func TestChecksRunSmallPolicyReducers(t *testing.T) {
	for _, value := range []string{"pass", "warning"} {
		if got := checksEnum(value, "pass", "warning", "fail"); got != value {
			t.Errorf("checksEnum(%q) = %q; want same allowlisted value", value, got)
		}
	}
	if status, code := checksRunFailure(context.Background(), context.DeadlineExceeded); status != "timeout" || code != 124 {
		t.Errorf("deadline error classification = %q/%d; want timeout/124", status, code)
	}
	for _, tc := range []struct {
		err    error
		status string
		code   int
	}{
		{context.Canceled, "cancelled", 130},
		{detective.ErrNotFound, "not_found", 3},
		{detective.ErrInvalid, "configuration_error", 2},
		{detective.ErrConflict, "configuration_error", 2},
		{errors.New("private source failure"), "error", 4},
	} {
		status, code := checksRunFailure(context.Background(), tc.err)
		if status != tc.status || code != tc.code {
			t.Errorf("checksRunFailure(%v) = %q/%d; want %q/%d", tc.err, status, code, tc.status, tc.code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if status, code := classifyChecksRun(ctx, detective.CheckRun{}, nil, "fail"); status != "cancelled" || code != 130 {
		t.Errorf("cancelled check classification = %q/%d; want cancelled/130", status, code)
	}
	for status, want := range map[string]int{"warning": 2, "unrecognized": 5} {
		if got := checksStatusPriority(status); got != want {
			t.Errorf("checksStatusPriority(%q) = %d; want %d", status, got, want)
		}
	}
}

func TestPrivateExecutionEvidencePathsUsePhysicalProjectIdentity(t *testing.T) {
	physical := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(physical, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "project-alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	paths := privateExecutionEvidencePaths
	repository, snapshots, err := paths(physical, private)
	if err != nil {
		t.Fatal(err)
	}
	aliasRepository, aliasSnapshots, err := paths(alias, private)
	if err != nil || aliasRepository != repository || aliasSnapshots != snapshots {
		t.Fatalf("physical alias paths = %q, %q, %v; want stable scoped paths %q, %q", aliasRepository, aliasSnapshots, err, repository, snapshots)
	}
	if _, _, err = paths(filepath.Join(t.TempDir(), "missing"), private); err == nil {
		t.Fatal("missing project directory unexpectedly resolved to a private evidence scope")
	}
}

func TestSecurePrivateExecutionTreeTightensModesAndRejectsUnsafeEntries(t *testing.T) {
	if err := securePrivateExecutionTree(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing evidence repository error = %v; want not-exist", err)
	}

	repository := filepath.Join(t.TempDir(), "repository")
	nested := filepath.Join(repository, "nested")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(nested, "receipt.json")
	if err := os.WriteFile(file, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := securePrivateExecutionTree(repository); err != nil {
		t.Fatalf("secure ordinary evidence tree: %v", err)
	}
	for path, want := range map[string]os.FileMode{repository: 0700, nested: 0700, file: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("mode(%s) = %v, %v; want %o", path, info.Mode().Perm(), err, want)
		}
	}

	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("private"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, "redirect.json")); err != nil {
		t.Fatal(err)
	}
	if err := securePrivateExecutionTree(repository); err == nil || !strings.Contains(err.Error(), "unsafe private execution entry") {
		t.Fatalf("symlink evidence error = %v; want unsafe-entry rejection", err)
	}
	info, err := os.Stat(outside)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("outside target mode = %v, %v; symlink target must remain untouched", info.Mode().Perm(), err)
	}
}

func TestConfigurePrivateExecutionEvidenceRejectsMissingAndUnsafeBases(t *testing.T) {
	project, _ := checksDemo(t)
	if _, err := configurePrivateExecutionEvidence("project", filepath.Join(t.TempDir(), "missing"), t.TempDir()); err == nil {
		t.Fatal("missing project directory unexpectedly configured execution evidence")
	}
	if _, err := configurePrivateExecutionEvidence("project", project, filepath.Join(t.TempDir(), "missing-base")); err == nil {
		t.Fatal("missing private base unexpectedly configured execution evidence")
	}
}

func TestChecksRunContextCancellationBeforePrivateSetup(t *testing.T) {
	project, private := checksDemo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary, err := runChecksWithOptions(t, ctx, checksRunOptions{
		project: project, environment: "local", evidence: private, checks: []string{"pass"}, owner: true,
		timeout: time.Second, warnings: "fail", format: "json",
	})
	var exit ExitCoder
	if !errors.As(err, &exit) || exit.ExitCode() != 130 || summary.Status != "cancelled" {
		t.Fatalf("cancelled checks command = %+v, %v; want cancellation", summary, err)
	}
}

func TestChecksRunCancelsAtCompletedHostOperationBoundaries(t *testing.T) {
	for _, stage := range []string{"store", "environment-error", "private-setup"} {
		t.Run(stage, func(t *testing.T) {
			project, private := checksDemo(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			oldStore, oldEnvironment, oldPrivate := checksNewLocalStore, checksLoadEnvironment, checksConfigurePrivateEvidence
			t.Cleanup(func() {
				checksNewLocalStore, checksLoadEnvironment, checksConfigurePrivateEvidence = oldStore, oldEnvironment, oldPrivate
			})
			environment := "local"
			switch stage {
			case "store":
				checksNewLocalStore = func(ctx context.Context, projectID, projectDir, evidence string) (*detective.LocalStore, error) {
					store, err := oldStore(ctx, projectID, projectDir, evidence)
					if err != nil {
						t.Errorf("real store setup: %v", err)
					}
					cancel()
					return store, err
				}
			case "environment-error":
				environment = "absent"
				checksLoadEnvironment = func(ctx context.Context, projectID, projectDir, env string) (*datatug.Environment, error) {
					value, err := oldEnvironment(ctx, projectID, projectDir, env)
					if value != nil || err == nil {
						t.Errorf("missing environment result = %v, %v", value, err)
					}
					cancel()
					return value, err
				}
			case "private-setup":
				checksConfigurePrivateEvidence = func(projectID, projectDir, evidence string) (string, error) {
					repository, err := oldPrivate(projectID, projectDir, evidence)
					if err != nil {
						t.Errorf("real private setup: %v", err)
					}
					cancel()
					return repository, err
				}
			}
			summary, err := runChecksWithOptions(t, ctx, checksRunOptions{
				project: project, environment: environment, evidence: private, checks: []string{"pass"}, owner: true,
				timeout: time.Second, warnings: "fail", format: "json",
			})
			var exit ExitCoder
			if !errors.As(err, &exit) || exit.ExitCode() != 130 || summary.Status != "cancelled" || len(summary.Checks) != 0 {
				t.Fatalf("cancellation after %s = %+v, %v; want cancelled before source execution", stage, summary, err)
			}
		})
	}
}

func TestChecksRunHostConfigurationAndPrivacyErrorsAreSanitized(t *testing.T) {
	for _, stage := range []string{"watch-setup", "privacy-scrub"} {
		t.Run(stage, func(t *testing.T) {
			project, private := checksDemo(t)
			oldConfigure, oldExecute := watchConfigureExecution, watchExecuteCheck
			t.Cleanup(func() { watchConfigureExecution, watchExecuteCheck = oldConfigure, oldExecute })
			if stage == "watch-setup" {
				watchConfigureExecution = func(watchdaemon.Config, string) (func(), error) { return nil, errors.New("private setup detail") }
			} else {
				watchExecuteCheck = func(ctx context.Context, projectID, environment, checkID, version, fingerprint, operation string, inputs map[string]detective.InputValue) (detective.CheckRun, string, error) {
					run, fingerprint, err := oldExecute(ctx, projectID, environment, checkID, version, fingerprint, operation, inputs)
					if err != nil {
						t.Errorf("real Check execution: %v", err)
					}
					repository, _, pathErr := privateExecutionEvidencePaths(project, private)
					if pathErr != nil {
						t.Error(pathErr)
					}
					if linkErr := os.Symlink(project, filepath.Join(repository, "private-link")); linkErr != nil {
						t.Error(linkErr)
					}
					return run, fingerprint, err
				}
			}
			summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
				project: project, environment: "local", evidence: private, checks: []string{"pass"}, owner: true,
				timeout: time.Second, warnings: "fail", format: "json",
			})
			wantCode, wantStatus, wantChecks := 2, "configuration_error", 0
			if stage == "privacy-scrub" {
				wantCode, wantStatus, wantChecks = 4, "error", 1
			}
			var exit ExitCoder
			if !errors.As(err, &exit) || exit.ExitCode() != wantCode || summary.Status != wantStatus || len(summary.Checks) != wantChecks || strings.Contains(err.Error(), "private") {
				t.Fatalf("%s error = %+v, %v", stage, summary, err)
			}
			if wantChecks == 1 && (summary.Checks[0].ExecutionStatus != "error" || summary.Checks[0].Outcome != "unknown") {
				t.Fatalf("privacy failure published a successful outcome: %+v", summary)
			}
		})
	}
}

func TestPrivateExecutionEvidencePropagatesDirectoryStatError(t *testing.T) {
	project, private := checksDemo(t)
	previous := checksPrivateLstat
	t.Cleanup(func() { checksPrivateLstat = previous })
	want := errors.New("private stat detail")
	checksPrivateLstat = func(string) (os.FileInfo, error) { return nil, want }
	if _, err := configurePrivateExecutionEvidence("project", project, private); !errors.Is(err, want) {
		t.Fatalf("private directory stat error = %v; want original error", err)
	}
}

func TestSecurePrivateExecutionTreePropagatesWalkAndStatErrors(t *testing.T) {
	for _, operation := range []string{"walk", "stat"} {
		t.Run(operation, func(t *testing.T) {
			previousWalk, previousStat := checksPrivateWalkDir, checksPrivateRootLstat
			t.Cleanup(func() { checksPrivateWalkDir, checksPrivateRootLstat = previousWalk, previousStat })
			want := errors.New("private filesystem detail")
			if operation == "walk" {
				checksPrivateWalkDir = func(_ fs.FS, _ string, visit fs.WalkDirFunc) error { return visit(".", nil, want) }
			} else {
				checksPrivateRootLstat = func(*os.Root, string) (os.FileInfo, error) { return nil, want }
			}
			if err := securePrivateExecutionTree(t.TempDir()); !errors.Is(err, want) {
				t.Fatalf("%s error = %v; want original error", operation, err)
			}
		})
	}
}

func TestChecksRunIgnoredWarningAggregatesAbovePass(t *testing.T) {
	for _, ids := range [][]string{{"pass", "warning"}, {"warning", "pass"}} {
		project, private := checksDemo(t)
		summary, err := runChecksWithOptions(t, context.Background(), checksRunOptions{
			project: project, environment: "local", evidence: private, checks: ids, owner: true,
			timeout: time.Second, warnings: "ignore", format: "json",
		})
		if err != nil || summary.Status != "warning" || summary.ExitCode != 0 || len(summary.Checks) != 2 {
			t.Fatalf("warning order %v = %+v, %v; want warning with successful exit", ids, summary, err)
		}
	}
}
