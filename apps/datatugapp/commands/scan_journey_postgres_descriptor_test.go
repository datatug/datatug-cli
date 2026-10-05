package commands

import (
	"errors"
	"os"
	"path/filepath"
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
