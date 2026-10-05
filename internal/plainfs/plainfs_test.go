package plainfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// project makes a project folder and, beside it, a folder that is not the project's and holds
// one file: what a refused write must leave exactly as it was.
func project(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "proj")
	outside = filepath.Join(base, "elsewhere")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("not the project's\n"), 0o644))
	return root, outside
}

// link makes a symbolic link, or skips the test where that is not possible: Windows needs a
// privilege for it. The refusal on Windows is covered by the tests that fake Lstat.
func link(t *testing.T, target, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege on Windows; the refusal there is covered with a faked Lstat")
	}
	require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
	require.NoError(t, os.Symlink(target, name))
}

// hashes is the content hash of every file and the name of every folder and link under dir,
// without following a link.
func hashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		rel, _ := filepath.Rel(dir, path)
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			require.NoError(t, linkErr)
			out[rel] = "link -> " + target
		case entry.IsDir():
			out[rel] = "dir"
		default:
			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			sum := sha256.Sum256(data)
			out[rel] = hex.EncodeToString(sum[:])
		}
		return nil
	}))
	return out
}

func tree(root string) Tree { return New(root, 0o755) }

func TestWriteFileMakesTheFoldersAndTheFile(t *testing.T) {
	root, _ := project(t)
	file := filepath.Join(root, "a", "b", "f.json")

	require.NoError(t, tree(root).WriteFile(file, []byte("one"), 0o644))
	got, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "one", string(got))

	require.NoError(t, tree(root).WriteFile(file, []byte("2"), 0o644), "a plain file is replaced")
	got, err = os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "2", string(got), "and what was in it is gone")
}

// Every kind of thing that can stand in the place of a folder or a file of the project, and
// every place on the path: the write is refused, naming the path inside the project and not
// where a link leads, the tree outside is as it was, and nothing new is made outside.
func TestWriteFileRefusesAnythingButPlainFilesInPlainFolders(t *testing.T) {
	cases := []struct {
		name   string
		build  func(t *testing.T, root, outside string)
		file   string // slash separated, below root
		naming string // the path inside the project that the message names
		reason string
	}{
		{"a link in the file's place, to a file", func(t *testing.T, root, outside string) {
			link(t, filepath.Join(outside, "keep.txt"), filepath.Join(root, "a", "f.json"))
		}, "a/f.json", "a/f.json", "is a link, not a plain file"},
		{"a link in the file's place, to nothing", func(t *testing.T, root, outside string) {
			link(t, filepath.Join(outside, "nothing.txt"), filepath.Join(root, "a", "f.json"))
		}, "a/f.json", "a/f.json", "is a link, not a plain file"},
		{"a link in the file's place, to a folder", func(t *testing.T, root, outside string) {
			link(t, outside, filepath.Join(root, "a", "f.json"))
		}, "a/f.json", "a/f.json", "is a link, not a plain file"},
		{"a link in the folder above it", func(t *testing.T, root, outside string) {
			link(t, outside, filepath.Join(root, "a"))
		}, "a/f.json", "a", "is a link, not a plain folder"},
		{"a link two folders above it", func(t *testing.T, root, outside string) {
			require.NoError(t, os.MkdirAll(filepath.Join(outside, "b"), 0o755))
			link(t, outside, filepath.Join(root, "a"))
		}, "a/b/f.json", "a", "is a link, not a plain folder"},
		{"a link to nothing in the folder above it", func(t *testing.T, root, outside string) {
			link(t, filepath.Join(outside, "nothing"), filepath.Join(root, "a"))
		}, "a/f.json", "a", "is a link, not a plain folder"},
		{"a folder in the file's place", func(t *testing.T, root, _ string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "f.json"), 0o755))
		}, "a/f.json", "a/f.json", "is not a plain file"},
		{"a file in a folder's place", func(t *testing.T, root, _ string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "a"), []byte("x"), 0o644))
		}, "a/f.json", "a", "is not a plain folder"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, outside := project(t)
			c.build(t, root, outside)
			outsideBefore, projectBefore := hashes(t, outside), hashes(t, root)

			err := tree(root).WriteFile(filepath.Join(root, filepath.FromSlash(c.file)), []byte("project bytes"), 0o644)

			require.Error(t, err)
			assert.ErrorIs(t, err, ErrNotPlain)
			assert.Contains(t, err.Error(), c.naming+": "+c.reason)
			assert.NotContains(t, err.Error(), outside, "never where a link leads")
			assert.NotContains(t, err.Error(), root, "the path inside the project, not the folder it is in")
			assert.Equal(t, outsideBefore, hashes(t, outside), "the tree outside is byte-identical and nothing new is in it")
			assert.Equal(t, projectBefore, hashes(t, root), "and the project has nothing new either")
		})
	}
}

