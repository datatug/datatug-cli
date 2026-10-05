package commands

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A scan that cannot save its project takes back the descriptor it wrote: it removes the one it
// made, with the folders it made, and leaves the one that was there as it was. A file where the
// folder of the models belongs makes the save fail after the descriptor was written, with
// nothing faked.
func TestScanJourneyPostgresFailedSaveTakesBackTheDescriptor(t *testing.T) {
	failingProject := func(t *testing.T) string {
		t.Helper()
		projectDir := filepath.Join(t.TempDir(), "shop-project")
		require.NoError(t, os.Mkdir(projectDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(projectDir, "dbmodels"), []byte("a file where the models belong"), 0o600))
		return projectDir
	}

	t.Run("the descriptor the scan made is removed with its folders", func(t *testing.T) {
		projectDir := failingProject(t)
		usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })

		_, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)

		require.Error(t, err, "the save failed")
		assert.NoDirExists(t, filepath.Join(projectDir, "connections"), "no descriptor, and none of the folders it was written in, is left behind")
		assert.FileExists(t, filepath.Join(projectDir, "dbmodels"), "what was there is as it was")
	})

	t.Run("a descriptor that was there is left as it was", func(t *testing.T) {
		projectDir := failingProject(t)
		descriptor := filepath.Join(projectDir, "connections", "local", "shop.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(descriptor), 0o755))
		require.NoError(t, os.WriteFile(descriptor, []byte(`{"dsnEnv":"DATATUG_OLD_PG_URL"}`), 0o600))
		usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })

		_, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)

		require.Error(t, err, "the save failed")
		content, readErr := os.ReadFile(descriptor)
		require.NoError(t, readErr)
		assert.Equal(t, `{"dsnEnv":"DATATUG_OLD_PG_URL"}`, string(content), "the descriptor of an earlier scan is as it was")
	})
}

// A rescan with another variable updates the descriptor, and the catalog file still names it:
// the descriptor and the path of the catalog are updated together, and nothing else changes.
func TestScanJourneyPostgresRescanWithAnotherVariableUpdatesTheDescriptor(t *testing.T) {
	projectDir := filepath.Join(t.TempDir(), "shop-project")
	opener := usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })
	const other = "DATATUG_JOURNEY_PG_OTHER_URL"
	opener.serve(other, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })
	_, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)
	require.NoError(t, err)
	first := treeHashes(t, projectDir, "")

	stderr, err := runScanCommand(t, pgScanArgs(projectDir, other, "shop", "local")...)

	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, map[string]any{"dsnEnv": other}, readJSONMap(t, filepath.Join(projectDir, "connections", "local", "shop.json")))
	assert.Equal(t, "connections/local/shop.json", readJSONMap(t, filepath.Join(projectDir, "environments", "local", "catalogs", "shop", "shop.db.json"))["path"],
		"the catalog names the descriptor that holds the new variable")
	added, removed, changed := diffTrees(first, treeHashes(t, projectDir, ""))
	assert.Empty(t, added)
	assert.Empty(t, removed)
	assert.Equal(t, []string{"connections/local/shop.json"}, changed, "only the descriptor changed")
	assertNoSourceIn(t, projectDir)
}

// A save that fails takes back what the scan wrote beside the project; one that works does not,
// and an undo that fails is said with the failure of the save.
func TestSaveScanned(t *testing.T) {
	saveFailure := errors.New("the save failed")
	undoFailure := errors.New("the folder is busy")
	for name, c := range map[string]struct {
		save, undo error
		wantUndone bool
		wantErr    string
	}{
		"the save works: nothing is taken back":     {nil, nil, false, ""},
		"the save fails: the descriptor goes":       {saveFailure, nil, true, "the save failed"},
		"the save fails and so does taking it back": {saveFailure, undoFailure, true, "the save failed\nand the connection descriptor this scan wrote could not be taken back: the folder is busy"},
	} {
		t.Run(name, func(t *testing.T) {
			undone := false
			err := saveScanned(func() error { return c.save }, func() error { undone = true; return c.undo })

			assert.Equal(t, c.wantUndone, undone)
			if c.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, c.wantErr)
			assert.ErrorIs(t, err, saveFailure)
			if c.undo != nil {
				assert.ErrorIs(t, err, undoFailure)
			}
		})
	}
}

