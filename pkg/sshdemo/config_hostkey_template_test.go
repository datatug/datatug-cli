package sshdemo

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/demofixtures"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gossh "golang.org/x/crypto/ssh"
)

func TestConfig_DefaultsAndValidate(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Listen != DefaultListen || cfg.MaxSessions != DefaultMaxSessions || cfg.Isolation != IsolationUID {
		t.Fatalf("unexpected DefaultConfig: %+v", cfg)
	}
	for _, c := range []Config{cfg, (Config{}).WithDefaults()} {
		if c.ShellStartTimeout != 30*time.Second || c.WatchdogInterval != 2*time.Second ||
			c.SessionMemQuota != 1<<30 || c.SessionDiskQuota != 256<<20 {
			t.Fatalf("unexpected pre-shell and quota defaults: %+v", c)
		}
	}
	if got := (Config{StateDir: "/tmp/s"}).ResolvedHostKey(); got != "/tmp/s/ssh_host_ed25519_key" {
		t.Fatalf("ResolvedHostKey = %q", got)
	}
	if err := (Config{}).WithDefaults().Validate(); err != nil {
		t.Fatalf("default config should validate: %v", err)
	}

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"bad-max-sessions", func(c *Config) { c.MaxSessions = -1 }, "max-sessions"},
		{"bad-max-per-ip", func(c *Config) { c.MaxSessionsPerIP = -1 }, "max-sessions-per-ip"},
		{"neg-idle", func(c *Config) { c.IdleTimeout = -time.Second }, "idle-timeout"},
		{"neg-max-dur", func(c *Config) { c.MaxSessionDuration = -time.Second }, "max-session-duration"},
		{"neg-handshake", func(c *Config) { c.HandshakeTimeout = -time.Second }, "handshake-timeout"},
		{"neg-shell-start", func(c *Config) { c.ShellStartTimeout = -time.Second }, "shell-start-timeout"},
		{"neg-watchdog", func(c *Config) { c.WatchdogInterval = -time.Second }, "watchdog interval"},
		{"empty-project-dir", func(c *Config) { c.ProjectDir = "" }, "project-dir"},
		{"abs-project-dir", func(c *Config) { c.ProjectDir = "/etc" }, "must be relative"},
		{"escape-project-dir", func(c *Config) { c.ProjectDir = "../escape" }, "escapes template"},
		{"bad-uid-base", func(c *Config) { c.UIDBase = -1 }, "uid-base"},
		{"invalid-isolation", func(c *Config) { c.Isolation = "magic" }, "invalid isolation"},
		{"process-non-loopback", func(c *Config) {
			c.Isolation = IsolationProcess
			c.Listen = ":2222"
			c.AllowInsecureIsolation = false
		}, "allow-insecure-isolation"},
		{"process-invalid-addr", func(c *Config) {
			c.Isolation = IsolationProcess
			c.Listen = "not-an-addr"
		}, "allow-insecure-isolation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}

	// Process isolation allowed on loopback addresses and with AllowInsecureIsolation.
	for _, listen := range []string{"127.0.0.1:0", "[::1]:2222", "localhost:2222"} {
		c := DefaultConfig()
		c.Isolation = IsolationProcess
		c.Listen = listen
		if err := c.Validate(); err != nil {
			t.Fatalf("process isolation on %s should validate: %v", listen, err)
		}
	}
	c := DefaultConfig()
	c.Isolation = IsolationProcess
	c.Listen = ":2222"
	c.AllowInsecureIsolation = true
	if err := c.Validate(); err != nil {
		t.Fatalf("process isolation with AllowInsecureIsolation should validate: %v", err)
	}
}