func TestPathsOutsideTheProjectAreRefused(t *testing.T) {
	root, outside := project(t)
	before := hashes(t, outside)
	siblingWithTheSamePrefix := root + "2"
	for _, path := range []string{
		filepath.Join(outside, "keep.txt"),
		filepath.Join(root, "..", "elsewhere", "keep.txt"),
		filepath.Join(siblingWithTheSamePrefix, "f.json"),
		filepath.Join(root, ".."),
	} {
		for name, op := range map[string]func() error{
			"WriteFile":         func() error { return tree(root).WriteFile(path, []byte("x"), 0o644) },
			"ReadFile":          func() error { _, err := tree(root).ReadFile(path); return err },
			"Remove":            func() error { return tree(root).Remove(path) },
			"RemoveAll":         func() error { return tree(root).RemoveAll(path) },
			"RemoveEmptyFolder": func() error { return tree(root).RemoveEmptyFolder(path) },
			"MkdirAll":          func() error { return tree(root).MkdirAll(path) },
			"MakeFolders":       func() error { _, err := tree(root).MakeFolders(path); return err },
			"CheckFolders":      func() error { _, err := tree(root).CheckFolders(path); return err },
			"Rename from":       func() error { return tree(root).Rename(path, filepath.Join(root, "x")) },
			"Rename to":         func() error { return tree(root).Rename(filepath.Join(root, "x"), path) },
		} {
			err := op()
			require.Error(t, err, "%s %s", name, path)
			assert.ErrorIs(t, err, ErrNotPlain, "%s %s", name, path)
			assert.Contains(t, err.Error(), "is outside the project")
		}
	}
	assert.Equal(t, before, hashes(t, outside))
	assert.NoDirExists(t, siblingWithTheSamePrefix)
}

func TestTheProjectFolderItselfIsNotAFileAndIsNeverRemoved(t *testing.T) {
	root, _ := project(t)
	for name, op := range map[string]func() error{
		"WriteFile":         func() error { return tree(root).WriteFile(root, []byte("x"), 0o644) },
		"ReadFile":          func() error { _, err := tree(root).ReadFile(root); return err },
		"Remove":            func() error { return tree(root).Remove(root) },
		"RemoveAll":         func() error { return tree(root).RemoveAll(root) },
		"RemoveEmptyFolder": func() error { return tree(root).RemoveEmptyFolder(root) },
		"Rename from":       func() error { return tree(root).Rename(root, filepath.Join(root, "x")) },
		"Rename to":         func() error { return tree(root).Rename(filepath.Join(root, "x"), root) },
	} {
		err := op()
		require.Error(t, err, name)
		assert.ErrorIs(t, err, ErrNotPlain, name)
		assert.Contains(t, err.Error(), "the project folder", name)
	}
	assert.DirExists(t, root)
	assert.NoError(t, tree(root).MkdirAll(root), "the project folder is a folder")
}

// A project folder that is itself a link is the person's own to follow: only what is below it
// is looked at.
func TestAProjectFolderThatIsALinkIsFollowed(t *testing.T) {
	root, _ := project(t)
	linked := filepath.Join(t.TempDir(), "linked")
	link(t, root, linked)

	require.NoError(t, tree(linked).WriteFile(filepath.Join(linked, "a", "f.json"), []byte("x"), 0o644))

	assert.FileExists(t, filepath.Join(root, "a", "f.json"))
}

