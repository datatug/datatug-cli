//go:build linux

package sshdemo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// TestPrepareTemplateHelperProcess is invoked via os.Args[0] with
// `-test.run=^TestPrepareTemplateHelperProcess$ -- demo ssh-session --prepare-template ...`
// to exercise runPrepareTemplateCommand end to end.
func TestPrepareTemplateHelperProcess(t *testing.T) {
	idx := -1
	for i, arg := range os.Args {
		if arg == "--" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+3 >= len(os.Args) || os.Args[idx+1] != "demo" || os.Args[idx+2] != "ssh-session" || os.Args[idx+3] != "--prepare-template" {
		return
	}
	args := os.Args[idx+1:]
	var tmplDir, projDir string
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--prepare-template":
			tmplDir = args[i+1]
		case "--project-dir":
			projDir = args[i+1]
		}
	}
	home := os.Getenv("HOME")
	if _, err := os.Stat(home); err != nil {
		fmt.Fprintf(os.Stderr, "home dir missing: %v\n", err)
		os.Exit(2)
	}
	if _, err := os.Stat(filepath.Join(tmplDir, projDir, "fail-with-stderr")); err == nil {
		fmt.Fprintln(os.Stderr, "boom: scan failed on catalog orders")
		os.Exit(3)
	}
	marker := filepath.Join(tmplDir, projDir, ".prepared-by-helper")
	_ = os.WriteFile(marker, []byte("home="+home+"\nproxy="+os.Getenv("HTTPS_PROXY")+"\n"), 0o600)
	os.Exit(0)
}

