package queryresults

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// swap sets *target to value for the rest of the test.
func swap[T any](t *testing.T, target *T, value T) {
	t.Helper()
	previous := *target
	*target = value
	t.Cleanup(func() { *target = previous })
}

var errBoom = errors.New("boom")

func reasonIs(t *testing.T, err error, want string) {
	t.Helper()
	if got := ReasonOf(err); got != want {
		t.Fatalf("reason = %q (%v), want %q", got, err, want)
	}
	if err != nil && (strings.Contains(err.Error(), "/") || strings.Contains(err.Error(), string(filepath.Separator))) {
		t.Fatalf("error %q names a path", err)
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), ".datatug", "query-results"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// writeResult writes a complete result with a given creation time.
func writeResult(t *testing.T, store *Store, id string, created time.Time, data string) {
	t.Helper()
	swap(t, &now, func() time.Time { return created })
	result, err := store.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	meta := NewMeta(id, "test", Data{Bytes: []byte(data)})
	if err := result.Write([]byte(data), meta.Encode()); err != nil {
		t.Fatal(err)
	}
	now = time.Now
}

func TestErrorAndID(t *testing.T) {
	reasonIs(t, fail(ReasonOwner), ReasonOwner)
	reasonIs(t, errBoom, "")
	if (&Error{Reason: ReasonSymlink}).Error() != "query results: symlink" {
		t.Fatal("error text")
	}
	for id, valid := range map[string]bool{"t1": true, "toolu_01-AB": true, strings.Repeat("a", 128): true, "": false, "../x": false, "a b": false, strings.Repeat("a", 129): false, "x.y": false} {
		if ValidID(id) != valid {
			t.Errorf("ValidID(%q) != %v", id, valid)
		}
	}
}

func TestDefaultRoot(t *testing.T) {
	home := t.TempDir()
	swap(t, &userHomeDir, func() (string, error) { return home, nil })
	root, err := DefaultRoot()
	if err != nil || root != filepath.Join(home, ".datatug", "query-results") {
		t.Fatalf("root %q %v", root, err)
	}
	store, err := OpenDefault()
	if err != nil || !strings.HasSuffix(store.Root(), filepath.Join(".datatug", "query-results")) {
		t.Fatalf("OpenDefault %v %v", store, err)
	}
	swap(t, &userHomeDir, func() (string, error) { return "", nil })
	_, err = OpenDefault()
	reasonIs(t, err, ReasonUnwritable)
	swap(t, &userHomeDir, func() (string, error) { return "", errBoom })
	_, err = DefaultRoot()
	reasonIs(t, err, ReasonUnwritable)
}

func TestOpenRefusals(t *testing.T) {
	base := t.TempDir()

	// a working tree
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Open(filepath.Join(repo, ".datatug", "query-results"))
	reasonIs(t, err, ReasonInsideGit)

	// a bare repository
	bare := filepath.Join(base, "bare")
	for _, marker := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(bare, marker), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bare, "HEAD"), []byte("ref"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Open(filepath.Join(bare, "query-results"))
	reasonIs(t, err, ReasonInsideGit)

	// a symlinked ancestor that leads into a working tree
	link := filepath.Join(base, "home-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	_, err = Open(filepath.Join(link, "query-results"))
	reasonIs(t, err, ReasonInsideGit)

	// a symlinked root
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(base, "rootlink")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Fatal(err)
	}
	_, err = Open(rootLink)
	reasonIs(t, err, ReasonSymlink)

	// a root that is a file
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Open(file)
	reasonIs(t, err, ReasonUnwritable)

	// a root owned by another user
	swap(t, &ownedByUser, func(os.FileInfo) bool { return false })
	_, err = Open(target)
	reasonIs(t, err, ReasonOwner)
	ownedByUser = isOwnedByCurrentUser

	// the path cannot be made absolute
	swap(t, &filepathAbs, func(string) (string, error) { return "", errBoom })
	_, err = Open("relative")
	reasonIs(t, err, ReasonUnwritable)
	filepathAbs = filepath.Abs

	// the Git check cannot inspect the path
	swap(t, &osLstat, func(string) (os.FileInfo, error) { return nil, os.ErrPermission })
	_, err = Open(target)
	reasonIs(t, err, ReasonUnwritable)
}

func TestResolveExisting(t *testing.T) {
	base := t.TempDir()
	resolvedBase, _ := filepath.EvalSymlinks(base)
	if got := resolveExisting(filepath.Join(base, "a", "b")); got != filepath.Join(resolvedBase, "a", "b") {
		t.Fatalf("resolveExisting = %q", got)
	}
	swap(t, &evalSymlinks, func(string) (string, error) { return "", errBoom })
	if got := resolveExisting(filepath.Join(base, "a")); got != filepath.Join(base, "a") {
		t.Fatalf("resolveExisting on failure = %q", got)
	}
	swap(t, &osLstat, func(string) (os.FileInfo, error) { return nil, os.ErrNotExist })
	if got := resolveExisting("/x/y"); got != "/x/y" {
		t.Fatalf("resolveExisting to the top = %q", got)
	}
}

func TestCheckAndEnsure(t *testing.T) {
	store := newStore(t)
	if err := store.Check(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{store.Root(), filepath.Dir(store.Root())} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode %v %v", dir, info.Mode(), err)
		}
	}
	entries, _ := os.ReadDir(store.Root())
	if len(entries) != 0 {
		t.Fatalf("check left %v", entries)
	}
	// an existing root with a wider mode is narrowed
	if err := os.Chmod(store.Root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Check(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(store.Root()); info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", info.Mode())
	}
}

func TestEnsureFailures(t *testing.T) {
	cases := map[string]struct {
		setup  func(t *testing.T, store *Store)
		reason string
	}{
		"parent cannot be made": {func(t *testing.T, _ *Store) {
			swap(t, &osMkdirAll, func(string, os.FileMode) error { return errBoom })
		}, ReasonUnwritable},
		"root cannot be made": {func(t *testing.T, _ *Store) {
			swap(t, &osMkdir, func(string, os.FileMode) error { return errBoom })
		}, ReasonUnwritable},
		"root cannot be inspected": {func(t *testing.T, _ *Store) {
			swap(t, &osLstat, func(string) (os.FileInfo, error) { return nil, errBoom })
		}, ReasonUnwritable},
		"root became a link": {func(t *testing.T, store *Store) {
			if err := os.MkdirAll(filepath.Dir(store.Root()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), store.Root()); err != nil {
				t.Fatal(err)
			}
		}, ReasonSymlink},
		"root became a file": {func(t *testing.T, store *Store) {
			if err := os.MkdirAll(filepath.Dir(store.Root()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Root(), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, ReasonUnwritable},
		"root owned by another user": {func(t *testing.T, _ *Store) {
			swap(t, &ownedByUser, func(os.FileInfo) bool { return false })
		}, ReasonOwner},
		"root mode cannot be narrowed": {func(t *testing.T, store *Store) {
			if err := os.MkdirAll(store.Root(), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(store.Root(), 0o755); err != nil {
				t.Fatal(err)
			}
			swap(t, &osChmod, func(string, os.FileMode) error { return errBoom })
		}, ReasonUnwritable},
		"probe cannot be created": {func(t *testing.T, _ *Store) {
			swap(t, &osCreateTemp, func(string, string) (*os.File, error) { return nil, errBoom })
		}, ReasonUnwritable},
		"probe cannot be removed": {func(t *testing.T, _ *Store) {
			swap(t, &osRemove, func(string) error { return errBoom })
		}, ReasonUnwritable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			tc.setup(t, store)
			reasonIs(t, store.Check(), tc.reason)
		})
	}
}

func TestCreateWriteLocate(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "t1", time.Now(), `{"rows":[]}`)
	dir := filepath.Join(store.Root(), "t1")
	for path, mode := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, DataFile): 0o600, filepath.Join(dir, MetaFile): 0o600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", path, info, err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("temporary files left: %v", entries)
	}
	locator, err := store.Locate("t1")
	if err != nil {
		t.Fatal(err)
	}
	if locator != (Locator{Version: 1, ID: "t1", Root: store.Root(), Data: filepath.Join(dir, DataFile), Meta: filepath.Join(dir, MetaFile)}) {
		t.Fatalf("locator %+v", locator)
	}
	_, err = store.Create("t1")
	reasonIs(t, err, ReasonIDInUse)
	_, err = store.Create("../x")
	reasonIs(t, err, ReasonBadID)
}

func TestCreateFailures(t *testing.T) {
	store := newStore(t)
	swap(t, &osCreateTemp, func(string, string) (*os.File, error) { return nil, errBoom })
	_, err := store.Create("a")
	reasonIs(t, err, ReasonUnwritable)
	osCreateTemp = os.CreateTemp

	calls := 0
	swap(t, &osMkdir, func(name string, mode os.FileMode) error {
		calls++
		if calls == 2 {
			return errBoom
		}
		return os.Mkdir(name, mode)
	})
	_, err = store.Create("b")
	reasonIs(t, err, ReasonUnwritable)
	osMkdir = os.Mkdir

	calls = 0
	swap(t, &osChmod, func(name string, mode os.FileMode) error {
		if filepath.Base(name) == "c" {
			return errBoom
		}
		return os.Chmod(name, mode)
	})
	_, err = store.Create("c")
	reasonIs(t, err, ReasonUnwritable)
	if _, statErr := os.Lstat(filepath.Join(store.Root(), "c")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("a failed create left its directory")
	}
}

func TestWriteFailures(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"temporary file": func(t *testing.T) {
			swap(t, &osCreateTemp, func(dir, pattern string) (*os.File, error) {
				if strings.Contains(pattern, DataFile) {
					return nil, errBoom
				}
				return os.CreateTemp(dir, pattern)
			})
		},
		"sync": func(t *testing.T) { swap(t, &syncFile, func(*os.File) error { return errBoom }) },
		"rename": func(t *testing.T) {
			swap(t, &osRename, func(string, string) error { return errBoom })
		},
		"directory sync": func(t *testing.T) { swap(t, &syncDir, func(string) error { return errBoom }) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			result, err := store.Create("r")
			if err != nil {
				t.Fatal(err)
			}
			setup(t)
			reasonIs(t, result.Write([]byte("{}"), []byte("{}")), ReasonUnwritable)
			entries, _ := os.ReadDir(filepath.Join(store.Root(), "r"))
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".tmp") {
					t.Fatalf("temporary file left: %s", entry.Name())
				}
			}
			result.Discard()
			if _, err := os.Lstat(filepath.Join(store.Root(), "r")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("discard left the directory")
			}
		})
	}
	// meta.json is written only after data.json
	store := newStore(t)
	result, _ := store.Create("m")
	swap(t, &osCreateTemp, func(dir, pattern string) (*os.File, error) {
		if strings.Contains(pattern, MetaFile) {
			return nil, errBoom
		}
		return os.CreateTemp(dir, pattern)
	})
	reasonIs(t, result.Write([]byte("{}"), []byte("{}")), ReasonUnwritable)
	if _, err := os.Lstat(filepath.Join(store.Root(), "m", DataFile)); err != nil {
		t.Fatal("data.json must be in place before meta.json")
	}
	_, err := store.Locate("m")
	reasonIs(t, err, ReasonIncomplete)
}