// What is refused on Windows is a junction or a link that Lstat reports, which a fake can
// stand in for on every platform.
func TestWhatLstatDoesNotReportAsPlainIsRefused(t *testing.T) {
	root, _ := project(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "f.json"), []byte("x"), 0o644))
	for name, report := range map[string]func(fs.FileInfo) fs.FileInfo{
		"a link": func(info fs.FileInfo) fs.FileInfo { return modeInfo{info, info.Mode() | fs.ModeSymlink} },
		// Go reports a junction as irregular, and not as a folder.
		"a junction": func(info fs.FileInfo) fs.FileInfo { return modeInfo{info, info.Mode()&^fs.ModeDir | fs.ModeIrregular} },
	} {
		for _, at := range []string{"a", "a/f.json"} {
			t.Run(name+" at "+at, func(t *testing.T) {
				ops := OSOps()
				ops.Lstat = func(path string) (fs.FileInfo, error) {
					info, err := os.Lstat(path)
					if err == nil && filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator))) == at {
						return report(info), nil
					}
					return info, err
				}
				before := hashes(t, root)

				err := NewWithOps(root, 0o755, ops).WriteFile(filepath.Join(root, "a", "f.json"), []byte("new"), 0o644)

				require.Error(t, err)
				assert.ErrorIs(t, err, ErrNotPlain)
				assert.Contains(t, err.Error(), at+": ")
				assert.Equal(t, before, hashes(t, root), "nothing is written")
			})
		}
	}
}

// modeInfo is a FileInfo that reports another mode.
type modeInfo struct {
	fs.FileInfo
	mode fs.FileMode
}

func (m modeInfo) Mode() fs.FileMode { return m.mode }
func (m modeInfo) IsDir() bool       { return m.mode.IsDir() }

// A link put in the file's place after the check is not followed where the platform has
// O_NOFOLLOW.
func TestALinkPutInTheFilesPlaceAfterTheCheckIsNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no O_NOFOLLOW: the Lstat check is its only guard, and the tests that fake Lstat cover that")
	}
	root, outside := project(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "f.json"), []byte("project's own"), 0o644))
	before := hashes(t, outside)
	ops := OSOps()
	ops.AfterCheck = func(path string) {
		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Symlink(filepath.Join(outside, "keep.txt"), path))
	}

	err := NewWithOps(root, 0o755, ops).WriteFile(filepath.Join(root, "f.json"), []byte("project bytes"), 0o644)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotPlain)
	assert.Contains(t, err.Error(), "f.json: is a link, not a plain file")
	assert.NotContains(t, err.Error(), outside)
	assert.Equal(t, before, hashes(t, outside))
}

