package projectvisibility

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errJournalIO = errors.New("injected journal I/O failure")

type failedCoordinatorLock struct {
	locked bool
	err    error
}

func (l failedCoordinatorLock) TryLockContext(context.Context, time.Duration) (bool, error) {
	return l.locked, l.err
}
func (failedCoordinatorLock) Close() error { return nil }
func preserveJournalOS(t *testing.T) {
	t.Helper()
	original := journalOS
	t.Cleanup(func() { journalOS = original })
}
func closedJournalFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}
func journalRoot(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestJournalReadsRejectCorruptionLinksAndDescriptorChanges(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "oversize", "open-error", "closed-descriptor", "bad-json", "trailing-json", "unknown-fields"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			root := journalRoot(t)
			name := "operation.json"
			body := []byte(`{"version":1}`)
			switch kind {
			case "missing":
			case "directory":
				if err := root.Mkdir(name, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				body = []byte(strings.Repeat("x", (1<<20)+1))
			case "bad-json":
				body = []byte("{")
			case "trailing-json":
				body = []byte(`{"version":1}{}`)
			case "unknown-fields":
				body = []byte(`{"unexpected":true}`)
			}
			if kind != "missing" && kind != "directory" {
				if err := root.WriteFile(name, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "open-error":
				journalOS.rootOpen = func(*os.Root, string) (*os.File, error) { return nil, errJournalIO }
			case "closed-descriptor":
				journalOS.rootOpen = func(*os.Root, string) (*os.File, error) { return closedJournalFile(t), nil }
			}
			var op operation
			if err := readOperation(root, name, &op); err == nil {
				t.Fatalf("unsafe %s journal accepted", kind)
			}
		})
	}
}

func TestJournalWritesSurfaceEveryDurabilityBoundary(t *testing.T) {
	for _, kind := range []string{"oversize", "open-directory", "read-directory", "capacity", "create-file", "write-file", "rename", "sync-directory", "windows"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			root := journalRoot(t)
			op := operation{Version: 1}
			originalOpen := journalOS.rootOpen
			switch kind {
			case "oversize":
				op.Digest = strings.Repeat("d", 1<<20)
			case "open-directory":
				journalOS.rootOpen = func(*os.Root, string) (*os.File, error) { return nil, errJournalIO }
			case "read-directory":
				journalOS.readNames = func(*os.File, int) ([]string, error) { return nil, errJournalIO }
			case "capacity":
				journalOS.readNames = func(*os.File, int) ([]string, error) { return make([]string, 10003), nil }
			case "create-file":
				journalOS.rootOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, errJournalIO }
			case "write-file":
				journalOS.rootOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return closedJournalFile(t), nil }
			case "rename":
				journalOS.rootRename = func(*os.Root, string, string) error { return errJournalIO }
			case "sync-directory":
				calls := 0
				journalOS.rootOpen = func(r *os.Root, name string) (*os.File, error) {
					calls++
					if calls == 2 {
						return nil, errJournalIO
					}
					return originalOpen(r, name)
				}
			case "windows":
				journalOS.platform = "windows"
			}
			err := writeOperation(root, "operation.json", op)
			if kind == "windows" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("%s journal error disappeared", kind)
			}
		})
	}
}

func TestJournalCompletionKeepsPendingReceiptUntilDurableRename(t *testing.T) {
	for _, kind := range []string{"already-done", "rename-error", "directory-error", "windows"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			root := journalRoot(t)
			name := "receipt.json"
			if err := root.WriteFile(name, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "already-done":
				name = "receipt.done.json"
			case "rename-error":
				journalOS.rootRename = func(*os.Root, string, string) error { return errJournalIO }
			case "directory-error":
				journalOS.rootOpen = func(*os.Root, string) (*os.File, error) { return nil, errJournalIO }
			case "windows":
				journalOS.platform = "windows"
			}
			err := completeOperation(root, name)
			if kind == "already-done" || kind == "windows" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failed completion acknowledged")
			}
		})
	}
}

