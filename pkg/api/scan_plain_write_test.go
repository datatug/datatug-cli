package api

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/internal/plainfs"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A scan writes only plain files in plain folders of the project: it does not write through a
// link. Every file a scan writes (the project file, the README, the environment file, the
// catalog file, the model file and the columns files of tables and views), and every folder above
// one, is put out of the project in turn and a link is put in its place, to a live place outside
// or to nothing; the scan is refused, names the path of the link inside the project, and the
// tree outside is byte-identical afterwards, with nothing new in it.

// sentinel is the content of every file that lies outside the project in these tests: not a
// project file, so that a write through a link changes it.
const sentinel = "a file that is not the project's, and that a scan does not write\n"

// useOps makes every write of a scan in this test go through the real calls of the operating
// system, with the changes that change makes to them.
func useOps(t *testing.T, change func(ops *plainfs.Ops)) {
	t.Helper()
	ops := plainfs.OSOps()
	change(&ops)
	setSeam(t, &scanOps, ops)
}

func linkOrSkip(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot make a symbolic link here (Windows needs a privilege for it; the refusal there is covered by the unit tests of internal/plainfs, with a faked Lstat): %v", err)
	}
}

// entriesOfTheScan is every file and folder a scan of two tables and a view writes into an empty
// folder, below the project folder, slash separated and in path order: where a link can stand in
// the way of it.
func entriesOfTheScan(t *testing.T, scan rescanScan) (files, folders []string) {
	t.Helper()
	projectDir := t.TempDir()
	_, err := scan.save(t, projectDir)
	require.NoError(t, err)
	require.NoError(t, filepath.WalkDir(projectDir, func(path string, entry fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(projectDir, path)
		switch {
		case err != nil || rel == ".":
			return err
		case entry.IsDir():
			folders = append(folders, filepath.ToSlash(rel))
		default:
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	}))
	sort.Strings(files)
	sort.Strings(folders)
	return files, folders
}

func TestSaveScannedProject_WritesNoFileThroughALink(t *testing.T) {
	scan := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id"), rescanTable("Old", "id")}, views: []*datatug.CollectionInfo{rescanTable("names", "full_name")}}
	files, folders := entriesOfTheScan(t, scan)
	for _, want := range []string{
		"README.md", "datatug-project.json",
		"environments/dev/dev.env.json",
		"environments/dev/catalogs/shop/shop.db.json",
		"dbmodels/shop/shop.dbmodel.json",
		"dbmodels/shop/main/tables/Customer/main.Customer.columns.json",
		"dbmodels/shop/main/views/names/main.names.columns.json",
	} {
		require.Contains(t, files, want, "the scan writes this file, so the test puts a link in its way")
	}

	// In a project that has no such file or folder yet, and in one that has it, and had it replaced.
	for _, scanned := range []string{"the first scan of the project", "a rescan of the project"} {
		for _, entry := range append(append([]string{}, files...), folders...) {
			isFolder := slices.Contains(folders, entry)
			for _, kind := range []string{"a link to a place outside", "a link to nothing"} {
				t.Run(scanned+", "+entry+", "+kind, func(t *testing.T) {
					projectDir := filepath.Join(t.TempDir(), "proj")
					require.NoError(t, os.Mkdir(projectDir, 0o755))
					if scanned == "a rescan of the project" {
						_, err := scan.save(t, projectDir)
						require.NoError(t, err)
						require.NoError(t, os.RemoveAll(filepath.Join(projectDir, filepath.FromSlash(entry))))
					}
					outside := filepath.Join(t.TempDir(), "outside")
					target := filepath.Join(outside, "nothing")
					if kind == "a link to a place outside" {
						target = outside
						if isFolder {
							// Everything the scan writes below the entry is outside too, with other content.
							for _, file := range files {
								if rel, below := strings.CutPrefix(file, entry+"/"); below {
									require.NoError(t, os.MkdirAll(filepath.Join(outside, filepath.Dir(filepath.FromSlash(rel))), 0o755))
									require.NoError(t, os.WriteFile(filepath.Join(outside, filepath.FromSlash(rel)), []byte(sentinel), 0o644))
								}
							}
							require.NoError(t, os.MkdirAll(outside, 0o755))
						} else {
							require.NoError(t, os.MkdirAll(outside, 0o755))
							target = filepath.Join(outside, "file")
							require.NoError(t, os.WriteFile(target, []byte(sentinel), 0o644))
						}
					} else {
						require.NoError(t, os.MkdirAll(outside, 0o755))
					}
					require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(projectDir, filepath.FromSlash(entry))), 0o755))
					linkOrSkip(t, target, filepath.Join(projectDir, filepath.FromSlash(entry)))
					outsideBefore := treeWithLinks(t, outside)

					_, err := scan.save(t, projectDir)

					require.Error(t, err, "a scan that would write through a link is refused")
					assert.ErrorContains(t, err, entry, "and names the path of the link inside the project")
					assert.NotContains(t, err.Error(), outside, "and never where it leads")
					assert.Equal(t, outsideBefore, treeWithLinks(t, outside), "the tree outside is byte-identical and nothing new is in it")
				})
			}
		}
	}
}

// treeWithLinks lists everything under dir by slash-separated path, without following a link: a
// folder as "dir", a link as "link -> where it leads", a file as its content.
func treeWithLinks(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)
		rel, _ := filepath.Rel(dir, path)
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			require.NoError(t, linkErr)
			tree[filepath.ToSlash(rel)] = "link -> " + target
		case entry.IsDir():
			tree[filepath.ToSlash(rel)] = "dir"
		default:
			content, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			tree[filepath.ToSlash(rel)] = string(content)
		}
		return nil
	}))
	return tree
}

// The README of the folder is kept: the scan reads it before the save and puts it back after, and
// it does neither through a link.
func TestSaveScannedProject_KeepsNoReadmeThroughALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege on Windows")
	}
	scan := rescanScan{env: "dev", tables: []*datatug.CollectionInfo{rescanTable("Customer", "id")}}
	projectDir := filepath.Join(t.TempDir(), "proj")
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(projectDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "README.md"), []byte(sentinel), 0o644))
	// Put back, the README would hold the same bytes: the time of its last change tells a write.
	longAgo := time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC)
	require.NoError(t, os.Chtimes(filepath.Join(outside, "README.md"), longAgo, longAgo))
	linkOrSkip(t, filepath.Join(outside, "README.md"), filepath.Join(projectDir, "README.md"))
	before := treeWithLinks(t, outside)

	_, err := scan.save(t, projectDir)

	require.Error(t, err)
	assert.ErrorContains(t, err, "README.md")
	assert.ErrorContains(t, err, "is a link", "the README is a link, and that is why")
	assert.NotContains(t, err.Error(), outside)
	assert.Equal(t, before, treeWithLinks(t, outside))
	info, statErr := os.Stat(filepath.Join(outside, "README.md"))
	require.NoError(t, statErr)
	assert.True(t, info.ModTime().Equal(longAgo), "what the link leads to was not written, not even with the bytes it had")
}