func TestErrorsOfTheOperatingSystemAreReturnedWithoutTheAbsolutePath(t *testing.T) {
	boom := errors.New("boom")
	pathError := func(op, path string) error { return &fs.PathError{Op: op, Path: path, Err: boom} }
	write := func(t *testing.T, ops Ops, setup func(root string)) (root string, err error) {
		t.Helper()
		root, _ = project(t)
		if setup != nil {
			setup(root)
		}
		return root, NewWithOps(root, 0o755, ops).WriteFile(filepath.Join(root, "a", "f.json"), []byte("x"), 0o644)
	}

	t.Run("a folder that cannot be looked at", func(t *testing.T) {
		ops := OSOps()
		ops.Lstat = func(path string) (fs.FileInfo, error) { return nil, pathError("lstat", path) }
		_, err := write(t, ops, nil)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a: boom")
	})
	t.Run("a folder that cannot be made", func(t *testing.T) {
		ops := OSOps()
		ops.Mkdir = func(path string, _ fs.FileMode) error { return pathError("mkdir", path) }
		_, err := write(t, ops, nil)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a: boom")
	})
	t.Run("a folder made by another process in the meantime is the folder", func(t *testing.T) {
		ops := OSOps()
		made := 0
		ops.Mkdir = func(path string, perm fs.FileMode) error {
			made++
			require.NoError(t, os.Mkdir(path, perm))
			return &fs.PathError{Op: "mkdir", Path: path, Err: fs.ErrExist}
		}
		_, err := write(t, ops, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, made)
	})
	t.Run("a folder that cannot be looked at after it was made", func(t *testing.T) {
		ops := OSOps()
		ops.Mkdir = func(string, fs.FileMode) error { return nil } // reports success and makes nothing
		_, err := write(t, ops, nil)
		require.ErrorIs(t, err, fs.ErrNotExist)
		assert.NotContains(t, err.Error(), os.TempDir())
	})
	t.Run("a file that cannot be looked at", func(t *testing.T) {
		var root string
		ops := OSOps()
		ops.Lstat = func(path string) (fs.FileInfo, error) {
			if path == filepath.Join(root, "a", "f.json") {
				return nil, pathError("lstat", path)
			}
			return os.Lstat(path)
		}
		root, _ = project(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
		err := NewWithOps(root, 0o755, ops).WriteFile(filepath.Join(root, "a", "f.json"), []byte("x"), 0o644)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a/f.json: boom")
		assert.NoFileExists(t, filepath.Join(root, "a", "f.json"))
	})
	t.Run("a file that cannot be opened", func(t *testing.T) {
		ops := OSOps()
		ops.OpenFile = func(path string, _ int, _ fs.FileMode) (File, error) { return nil, pathError("open", path) }
		_, err := write(t, ops, nil)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a/f.json: boom")
	})
	t.Run("a file that cannot be written is closed", func(t *testing.T) {
		f := &fakeFile{writeErr: boom}
		ops := OSOps()
		ops.OpenFile = func(string, int, fs.FileMode) (File, error) { return f, nil }
		_, err := write(t, ops, nil)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a/f.json: boom")
		assert.True(t, f.closed)
	})
	t.Run("a file that cannot be closed", func(t *testing.T) {
		f := &fakeFile{closeErr: boom}
		ops := OSOps()
		ops.OpenFile = func(string, int, fs.FileMode) (File, error) { return f, nil }
		_, err := write(t, ops, nil)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a/f.json: boom")
	})
}

// fakeFile is a File that fails where it is told to, and reads content.
type fakeFile struct {
	writeErr, closeErr, readErr error
	content                     string
	closed                      bool
}

func (f *fakeFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}

func (f *fakeFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	if f.content == "" {
		return 0, io.EOF
	}
	n := copy(p, f.content)
	f.content = f.content[n:]
	return n, nil
}

func (f *fakeFile) Close() error { f.closed = true; return f.closeErr }

func TestReadFile(t *testing.T) {
	root, outside := project(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "f.json"), []byte("content"), 0o644))

	t.Run("reads a plain file", func(t *testing.T) {
		data, err := tree(root).ReadFile(filepath.Join(root, "a", "f.json"))
		require.NoError(t, err)
		assert.Equal(t, "content", string(data))
	})
	t.Run("a file that is not there, or a folder above it", func(t *testing.T) {
		for _, name := range []string{"a/missing.json", "missing/f.json"} {
			_, err := tree(root).ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			require.ErrorIs(t, err, fs.ErrNotExist)
			assert.NotContains(t, err.Error(), root)
		}
	})
	t.Run("never reads through a link", func(t *testing.T) {
		link(t, filepath.Join(outside, "keep.txt"), filepath.Join(root, "a", "linked.json"))
		link(t, outside, filepath.Join(root, "b"))
		for _, name := range []string{"a/linked.json", "b/keep.txt"} {
			data, err := tree(root).ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			require.ErrorIs(t, err, ErrNotPlain, name)
			assert.Empty(t, data)
			assert.NotContains(t, err.Error(), outside)
		}
	})
	t.Run("errors are returned", func(t *testing.T) {
		boom := errors.New("boom")
		file := filepath.Join(root, "a", "f.json")
		ops := OSOps()
		ops.OpenFile = func(string, int, fs.FileMode) (File, error) {
			return nil, &fs.PathError{Op: "open", Path: root, Err: boom}
		}
		_, err := NewWithOps(root, 0o755, ops).ReadFile(file)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a/f.json: boom")

		f := &fakeFile{readErr: boom}
		ops.OpenFile = func(string, int, fs.FileMode) (File, error) { return f, nil }
		_, err = NewWithOps(root, 0o755, ops).ReadFile(file)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a/f.json: boom")
		assert.True(t, f.closed)

		ops.Lstat = func(string) (fs.FileInfo, error) { return nil, boom }
		_, err = NewWithOps(root, 0o755, ops).ReadFile(file)
		require.ErrorIs(t, err, boom)
	})
}

