package commands

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Chat opens the SQLite file of a source read-only, with the one builder of the URI of a file
// (dbcopy.SQLiteFileURIMode): the path is the name of the file whatever it holds, and a
// relative path is not read as a host.
func TestChat_OpensASQLiteSourceWithTheOneBuilderOfTheFileURI(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, os.MkdirAll("data", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("data", "a?b#c%d.db"), nil, 0o644))
	source := api.LocalSQLiteSourceURL("data/a?b#c%d.db")
	ref, err := dbcopy.Parse(source)
	require.NoError(t, err)
	want := dbcopy.SQLiteFileURIMode(ref.Path, dbcopy.SQLiteReadOnly)
	assert.Equal(t, "file:./data/a%3Fb%23c%25d.db?mode=ro", want, "a relative path follows the scheme alone: it is not the host of the URI")

	t.Run("the join metadata", func(t *testing.T) {
		original := openJoinMetadataDB
		t.Cleanup(func() { openJoinMetadataDB = original })
		var opened []string
		stop := errors.New("stop")
		openJoinMetadataDB = func(driverName, dataSourceName string) (*sql.DB, error) {
			opened = append(opened, driverName+" "+dataSourceName)
			return nil, stop
		}

		_, _, err := loadChatJoinApplication(context.Background(), source, nil, false)

		require.ErrorIs(t, err, stop)
		assert.Equal(t, []string{"sqlite " + want}, opened)
	})

	t.Run("the lookup of a saved query's parameter", func(t *testing.T) {
		original := chatOpenSQLite
		t.Cleanup(func() { chatOpenSQLite = original })
		var opened []string
		stop := errors.New("stop")
		chatOpenSQLite = func(driverName, dataSourceName string) (*sql.DB, error) {
			opened = append(opened, driverName+" "+dataSourceName)
			return nil, stop
		}
		query := covCQueryFiles("f", "inv", "DTQL", "dtql", covCCustomerDTQL, covCCustomerParams)
		project := covCWriteProject(t, covCMerge(map[string]string{
			"environments/local/local.env.json":             `{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["main"]}]}`,
			"environments/local/catalogs/main/main.db.json": `{"driver":"sqlite3","path":"` + filepath.Join(dir, "data", "a?b#c%d.db") + `"}`,
		}, query))

		_, err := covCChatService(t, project, true).LookupParameter(context.Background(), "f/inv", "CustomerId")

		require.ErrorIs(t, err, stop)
		// The folder of the test may be reached through a link, so the name of the file is
		// what is compared: the URI of a path that holds a "?", a "#" and a "%" escapes them.
		require.Len(t, opened, 1)
		assert.Contains(t, opened[0], "sqlite file:")
		assert.Contains(t, opened[0], "a%3Fb%23c%25d.db?mode=ro")
	})
}
