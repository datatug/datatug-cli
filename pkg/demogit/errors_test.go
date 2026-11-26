package demogit

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
)

var errFixtureIO = errors.New("test I/O failure")

type faultStorage struct {
	storage.Storer
	badObject plumbing.Hash
	badHead   bool
}

func (s faultStorage) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	if s.badHead && name == plumbing.HEAD {
		return nil, errFixtureIO
	}
	return s.Storer.Reference(name)
}

func (s faultStorage) EncodedObject(kind plumbing.ObjectType, hash plumbing.Hash) (plumbing.EncodedObject, error) {
	if hash == s.badObject {
		return nil, errFixtureIO
	}
	return s.Storer.EncodedObject(kind, hash)
}

func TestSaveAncestryReadErrors(t *testing.T) {
	f := newGitFixture(t)
	writeFile(t, f.path, "ancestor", "earlier")
	middle := commitAll(t, f.local, "middle commit")
	writeFile(t, f.path, "latest", "later")
	latest := commitAll(t, f.local, "latest commit")
	base := plumbing.NewHashReference(mainRef, latest)
	refs := []*plumbing.Reference{plumbing.NewHashReference(demoRef, f.base)}
	for _, tc := range []struct {
		name    string
		corrupt plumbing.Hash
		message string
	}{
		{"remote-tip", f.base, "read fork branch commit"},
		{"local-base", latest, "read workspace base commit"},
		{"parent", middle, "check fork branch ancestry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := git.Open(faultStorage{Storer: f.local.Storer, badObject: tc.corrupt}, nil)
			must(t, err)
			if _, err := checkSaveBranch(repo, refs, base, demoRef); !errors.Is(err, errFixtureIO) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("checkSaveBranch() = %v, want %q", err, tc.message)
			}
		})
	}
}

func TestStageCommitInvalidConfig(t *testing.T) {
	f := newGitFixture(t)
	wt, err := f.local.Worktree()
	must(t, err)
	writeFile(t, f.path, "new", "new")
	writeFile(t, f.path, ".git/config", "[invalid")
	if _, err := stageCommit(f.local, wt, "unused", &git.CommitOptions{}); err == nil || !strings.Contains(err.Error(), "section") {
		t.Fatalf("stageCommit() = %v", err)
	}
}

func TestPushNonFastForwardErrors(t *testing.T) {
	for _, err := range []error{
		git.ErrNonFastForwardUpdate,
		storage.ErrReferenceHasChanged,
		errors.New("remote rejected: non-fast-forward"),
		errors.New("failed to update ref"),
		errors.New("remote rejected: cannot lock ref"),
	} {
		if !pushDiverged(err) {
			t.Errorf("pushDiverged(%v) = false", err)
		}
	}
}

type hookContext struct {
	context.Context
	onCheck func()
}

func (c hookContext) Err() error { c.onCheck(); return c.Context.Err() }

func TestOpenLosesHeadBeforeCheckout(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(string) error
		want string
	}{
		{"missing metadata", func(ref string) error {
			return os.RemoveAll(filepath.Dir(filepath.Dir(filepath.Dir(ref))))
		}, "no such file or directory"},
		{"missing ref", os.Remove, "read cloned branch"},
		{"missing commit", func(ref string) error {
			return os.WriteFile(ref, []byte(strings.Repeat("1", 40)+"\n"), 0o600)
		}, "read cloned commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitFixture(t)
			pushToFixture(t, f.local, f.url, f.base, demoRef)
			parent := t.TempDir()
			edited := false
			ctx := hookContext{Context: context.Background(), onCheck: func() {
				if edited {
					return
				}
				refs, err := filepath.Glob(filepath.Join(parent, ".datatug-demo-*", ".git", filepath.FromSlash(demoRef.String())))
				must(t, err)
				if len(refs) == 1 {
					must(t, tc.edit(refs[0]))
					edited = true
				}
			}}
			c := testAPI(t, f.cfg, userReply(), forkReply())
			if _, err := c.Open(ctx, aliceFork, filepath.Join(parent, "repo"), project); !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Open() = %v", err)
			}
			entries, err := os.ReadDir(parent)
			must(t, err)
			if len(entries) != 0 {
				t.Fatalf("partial clone remains: %v", entries)
			}
		})
	}
}

func TestChangesBaseReadErrors(t *testing.T) {
	f := newGitFixture(t)
	wt, err := f.local.Worktree()
	must(t, err)
	for _, store := range []faultStorage{
		{Storer: f.local.Storer, badHead: true},
		{Storer: f.local.Storer, badObject: f.base},
	} {
		repo, err := git.Open(store, wt.Filesystem)
		must(t, err)
		if _, err := workspaceChanges(repo, wt); !errors.Is(err, errFixtureIO) {
			t.Fatalf("Changes() = %v", err)
		}
	}
}