func TestRemove(t *testing.T) {
	t.Run("removes a plain file, and a file that is not there is not an error", func(t *testing.T) {
		root, _ := project(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "a", "f.json"), []byte("x"), 0o644))
		require.NoError(t, tree(root).Remove(filepath.Join(root, "a", "f.json")))
		assert.NoFileExists(t, filepath.Join(root, "a", "f.json"))
		assert.NoError(t, tree(root).Remove(filepath.Join(root, "a", "f.json")))
		assert.NoError(t, tree(root).Remove(filepath.Join(root, "missing", "f.json")), "nor is a folder above it that is not there")
	})
	t.Run("refuses a link, a folder and what is below a link, and removes nothing", func(t *testing.T) {
		root, outside := project(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "dir"), 0o755))
		link(t, filepath.Join(outside, "keep.txt"), filepath.Join(root, "linked.json"))
		link(t, outside, filepath.Join(root, "b"))
		before := hashes(t, outside)
		for _, name := range []string{"linked.json", "dir", "b/keep.txt"} {
			err := tree(root).Remove(filepath.Join(root, filepath.FromSlash(name)))
			require.ErrorIs(t, err, ErrNotPlain, name)
			assert.NotContains(t, err.Error(), outside)
		}
		assert.Equal(t, before, hashes(t, outside))
		assert.DirExists(t, filepath.Join(root, "dir"))
	})
	t.Run("errors are returned, but a file that is gone in the meantime is not one", func(t *testing.T) {
		root, _ := project(t)
		file := filepath.Join(root, "f.json")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
		boom := errors.New("boom")
		ops := OSOps()
		ops.Remove = func(path string) error { return &fs.PathError{Op: "remove", Path: path, Err: boom} }
		err := NewWithOps(root, 0o755, ops).Remove(file)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "f.json: boom")
		ops.Remove = func(path string) error { return &fs.PathError{Op: "remove", Path: path, Err: fs.ErrNotExist} }
		assert.NoError(t, NewWithOps(root, 0o755, ops).Remove(file))
		ops.Lstat = func(string) (fs.FileInfo, error) { return nil, boom }
		require.ErrorIs(t, NewWithOps(root, 0o755, ops).Remove(file), boom)
	})
}

func TestRemoveAll(t *testing.T) {
	t.Run("removes a plain folder and what is in it, and a folder that is not there is not an error", func(t *testing.T) {
		root, _ := project(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "a", "b", "f.json"), []byte("x"), 0o644))
		require.NoError(t, tree(root).RemoveAll(filepath.Join(root, "a")))
		assert.NoDirExists(t, filepath.Join(root, "a"))
		assert.NoError(t, tree(root).RemoveAll(filepath.Join(root, "a")))
		assert.NoError(t, tree(root).RemoveAll(filepath.Join(root, "missing", "deeper")))
	})
	t.Run("refuses a link at the folder or above it", func(t *testing.T) {
		root, outside := project(t)
		link(t, outside, filepath.Join(root, "linked"))
		before := hashes(t, outside)
		for _, name := range []string{"linked", "linked/sub"} {
			err := tree(root).RemoveAll(filepath.Join(root, filepath.FromSlash(name)))
			require.ErrorIs(t, err, ErrNotPlain, name)
			assert.NotContains(t, err.Error(), outside)
		}
		assert.Equal(t, before, hashes(t, outside))
	})
	t.Run("an error is returned", func(t *testing.T) {
		root, _ := project(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))
		boom := errors.New("boom")
		ops := OSOps()
		ops.RemoveAll = func(path string) error { return &fs.PathError{Op: "unlinkat", Path: path, Err: boom} }
		err := NewWithOps(root, 0o755, ops).RemoveAll(filepath.Join(root, "a"))
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a: boom")
	})
}

func TestRemoveEmptyFolder(t *testing.T) {
	t.Run("removes an empty folder, and leaves one that holds something or is not there", func(t *testing.T) {
		root, _ := project(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "empty"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "full"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "full", "f"), []byte("x"), 0o644))
		require.NoError(t, tree(root).RemoveEmptyFolder(filepath.Join(root, "empty")))
		require.NoError(t, tree(root).RemoveEmptyFolder(filepath.Join(root, "full")), "somebody else's now")
		require.NoError(t, tree(root).RemoveEmptyFolder(filepath.Join(root, "missing")))
		require.NoError(t, tree(root).RemoveEmptyFolder(filepath.Join(root, "missing", "deeper")))
		assert.NoDirExists(t, filepath.Join(root, "empty"))
		assert.FileExists(t, filepath.Join(root, "full", "f"))
	})
	t.Run("refuses a link and a file", func(t *testing.T) {
		root, outside := project(t)
		require.NoError(t, os.MkdirAll(filepath.Join(outside, "empty"), 0o755))
		link(t, filepath.Join(outside, "empty"), filepath.Join(root, "linked"))
		require.NoError(t, os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644))
		before := hashes(t, outside)
		for _, name := range []string{"linked", "file"} {
			require.ErrorIs(t, tree(root).RemoveEmptyFolder(filepath.Join(root, name)), ErrNotPlain, name)
		}
		assert.Equal(t, before, hashes(t, outside), "the empty folder a link leads to is still there")
	})
	t.Run("errors are returned", func(t *testing.T) {
		root, _ := project(t)
		folder := filepath.Join(root, "a")
		require.NoError(t, os.MkdirAll(folder, 0o755))
		boom := errors.New("boom")
		ops := OSOps()
		ops.Remove = func(path string) error { return &fs.PathError{Op: "remove", Path: path, Err: boom} }
		err := NewWithOps(root, 0o755, ops).RemoveEmptyFolder(folder)
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "a: boom")
		ops.Remove = func(path string) error { return &fs.PathError{Op: "remove", Path: path, Err: fs.ErrNotExist} }
		assert.NoError(t, NewWithOps(root, 0o755, ops).RemoveEmptyFolder(folder), "gone in the meantime")
		ops.Lstat = func(string) (fs.FileInfo, error) { return nil, boom }
		require.ErrorIs(t, NewWithOps(root, 0o755, ops).RemoveEmptyFolder(folder), boom)
	})
}

