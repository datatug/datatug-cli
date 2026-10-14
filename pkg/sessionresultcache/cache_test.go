package sessionresultcache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/sessioncontext"
)

func TestPrivateDisplayCacheRestartIsolationAndTamper(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "display")
	cache, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Authority: "https://example.test|github|project|space", Actor: "alice", DatabaseID: "database", ProjectID: "project", SessionID: "session", RunID: "run", InputSnapshotID: "input", Scope: sessioncontext.Scope{EnvironmentID: "local", IdentityDomain: "domain"}, DefinitionDigest: strings.Repeat("a", 64)}
	if _, err = cache.Get(binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing cache: %v", err)
	}
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reading absent results created state")
	}
	if err = cache.Put(binding, json.RawMessage(`{ "rows": [[1,"private-value"]] }`)); err != nil {
		t.Fatal(err)
	}
	cache, err = New(root)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := cache.Get(binding)
	if err != nil || string(payload) != `{"rows":[[1,"private-value"]]}` {
		t.Fatalf("restart: %s %v", payload, err)
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory mode: %v %v", info, err)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 1 || strings.Contains(files[0].Name(), "private-value") {
		t.Fatalf("cache files: %v %v", files, err)
	}
	path := filepath.Join(root, files[0].Name())
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file mode: %v %v", info, err)
	}
	for _, modify := range []func(*Binding){func(b *Binding) { b.Actor = "bob" }, func(b *Binding) { b.DatabaseID = "other" }, func(b *Binding) { b.Scope.IdentityDomain = "other" }, func(b *Binding) { b.DefinitionDigest = strings.Repeat("b", 64) }} {
		other := binding
		modify(&other)
		if _, err = cache.Get(other); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("binding leaked: %v", err)
		}
	}
	if err = cache.Put(binding, json.RawMessage(`{"rows":"`+strings.Repeat("x", MaxBytes)+`"}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unbounded cache: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "private-value", "altered-value", 1))
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = cache.Get(binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("tamper: %v", err)
	}
	if err = cache.Put(binding, json.RawMessage(`{"rows":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = cache.Get(binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("public cache mode: %v", err)
	}
}

func TestPrivateDisplayCacheRejectsGitAndRedirectedDirectory(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(project, "results")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Git cache accepted: %v", err)
	}
	link := filepath.Join(t.TempDir(), "redirect")
	if err := os.Symlink(project, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(link, "results")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirected Git cache accepted: %v", err)
	}
}

func TestNewDefaultAndErrorBranches(t *testing.T) {
	// Not parallel: uses t.Setenv for DirEnv and HOME.
	customDir := filepath.Join(t.TempDir(), "custom-results")
	t.Setenv(DirEnv, customDir)
	if _, err := NewDefault(); err != nil {
		t.Fatalf("NewDefault with DirEnv: %v", err)
	}

	homeDir := t.TempDir()
	t.Setenv(DirEnv, "")
	t.Setenv("HOME", homeDir)
	if _, err := NewDefault(); err != nil {
		t.Fatalf("NewDefault with HOME: %v", err)
	}

	t.Setenv("HOME", "")
	if _, err := NewDefault(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("NewDefault without HOME: %v", err)
	}

	binding := Binding{
		Authority:        "https://example.test|github|project|space",
		Actor:            "alice",
		DatabaseID:       "database",
		ProjectID:        "project",
		SessionID:        "session",
		RunID:            "run",
		InputSnapshotID:  "input",
		Scope:            sessioncontext.Scope{EnvironmentID: "local", IdentityDomain: "domain"},
		DefinitionDigest: strings.Repeat("a", 64),
	}

	root := filepath.Join(t.TempDir(), "cache")
	cache, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Invalid binding in Put and Get.
	if err := cache.Put(Binding{}, json.RawMessage(`{}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for empty binding in Put, got %v", err)
	}
	if _, err := cache.Get(Binding{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for empty binding in Get, got %v", err)
	}

	// Invalid JSON in Put.
	if err := cache.Put(binding, json.RawMessage(`{invalid`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable for invalid JSON in Put, got %v", err)
	}

	// Put when target path is a directory (causes os.Rename to fail).
	targetPath, err := cache.path(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := cache.Put(binding, json.RawMessage(`{}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when target path is a directory, got %v", err)
	}
	if err := os.Remove(targetPath); err != nil {
		t.Fatal(err)
	}

	// Get when file is write-only (os.Lstat passes Perm()&0077 == 0, os.Open fails).
	if err := os.WriteFile(targetPath, []byte(`{}`), 0200); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when file cannot be opened for read, got %v", err)
	}
	_ = os.Chmod(targetPath, 0600)

	// Get when file exceeds MaxBytes.
	if err := os.WriteFile(targetPath, make([]byte, MaxBytes+2), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when file exceeds MaxBytes, got %v", err)
	}

	// MkdirAll failure when parent directory is read-only (0500).
	roParent := filepath.Join(t.TempDir(), "ro-parent")
	if err := os.MkdirAll(roParent, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roParent, 0700) })
	roCache, err := New(filepath.Join(roParent, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := roCache.Put(binding, json.RawMessage(`{}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when MkdirAll fails on 0500 parent, got %v", err)
	}

	// ResolvePrivateDirectory failure when parent is a regular file.
	badRootParent := filepath.Join(t.TempDir(), "file-parent")
	badCache, err := New(filepath.Join(badRootParent, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badRootParent, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := badCache.Put(binding, json.RawMessage(`{}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when ResolvePrivateDirectory fails, got %v", err)
	}
	if _, err := badCache.Get(binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when ResolvePrivateDirectory fails in Get, got %v", err)
	}

	// ResolvePrivateDirectory failure in Put (e.g. .git created in root).
	gitRoot := filepath.Join(t.TempDir(), "git-root")
	gitCache, err := New(gitRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(gitRoot, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := gitCache.Put(binding, json.RawMessage(`{}`)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable when .git exists in Put, got %v", err)
	}

	// syncDirectory on missing directory.
	if err := syncDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected error from syncDirectory on missing path")
	}
}
