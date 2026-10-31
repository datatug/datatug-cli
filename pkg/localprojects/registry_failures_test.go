package localprojects

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-core/pkg/datatug"
)

var errRegistryIO = errors.New("injected registry I/O error")

func preserveRegistryIO(t *testing.T) {
	t.Helper()
	old := registryIO
	t.Cleanup(func() { registryIO = old })
}
func failRegistryNth[A any](original func(A) ([]byte, error), nth int) func(A) ([]byte, error) {
	calls := 0
	return func(v A) ([]byte, error) {
		calls++
		if calls == nth {
			return nil, errRegistryIO
		}
		return original(v)
	}
}

type testRegistryLock struct {
	cancel context.CancelFunc
	locked bool
	err    error
}

func (l testRegistryLock) TryLockContext(context.Context, time.Duration) (bool, error) {
	if l.cancel != nil {
		l.cancel()
	}
	return l.locked, l.err
}
func (testRegistryLock) Close() error { return nil }
func closedRegistryFile(t *testing.T) *os.File {
	t.Helper()
	f, e := os.CreateTemp(t.TempDir(), "closed")
	if e != nil {
		t.Fatal(e)
	}
	_ = f.Close()
	return f
}
func minimalProject(t *testing.T, dir string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"id":"p","title":"Project"}`), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestRegistryOpenAndDiscoveryErrorsAreExplicit(t *testing.T) {
	for _, kind := range []string{"mkdir", "eval", "abs", "read-dir", "unrelated", "malformed", "mismatched", "duplicate", "list-discovery", "list-project"} {
		t.Run(kind, func(t *testing.T) {
			preserveRegistryIO(t)
			root := t.TempDir()
			configured := map[string]string{}
			switch kind {
			case "mkdir":
				registryIO.mkdirAll = func(string, os.FileMode) error { return errRegistryIO }
			case "eval":
				registryIO.eval = func(string) (string, error) { return "", errRegistryIO }
			case "abs":
				registryIO.abs = func(string) (string, error) { return "", errRegistryIO }
			case "read-dir":
				registryIO.readDir = func(string) ([]os.DirEntry, error) { return nil, errRegistryIO }
			case "unrelated":
				if e := os.Mkdir(filepath.Join(root, "unrelated"), 0700); e != nil {
					t.Fatal(e)
				}
			case "malformed", "mismatched", "duplicate":
				dir := filepath.Join(root, "p")
				if e := os.Mkdir(dir, 0700); e != nil {
					t.Fatal(e)
				}
				body := `{"id":"different"}`
				if kind == "malformed" {
					body = "{"
				}
				if kind == "duplicate" {
					body = `{"id":"p"}`
					configured["p"] = t.TempDir()
				}
				if e := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(body), 0600); e != nil {
					t.Fatal(e)
				}
			}
			r, e := Open(root, configured)
			switch kind {
			case "unrelated":
				if e != nil {
					t.Fatal(e)
				}
				return
			case "list-discovery":
				if e != nil {
					t.Fatal(e)
				}
				registryIO.readDir = func(string) ([]os.DirEntry, error) { return nil, errRegistryIO }
				_, e = r.List()
			case "list-project":
				if e != nil {
					t.Fatal(e)
				}
				r.dirs["p"] = t.TempDir()
				_, e = r.List()
			}
			if e == nil {
				t.Fatalf("%s failure disappeared", kind)
			}
		})
	}
}
func TestRegistryCreationNeverPublishesIncompleteProject(t *testing.T) {
	for _, kind := range []string{"discover", "configured-read", "open-root", "lstat", "random", "mkdir-stage", "mkdir-envs", "mkdir-env", "open-stage", "open-env", "open-envs", "invalid-clock", "write-project", "write-env", "sync-envs", "sync-stage", "git", "revoked-before-publish", "publish"} {
		t.Run(kind, func(t *testing.T) {
			preserveRegistryIO(t)
			root := t.TempDir()
			r, e := Open(root, nil)
			if e != nil {
				t.Fatal(e)
			}
			request := Create{Store: "local", ID: "p", Title: "Private project", OperationID: "create"}
			checks := []func() error{}
			originalMkdir, originalOpenRoot, originalOpenFile, originalOpen := registryIO.rootMkdir, registryIO.rootOpenRoot, registryIO.rootOpenFile, registryIO.rootOpen
			switch kind {
			case "discover":
				registryIO.readDir = func(string) ([]os.DirEntry, error) { return nil, errRegistryIO }
			case "configured-read":
				r.dirs["legacy"] = t.TempDir()
			case "open-root":
				registryIO.openRoot = func(string) (*os.Root, error) { return nil, errRegistryIO }
			case "lstat":
				registryIO.rootLstat = func(*os.Root, string) (os.FileInfo, error) { return nil, errRegistryIO }
			case "random":
				registryIO.read = func([]byte) (int, error) { return 0, errRegistryIO }
			case "mkdir-stage", "mkdir-envs", "mkdir-env":
				nth := map[string]int{"mkdir-stage": 1, "mkdir-envs": 2, "mkdir-env": 3}[kind]
				calls := 0
				registryIO.rootMkdir = func(r *os.Root, n string, m os.FileMode) error {
					calls++
					if calls == nth {
						return errRegistryIO
					}
					return originalMkdir(r, n, m)
				}
			case "open-stage", "open-env", "open-envs":
				nth := map[string]int{"open-stage": 1, "open-env": 2, "open-envs": 3}[kind]
				calls := 0
				registryIO.rootOpenRoot = func(r *os.Root, n string) (*os.Root, error) {
					calls++
					if calls == nth {
						return nil, errRegistryIO
					}
					return originalOpenRoot(r, n)
				}
			case "invalid-clock":
				registryIO.now = func() time.Time { return time.Time{} }
			case "write-project", "write-env":
				nth := map[string]int{"write-project": 1, "write-env": 2}[kind]
				calls := 0
				registryIO.rootOpenFile = func(r *os.Root, n string, f int, m os.FileMode) (*os.File, error) {
					if f&os.O_CREATE != 0 {
						calls++
						if calls == nth {
							return nil, errRegistryIO
						}
					}
					return originalOpenFile(r, n, f, m)
				}
			case "sync-envs", "sync-stage":
				nth := map[string]int{"sync-envs": 3, "sync-stage": 4}[kind]
				calls := 0
				registryIO.rootOpen = func(r *os.Root, n string) (*os.File, error) {
					calls++
					if calls == nth {
						return nil, errRegistryIO
					}
					return originalOpen(r, n)
				}
			case "git":
				registryIO.command = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
					return exec.CommandContext(ctx, "/nonexistent-datatug-test-git")
				}
			case "revoked-before-publish":
				calls := 0
				checks = []func() error{func() error {
					calls++
					if calls == 2 {
						return os.ErrPermission
					}
					return nil
				}}
			case "publish":
				originalRename := registryIO.rootRename
				registryIO.rootRename = func(r *os.Root, a, b string) error {
					if b == request.ID {
						return errRegistryIO
					}
					return originalRename(r, a, b)
				}
			}
			if _, e = r.Create(context.Background(), request, checks...); e == nil {
				t.Fatalf("%s failure acknowledged", kind)
			}
			if _, e = os.Stat(filepath.Join(root, request.ID)); !errors.Is(e, os.ErrNotExist) {
				t.Fatalf("incomplete project published for %s: %v", kind, e)
			}
		})
	}
}
func TestRegistryWireFormatsRejectBrokenNativeMetadata(t *testing.T) {
	var p Project
	if e := json.Unmarshal([]byte(`{"id":"p","title":"Project","archived":true}`), &p); e != nil || !p.Archived || p.ID != "p" {
		t.Fatalf("wire projection failed %+v %v", p, e)
	}
	for _, kind := range []string{"project-first", "project-second", "document-first", "document-second", "marshal-project", "marshal-fields", "marshal-creation"} {
		t.Run(kind, func(t *testing.T) {
			preserveRegistryIO(t)
			var e error
			switch kind {
			case "project-first", "project-second", "document-first", "document-second":
				calls := 0
				original := registryIO.unmarshal
				nth := 1
				if strings.HasSuffix(kind, "second") {
					nth = 2
				}
				registryIO.unmarshal = func(b []byte, v any) error {
					calls++
					if calls == nth {
						return errRegistryIO
					}
					return original(b, v)
				}
				if strings.HasPrefix(kind, "project") {
					e = p.UnmarshalJSON([]byte(`{"id":"p"}`))
				} else {
					var d projectDocument
					e = d.UnmarshalJSON([]byte(`{"id":"p"}`))
				}
			default:
				d := projectDocument{ProjectFile: datatug.ProjectFile{}, LocalCreation: &creation{OperationID: "op"}}
				switch kind {
				case "marshal-project":
					registryIO.marshal = failRegistryNth(registryIO.marshal, 1)
				case "marshal-fields":
					registryIO.unmarshal = func([]byte, any) error { return errRegistryIO }
				case "marshal-creation":
					registryIO.marshal = failRegistryNth(registryIO.marshal, 2)
				}
				_, e = d.MarshalJSON()
			}
			if e == nil {
				t.Fatal("invalid wire encoding was accepted")
			}
		})
	}
}
func TestRegistryMetadataIOFailuresAndCorruption(t *testing.T) {
	for _, kind := range []string{"read-open", "read-lstat", "read-directory", "read-large", "read-file", "read-body", "read-grown", "write-open", "write-marshal", "write-large", "write-random", "write-create", "write-body", "write-rename", "sync-open", "sync-closed", "windows"} {
		t.Run(kind, func(t *testing.T) {
			preserveRegistryIO(t)
			dir := t.TempDir()
			if e := os.WriteFile(filepath.Join(dir, "metadata"), []byte(`{}`), 0600); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "read-open", "write-open":
				registryIO.openRoot = func(string) (*os.Root, error) { return nil, errRegistryIO }
			case "read-lstat":
				registryIO.rootLstat = func(*os.Root, string) (os.FileInfo, error) { return nil, errRegistryIO }
			case "read-directory":
				if e := os.Mkdir(filepath.Join(dir, "directory"), 0700); e != nil {
					t.Fatal(e)
				}
				if e := readJSON(dir, "directory", new(any)); e == nil {
					t.Fatal("directory accepted")
				}
				return
			case "read-large":
				if e := os.WriteFile(filepath.Join(dir, "metadata"), []byte(strings.Repeat("x", maxMetadata+1)), 0600); e != nil {
					t.Fatal(e)
				}
			case "read-file", "write-create":
				registryIO.rootOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, errRegistryIO }
			case "read-body":
				registryIO.readAll = func(*os.File) ([]byte, error) { return nil, errRegistryIO }
			case "read-grown":
				registryIO.readAll = func(*os.File) ([]byte, error) { return make([]byte, maxMetadata+1), nil }
			case "write-marshal":
				registryIO.marshal = func(any) ([]byte, error) { return nil, errRegistryIO }
			case "write-large":
				registryIO.marshal = func(any) ([]byte, error) { return make([]byte, maxMetadata+1), nil }
			case "write-random":
				registryIO.read = func([]byte) (int, error) { return 0, errRegistryIO }
			case "write-body":
				registryIO.rootOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return closedRegistryFile(t), nil }
			case "write-rename":
				registryIO.rootRename = func(*os.Root, string, string) error { return errRegistryIO }
			case "sync-open":
				registryIO.rootOpen = func(*os.Root, string) (*os.File, error) { return nil, errRegistryIO }
			case "sync-closed":
				registryIO.rootOpen = func(*os.Root, string) (*os.File, error) { return closedRegistryFile(t), nil }
			case "windows":
				registryIO.platform = "windows"
			}
			var e error
			if strings.HasPrefix(kind, "read-") {
				e = readJSON(dir, "metadata", new(any))
			} else {
				e = writeJSON(dir, "metadata", map[string]string{"safe": "metadata"})
			}
			if kind == "windows" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatalf("%s failure disappeared", kind)
			}
		})
	}
}

func TestArchiveAdmissionAndPersistenceFailuresNeverHideProject(t *testing.T) {
	for _, kind := range []string{"invalid", "unknown", "project-read", "state-second-read", "capacity", "revoked", "persist"} {
		t.Run(kind, func(t *testing.T) {
			preserveRegistryIO(t)
			dir := t.TempDir()
			minimalProject(t, dir)
			r, e := Open("", map[string]string{"p": dir, "a": dir})
			if e != nil {
				t.Fatal(e)
			}
			request := Archive{Storage: "local", Project: "p", Archived: true, OperationID: "archive"}
			checks := []func() error{}
			switch kind {
			case "invalid":
				request.Storage = "github.com"
			case "unknown":
				request.Project = "unknown"
			case "project-read":
				if e = os.Remove(filepath.Join(dir, "datatug-project.json")); e != nil {
					t.Fatal(e)
				}
			case "state-second-read":
				calls := 0
				original := registryIO.rootLstat
				registryIO.rootLstat = func(r *os.Root, n string) (os.FileInfo, error) {
					if n == stateName {
						calls++
						if calls == 2 {
							return nil, errRegistryIO
						}
					}
					return original(r, n)
				}
			case "capacity":
				state := projectState{Version: 1, Operations: map[string]archiveReceipt{}}
				for i := range 10000 {
					state.Operations[strings.Repeat("x", i/1000)+string(rune(i%1000+1))] = archiveReceipt{}
				}
				if e = writeJSON(dir, stateName, state); e != nil {
					t.Fatal(e)
				}
			case "revoked":
				calls := 0
				checks = []func() error{func() error {
					calls++
					if calls == 2 {
						return os.ErrPermission
					}
					return nil
				}}
			case "persist":
				registryIO.rootRename = func(*os.Root, string, string) error { return errRegistryIO }
			}
			if _, e = r.SetArchived(context.Background(), request, checks...); e == nil {
				t.Fatal("failed archive acknowledged")
			}
			if kind != "project-read" {
				if _, e = r.List(); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestRegistryLockErrorsAndCancellationAreExplicit(t *testing.T) {
	for _, kind := range []string{"link", "lstat", "lock", "busy", "canceled-after-lock"} {
		t.Run(kind, func(t *testing.T) {
			preserveRegistryIO(t)
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "link":
				if e := os.Mkdir(filepath.Join(dir, "lock"), 0700); e != nil {
					t.Fatal(e)
				}
			case "lstat":
				registryIO.lstat = func(string) (os.FileInfo, error) { return nil, errRegistryIO }
			case "lock":
				registryIO.lock = func(string) registryLock { return testRegistryLock{err: errRegistryIO} }
			case "busy":
				registryIO.lock = func(string) registryLock { return testRegistryLock{} }
			case "canceled-after-lock":
				registryIO.lock = func(string) registryLock { return testRegistryLock{locked: true, cancel: cancel} }
			}
			called := false
			if e := withLock(ctx, dir, "lock", func() error { called = true; return nil }); e == nil || called {
				t.Fatalf("unsafe lock admitted: %v %v", e, called)
			}
		})
	}
}