func TestRename(t *testing.T) {
	at := func(root, name string) string { return filepath.Join(root, filepath.FromSlash(name)) }

	t.Run("renames a plain file over a plain file or over nothing", func(t *testing.T) {
		root, _ := project(t)
		require.NoError(t, os.MkdirAll(at(root, "a"), 0o755))
		require.NoError(t, os.WriteFile(at(root, "a/f.tmp"), []byte("new"), 0o644))
		require.NoError(t, tree(root).Rename(at(root, "a/f.tmp"), at(root, "a/f.json")))
		require.NoError(t, os.WriteFile(at(root, "a/g.tmp"), []byte("newer"), 0o644))
		require.NoError(t, tree(root).Rename(at(root, "a/g.tmp"), at(root, "a/f.json")))
		got, err := os.ReadFile(at(root, "a/f.json"))
		require.NoError(t, err)
		assert.Equal(t, "newer", string(got))
	})
	t.Run("lands only on a checked path", func(t *testing.T) {
		root, outside := project(t)
		require.NoError(t, os.MkdirAll(at(root, "a/dir"), 0o755))
		require.NoError(t, os.WriteFile(at(root, "a/f.tmp"), []byte("new"), 0o644))
		link(t, filepath.Join(outside, "keep.txt"), at(root, "a/linked.json"))
		link(t, outside, at(root, "b"))
		before, projectBefore := hashes(t, outside), hashes(t, root)
		for name, c := range map[string][2]string{
			"onto a link":                  {"a/f.tmp", "a/linked.json"},
			"onto a folder":                {"a/f.tmp", "a/dir"},
			"into a folder that is a link": {"a/f.tmp", "b/f.json"},
			"from a link":                  {"a/linked.json", "a/f.json"},
			"from a folder":                {"a/dir", "a/f.json"},
			"from below a link":            {"b/keep.txt", "a/f.json"},
		} {
			err := tree(root).Rename(at(root, c[0]), at(root, c[1]))
			require.ErrorIs(t, err, ErrNotPlain, name)
			assert.NotContains(t, err.Error(), outside, name)
		}
		assert.Equal(t, before, hashes(t, outside))
		assert.Equal(t, projectBefore, hashes(t, root))
	})
	t.Run("a file or a folder that is not there is an error", func(t *testing.T) {
		root, _ := project(t)
		require.NoError(t, os.WriteFile(at(root, "f.tmp"), []byte("x"), 0o644))
		for name, c := range map[string][2]string{
			"from nothing":                    {"missing.tmp", "f.json"},
			"from a folder that is not there": {"missing/f.tmp", "f.json"},
			"to a folder that is not there":   {"f.tmp", "missing/f.json"},
		} {
			err := tree(root).Rename(at(root, c[0]), at(root, c[1]))
			require.ErrorIs(t, err, fs.ErrNotExist, name)
			assert.NotContains(t, err.Error(), root, name)
		}
	})
	t.Run("errors are returned", func(t *testing.T) {
		root, _ := project(t)
		require.NoError(t, os.WriteFile(at(root, "f.tmp"), []byte("x"), 0o644))
		boom := errors.New("boom")
		ops := OSOps()
		ops.Rename = func(from, to string) error { return &os.LinkError{Op: "rename", Old: from, New: to, Err: boom} }
		err := NewWithOps(root, 0o755, ops).Rename(at(root, "f.tmp"), at(root, "f.json"))
		require.ErrorIs(t, err, boom)
		assert.EqualError(t, err, "f.json: boom")
		ops.Rename = os.Rename
		ops.Lstat = func(path string) (fs.FileInfo, error) {
			if filepath.Base(path) == "f.json" {
				return nil, boom
			}
			return os.Lstat(path)
		}
		require.ErrorIs(t, NewWithOps(root, 0o755, ops).Rename(at(root, "f.tmp"), at(root, "f.json")), boom)
		ops.Lstat = func(string) (fs.FileInfo, error) { return nil, boom }
		require.ErrorIs(t, NewWithOps(root, 0o755, ops).Rename(at(root, "f.tmp"), at(root, "f.json")), boom)
	})
}

