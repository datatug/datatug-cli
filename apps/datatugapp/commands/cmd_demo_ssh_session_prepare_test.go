//go:build unix

package commands

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func createMinimalSQLiteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("sql.Open sqlite3: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE Invoice (InvoiceId INTEGER PRIMARY KEY, Total REAL NOT NULL); INSERT INTO Invoice VALUES (1, 9.99);`); err != nil {
		t.Fatalf("create Invoice table: %v", err)
	}
}

func makePreparableGitTemplate(t *testing.T, commitTime time.Time) (string, plumbing.Hash) {
	t.Helper()
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}

	writeTracked := func(rel, content string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("git add %s: %v", rel, err)
		}
	}

	writeTracked(".gitignore", "demo-project-1/.demo-data/\n")
	writeTracked("demo-project-1/datatug-project.json", `{"id":"demo-project-1","title":"Demo Project","version":"0.1.0","created":{"at":"2020-11-02T22:22:13.1313277Z"},"access":"public","environments":[{"id":"local"}]}`)
	writeTracked("demo-project-1/environments/local/local.env.json", `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["chinook","chinook-local","geo","orders","missing"]}]}`)
	writeTracked("demo-project-1/environments/local/catalogs/chinook/chinook.db.json", `{"driver":"sqlite3","path":".demo-data/chinook.sqlite"}`)
	writeTracked("demo-project-1/environments/local/catalogs/chinook-local/chinook-local.db.json", `{"driver":"sqlite3","path":"~/datatug/dbs/chinook-local.sqlite","dbModel":"chinook"}`)
	writeTracked("demo-project-1/environments/local/catalogs/geo/geo.db.json", `{"driver":"ingitdb","path":"data/geo"}`)
	writeTracked("demo-project-1/environments/local/catalogs/orders/orders.db.json", `{"driver":"sqlite3","path":"fixtures/orders.sqlite"}`)
	writeTracked("demo-project-1/environments/local/catalogs/missing/missing.db.json", `{"driver":"sqlite3","path":"fixtures/not-there.sqlite"}`)

	// Create tracked orders.sqlite inside repo.
	createMinimalSQLiteFile(t, filepath.Join(root, "demo-project-1", "fixtures", "orders.sqlite"))
	if _, err := wt.Add("demo-project-1/fixtures/orders.sqlite"); err != nil {
		t.Fatalf("git add orders.sqlite: %v", err)
	}

	sig := &object.Signature{Name: "Canonical", Email: "canon@example.com", When: commitTime}
	headHash, err := wt.Commit("canonical commit", &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Write gitignored .demo-data/chinook.sqlite AFTER canonical commit (just like PrepareTemplate does).
	createMinimalSQLiteFile(t, filepath.Join(root, "demo-project-1", ".demo-data", "chinook.sqlite"))
	return root, headHash
}

