//go:build cgo

package dbcopy

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo2duckdb"
	"github.com/stretchr/testify/require"
)

func TestDuckDBSourceOpenPreservesReadOnlyFiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "items.duckdb")
	ref, err := Parse("duckdb://" + path)
	require.NoError(t, err)
	_, err = ref.Open(ctx)
	require.ErrorIs(t, err, ErrSourceFileMissing)
	writable, err := ref.OpenForWrite(ctx)
	require.NoError(t, err)
	writer := writable.(*dalgo2duckdb.Database)
	_, err = writer.SQLDB().Exec("CREATE TABLE items(id INTEGER); INSERT INTO items VALUES (7)")
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	opened, err := ref.Open(ctx)
	require.NoError(t, err)
	reader := opened.(*dalgo2duckdb.Database)
	defer func() { require.NoError(t, reader.Close()) }()
	var id int
	require.NoError(t, reader.SQLDB().QueryRow("SELECT id FROM items").Scan(&id))
	require.Equal(t, 7, id)
	_, err = reader.SQLDB().Exec("DELETE FROM items")
	require.Error(t, err, "read source must not write its database file")

	memory, err := (BackendRef{Scheme: "duckdb"}).Open(ctx)
	require.NoError(t, err)
	require.NoError(t, memory.(*dalgo2duckdb.Database).Close())
}

func TestDuckDBReadOnlySession(t *testing.T) {
	db, err := OpenReadOnlyDuckDB("")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	require.NoError(t, db.ValidateReadQuery(context.Background(), "SELECT 42 AS answer"))
	var answer int
	require.NoError(t, db.SQLDB().QueryRow("SELECT 42").Scan(&answer))
	require.Equal(t, 42, answer)
	_, err = db.SQLDB().Exec("CREATE TABLE forbidden(id INTEGER)")
	require.Error(t, err)
}