func TestPrepareTemplate_PreservesExecModesAndGitStatusClean(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	stateDir := t.TempDir()

	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}

	writeTracked := func(rel string, body []byte, perm fs.FileMode) {
		t.Helper()
		full := filepath.Join(repoDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, perm); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, perm); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("git add %s: %v", rel, err)
		}
	}
	writeTracked(".gitignore", []byte("demo-project-1/.demo-data/\n"), 0o644)
	writeTracked("demo-project-1/datatug-project.json", []byte(`{"id":"demo"}`), 0o644)
	writeTracked(".git-hooks/pre-commit", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	writeTracked("scripts/run.sh", []byte("#!/bin/sh\necho hi\n"), 0o755)

	_, err = wt.Commit("initial with exec scripts", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@example.com", When: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	tmplDir, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:     stateDir,
		Repo:         repoDir,
		ProjectDir:   "demo-project-1",
		ChinookBytes: []byte("mini-chinook"),
	})
	if err != nil {
		t.Fatalf("PrepareTemplate: %v", err)
	}

	// Executable scripts keep their 0755 bits and world-readability.
	for _, rel := range []string{".git-hooks/pre-commit", "scripts/run.sh"} {
		fi, err := os.Stat(filepath.Join(tmplDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if fi.Mode().Perm()&0o111 != 0o111 || fi.Mode().Perm()&0o004 == 0 {
			t.Fatalf("%s mode = %04o, want exec+world-read preserved", rel, fi.Mode().Perm())
		}
	}
	// Regular non-exec file stays non-executable and world-readable.
	mf, err := os.Stat(filepath.Join(tmplDir, "demo-project-1", "datatug-project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mf.Mode().Perm()&0o111 != 0 || mf.Mode().Perm()&0o004 == 0 {
		t.Fatalf("manifest mode = %04o, want non-exec world-readable", mf.Mode().Perm())
	}

	// go-git worktree Status must be completely clean (no mode diffs, ignored chinook ignored).
	cloned, err := git.PlainOpen(tmplDir)
	if err != nil {
		t.Fatalf("PlainOpen template: %v", err)
	}
	clonedWT, err := cloned.Worktree()
	if err != nil {
		t.Fatalf("Worktree template: %v", err)
	}
	status, err := clonedWT.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.IsClean() {
		t.Fatalf("expected clean git status after PrepareTemplate, got:\n%s", status.String())
	}
}

func TestChmodWorldReadable_AddsOnlyReadExecAndHandlesErrors(t *testing.T) {
	root := t.TempDir()
	lockedDir := filepath.Join(root, "dir700")
	if err := os.Mkdir(lockedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file600 := filepath.Join(lockedDir, "plain.txt")
	if err := os.WriteFile(file600, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	exec700 := filepath.Join(lockedDir, "tool.sh")
	if err := os.WriteFile(exec700, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := chmodWorldReadable(root); err != nil {
		t.Fatalf("chmodWorldReadable: %v", err)
	}
	if fi, _ := os.Stat(lockedDir); fi.Mode().Perm() != 0o705 {
		t.Fatalf("dir700 mode = %04o, want 0705", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(file600); fi.Mode().Perm() != 0o604 {
		t.Fatalf("file600 mode = %04o, want 0604", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(exec700); fi.Mode().Perm() != 0o705 {
		t.Fatalf("exec700 mode = %04o, want 0705", fi.Mode().Perm())
	}

	// Missing root returns WalkDir error.
	if err := chmodWorldReadable(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error for missing root")
	}

	// DirEntry.Info error propagates cleanly.
	origInfo := templateDirEntryInfo
	t.Cleanup(func() { templateDirEntryInfo = origInfo })
	templateDirEntryInfo = func(fs.DirEntry) (fs.FileInfo, error) {
		return nil, errors.New("stat boom")
	}
	if err := chmodWorldReadable(root); err == nil || !strings.Contains(err.Error(), "stat boom") {
		t.Fatalf("expected stat boom error, got %v", err)
	}
}

func TestPrepareTemplate_PrepareFuncAndCommandExecution(t *testing.T) {
	ctx := context.Background()

	// BuildPrepareTemplateArgs and buildPrepareTemplateEnv coverage.
	wantArgs := []string{"demo", "ssh-session", "--prepare-template", "/s/template.tmp", "--project-dir", "demo-project-1"}
	if got := BuildPrepareTemplateArgs("/s/template.tmp", "demo-project-1"); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("BuildPrepareTemplateArgs = %v, want %v", got, wantArgs)
	}
	envNoProxy := buildPrepareTemplateEnv("/s/template.home", "")
	if strings.Contains(strings.Join(envNoProxy, "\n"), "HTTPS_PROXY=") {
		t.Fatalf("unexpected HTTPS_PROXY in %v", envNoProxy)
	}
	envWithProxy := buildPrepareTemplateEnv("/s/template.home", "http://edge:3128")
	if !strings.Contains(strings.Join(envWithProxy, "\n"), "HTTPS_PROXY=http://edge:3128") {
		t.Fatalf("missing HTTPS_PROXY in %v", envWithProxy)
	}

	repoDir := t.TempDir()
	initTestGitRepo(t, repoDir, map[string][]byte{
		"demo-project-1/datatug-project.json": []byte(`{"id":"demo"}`),
	})

	// 1. PrepareFunc seam success & error.
	stateDir := t.TempDir()
	called := false
	tmplDir, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:     stateDir,
		Repo:         repoDir,
		ProjectDir:   "demo-project-1",
		ChinookBytes: []byte("mini"),
		PrepareFunc: func(_ context.Context, dir, rel string) error {
			called = true
			if rel != "demo-project-1" || !strings.HasSuffix(dir, "template.tmp") {
				return fmt.Errorf("unexpected args: dir=%q rel=%q", dir, rel)
			}
			return nil
		},
	})
	if err != nil || !called || tmplDir == "" {
		t.Fatalf("PrepareFunc success failed: called=%v err=%v", called, err)
	}
	if _, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:     stateDir,
		Repo:         repoDir,
		ProjectDir:   "demo-project-1",
		ChinookBytes: []byte("mini"),
		PrepareFunc: func(context.Context, string, string) error {
			return errors.New("seam boom")
		},
	}); err == nil || !strings.Contains(err.Error(), "seam boom") {
		t.Fatalf("expected seam boom error, got %v", err)
	}

	// 2. Real subprocess execution via Executable + ExecPrefixArgs.
	stateExec := t.TempDir()
	tmplExec, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:       stateExec,
		Repo:           repoDir,
		ProjectDir:     "demo-project-1",
		EgressProxy:    "http://edge:3128",
		ChinookBytes:   []byte("mini"),
		Executable:     os.Args[0],
		ExecPrefixArgs: []string{"-test.run=^TestPrepareTemplateHelperProcess$", "--"},
	})
	if err != nil {
		t.Fatalf("PrepareTemplate with Executable: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(tmplExec, "demo-project-1", ".prepared-by-helper"))
	if err != nil {
		t.Fatalf("helper marker missing: %v", err)
	}
	wantHome := filepath.Join(stateExec, "template.home")
	if !strings.Contains(string(marker), "home="+wantHome) || !strings.Contains(string(marker), "proxy=http://edge:3128") {
		t.Fatalf("unexpected helper marker: %q", string(marker))
	}
	// Throwaway HOME must be removed after preparation completes.
	if _, err := os.Stat(wantHome); !os.IsNotExist(err) {
		t.Fatalf("template.home should be cleaned up, stat err=%v", err)
	}

	// 3. Subprocess failure includes stderr in returned error.
	failRepo := t.TempDir()
	initTestGitRepo(t, failRepo, map[string][]byte{
		"demo-project-1/datatug-project.json": []byte(`{"id":"demo"}`),
		"demo-project-1/fail-with-stderr":     []byte("1"),
	})
	_, err = PrepareTemplate(ctx, TemplateConfig{
		StateDir:       stateExec,
		Repo:           failRepo,
		ProjectDir:     "demo-project-1",
		ChinookBytes:   []byte("mini"),
		Executable:     os.Args[0],
		ExecPrefixArgs: []string{"-test.run=^TestPrepareTemplateHelperProcess$", "--"},
	})
	if err == nil || !strings.Contains(err.Error(), "boom: scan failed on catalog orders") {
		t.Fatalf("expected child stderr in error, got %v", err)
	}

	// 4. Subprocess failure with empty stderr (nonexistent executable) returns wrapped error.
	if err := runPrepareTemplateCommand(ctx, filepath.Join(t.TempDir(), "missing-bin"), nil, stateExec, t.TempDir(), "demo-project-1", ""); err == nil || !strings.Contains(err.Error(), "prepare template") {
		t.Fatalf("expected prepare template error for missing executable, got %v", err)
	}

	// 5. runPrepareTemplateCommand fails cleanly when template.home cannot be created.
	badStateFile := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(badStateFile, []byte("x"), 0o600)
	if err := runPrepareTemplateCommand(ctx, os.Args[0], nil, badStateFile, t.TempDir(), "demo-project-1", ""); err == nil || !strings.Contains(err.Error(), "create template home dir") {
		t.Fatalf("expected template home dir error, got %v", err)
	}
}