type faultFilesystem struct {
	billy.Filesystem
	lstat    func(string) (fs.FileInfo, error)
	readlink func(string) (string, error)
	open     func(string) (billy.File, error)
}

func (f faultFilesystem) Lstat(path string) (fs.FileInfo, error) {
	if f.lstat != nil {
		return f.lstat(path)
	}
	return f.Filesystem.Lstat(path)
}

func (f faultFilesystem) Readlink(path string) (string, error) {
	if f.readlink != nil {
		return f.readlink(path)
	}
	return f.Filesystem.Readlink(path)
}

func (f faultFilesystem) Open(path string) (billy.File, error) {
	if f.open != nil {
		return f.open(path)
	}
	return f.Filesystem.Open(path)
}

type failingReadFile struct{ billy.File }

func (f failingReadFile) Read([]byte) (int, error) { return 0, errFixtureIO }

func TestChangesWorktreeReadErrors(t *testing.T) {
	f := newGitFixture(t)
	wt, err := f.local.Worktree()
	must(t, err)
	link := filepath.Join(f.path, "link")
	must(t, os.Symlink("modify.txt", link))
	commitAll(t, f.local, "add link")
	tree, err := workspaceBaseTree(f.local)
	must(t, err)
	badLstat := func(string) (fs.FileInfo, error) { return nil, errFixtureIO }
	for _, tc := range []struct {
		name string
		path string
		fs   faultFilesystem
	}{
		{"stat", "modify.txt", faultFilesystem{lstat: badLstat}},
		{"readlink", "link", faultFilesystem{readlink: func(string) (string, error) { return "", errFixtureIO }}},
		{"open", "modify.txt", faultFilesystem{open: func(string) (billy.File, error) { return nil, errFixtureIO }}},
		{"read", "modify.txt", faultFilesystem{open: func(path string) (billy.File, error) {
			file, err := wt.Filesystem.Open(path)
			return failingReadFile{file}, err
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry, err := tree.FindEntry(tc.path)
			must(t, err)
			tc.fs.Filesystem = wt.Filesystem
			if _, err := matchesEntry(&git.Worktree{Filesystem: tc.fs}, tc.path, entry); !errors.Is(err, errFixtureIO) {
				t.Fatalf("matchesEntry() = %v", err)
			}
		})
	}
	if _, err := changeKind(wt, tree, "../bad", &git.FileStatus{Staging: git.Untracked, Worktree: git.Untracked}); err == nil {
		t.Fatal("changeKind accepted invalid Git path")
	}
	entry, err := tree.FindEntry("modify.txt")
	must(t, err)
	must(t, os.Chmod(filepath.Join(f.path, "modify.txt"), 0o700))
	if match, err := matchesEntry(wt, "modify.txt", entry); err != nil || match {
		t.Fatalf("matchesEntry() = %t, %v for an executable file", match, err)
	}
	_, err = wt.Remove("modify.txt")
	must(t, err)
	writeFile(t, f.path, "modify.txt", "recreated")
	wt.Filesystem = faultFilesystem{Filesystem: wt.Filesystem, lstat: func(path string) (fs.FileInfo, error) {
		if path == "modify.txt" {
			return nil, errFixtureIO
		}
		return os.Lstat(filepath.Join(f.path, path))
	}}
	if _, err := workspaceChanges(f.local, wt); !errors.Is(err, errFixtureIO) || !strings.Contains(err.Error(), "compare workspace path") {
		t.Fatalf("Changes() = %v", err)
	}
}

type failingRoot struct{ *os.Root }

func (r failingRoot) Readlink(string) (string, error) { return "", errFixtureIO }

func TestValidateLinkReadError(t *testing.T) {
	root := t.TempDir()
	must(t, os.Symlink("other", filepath.Join(root, "link")))
	dir, err := os.OpenRoot(root)
	must(t, err)
	defer dir.Close()
	if err := validateLink(failingRoot{dir}, "link"); !errors.Is(err, errFixtureIO) {
		t.Fatalf("validateLink() = %v", err)
	}
}

func TestValidateWorkspacePermissions(t *testing.T) {
	requireNonroot(t)
	for _, tc := range []struct {
		name string
		path string
		mode fs.FileMode
		want string
	}{
		{"root", ".", 0o000, `"."`},
		{"directory", "dir", 0o100, `"dir"`},
		{"entry-permissions", "dir", 0o400, `"dir"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "datatug-project.json", `{}`)
			writeFile(t, root, "dir/file", "data")
			dir := filepath.Join(root, tc.path)
			must(t, os.Chmod(dir, tc.mode))
			t.Cleanup(func() { must(t, os.Chmod(dir, 0o700)) })
			err := ValidateWorkspace(root, ".", CloneLimits{})
			if !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("ValidateWorkspace() = %v, want permission error for %s", err, tc.want)
			}
		})
	}
}