func TestDemoSSHSessionPrepareTemplate_ScansCommitsDeterministicallyAndIgnoresGitignored(t *testing.T) {
	fixedTime := time.Date(2026, 11, 25, 12, 34, 56, 0, time.UTC)

	// Prepare two independent clones of the same canonical commit (using the real scan path)
	// and verify they produce the EXACT SAME prepared commit hash.
	tplA, canonA := makePreparableGitTemplate(t, fixedTime)
	tplB, canonB := makePreparableGitTemplate(t, fixedTime)
	if canonA != canonB {
		t.Fatalf("canonical commits should match: %s != %s", canonA, canonB)
	}

	// Run A via the hidden cobra flag `--prepare-template`.
	cmd := demoSSHSessionCommand()
	cmd.SetArgs([]string{"--prepare-template", tplA, "--project-dir", "demo-project-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("prepare-template A: %v", err)
	}
	// Run B directly (testing nil ctx fallback too).
	//nolint:staticcheck // intentionally exercising nil context fallback
	if err := runDemoSSHPrepareTemplate(nil, tplB, "demo-project-1"); err != nil {
		t.Fatalf("prepare-template B: %v", err)
	}

	repoA, err := git.PlainOpen(tplA)
	if err != nil {
		t.Fatal(err)
	}
	headA, err := repoA.Head()
	if err != nil {
		t.Fatal(err)
	}
	repoB, err := git.PlainOpen(tplB)
	if err != nil {
		t.Fatal(err)
	}
	headB, err := repoB.Head()
	if err != nil {
		t.Fatal(err)
	}

	if headA.Hash() == canonA {
		t.Fatal("expected prepared commit on top of canonical HEAD")
	}
	if headA.Hash() != headB.Hash() {
		cA, _ := repoA.CommitObject(headA.Hash())
		cB, _ := repoB.CommitObject(headB.Hash())
		tA, _ := cA.Tree()
		tB, _ := cB.Tree()
		changes, _ := tA.Diff(tB)
		var detail strings.Builder
		for _, ch := range changes {
			p, _ := ch.Patch()
			detail.WriteString(p.String())
		}
		t.Fatalf("prepared commit hash is not deterministic: %s != %s\nA: %v\nB: %v\ndiff:\n%s", headA.Hash(), headB.Hash(), cA, cB, detail.String())
	}

	commitObj, err := repoA.CommitObject(headA.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if commitObj.Message != demoSSHPreparedCommitMessage {
		t.Fatalf("commit message = %q, want %q", commitObj.Message, demoSSHPreparedCommitMessage)
	}
	if commitObj.Author.Name != demoSSHPreparedAuthorName || commitObj.Author.Email != demoSSHPreparedAuthorEmail || !commitObj.Author.When.Equal(fixedTime) {
		t.Fatalf("unexpected author: %+v", commitObj.Author)
	}
	if commitObj.Committer.Name != demoSSHPreparedAuthorName || commitObj.Committer.Email != demoSSHPreparedAuthorEmail || !commitObj.Committer.When.Equal(fixedTime) {
		t.Fatalf("unexpected committer: %+v", commitObj.Committer)
	}

	// Verify git worktree is clean and .demo-data/chinook.sqlite was NOT committed.
	wtA, err := repoA.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	status, err := wtA.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !status.IsClean() {
		t.Fatalf("expected clean worktree after prepare, got:\n%s", status.String())
	}
	tree, err := commitObj.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.File("demo-project-1/.demo-data/chinook.sqlite"); err == nil {
		t.Fatal("gitignored chinook.sqlite must NOT be committed")
	}
	// Verify chinook and orders were scanned (dbModel written to catalog JSONs).
	for _, cat := range []string{"chinook", "orders"} {
		raw, err := os.ReadFile(filepath.Join(tplA, "demo-project-1", "environments", "local", "catalogs", cat, cat+".db.json"))
		if err != nil {
			t.Fatalf("read %s.db.json: %v", cat, err)
		}
		if !strings.Contains(string(raw), `"dbModel"`) {
			t.Fatalf("%s.db.json missing dbModel after scan: %s", cat, string(raw))
		}
	}
	// Running prepare again when worktree is already clean is a no-op (same HEAD).
	stubSSHSession(t, &demoSSHScanSQLite, func(string, string, string) error { return nil })
	if err := runDemoSSHPrepareTemplate(context.Background(), tplA, "demo-project-1"); err != nil {
		t.Fatalf("second prepare on clean worktree: %v", err)
	}
	headAfter, _ := repoA.Head()
	if headAfter.Hash() != headA.Hash() {
		t.Fatalf("clean worktree should not create a second commit: %s != %s", headAfter.Hash(), headA.Hash())
	}
}

func TestDemoSSHSessionPrepareTemplate_EdgeCasesAndErrors(t *testing.T) {
	// Path resolution edge cases.
	proj := t.TempDir()
	inProj := filepath.Join(proj, "data", "a.sqlite")
	_ = os.MkdirAll(filepath.Dir(inProj), 0o755)
	_ = os.WriteFile(inProj, []byte("x"), 0o644)
	if got, ok := resolveProjectLocalSQLitePath(proj, inProj); !ok || got != inProj {
		t.Fatalf("abs path inside project = (%q, %v)", got, ok)
	}
	outside := filepath.Join(t.TempDir(), "out.sqlite")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	for _, skip := range []string{"", "~/x.sqlite", "$HOME/x.sqlite", "../escape.sqlite", outside, "missing.sqlite"} {
		if _, ok := resolveProjectLocalSQLitePath(proj, skip); ok {
			t.Errorf("expected skip for %q", skip)
		}
	}

	// Missing template dir, regular file template path, invalid project-dir.
	if err := runDemoSSHPrepareTemplate(context.Background(), filepath.Join(t.TempDir(), "missing"), "demo-project-1"); err == nil {
		t.Fatal("expected error for missing template dir")
	}
	fileTpl := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(fileTpl, []byte("x"), 0o600)
	if err := runDemoSSHPrepareTemplate(context.Background(), fileTpl, "demo-project-1"); err == nil {
		t.Fatal("expected error for file template path")
	}
	if err := runDemoSSHPrepareTemplate(context.Background(), t.TempDir(), "../escape"); err == nil {
		t.Fatal("expected error for escaping project-dir")
	}

	// Scan error aborts preparation.
	tplScanErr, _ := makePreparableGitTemplate(t, time.Now().UTC())
	stubSSHSession(t, &demoSSHScanSQLite, func(_, catID, _ string) error {
		return errors.New("scan exploded on " + catID)
	})
	if err := runDemoSSHPrepareTemplate(context.Background(), tplScanErr, "demo-project-1"); err == nil || !strings.Contains(err.Error(), "scan exploded") {
		t.Fatalf("expected scan error, got %v", err)
	}

	// Corrupt catalog JSON causes LoadEnvDbCatalogs error.
	tplCorrupt, _ := makePreparableGitTemplate(t, time.Now().UTC())
	_ = os.WriteFile(filepath.Join(tplCorrupt, "demo-project-1", "environments", "local", "catalogs", "orders", "orders.db.json"), []byte("{bad-json"), 0o644)
	if err := runDemoSSHPrepareTemplate(context.Background(), tplCorrupt, "demo-project-1"); err == nil || !strings.Contains(err.Error(), "load local catalogs") {
		t.Fatalf("expected catalog load error, got %v", err)
	}

	// commitPreparedTemplate: handles deleted tracked files, staged-only unmodified worktree entries, and git errors.
	tplDel, _ := makePreparableGitTemplate(t, time.Date(2026, 11, 25, 1, 2, 3, 0, time.UTC))
	if err := os.Remove(filepath.Join(tplDel, "demo-project-1", "environments", "local", "catalogs", "missing", "missing.db.json")); err != nil {
		t.Fatal(err)
	}
	// Pre-stage one file so status also has an entry with Worktree == git.Unmodified.
	repoDel, err := git.PlainOpen(tplDel)
	if err != nil {
		t.Fatal(err)
	}
	wtDel, err := repoDel.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	stagedPath := filepath.Join(tplDel, "demo-project-1", "staged-only.txt")
	_ = os.WriteFile(stagedPath, []byte("ok"), 0o644)
	if _, err := wtDel.Add("demo-project-1/staged-only.txt"); err != nil {
		t.Fatal(err)
	}
	if err := commitPreparedTemplate(tplDel); err != nil {
		t.Fatalf("commitPreparedTemplate with deletion: %v", err)
	}

	if err := commitPreparedTemplate(t.TempDir()); err == nil || !strings.Contains(err.Error(), "open template git repo") {
		t.Fatalf("expected non-git repo error, got %v", err)
	}
	// Bare git repo fails Worktree().
	bareRepoDir := t.TempDir()
	if _, err := git.PlainInit(bareRepoDir, true); err != nil {
		t.Fatal(err)
	}
	if err := commitPreparedTemplate(bareRepoDir); err == nil || !strings.Contains(err.Error(), "open template worktree") {
		t.Fatalf("expected worktree error for bare repo, got %v", err)
	}
	// Empty git repo with no HEAD commit.
	emptyRepoDir := t.TempDir()
	if _, err := git.PlainInit(emptyRepoDir, false); err != nil {
		t.Fatal(err)
	}
	if err := commitPreparedTemplate(emptyRepoDir); err == nil || !strings.Contains(err.Error(), "resolve template HEAD") {
		t.Fatalf("expected missing HEAD error, got %v", err)
	}
	// Detached HEAD pointing to missing commit object fails CommitObject.
	brokenHeadDir, _ := makePreparableGitTemplate(t, time.Now().UTC())
	_ = os.WriteFile(filepath.Join(brokenHeadDir, ".git", "HEAD"), []byte("1111111111111111111111111111111111111111\n"), 0o644)
	if err := commitPreparedTemplate(brokenHeadDir); err == nil || !strings.Contains(err.Error(), "load template HEAD commit") {
		t.Fatalf("expected CommitObject error, got %v", err)
	}

	// Seam-injected status, stage, and commit errors.
	tplSeam, _ := makePreparableGitTemplate(t, time.Now().UTC())
	_ = os.WriteFile(filepath.Join(tplSeam, "demo-project-1", "new.txt"), []byte("1"), 0o644)
	t.Run("status error", func(t *testing.T) {
		stubSSHSession(t, &demoSSHWorktreeStatus, func(*git.Worktree) (git.Status, error) {
			return nil, errors.New("status boom")
		})
		if err := commitPreparedTemplate(tplSeam); err == nil || !strings.Contains(err.Error(), "status boom") {
			t.Fatalf("expected status boom, got %v", err)
		}
	})
	t.Run("stage error", func(t *testing.T) {
		stubSSHSession(t, &demoSSHWorktreeStage, func(*git.Worktree, string, bool) error {
			return errors.New("stage boom")
		})
		if err := commitPreparedTemplate(tplSeam); err == nil || !strings.Contains(err.Error(), "stage boom") {
			t.Fatalf("expected stage boom, got %v", err)
		}
	})
	t.Run("commit error", func(t *testing.T) {
		stubSSHSession(t, &demoSSHWorktreeCommit, func(*git.Worktree, string, *git.CommitOptions) (plumbing.Hash, error) {
			return plumbing.ZeroHash, errors.New("commit boom")
		})
		if err := commitPreparedTemplate(tplSeam); err == nil || !strings.Contains(err.Error(), "commit boom") {
			t.Fatalf("expected commit boom, got %v", err)
		}
	})
}

func TestExcludeDemoRuntimeFiles(t *testing.T) {
	dir := t.TempDir()
	if err := excludeDemoRuntimeFiles(dir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	path := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.WriteFile(path, append(mustReadFile(t, path), []byte("custom")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := excludeDemoRuntimeFiles(dir); err != nil {
		t.Fatalf("second call: %v", err)
	}
	got := string(mustReadFile(t, path))
	want := ".datatug-environments.lock\n.datatug-environments-owner.json\ncustom\n"
	if got != want {
		t.Fatalf("exclude = %q, want %q", got, want)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if err := excludeDemoRuntimeFiles(dir); err == nil {
			t.Fatal("unreadable exclude file: want error")
		}
	}
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, ".git"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := excludeDemoRuntimeFiles(blocked); err == nil {
		t.Fatal(".git is a file: want error")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
