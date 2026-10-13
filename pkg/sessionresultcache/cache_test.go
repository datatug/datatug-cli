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
