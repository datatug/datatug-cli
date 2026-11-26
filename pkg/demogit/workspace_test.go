package demogit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestChanges(t *testing.T) {
	f := newGitFixture(t)
	writeFile(t, f.path, "z-added.txt", "new")
	writeFile(t, f.path, "modify.txt", "after")
	writeFile(t, f.path, "ignored.txt", "ignored")
	must(t, os.Remove(filepath.Join(f.path, "delete.txt")))
	got, err := Changes(f.path)
	must(t, err)
	want := []Change{{"delete.txt", "deleted"}, {"modify.txt", "modified"}, {"z-added.txt", "added"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Changes() = %v, want %v", got, want)
	}
}

func TestChangesStagedAndUnstaged(t *testing.T) {
	f := newGitFixture(t)
	must(t, os.Symlink("modify.txt", filepath.Join(f.path, "link")))
	commitAll(t, f.local, "track a symlink")
	wt, err := f.local.Worktree()
	must(t, err)
	writeFile(t, f.path, "modify.txt", "staged")
	writeFile(t, f.path, "new-file", "will be deleted")
	must(t, os.Remove(filepath.Join(f.path, "link")))
	must(t, os.Symlink("delete.txt", filepath.Join(f.path, "link")))
	must(t, wt.AddWithOptions(&git.AddOptions{All: true}))
	_, err = wt.Remove("delete.txt")
	must(t, err)
	writeFile(t, f.path, "delete.txt", "recreated")
	writeFile(t, f.path, "modify.txt", "before")
	must(t, os.Remove(filepath.Join(f.path, "new-file")))
	must(t, os.Remove(filepath.Join(f.path, "link")))
	must(t, os.Symlink("modify.txt", filepath.Join(f.path, "link")))
	got, err := Changes(f.path)
	must(t, err)
	want := []Change{{"delete.txt", "modified"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Changes() = %v, want %v", got, want)
	}
	c := testAPI(t, f.cfg, userReply())
	result, err := c.Save(context.Background(), f.path, aliceFork, SaveOptions{})
	must(t, err)
	assertRef(t, f.remote, demoRef, f.localHeadForTest(t))
	if changes, err := Changes(f.path); err != nil || len(changes) != 0 {
		t.Fatalf("Changes() after %s = %v, %v", result.CommitSHA, changes, err)
	}
}

func (f gitFixture) localHeadForTest(t *testing.T) plumbing.Hash {
	t.Helper()
	head, err := f.local.Head()
	must(t, err)
	return head.Hash()
}

func TestChangesEmptyAndInvalidRepos(t *testing.T) {
	t.Run("unborn", func(t *testing.T) {
		path := t.TempDir()
		initRepo(t, path, false)
		writeFile(t, path, "new", "new")
		got, err := Changes(path)
		if err != nil || !reflect.DeepEqual(got, []Change{{"new", "added"}}) {
			t.Fatalf("Changes() = %v, %v", got, err)
		}
	})
	t.Run("not-a-repository", func(t *testing.T) {
		if _, err := Changes(t.TempDir()); !errors.Is(err, git.ErrRepositoryNotExists) {
			t.Fatalf("Changes() = %v", err)
		}
	})
	t.Run("bare", func(t *testing.T) {
		path := t.TempDir()
		initRepo(t, path, true)
		if _, err := Changes(path); !errors.Is(err, git.ErrIsBareRepository) {
			t.Fatalf("Changes() = %v", err)
		}
	})
	t.Run("invalid-index", func(t *testing.T) {
		f := newGitFixture(t)
		writeFile(t, f.path, ".git/index", "broken")
		if _, err := Changes(f.path); err == nil || !strings.Contains(err.Error(), "read workspace status") {
			t.Fatalf("Changes() = %v", err)
		}
	})
}

func TestValidateWorkspace(t *testing.T) {
	for _, projectDir := range []string{"..", "nested/../project", "./project", "/tmp", ""} {
		t.Run("project-path="+projectDir, func(t *testing.T) {
			if err := ValidateWorkspace(t.TempDir(), projectDir, CloneLimits{}); !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), "clean relative path") {
				t.Fatalf("ValidateWorkspace() = %v", err)
			}
		})
	}
	t.Run("relative-links", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "real/"+"datatug-project.json", `null`)
		writeFile(t, root, ".git/info/extra", strings.Repeat("x", 16))
		must(t, os.Symlink("real", filepath.Join(root, "alias")))
		must(t, os.Symlink(".././missing", filepath.Join(root, "real", "dangling")))
		must(t, ValidateWorkspace(root, "alias", CloneLimits{MaxFiles: 3, MaxBytes: 20}))
	})
	t.Run("redirected-parent-escapes", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "real/datatug-project.json", `{}`)
		must(t, os.Symlink("..", filepath.Join(root, "real", "redirect")))
		must(t, os.Symlink("redirect/../missing", filepath.Join(root, "real", "bad")))
		if err := ValidateWorkspace(root, "real", CloneLimits{}); !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), `"real/bad"`) {
			t.Fatalf("ValidateWorkspace() = %v", err)
		}
	})
	t.Run("cycle", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "datatug-project.json", `{}`)
		must(t, os.Symlink("loop", filepath.Join(root, "loop")))
		if err := ValidateWorkspace(root, ".", CloneLimits{}); !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), "too many symlinks") {
			t.Fatalf("ValidateWorkspace() = %v", err)
		}
	})
	t.Run("manifest-is-symlink", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "other", `{}`)
		must(t, os.Symlink("other", filepath.Join(root, "datatug-project.json")))
		if err := ValidateWorkspace(root, ".", CloneLimits{}); !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("ValidateWorkspace() = %v", err)
		}
	})
	t.Run("invalid-root", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "file", "")
		for _, dir := range []string{filepath.Join(root, "file"), filepath.Join(root, "missing")} {
			if err := ValidateWorkspace(dir, ".", CloneLimits{}); !errors.Is(err, ErrUntrustedContent) {
				t.Fatalf("ValidateWorkspace() = %v", err)
			}
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := validateWorkspace(ctx, t.TempDir(), ".", defaultLimits(CloneLimits{})); !errors.Is(err, context.Canceled) {
			t.Fatalf("validateWorkspace() = %v", err)
		}
	})
	t.Run("link-target-crosses-a-file", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "datatug-project.json", `{}`)
		must(t, os.Symlink("datatug-project.json/child", filepath.Join(root, "link")))
		if err := ValidateWorkspace(root, ".", CloneLimits{}); !errors.Is(err, ErrUntrustedContent) || !strings.Contains(err.Error(), `"link"`) {
			t.Fatalf("ValidateWorkspace() = %v", err)
		}
	})
}