// A project is not trusted: a link in the path of the descriptor, at connections, at
// connections/<env> or at the descriptor, that leads out of the project folder is refused before
// anything is written or saved, and what the link leads to is not touched.
func TestScanJourneyPostgresRefusesALinkWhereTheDescriptorGoes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symbolic link needs a privilege on Windows")
	}
	const before = "a file of the machine, not the scan's to touch\n"
	for _, link := range []string{"connections", "connections/local", "connections/local/shop.json"} {
		t.Run(link, func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), "shop-project")
			outside := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(projectDir, filepath.Dir(link)), 0o755))
			target := outside // a folder, for the links that stand for one
			switch link {
			case "connections":
				require.NoError(t, os.MkdirAll(filepath.Join(outside, "local"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(outside, "local", "shop.json"), []byte(before), 0o600))
			case "connections/local":
				require.NoError(t, os.WriteFile(filepath.Join(outside, "shop.json"), []byte(before), 0o600))
			default:
				target = filepath.Join(outside, "shop.json")
				require.NoError(t, os.WriteFile(target, []byte(before), 0o600))
			}
			require.NoError(t, os.Symlink(target, filepath.Join(projectDir, link)))
			usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })
			treeBefore := treeWithLinks(t, projectDir)

			_, err := runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", "local")...)

			require.Error(t, err, "a scan that would write through a link fails")
			assert.ErrorContains(t, err, link, "and names the path of the project that is the link")
			assert.NotContains(t, err.Error(), outside, "not where it leads")
			assert.Equal(t, treeBefore, treeWithLinks(t, projectDir), "nothing was written into the project, and no project was saved")
			for _, file := range projectFiles(t, outside, "") {
				content, readErr := os.ReadFile(filepath.Join(outside, file))
				require.NoError(t, readErr)
				assert.Equal(t, before, string(content), "what the link leads to is as it was: %s", file)
			}
			assert.Len(t, projectFiles(t, outside, ""), 1, "and nothing was made beside it")
		})
	}
}

// One id under two drivers in one environment is refused, naming both, before the database is read
// and before anything is written: the catalog file is kept by environment and id, so the second scan
// would take the first one's catalog file and its tables. Another id, or another environment, is fine.
func TestScanJourneyOneDatabaseIdUnderTwoDriversIsRefused(t *testing.T) {
	for _, order := range []string{"sqlite first", "postgres first"} {
		t.Run(order, func(t *testing.T) {
			projectDir := filepath.Join(t.TempDir(), "company")
			require.NoError(t, os.Mkdir(projectDir, 0o755))
			sqlitePath := filepath.Join(projectDir, "data", "shop.db")
			writeJourneyDB(t, sqlitePath)
			opener := usePostgres(t, journeyPgVar, "shop", func() dbcopy.SchemaScanDB { return newJourneyPgDatabase() })
			scanSQLite := func() (string, error) {
				return runScanCommand(t, "-d", projectDir, "-D", "sqlite3", "--path", sqlitePath, "--db", "shop", "--env", "local")
			}
			scanPostgres := func(env string) (string, error) {
				return runScanCommand(t, pgScanArgs(projectDir, journeyPgVar, "shop", env)...)
			}
			first, second, firstDriver, secondDriver := scanSQLite, func() (string, error) { return scanPostgres("local") }, "sqlite3", "postgres"
			if order == "postgres first" {
				first, second, firstDriver, secondDriver = func() (string, error) { return scanPostgres("local") }, scanSQLite, "postgres", "sqlite3"
			}
			_, err := first()
			require.NoError(t, err)
			opened := len(opener.opened)
			treeBefore := treeHashes(t, projectDir, "")

			stderr, err := second()

			require.Error(t, err)
			assert.ErrorContains(t, err, `--db "shop"`)
			assert.ErrorContains(t, err, `"`+firstDriver+`"`, "the driver the project has")
			assert.ErrorContains(t, err, "-D "+secondDriver, "and the one the scan is")
			assert.ErrorContains(t, err, "use another --db")
			assert.Empty(t, stderr)
			assert.Equal(t, opened, len(opener.opened), "it is refused before the database is read: no source was opened")
			assert.Equal(t, treeBefore, treeHashes(t, projectDir, ""), "nothing was written or taken back: the tables of the first are still there")

			// The catalog files are kept by environment: the same id in another environment is not in the way.
			_, err = scanPostgres("prod")
			require.NoError(t, err, "the id is the first scan's in the environment local only")
		})
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
