package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

func checksDemo(t *testing.T) (string, string) {
	t.Helper()
	project := filepath.Join(t.TempDir(), "project")
	if err := os.CopyFS(project, os.DirFS("../../../testdata/checks-demo")); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(project, "datatug-project.json")
	body, e := os.ReadFile(manifest)
	if e != nil {
		t.Fatal(e)
	}
	body = bytes.ReplaceAll(body, []byte("checks-demo"), []byte("checks-demo-"+uuid.NewString()))
	if e = os.WriteFile(manifest, body, 0600); e != nil {
		t.Fatal(e)
	}
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATATUG_POLICIES_DIR", t.TempDir())
	return project, private
}
func executeChecks(t *testing.T, ctx context.Context, project, private string, flags ...string) (checksRunSummary, string, error) {
	t.Helper()
	cmd := detectiveUICommand("checks")
	var output, diagnostics bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&diagnostics)
	args := []string{"run", "--project", project, "--env", "local", "--local-owner", "--evidence-dir", private}
	cmd.SetArgs(append(args, flags...))
	err := cmd.ExecuteContext(ctx)
	var summary checksRunSummary
	if json.Unmarshal(output.Bytes(), &summary) != nil {
		t.Fatalf("invalid envelope %q: %v", output.String(), err)
	}
	if summary.SchemaVersion != "datatug.checks.run/v1" || summary.DeepLink != "https://datatug.app" || summary.DeepLinkReason != "hosted_result_reference_unsupported" {
		t.Fatal(summary)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics %q", diagnostics.String())
	}
	if summary.ExitCode == 0 {
		if err != nil {
			t.Fatal(err)
		}
	} else {
		var ec ExitCoder
		if !errors.As(err, &ec) || ec.ExitCode() != summary.ExitCode {
			t.Fatalf("summary code %d != error %v", summary.ExitCode, err)
		}
	}
	text := output.String()
	if err != nil {
		text += err.Error()
	}
	for _, privateText := range []string{project, private, "synthetic-private-row-sentinel", "MissingTable", "Trusted demo Check", "rows", "count"} {
		if strings.Contains(text, privateText) {
			t.Fatalf("private content %q leaked in %s", privateText, text)
		}
	}
	return summary, output.String(), err
}
func TestChecksRunRealSQLiteOutcomesAndPrivateEvidence(t *testing.T) {
	for _, tc := range []struct {
		id, status string
		code       int
	}{{"pass", "pass", 0}, {"fail", "fail", 1}, {"source-error", "error", 4}, {"warning", "warning", 1}, {"unknown", "unknown", 1}} {
		t.Run(tc.id, func(t *testing.T) {
			project, private := checksDemo(t)
			before := checksProjectTree(t, project)
			s, _, _ := executeChecks(t, context.Background(), project, private, "--check", tc.id)
			if after := checksProjectTree(t, project); !reflect.DeepEqual(before, after) {
				t.Fatalf("Check changed project tree: before=%v after=%v", before, after)
			}
			if s.Status != tc.status || s.ExitCode != tc.code || len(s.Checks) != 1 {
				t.Fatalf("summary %+v", s)
			}
			if tc.id == "pass" || tc.id == "fail" || tc.id == "warning" {
				if s.Checks[0].ExecutionStatus != "completed" {
					t.Fatal(s)
				}
			}
			// Recording remains enabled: successful saved queries have immutable
			// execution receipts and snapshot SQLite sidecars, both privately routed.
			if tc.id != "source-error" {
				receipts, err := filepath.Glob(filepath.Join(private, "check-executions", "*", "repository", "executions", "*", "*", "*.json"))
				if err != nil || len(receipts) != 1 {
					t.Fatalf("missing private execution receipt: %v %v", receipts, err)
				}
				sidecars, err := filepath.Glob(filepath.Join(private, "check-executions", "*", "snapshots", "*", "snapshots.sqlite"))
				if err != nil || len(sidecars) != 1 {
					t.Fatalf("missing private snapshot sidecar: %v %v", sidecars, err)
				}
			}
			// A real saved-query run records evidence outside the shared project.
			runs, err := filepath.Glob(filepath.Join(private, "detective", "*", "runs", "*.json"))
			if err != nil || len(runs) != 1 {
				t.Fatalf("private run evidence %v %v", runs, err)
			}
			if err = filepath.WalkDir(private, func(path string, d os.DirEntry, e error) error {
				if e != nil {
					return e
				}
				info, e := d.Info()
				if e != nil {
					return e
				}
				if info.Mode().Perm()&0077 != 0 {
					t.Fatalf("nonprivate evidence %s %o", path, info.Mode().Perm())
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(project, "runs")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("runs written to project")
			}
		})
	}
}
func TestChecksRunWarningsRepeatAndRerun(t *testing.T) {
	project, private := checksDemo(t)
	for attempt := 0; attempt < 2; attempt++ {
		s, _, _ := executeChecks(t, context.Background(), project, private, "--check", "pass", "--check", "warning", "--warnings", "ignore")
		if s.Status != "warning" || s.ExitCode != 0 || len(s.Checks) != 2 || s.Checks[1].Index != 2 {
			t.Fatal(s)
		}
	}
	runs, _ := filepath.Glob(filepath.Join(private, "detective", "*", "runs", "*.json"))
	if len(runs) != 4 {
		t.Fatalf("explicit reruns reused an operation: %v", runs)
	}
}
func TestChecksRunConsentPoliciesAndConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		flags  []string
		status string
		code   int
	}{
		{"consent", []string{"--local-owner=false", "--check", "pass"}, "access_denied", 5},
		{"absent-check", []string{"--check", "absent"}, "not_found", 3},
		{"invalid-check", []string{"--check", "../private-secret"}, "configuration_error", 2},
		{"no-check", nil, "configuration_error", 2},
		{"bad-timeout", []string{"--check", "pass", "--timeout", "private-secret"}, "configuration_error", 2},
		{"bad-warning", []string{"--check", "pass", "--warnings", "private-secret"}, "configuration_error", 2},
		{"bad-format", []string{"--check", "pass", "--format", "private-secret"}, "configuration_error", 2},
		{"bad-flag", []string{"--private-secret"}, "configuration_error", 2},
		{"extra-arg", []string{"private-secret"}, "configuration_error", 2},
		{"missing-env", []string{"--check", "pass", "--env", "private-secret"}, "configuration_error", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, private := checksDemo(t)
			s, text, _ := executeChecks(t, context.Background(), project, private, tc.flags...)
			if s.Status != tc.status || s.ExitCode != tc.code || strings.Contains(text, "private-secret") {
				t.Fatal(s, text)
			}
		})
	}
	t.Run("secured", func(t *testing.T) {
		project, private := checksDemo(t)
		policyDir := projectWithPolicies(t)
		if err := os.CopyFS(filepath.Join(project, "policies"), os.DirFS(filepath.Join(policyDir, "policies"))); err != nil {
			t.Fatal(err)
		}
		s, _, _ := executeChecks(t, context.Background(), project, private, "--check", "pass")
		if s.Status != "access_denied" || s.ExitCode != 5 {
			t.Fatal(s)
		}
	})
	t.Run("environment-policies", func(t *testing.T) {
		project, private := checksDemo(t)
		t.Setenv("DATATUG_POLICIES_DIR", filepath.Join(projectWithPolicies(t), "policies"))
		s, _, _ := executeChecks(t, context.Background(), project, private, "--check", "pass")
		if s.Status != "access_denied" || s.ExitCode != 5 {
			t.Fatal(s)
		}
	})
}
func TestChecksRunRejectsSharedAndAliasedEvidence(t *testing.T) {
	for _, kind := range []string{"inside", "ancestor", "world-readable", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			project, private := checksDemo(t)
			switch kind {
			case "inside":
				private = filepath.Join(project, "private")
				if err := os.Mkdir(private, 0700); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				private = filepath.Dir(project)
				if err := os.Chmod(private, 0700); err != nil {
					t.Fatal(err)
				}
			case "world-readable":
				if err := os.Chmod(private, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(project, alias); err != nil {
					t.Fatal(err)
				}
				private = alias
			}
			s, _, _ := executeChecks(t, context.Background(), project, private, "--check", "pass")
			if s.Status != "configuration_error" || s.ExitCode != 2 {
				t.Fatal(s)
			}
		})
	}
}
func TestChecksRunCancellationAndTimeout(t *testing.T) {
	project, private := checksDemo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _, _ := executeChecks(t, ctx, project, private, "--check", "pass")
	if s.Status != "cancelled" || s.ExitCode != 130 {
		t.Fatal(s)
	}
	s, _, _ = executeChecks(t, context.Background(), project, private, "--check", "pass", "--timeout", "1ns")
	if s.Status != "timeout" || s.ExitCode != 124 {
		t.Fatal(s)
	}
}
func TestChecksRunClassifiesUnknownAndSanitizesEnums(t *testing.T) {
	for _, tc := range []struct {
		run    detective.CheckRun
		status string
		code   int
	}{
		{detective.CheckRun{ExecutionStatus: "completed", Outcome: "unknown"}, "unknown", 1},
		{detective.CheckRun{ExecutionStatus: "error", Outcome: "fail", Severity: "warning"}, "error", 4},
		{detective.CheckRun{ExecutionStatus: "completed", Outcome: "private-secret"}, "error", 4},
	} {
		status, code := classifyChecksRun(context.Background(), tc.run, nil, "ignore")
		if status != tc.status || code != tc.code {
			t.Fatal(status, code)
		}
	}
	if checksEnum("private-secret", "pass", "fail") != "" {
		t.Fatal("unsafe enum accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	status, code := checksRunFailure(ctx, errors.New("private-secret"))
	if status != "timeout" || code != 124 {
		t.Fatal(status, code)
	}
}

func TestChecksRunHeldPrivateLockHonorsDeadlineAndCancellation(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			project, private := checksDemo(t)
			s, _, _ := executeChecks(t, context.Background(), project, private, "--check", "pass")
			if s.ExitCode != 0 {
				t.Fatal(s)
			}
			locks, err := filepath.Glob(filepath.Join(private, "detective", "*", "store.lock"))
			if err != nil || len(locks) != 1 {
				t.Fatal(locks, err)
			}
			lock := flock.New(locks[0])
			if err = lock.Lock(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := lock.Unlock(); err != nil {
					t.Error(err)
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			flags := []string{"--check", "pass", "--timeout", "40ms"}
			wantStatus, wantCode := "timeout", 124
			if mode == "cancel" {
				flags = []string{"--check", "pass"}
				wantStatus, wantCode = "cancelled", 130
				go func() { time.Sleep(40 * time.Millisecond); cancel() }()
			}
			start := time.Now()
			s, _, _ = executeChecks(t, ctx, project, private, flags...)
			if s.Status != wantStatus || s.ExitCode != wantCode || time.Since(start) > time.Second {
				t.Fatal(s, time.Since(start))
			}
		})
	}
}
func TestChecksRunCannotAutostartDaemon(t *testing.T) {
	f := &daemonFake{}
	useDaemonFake(t, f)
	t.Setenv(ClientEnv, "claude-mod")
	var stderr bytes.Buffer
	EnsureDaemonForCaller(DatatugCommand(), []string{"checks", "run", "--project", "unused", "--check", "pass", "--local-owner"}, &stderr)
	if f.spawned != 0 || stderr.Len() != 0 {
		t.Fatalf("one-shot command spawned daemon: %d %s", f.spawned, stderr.String())
	}
	EnsureDaemonForCaller(DatatugCommand(), []string{"checks"}, &stderr)
	if f.spawned != 1 {
		t.Fatal("bare checks behavior changed")
	}
}

func TestChecksRunMixedBusinessStatusPrecedence(t *testing.T) {
	for _, tc := range []struct {
		ids    []string
		status string
	}{{[]string{"warning", "unknown"}, "unknown"}, {[]string{"unknown", "warning"}, "unknown"}, {[]string{"warning", "fail"}, "fail"}, {[]string{"fail", "warning"}, "fail"}} {
		project, private := checksDemo(t)
		flags := []string{"--warnings", "ignore"}
		for _, id := range tc.ids {
			flags = append(flags, "--check", id)
		}
		s, _, _ := executeChecks(t, context.Background(), project, private, flags...)
		if s.Status != tc.status || s.ExitCode != 1 || len(s.Checks) != 2 {
			t.Fatal(s)
		}
	}
}

// Includes all paths, types, permissions, symlink targets and file contents:
// checking only the legacy runs/ directory missed immutable execution receipts.
func checksProjectTree(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := fmt.Sprint(info.Mode())
		if info.Mode().IsRegular() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(":%x", sha256.Sum256(body))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":" + target
		}
		entries[rel] = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}
