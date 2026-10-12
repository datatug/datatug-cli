package dbviewer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The viewer opens the file its path names, whatever the path holds: a "?", a "#" or a "%"
// is a character of the name, and is not where the driver's own parameters start (which
// would open, and create, a shorter name, beside the file that was named).
func TestGetSQLiteDbContext_OpensTheFileItsPathNamesWhateverItHolds(t *testing.T) {
	for _, name := range []string{"what?mode=rw.db", "a#b.db", "100%.db", "a%23b.db", "with space.db"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			plain := filepath.Join(dir, "plain.db")
			raw, err := sql.Open("sqlite3", plain)
			require.NoError(t, err)
			_, err = raw.Exec("CREATE TABLE marker (name TEXT)")
			require.NoError(t, err)
			_, err = raw.Exec("INSERT INTO marker VALUES (?)", name)
			require.NoError(t, err)
			require.NoError(t, raw.Close())
			path := filepath.Join(dir, name)
			require.NoError(t, os.Rename(plain, path))

			opened, err := dtviewers.GetSQLiteDbContext(path).GetSqlDB(context.Background(), "sqlite3")
			require.NoError(t, err)
			defer func() { _ = opened.Close() }()
			var marker string
			require.NoError(t, opened.QueryRow("SELECT name FROM marker").Scan(&marker))
			assert.Equal(t, name, marker, "the rows of another file were read")

			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "the open made a file beside the one that was named")
			assert.Equal(t, name, entries[0].Name())
		})
	}
	// A file that is not there is not made.
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing#1.db")
	opened, err := dtviewers.GetSQLiteDbContext(missing).GetSqlDB(context.Background(), "sqlite3")
	require.NoError(t, err)
	assert.Error(t, opened.Ping())
	_ = opened.Close()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "an open of a file that is not there made it")
}
