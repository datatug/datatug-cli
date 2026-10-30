//go:build cgo

package commands

import (
	"context"
	"github.com/dal-go/dalgo2duckdb"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryDuckDBParquet(t *testing.T) {
	db, err := dalgo2duckdb.NewDatabase("")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	path := filepath.Join(t.TempDir(), "items.parquet")
	_, err = db.SQLDB().ExecContext(context.Background(), "COPY (SELECT 1 AS id, 'DuckDB' AS name UNION ALL SELECT 2, 'Parquet') TO '"+strings.ReplaceAll(path, "'", "''")+"' (FORMAT PARQUET)")
	require.NoError(t, err)
	stdout, stderr, code := runQuery(t, "", "--db", "duckdb://:memory:", "--sql", "SELECT name FROM read_parquet('"+strings.ReplaceAll(path, "'", "''")+"') WHERE id = 2", "--format", "json", "--no-policies")
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "Parquet")
	_, stderr, code = runQuery(t, "", "--db", "duckdb://:memory:", "--sql", "SELECT 1; COPY (SELECT 1) TO '/tmp/forbidden.parquet' (FORMAT PARQUET)", "--no-policies")
	require.NotEqual(t, 0, code, stderr)
}

func TestQueryDuckDBDatabaseReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "items.duckdb")
	db, err := dalgo2duckdb.NewDatabase(path)
	require.NoError(t, err)
	_, err = db.SQLDB().Exec("CREATE TABLE items (id INTEGER); INSERT INTO items VALUES (7)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	stdout, stderr, code := runQuery(t, "", "--db", "duckdb://"+path, "--sql", "SELECT id FROM items", "--format", "json", "--no-policies")
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "7")
	_, stderr, code = runQuery(t, "", "--db", "duckdb://"+path, "--sql", "DELETE FROM items", "--no-policies")
	require.NotEqual(t, 0, code, stderr)
	_, stderr, code = runQuery(t, "", "--db", "duckdb://"+filepath.Join(t.TempDir(), "missing.duckdb"), "--sql", "SELECT 1", "--no-policies")
	require.NotEqual(t, 0, code, stderr)
}

func TestQueryDuckDBDefaultPolicies(t *testing.T) {
	stdout, stderr, code := runQuery(t, "", "--db", "duckdb://:memory:", "--sql", "SELECT 42 AS answer", "--format", "json", "--policies-dir", t.TempDir())
	require.NotEqual(t, 0, code, stderr)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "no access policies loaded")
	_, stderr, code = runQuery(t, "", "--db", "duckdb://:memory:", "--sql", "SELECT 42 AS answer", "--policies-dir", globalPolicyDir(t), "--as", "alice")
	require.NotEqual(t, 0, code, stderr)
	require.Contains(t, stderr, "opaque")
}