func TestPrivateDirectoryCreationValidatesIdentityAndDurability(t *testing.T) {
	for _, kind := range []string{"resolve-error", "ancestor-link", "lstat-error", "depth", "mkdir-error", "final-lstat-error", "final-not-directory", "open-error", "identity-change", "chmod-error", "sync-open-error", "sync-error", "windows"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(base, "new-private", "namespace")
			originalLstat, originalMkdir, originalOpen := journalOS.lstat, journalOS.mkdirAll, journalOS.open
			switch kind {
			case "resolve-error":
				if err = os.WriteFile(filepath.Join(base, ".git"), []byte("marker"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ancestor-link":
				outside := t.TempDir()
				if err = os.Symlink(outside, filepath.Join(base, "new-private")); err != nil {
					t.Fatal(err)
				}
			case "lstat-error":
				journalOS.lstat = func(string) (os.FileInfo, error) { return nil, errJournalIO }
			case "depth":
				journalOS.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			case "mkdir-error":
				journalOS.mkdirAll = func(string, os.FileMode) error { return errJournalIO }
			case "final-lstat-error":
				made := false
				journalOS.mkdirAll = func(path string, mode os.FileMode) error { made = true; return originalMkdir(path, mode) }
				journalOS.lstat = func(path string) (os.FileInfo, error) {
					if made {
						return nil, errJournalIO
					}
					return originalLstat(path)
				}
			case "final-not-directory":
				journalOS.mkdirAll = func(path string, mode os.FileMode) error {
					if err := originalMkdir(filepath.Dir(path), mode); err != nil {
						return err
					}
					return os.WriteFile(path, []byte("file"), 0600)
				}
			case "open-error":
				journalOS.open = func(string) (*os.File, error) { return nil, errJournalIO }
			case "identity-change":
				other := t.TempDir()
				journalOS.open = func(string) (*os.File, error) { return os.Open(other) }
			case "chmod-error":
				journalOS.chmod = func(*os.File, os.FileMode) error { return errJournalIO }
			case "sync-open-error":
				calls := 0
				journalOS.open = func(path string) (*os.File, error) {
					calls++
					if calls == 2 {
						return nil, errJournalIO
					}
					return originalOpen(path)
				}
			case "sync-error":
				calls := 0
				journalOS.open = func(path string) (*os.File, error) {
					calls++
					if calls == 2 {
						return closedJournalFile(t), nil
					}
					return originalOpen(path)
				}
			case "windows":
				journalOS.platform = "windows"
			}
			err = durablePrivateDirectory(path)
			if kind == "windows" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("unsafe/private directory %s accepted", kind)
			}
		})
	}
}

func TestJournalAdmissionFailureNeverStartsNativeRestore(t *testing.T) {
	for _, kind := range []string{"mkdir", "lock-error", "lock-busy", "open-root", "acceptance-write", "directory-swapped-to-git"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			create, state, scope := nativeStoreFixture(t)
			store := create()
			write := restoreWrite(t, store, scope)
			switch kind {
			case "mkdir":
				journalOS.mkdirAll = func(string, os.FileMode) error { return errJournalIO }
			case "lock-error":
				journalOS.lock = func(string) coordinatorLocker { return failedCoordinatorLock{err: errJournalIO} }
			case "lock-busy":
				journalOS.lock = func(string) coordinatorLocker { return failedCoordinatorLock{} }
			case "open-root":
				journalOS.openRoot = func(string) (*os.Root, error) { return nil, errJournalIO }
			case "acceptance-write":
				journalOS.rootOpenFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) { return nil, errJournalIO }
			case "directory-swapped-to-git":
				original := journalOS.chmod
				journalOS.chmod = func(file *os.File, mode os.FileMode) error {
					if err := original(file, mode); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(store.private, ".git"), []byte("marker"), 0600)
				}
			}
			if _, err := store.ChangeProjectLifecycle(context.Background(), scope, write); err == nil {
				t.Fatal("failed admission acknowledged")
			}
			if state.restores != 0 || !state.archived {
				t.Fatal("native effect started before durable safe admission")
			}
		})
	}
}

func TestPrivateDirectoryRejectsAncestorSwapDuringCreationBeforeChmod(t *testing.T) {
	preserveJournalOS(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(base, "private")
	if err = os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err = os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(outside, "namespace")
	if err = os.Mkdir(foreign, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(foreign, "retained")
	if err = os.WriteFile(marker, []byte("outside bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(foreign)
	if err != nil {
		t.Fatal(err)
	}
	originalMkdir := journalOS.mkdirAll
	journalOS.mkdirAll = func(path string, mode os.FileMode) error {
		if err := os.Rename(parent, parent+"-original"); err != nil {
			return err
		}
		if err := os.Symlink(outside, parent); err != nil {
			return err
		}
		return originalMkdir(path, mode)
	}
	if err = durablePrivateDirectory(filepath.Join(parent, "namespace")); err == nil {
		t.Fatal("mid-creation ancestor swap accepted")
	}
	after, err := os.Stat(foreign)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(marker)
	if err != nil || string(bytes) != "outside bytes" || before.Mode() != after.Mode() {
		t.Fatalf("foreign directory changed: %v->%v %q %v", before.Mode(), after.Mode(), bytes, err)
	}
}
func TestPrivateDirectoryRechecksPathIdentityImmediatelyBeforeChmod(t *testing.T) {
	for _, kind := range []string{"missing-path", "changed-inode"} {
		t.Run(kind, func(t *testing.T) {
			preserveJournalOS(t)
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(base, "private")
			original := journalOS.lstat
			calls := 0
			journalOS.lstat = func(p string) (os.FileInfo, error) {
				calls++
				if calls == 4 {
					if kind == "missing-path" {
						return nil, errJournalIO
					}
					return os.Stat(base)
				}
				return original(p)
			}
			if err = durablePrivateDirectory(path); err == nil {
				t.Fatal("changed path identity accepted")
			}
		})
	}
}