func TestChecksRunReadOnlyProjectTreeRemainsUnchanged(t *testing.T) {
	project, private := checksDemo(t)
	if err := filepath.WalkDir(project, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := os.FileMode(0444)
		if entry.IsDir() {
			mode = 0555
		}
		return os.Chmod(path, mode)
	}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := filepath.WalkDir(project, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0755)
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
	}()
	before := checksProjectTree(t, project)
	summary, _, _ := executeChecks(t, context.Background(), project, private, "--check", "pass")
	if summary.Status != "pass" || summary.ExitCode != 0 {
		t.Fatal(summary)
	}
	if after := checksProjectTree(t, project); !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only project changed: before=%v after=%v", before, after)
	}
}

func TestChecksRunExecutionEvidenceScopesAndUnsafeRoutes(t *testing.T) {
	first, private := checksDemo(t)
	second, _ := checksDemo(t)
	a, err := configurePrivateExecutionEvidence("same-project", first, private)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := api.CloseExecutionEvidence(); err != nil {
			t.Error(err)
		}
	}()
	b, err := configurePrivateExecutionEvidence("same-project", second, private)
	if err != nil || a == b {
		t.Fatalf("physical projects shared execution repository: %s %s %v", a, b, err)
	}
	again, err := configurePrivateExecutionEvidence("same-project", first, private)
	if err != nil || again != a {
		t.Fatalf("same physical project lost evidence scope: %v", err)
	}
	// Pre-existing symlinks cannot redirect either the metadata repository or
	// snapshots back into a project, nor can a permissive ancestor be reused.
	for _, kind := range []string{"symlink", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			project, private := checksDemo(t)
			container := filepath.Join(private, "check-executions")
			if kind == "symlink" {
				if err := os.Symlink(project, container); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(container, 0755); err != nil {
					t.Fatal(err)
				}
			}
			before := checksProjectTree(t, project)
			summary, _, _ := executeChecks(t, context.Background(), project, private, "--check", "pass")
			if summary.Status != "configuration_error" || summary.ExitCode != 2 {
				t.Fatal(summary)
			}
			if after := checksProjectTree(t, project); !reflect.DeepEqual(before, after) {
				t.Fatal("unsafe route wrote into project")
			}
		})
	}
}

func TestChecksRunRestoresLogging(t *testing.T) {
	oldWriter, oldFlags, oldSlog := log.Writer(), log.Flags(), slog.Default()
	defer func() { slog.SetDefault(oldSlog); log.SetOutput(oldWriter); log.SetFlags(oldFlags) }()
	var buffer bytes.Buffer
	expected := slog.New(slog.NewTextHandler(&buffer, nil))
	slog.SetDefault(expected)
	log.SetOutput(&buffer)
	log.SetFlags(log.Lshortfile)
	cmd := checksRunCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--local-owner=false"})
	_ = cmd.Execute()
	if slog.Default() != expected || log.Writer() != &buffer || log.Flags() != log.Lshortfile {
		t.Fatal("one-shot command did not restore process logging")
	}
}