func TestMakeFoldersReportsWhatItMadeOutermostFirst(t *testing.T) {
	root, outside := project(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a"), 0o755))

	made, err := tree(root).MakeFolders(filepath.Join(root, "a", "b", "c"))
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(root, "a", "b"), filepath.Join(root, "a", "b", "c")}, made)
	made, err = tree(root).MakeFolders(filepath.Join(root, "a", "b", "c"))
	require.NoError(t, err)
	assert.Empty(t, made, "nothing was made")

	t.Run("a refusal returns what was made before it", func(t *testing.T) {
		link(t, outside, filepath.Join(root, "a", "b", "c", "d"))
		made, err := tree(root).MakeFolders(filepath.Join(root, "a", "b", "c", "d", "e"))
		require.ErrorIs(t, err, ErrNotPlain)
		assert.Empty(t, made)

		ops := OSOps()
		ops.Mkdir = func(path string, perm fs.FileMode) error {
			if filepath.Base(path) == "n2" {
				return errors.New("boom")
			}
			return os.Mkdir(path, perm)
		}
		made, err = NewWithOps(root, 0o755, ops).MakeFolders(filepath.Join(root, "n1", "n2", "n3"))
		require.Error(t, err)
		assert.Equal(t, []string{filepath.Join(root, "n1")}, made, "the one made before the one that failed")
	})
}

func TestCheckFolders(t *testing.T) {
	root, outside := project(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o755))
	link(t, outside, filepath.Join(root, "a", "linked"))

	found, err := tree(root).CheckFolders(filepath.Join(root, "a", "b"))
	require.NoError(t, err)
	assert.True(t, found)
	found, err = tree(root).CheckFolders(filepath.Join(root, "a", "missing", "deeper"))
	require.NoError(t, err)
	assert.False(t, found, "a folder that is not there is not a link")
	found, err = tree(root).CheckFolders(root)
	require.NoError(t, err)
	assert.True(t, found)
	_, err = tree(root).CheckFolders(filepath.Join(root, "a", "linked", "x"))
	require.ErrorIs(t, err, ErrNotPlain)
	assert.EqualError(t, err, "a/linked: is a link, not a plain folder; refusing to use it")
	assert.NoDirExists(t, filepath.Join(root, "a", "missing"), "it makes nothing")
}

func TestMkdirAllMakesPlainFoldersOnly(t *testing.T) {
	root, outside := project(t)
	require.NoError(t, tree(root).MkdirAll(filepath.Join(root, "a", "b")))
	assert.DirExists(t, filepath.Join(root, "a", "b"))
	link(t, outside, filepath.Join(root, "linked"))
	before := hashes(t, outside)
	require.ErrorIs(t, tree(root).MkdirAll(filepath.Join(root, "linked", "x")), ErrNotPlain)
	assert.Equal(t, before, hashes(t, outside))
}

func TestTheRealOperatingSystemCallsAreTheDefault(t *testing.T) {
	ops := OSOps()
	assert.Nil(t, ops.AfterCheck)
	root, _ := project(t)
	f, err := ops.OpenFile(filepath.Join(root, "f"), os.O_WRONLY|os.O_CREATE, 0o644)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	f, err = ops.OpenFile(filepath.Join(root, "missing", "f"), os.O_WRONLY, 0o644)
	require.Error(t, err)
	assert.True(t, f == nil, "a failed open is a nil File, not a File that holds a nil pointer")
}
