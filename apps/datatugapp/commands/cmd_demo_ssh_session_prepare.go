package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	demoSSHPreparedCommitMessage = "DataTug demo: prepared workspace"
	demoSSHPreparedAuthorName    = "DataTug demo"
	demoSSHPreparedAuthorEmail   = "demo@datatug.app"
)

// Seams for unit tests in cmd_demo_ssh_session_prepare_test.go.
var (
	demoSSHScanSQLite     = scanDemoSQLiteCatalog
	demoSSHWorktreeStatus = func(wt *git.Worktree) (git.Status, error) { return wt.Status() }
	demoSSHWorktreeStage  = func(wt *git.Worktree, path string, deleted bool) error {
		if deleted {
			_, err := wt.Remove(path)
			return err
		}
		_, err := wt.Add(path)
		return err
	}
	demoSSHWorktreeCommit = func(wt *git.Worktree, msg string, opts *git.CommitOptions) (plumbing.Hash, error) {
		return wt.Commit(msg, opts)
	}
)

// runDemoSSHPrepareTemplate prepares a freshly cloned demo template repository:
// it scans every sqlite3 catalog of env "local" whose database file exists
// inside the project directory, removes transient filestore lock files, and
// commits all non-ignored worktree changes as one deterministic local commit
// whose timestamp matches canonical HEAD.
func runDemoSSHPrepareTemplate(ctx context.Context, templateDir, projectDirRel string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cleanTemplate := filepath.Clean(strings.TrimSpace(templateDir))
	info, err := os.Stat(cleanTemplate)
	if err != nil {
		return fmt.Errorf("stat template dir %q: %w", cleanTemplate, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("template path %q is not a directory", cleanTemplate)
	}
	projectPath, err := resolveSafeProjectDir(cleanTemplate, projectDirRel)
	if err != nil {
		return fmt.Errorf("invalid --project-dir: %w", err)
	}

	store := filestore.NewProjectStore(demoSSHProjectID, projectPath)
	catalogs, err := store.LoadEnvDbCatalogs(ctx, demoSSHDefaultEnv)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load %s catalogs: %w", demoSSHDefaultEnv, err)
	}
	sort.Slice(catalogs, func(i, j int) bool { return catalogs[i].ID < catalogs[j].ID })
	for _, cat := range catalogs {
		if cat.Driver != "sqlite3" && cat.Driver != "sqlite" {
			continue
		}
		dbPath, ok := resolveProjectLocalSQLitePath(projectPath, cat.Path)
		if !ok {
			continue
		}
		if err := demoSSHScanSQLite(projectPath, cat.ID, dbPath); err != nil {
			return fmt.Errorf("scan catalog %q: %w", cat.ID, err)
		}
	}

	// datatug-core's filestore writes path-bound environment lock files during
	// SaveProject; remove them before committing so the prepared tree is clean
	// and deterministic across clone locations.
	for _, name := range []string{".datatug-environments.lock", ".datatug-environments-owner.json"} {
		_ = os.Remove(filepath.Join(projectPath, name))
	}

	if err := commitPreparedTemplate(cleanTemplate); err != nil {
		return err
	}
	return excludeDemoRuntimeFiles(cleanTemplate)
}

// demoRuntimeExcludes are files the workspace itself writes into a project
// while it runs; they are never a visitor's change, so they are excluded in
// the template's own .git/info/exclude (local, never committed or pushed),
// which go-git's status and staging honour.
var demoRuntimeExcludes = []string{".datatug-environments.lock", ".datatug-environments-owner.json"}

func excludeDemoRuntimeFiles(templateDir string) error {
	infoDir := filepath.Join(templateDir, ".git", "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", infoDir, err)
	}
	path := filepath.Join(infoDir, "exclude")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	text := string(existing)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	for _, pattern := range demoRuntimeExcludes {
		if !strings.Contains("\n"+text, "\n"+pattern+"\n") {
			text += pattern + "\n"
		}
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func resolveProjectLocalSQLitePath(projectPath, catalogPath string) (string, bool) {
	p := strings.TrimSpace(catalogPath)
	if p == "" || strings.HasPrefix(p, "~") || strings.Contains(p, "$HOME") {
		return "", false
	}
	var full string
	if filepath.IsAbs(p) {
		full = filepath.Clean(p)
	} else {
		full = filepath.Clean(filepath.Join(projectPath, filepath.FromSlash(p)))
	}
	if !isWithinDir(projectPath, full) {
		return "", false
	}
	info, err := os.Stat(full)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return full, true
}

func scanDemoSQLiteCatalog(projectPath, catalogID, dbPath string) error {
	scan := scanCommandArgs()
	scan.SetOut(io.Discard)
	scan.SetErr(io.Discard)
	for name, value := range map[string]string{
		"directory": projectPath,
		"project":   demoSSHProjectID,
		"driver":    "sqlite3",
		"db":        catalogID,
		"env":       demoSSHDefaultEnv,
		"path":      dbPath,
	} {
		_ = scan.Flags().Set(name, value)
	}
	previous := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previous)
	return scanCommandAction(scan, nil)
}

func commitPreparedTemplate(templateDir string) error {
	repo, err := git.PlainOpen(templateDir)
	if err != nil {
		return fmt.Errorf("open template git repo: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("open template worktree: %w", err)
	}
	headRef, err := repo.Head()
	if err != nil {
		return fmt.Errorf("resolve template HEAD: %w", err)
	}
	headCommit, err := repo.CommitObject(headRef.Hash())
	if err != nil {
		return fmt.Errorf("load template HEAD commit: %w", err)
	}
	status, err := demoSSHWorktreeStatus(wt)
	if err != nil {
		return fmt.Errorf("template git status: %w", err)
	}
	if status.IsClean() {
		return nil
	}

	paths := make([]string, 0, len(status))
	for p := range status {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		st := status[p]
		if st.Worktree == git.Unmodified {
			continue
		}
		if err := demoSSHWorktreeStage(wt, p, st.Worktree == git.Deleted); err != nil {
			return fmt.Errorf("stage %q: %w", p, err)
		}
	}

	sig := &object.Signature{
		Name:  demoSSHPreparedAuthorName,
		Email: demoSSHPreparedAuthorEmail,
		When:  headCommit.Committer.When,
	}
	if _, err := demoSSHWorktreeCommit(wt, demoSSHPreparedCommitMessage, &git.CommitOptions{
		Author:    sig,
		Committer: sig,
	}); err != nil {
		return fmt.Errorf("commit prepared template: %w", err)
	}
	return nil
}
