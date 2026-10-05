package secureread

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenReadOnlySQLite_SetsStructuredQueryDialect proves the native-SQL
// connection is wrapped with the sqlite structured-query dialect like every
// other SQLite open in this CLI, so a structured read can never reach the
// legacy text emitter through it.
func TestOpenReadOnlySQLite_SetsStructuredQueryDialect(t *testing.T) {
	// Not parallel: the seam is package state.
	path := filepath.Join(t.TempDir(), "n.db")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	var got dalgo2sql.DbOptions
	orig := newNativeSQLDatabase
	t.Cleanup(func() { newNativeSQLDatabase = orig })
	newNativeSQLDatabase = func(db *sql.DB, schema dal.Schema, opts dalgo2sql.DbOptions) dal.DB {
		got = opts
		return orig(db, schema, opts)
	}

	db, closeDB, err := openReadOnlySQLite(context.Background(), path)
	require.NoError(t, err)
	defer closeDB()
	assert.NotNil(t, db)
	assert.Equal(t, "sqlite", got.StructuredQueryDialect)
}

// The native-SQL connection opens the file the source names whatever its name holds: a
// driver that is given a bare path reads everything after the first "?" in it as its own
// parameters, opens the shorter name and creates it. The rows read are those of the file
// that was named, and no other file is made beside it.
func TestOpenReadOnlySQLite_OpensTheFileWhoseNameHoldsAnyCharacter(t *testing.T) {
	for _, name := range []string{"what?mode=rw.db", "q?.db", "a#b.db", "100%.db", "a%23b.db", "shop #1 50%.db", "with space.db"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.Contains(name, "?") {
				t.Skip("a file name cannot have a ? on Windows")
			}
			dir := t.TempDir()
			// Made under a plain name and moved: the driver that makes the file reads a "?" in
			// the name it is given as the start of its own parameters.
			plain := filepath.Join(t.TempDir(), "plain.db")
			raw, err := sql.Open("sqlite", plain)
			require.NoError(t, err)
			_, err = raw.Exec("CREATE TABLE marker (name TEXT)")
			require.NoError(t, err)
			_, err = raw.Exec("INSERT INTO marker (name) VALUES (?)", name)
			require.NoError(t, err)
			require.NoError(t, raw.Close())
			path := filepath.Join(dir, name)
			require.NoError(t, os.Rename(plain, path))

			// The URL of the file: "%", "#" and "?" in a path are percent-encoded in a URL.
			source := "sqlite://" + strings.NewReplacer("%", "%25", "#", "%23", "?", "%3F").Replace(path)

			result, err := NewExecutor(Session{Unrestricted: true}).RunNativeSQL(context.Background(), source, "SELECT name FROM marker")

			require.NoError(t, err)
			require.Len(t, result.Rows, 1)
			assert.Equal(t, name, result.Rows[0].Data["name"], "the rows are those of another file")
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "another file was made beside it")
			assert.Equal(t, name, entries[0].Name())
		})
	}
}
