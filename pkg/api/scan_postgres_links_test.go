package api

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The writer of the connection descriptor does not follow a link. A project is not trusted
// (ResolveDescriptorPath refuses a descriptor that a link leads out of the folder to), and a
// scan that wrote through such a link would read a file of the machine into memory, truncate it
// and put the descriptor there, and exit 0 on a project that no reader opens.

const outsideBefore = "not the scan's to touch\n"

// linkedProject is a project folder where the part of the descriptor path that link names is
// a link to something outside the folder, and the folder outside, whose file the link leads to.
func linkedProject(t *testing.T, link string) (dir, outside string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("creating a symbolic link needs a privilege on Windows")
	}
	dir, outside = t.TempDir(), t.TempDir()
	switch link {
	case "connections":
		require.NoError(t, os.MkdirAll(filepath.Join(outside, "prod"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(outside, "prod", "shop.json"), []byte(outsideBefore), 0o600))
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "connections")))
	case "connections/prod":
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "connections"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(outside, "shop.json"), []byte(outsideBefore), 0o600))
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "connections", "prod")))
	case "connections/prod/shop.json":
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "connections", "prod"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(outside, "shop.json"), []byte(outsideBefore), 0o600))
		require.NoError(t, os.Symlink(filepath.Join(outside, "shop.json"), filepath.Join(dir, "connections", "prod", "shop.json")))
	}
	return dir, outside
}

func assertOutsideUntouched(t *testing.T, outside string) {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(outside, func(path string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		if !entry.IsDir() {
			rel, _ := filepath.Rel(outside, path)
			files = append(files, filepath.ToSlash(rel))
			content, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, outsideBefore, string(content), "the file a link leads to is not written: %s", rel)
		}
		return nil
	}))
	assert.Len(t, files, 1, "and nothing is made beside it: %v", files)
}

func TestWriteDescriptor_RefusesALinkAnywhereOnItsPath(t *testing.T) {
	for _, link := range []string{"connections", "connections/prod", "connections/prod/shop.json"} {
		t.Run(link, func(t *testing.T) {
			dir, outside := linkedProject(t, link)

			undo, err := newShopParams(t).WriteDescriptor(dir)

			assert.Nil(t, undo)
			if assert.Error(t, err) {
				assert.ErrorContains(t, err, link, "the refusal names the path in the project that is a link")
				assert.ErrorContains(t, err, "link")
				assert.NotContains(t, err.Error(), outside, "and not where it leads")
			}
			assertOutsideUntouched(t, outside)
		})
	}
}

func TestWriteDescriptor_RefusesADanglingLinkAndANonFileWhereTheDescriptorGoes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symbolic link needs a privilege on Windows")
	}
	t.Run("a link to nothing", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "connections", "prod"), 0o755))
		missing := filepath.Join(t.TempDir(), "not-yet.json")
		require.NoError(t, os.Symlink(missing, filepath.Join(dir, "connections", "prod", "shop.json")))

		_, err := newShopParams(t).WriteDescriptor(dir)

		assert.ErrorContains(t, err, "connections/prod/shop.json")
		assert.NoFileExists(t, missing, "a link that leads nowhere is not followed to make the file")
	})
	t.Run("a folder where the file goes", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "connections", "prod", "shop.json"), 0o755))

		_, err := newShopParams(t).WriteDescriptor(dir)

		assert.ErrorContains(t, err, "connections/prod/shop.json")
		assert.ErrorContains(t, err, "not a file")
	})
}

func TestWriteDescriptor_ALinkThatCannotBeLookedAtIsRefused(t *testing.T) {
	boom := errors.New("cannot look")
	original := scanLstat
	t.Cleanup(func() { scanLstat = original })
	scanLstat = func(name string) (fs.FileInfo, error) {
		if filepath.Base(name) == "prod" {
			return nil, boom
		}
		return original(name)
	}
	dir := t.TempDir()

	undo, err := newShopParams(t).WriteDescriptor(dir)

	assert.Nil(t, undo)
	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "connections/prod")
	assert.NoDirExists(t, filepath.Join(dir, "connections"), "what was made on the way is not left behind")
}

func TestWriteDescriptor_ADescriptorThatCannotBeLookedAtIsRefused(t *testing.T) {
	boom := errors.New("cannot look")
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_OLD_PG_URL"}`)
	original := scanLstat
	t.Cleanup(func() { scanLstat = original })
	scanLstat = func(name string) (fs.FileInfo, error) {
		if filepath.Base(name) == "shop.json" {
			return nil, boom
		}
		return original(name)
	}

	undo, err := newShopParams(t).WriteDescriptor(dir)

	assert.Nil(t, undo)
	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "connections/prod/shop.json")
}

// A descriptor path that the check cannot classify is not passed on to the reader: only a
// file that is not there is "not a link to anywhere".
func TestResolveDescriptorPath_RefusesAFileItCannotClassify(t *testing.T) {
	dir := t.TempDir()
	writeDescriptor(t, dir, "connections/prod/shop.json", `{"dsnEnv":"DATATUG_SHOP_PG_URL"}`)
	original := scanEvalSymlinks
	t.Cleanup(func() { scanEvalSymlinks = original })
	scanEvalSymlinks = func(path string) (string, error) {
		if filepath.Base(path) == "shop.json" {
			return "", errors.New("a reparse point it cannot look through")
		}
		return original(path)
	}

	_, err := ResolveDescriptorPath(dir, "connections/prod/shop.json")

	assert.ErrorIs(t, err, errDescriptorOutsideProject)
}

// A descriptor that cannot be written, for a reason that is not what the project has in its place,
// leaves no folder behind that the write made.
func TestWriteDescriptor_AFileThatCannotBeWrittenLeavesNoFolderBehind(t *testing.T) {
	boom := errors.New("no space")
	original := scanWriteFile
	t.Cleanup(func() { scanWriteFile = original })
	scanWriteFile = func(string, []byte, os.FileMode) error { return boom }
	dir := t.TempDir()

	undo, err := newShopParams(t).WriteDescriptor(dir)

	assert.Nil(t, undo)
	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "write the connection descriptor")
	assert.NoDirExists(t, filepath.Join(dir, "connections"), "the folders made on the way are removed again")
}