func TestSyncDir(t *testing.T) {
	if err := syncDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := syncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("syncing a missing directory succeeded")
	}
}

func TestLocateStates(t *testing.T) {
	store := newStore(t)
	_, err := store.Locate("nope")
	reasonIs(t, err, ReasonNotFound)
	_, err = store.Locate("../etc")
	reasonIs(t, err, ReasonBadID)

	writeResult(t, store, "old", time.Now().Add(-8*24*time.Hour), "{}")
	_, err = store.Locate("old")
	reasonIs(t, err, ReasonExpired)

	writeResult(t, store, "short", time.Now(), "{}")
	if err := os.WriteFile(filepath.Join(store.Root(), "short", DataFile), []byte("{ }"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.Locate("short")
	reasonIs(t, err, ReasonIncomplete)

	writeResult(t, store, "nodata", time.Now(), "{}")
	_ = os.Remove(filepath.Join(store.Root(), "nodata", DataFile))
	_, err = store.Locate("nodata")
	reasonIs(t, err, ReasonIncomplete)

	dir := filepath.Join(store.Root(), "bad")
	_ = os.Mkdir(dir, 0o700)
	for content, reason := range map[string]string{"not json": ReasonIncomplete, `{"dataBytes":2}`: ReasonIncomplete} {
		_ = os.WriteFile(filepath.Join(dir, MetaFile), []byte(content), 0o600)
		_, err = store.Locate("bad")
		reasonIs(t, err, reason)
	}
	_ = os.Remove(filepath.Join(dir, MetaFile))
	_ = os.Mkdir(filepath.Join(dir, MetaFile), 0o700)
	_, err = store.Locate("bad")
	reasonIs(t, err, ReasonIncomplete)

	big := filepath.Join(store.Root(), "big")
	_ = os.Mkdir(big, 0o700)
	_ = os.WriteFile(filepath.Join(big, MetaFile), make([]byte, MaxMetaBytes+1), 0o600)
	_, err = store.Locate("big")
	reasonIs(t, err, ReasonIncomplete)

	// a result directory that is a link is never followed
	outside := t.TempDir()
	writeResult(t, newStoreAt(t, outside), "x", time.Now(), "{}")
	if err := os.Symlink(filepath.Join(outside, "x"), filepath.Join(store.Root(), "linked")); err != nil {
		t.Fatal(err)
	}
	_, err = store.Locate("linked")
	reasonIs(t, err, ReasonSymlink)

	_ = os.WriteFile(filepath.Join(store.Root(), "file"), nil, 0o600)
	_, err = store.Locate("file")
	reasonIs(t, err, ReasonNotFound)

	swap(t, &ownedByUser, func(os.FileInfo) bool { return false })
	_, err = store.Locate("short")
	reasonIs(t, err, ReasonOwner)
	ownedByUser = isOwnedByCurrentUser

	swap(t, &osOpen, func(string) (*os.File, error) { return nil, errBoom })
	_, err = store.Locate("short")
	reasonIs(t, err, ReasonIncomplete)
	osOpen = os.Open

	swap(t, &osLstat, func(string) (os.FileInfo, error) { return nil, errBoom })
	_, err = store.Locate("short")
	reasonIs(t, err, ReasonUnwritable)
}

func newStoreAt(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRemove(t *testing.T) {
	store := newStore(t)
	writeResult(t, store, "t1", time.Now(), "{}")
	writeResult(t, store, "t2", time.Now(), "{}")
	if err := store.Remove("t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Locate("t2"); err != nil {
		t.Fatalf("rm removed another result: %v", err)
	}
	reasonIs(t, store.Remove("t1"), ReasonNotFound)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(store.Root(), "link")); err != nil {
		t.Fatal(err)
	}
	reasonIs(t, store.Remove("link"), ReasonSymlink)
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("rm followed a link")
	}
	swap(t, &osRemoveAll, func(string) error { return errBoom })
	reasonIs(t, store.Remove("t2"), ReasonUnwritable)
}

func TestPrune(t *testing.T) {
	store := newStore(t)
	if removed, err := store.Prune(false); err != nil || removed != 0 {
		t.Fatalf("pruning a missing root: %d %v", removed, err)
	}
	current := time.Now()
	writeResult(t, store, "fresh", current, "{}")
	writeResult(t, store, "expired", current.Add(-8*24*time.Hour), "{}")
	// an incomplete result: old ones go, a recent one may still be written
	for name, age := range map[string]time.Duration{"stale": 2 * time.Hour, "writing": time.Minute} {
		dir := filepath.Join(store.Root(), name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		when := current.Add(-age)
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatal(err)
		}
	}
	// names outside the grammar and links are never touched
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(store.Root(), "link")); err != nil {
		t.Fatal(err)
	}
	old := current.Add(-2 * time.Hour)
	if err := os.Mkdir(filepath.Join(store.Root(), "not.an.id"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(store.Root(), "not.an.id"), old, old)
	removed, err := store.Prune(false)
	if err != nil || removed != 2 {
		t.Fatalf("removed %d %v", removed, err)
	}
	for name, present := range map[string]bool{"fresh": true, "expired": false, "stale": false, "writing": true, "link": true, "not.an.id": true} {
		if _, err := os.Lstat(filepath.Join(store.Root(), name)); (err == nil) != present {
			t.Errorf("%s present = %v", name, err == nil)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("prune followed a link")
	}
	removed, err = store.Prune(true)
	if err != nil || removed != 2 {
		t.Fatalf("prune --all removed %d %v", removed, err)
	}
}

func TestPruneBounds(t *testing.T) {
	store := newStore(t)
	current := time.Now()
	for i := 0; i < MaxResults+2; i++ {
		writeResult(t, store, fmt.Sprintf("r%03d", i), current.Add(time.Duration(i)*time.Second-time.Hour), "{}")
	}
	removed, err := store.Prune(false)
	if err != nil || removed != 2 {
		t.Fatalf("count bound removed %d %v", removed, err)
	}
	for _, id := range []string{"r000", "r001"} {
		if _, err := store.Locate(id); ReasonOf(err) != ReasonNotFound {
			t.Fatalf("%s should be the oldest and gone: %v", id, err)
		}
	}
	// the byte bound, through a result missing its data file
	swap(t, &osLstat, func(name string) (os.FileInfo, error) {
		info, err := os.Lstat(name)
		if err == nil && filepath.Base(name) == DataFile && strings.HasSuffix(filepath.Dir(name), "r002") {
			return bigInfo{info}, nil
		}
		return info, err
	})
	removed, err = store.Prune(false)
	if err != nil || removed != 1 {
		t.Fatalf("byte bound removed %d %v", removed, err)
	}
	osLstat = os.Lstat
	_ = os.Remove(filepath.Join(store.Root(), "r003", DataFile))
	if removed, _ := store.Prune(false); removed != 0 {
		t.Fatalf("a result without data.json counts its meta only, removed %d", removed)
	}
	swap(t, &osRemoveAll, func(string) error { return errBoom })
	if removed, _ := store.Prune(true); removed != 0 {
		t.Fatalf("failed removals counted: %d", removed)
	}
	swap(t, &osReadDir, func(string) ([]os.DirEntry, error) { return nil, errBoom })
	_, err = store.Prune(false)
	reasonIs(t, err, ReasonUnwritable)
}

type bigInfo struct{ os.FileInfo }

func (bigInfo) Size() int64 { return MaxStoreBytes + 1 }