func TestHostKey_GeneratePersistAndRefuseInsecure(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "sub", "ssh_host_ed25519_key")

	// Empty path refused.
	if _, err := LoadOrGenerateHostKey(""); err == nil {
		t.Fatal("expected error for empty host key path")
	}

	// Generates once with 0600 permissions.
	s1, err := LoadOrGenerateHostKey(keyPath)
	if err != nil {
		t.Fatalf("first LoadOrGenerateHostKey: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key perm = %04o, want 0600", perm)
	}
	fp1 := gossh.FingerprintSHA256(s1.PublicKey())

	// Second load returns the exact same key (never overwritten).
	s2, err := LoadOrGenerateHostKey(keyPath)
	if err != nil {
		t.Fatalf("second LoadOrGenerateHostKey: %v", err)
	}
	fp2 := gossh.FingerprintSHA256(s2.PublicKey())
	if fp1 != fp2 {
		t.Fatalf("host key changed across loads: %s != %s", fp1, fp2)
	}

	// PrintHostKey prints authorized-key line and SHA256 fingerprint.
	var buf bytes.Buffer
	if err := PrintHostKey(keyPath, &buf); err != nil {
		t.Fatalf("PrintHostKey: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "ssh-ed25519 ") || lines[1] != fp1 {
		t.Fatalf("unexpected PrintHostKey output: %q", buf.String())
	}

	// Refuses 0644 key without overwriting it.
	rawBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod 0644: %v", err)
	}
	if _, err := LoadOrGenerateHostKey(keyPath); err == nil || !strings.Contains(err.Error(), "insecure permissions") {
		t.Fatalf("expected insecure permissions error, got %v", err)
	}
	if err := PrintHostKey(keyPath, &buf); err == nil {
		t.Fatal("PrintHostKey should fail on 0644 key")
	}
	rawAfter, _ := os.ReadFile(keyPath)
	if !bytes.Equal(rawBefore, rawAfter) {
		t.Fatal("host key file was modified on insecure permission refusal")
	}

	// Refuses invalid PEM without overwriting it.
	badKeyPath := filepath.Join(dir, "bad_key")
	badContent := []byte("not-a-valid-private-key")
	if err := os.WriteFile(badKeyPath, badContent, 0o600); err != nil {
		t.Fatalf("write bad key: %v", err)
	}
	if _, err := LoadOrGenerateHostKey(badKeyPath); err == nil || !strings.Contains(err.Error(), "parse host key") {
		t.Fatalf("expected parse error, got %v", err)
	}
	gotBad, _ := os.ReadFile(badKeyPath)
	if !bytes.Equal(badContent, gotBad) {
		t.Fatal("invalid host key file was overwritten")
	}

	// Refuses a directory path.
	if _, err := LoadOrGenerateHostKey(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular file error, got %v", err)
	}

	// Refuses exclusive link collision (never overwrites an existing file).
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	block, _ := gossh.MarshalPrivateKey(priv, "")
	if err := writeFileExclusive0600(badKeyPath, pem.EncodeToMemory(block)); err == nil {
		t.Fatal("writeFileExclusive0600 must refuse to overwrite existing file")
	}
}

// initTestGitRepo creates a local go-git repository in dir with the given files
// and returns the repository plus its initial commit hash.
func initTestGitRepo(t *testing.T, dir string, files map[string][]byte) (*git.Repository, plumbing.Hash) {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git.PlainInit: %v", err)
	}
	h := commitFilesToRepo(t, repo, dir, files, "initial commit")
	return repo, h
}

func commitFilesToRepo(t *testing.T, repo *git.Repository, dir string, files map[string][]byte, msg string) plumbing.Hash {
	t.Helper()
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("git add %s: %v", rel, err)
		}
	}
	h, err := wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("git commit: %v", err)
	}
	return h
}

