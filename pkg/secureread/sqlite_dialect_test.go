package secureread

import (
	"context"
	"database/sql"
	"path/filepath"
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