func TestPrepareTemplate_CloneChinookRefuseMissingAndAtomicReplace(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	stateDir := t.TempDir()

	repo, _ := initTestGitRepo(t, repoDir, map[string][]byte{
		"demo-project-1/datatug-project.json": []byte(`{"id":"demo","version":"v1"}`),
		"demo-project-1/query.sql":            []byte(`SELECT 1;`),
	})

	// 1. Clone using default embedded Chinook fixture from pkg/demofixtures.
	tmplDir, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:   stateDir,
		Repo:       repoDir,
		ProjectDir: "demo-project-1",
	})
	if err != nil {
		t.Fatalf("PrepareTemplate v1: %v", err)
	}
	if tmplDir != filepath.Join(stateDir, "template") {
		t.Fatalf("unexpected template dir: %s", tmplDir)
	}

	// Verify Chinook bytes match demofixtures pinned size and permissions are world-readable.
	chinookPath := filepath.Join(tmplDir, "demo-project-1", ".demo-data", "chinook.sqlite")
	ci, err := os.Stat(chinookPath)
	if err != nil {
		t.Fatalf("stat chinook: %v", err)
	}
	if ci.Size() != demofixtures.ChinookSize {
		t.Fatalf("chinook size = %d, want %d", ci.Size(), demofixtures.ChinookSize)
	}
	if ci.Mode().Perm() != 0o644 {
		t.Fatalf("chinook mode = %04o, want 0644", ci.Mode().Perm())
	}
	di, err := os.Stat(filepath.Join(tmplDir, "demo-project-1"))
	if err != nil || di.Mode().Perm() != 0o755 {
		t.Fatalf("project dir mode = %v / %v, want 0755", di, err)
	}
	// .git must be present in template.
	if _, err := os.Stat(filepath.Join(tmplDir, ".git")); err != nil {
		t.Fatalf("template missing .git: %v", err)
	}

	// 2. Atomic replace: commit v2, also test explicit branch ref and existing chinook preservation.
	commitFilesToRepo(t, repo, repoDir, map[string][]byte{
		"demo-project-1/datatug-project.json":      []byte(`{"id":"demo","version":"v2"}`),
		"demo-project-1/.demo-data/chinook.sqlite": []byte("preexisting-sqlite"),
	}, "v2 with custom chinook")

	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	tmplDir2, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:   stateDir,
		Repo:       "file://" + repoDir,
		Ref:        head.Name().Short(),
		ProjectDir: "demo-project-1",
	})
	if err != nil {
		t.Fatalf("PrepareTemplate v2: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(tmplDir2, "demo-project-1", "datatug-project.json"))
	if err != nil || !strings.Contains(string(manifest), `"v2"`) {
		t.Fatalf("template was not atomically replaced: %q (%v)", manifest, err)
	}
	chinook2, err := os.ReadFile(filepath.Join(tmplDir2, "demo-project-1", ".demo-data", "chinook.sqlite"))
	if err != nil || string(chinook2) != "preexisting-sqlite" {
		t.Fatalf("existing chinook should not be overwritten: %q (%v)", chinook2, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "template.tmp")); !os.IsNotExist(err) {
		t.Fatalf("template.tmp should not remain after success")
	}
	if _, err := os.Stat(filepath.Join(stateDir, "template.old")); !os.IsNotExist(err) {
		t.Fatalf("template.old should not remain after success")
	}

	// 3. Full refs/heads/... ref also works.
	if _, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:   stateDir,
		Repo:       repoDir,
		Ref:        string(head.Name()),
		ProjectDir: "demo-project-1",
	}); err != nil {
		t.Fatalf("PrepareTemplate with full ref: %v", err)
	}

	// 4. Missing datatug-project.json is refused AND does not clobber the existing template.
	badRepo := t.TempDir()
	initTestGitRepo(t, badRepo, map[string][]byte{
		"demo-project-1/README.md": []byte("no project json"),
	})
	if _, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:   stateDir,
		Repo:       badRepo,
		ProjectDir: "demo-project-1",
	}); err == nil || !strings.Contains(err.Error(), "datatug-project.json") {
		t.Fatalf("expected missing datatug-project.json error, got %v", err)
	}
	// Previous v2 template must remain intact.
	manifestAfter, err := os.ReadFile(filepath.Join(tmplDir2, "demo-project-1", "datatug-project.json"))
	if err != nil || !strings.Contains(string(manifestAfter), `"v2"`) {
		t.Fatalf("failed PrepareTemplate damaged previous template: %q (%v)", manifestAfter, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "template.tmp")); !os.IsNotExist(err) {
		t.Fatal("failed PrepareTemplate left template.tmp behind")
	}

	// 5. Validation errors.
	if _, err := PrepareTemplate(ctx, TemplateConfig{Repo: repoDir, ProjectDir: "demo-project-1"}); err == nil {
		t.Fatal("expected error for empty StateDir")
	}
	if _, err := PrepareTemplate(ctx, TemplateConfig{StateDir: stateDir, ProjectDir: "demo-project-1"}); err == nil {
		t.Fatal("expected error for empty Repo")
	}
	if _, err := PrepareTemplate(ctx, TemplateConfig{StateDir: stateDir, Repo: repoDir, ProjectDir: "../bad"}); err == nil {
		t.Fatal("expected error for invalid ProjectDir")
	}
	if _, err := PrepareTemplate(ctx, TemplateConfig{StateDir: stateDir, Repo: filepath.Join(t.TempDir(), "missing"), ProjectDir: "demo-project-1"}); err == nil {
		t.Fatal("expected clone error for missing repo")
	}
	if !isHTTPURL("https://github.com/a/b") || !isHTTPURL("HTTP://example.com/a") || isHTTPURL("/tmp/repo") {
		t.Fatal("unexpected isHTTPURL results")
	}

	// Directory named datatug-project.json is refused as non-regular file.
	dirManifestRepo := t.TempDir()
	initTestGitRepo(t, dirManifestRepo, map[string][]byte{
		"demo-project-1/datatug-project.json/nested": []byte("x"),
	})
	if _, err := PrepareTemplate(ctx, TemplateConfig{
		StateDir:   stateDir,
		Repo:       dirManifestRepo,
		ProjectDir: "demo-project-1",
	}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular file error, got %v", err)
	}

	// chmodWorldReadable skips symlinks cleanly.
	symRoot := t.TempDir()
	_ = os.WriteFile(filepath.Join(symRoot, "real.txt"), []byte("hi"), 0o600)
	_ = os.Symlink(filepath.Join(symRoot, "real.txt"), filepath.Join(symRoot, "link.txt"))
	if err := chmodWorldReadable(symRoot); err != nil {
		t.Fatalf("chmodWorldReadable with symlink: %v", err)
	}

	// replaceDirAtomic error when source is missing.
	if err := replaceDirAtomic(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dst"), filepath.Join(t.TempDir(), "old")); err == nil {
		t.Fatal("expected error for missing tmpDir in replaceDirAtomic")
	}
}
