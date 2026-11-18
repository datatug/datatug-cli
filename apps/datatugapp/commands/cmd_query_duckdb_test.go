//go:build cgo

package commands

import (
	"context"
	"encoding/json"
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

func TestQueryDuckDBParquetExactValuesAndBoundedOutput(t *testing.T) {
	db, err := dalgo2duckdb.NewDatabase("")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	path := filepath.Join(t.TempDir(), "exact.parquet")
	_, err = db.SQLDB().Exec("COPY (SELECT i, (9007199254740993+i)::BIGINT AS exact, (12.0034)::DECIMAL(12,4) AS amount, CASE i WHEN 0 THEN false WHEN 1 THEN true ELSE NULL END AS flag, (-2+i)::TINYINT AS negative FROM range(3) t(i)) TO '" + strings.ReplaceAll(path, "'", "''") + "' (FORMAT PARQUET)")
	require.NoError(t, err)
	stdout, stderr, code := runQuery(t, "", "--db", "duckdb://:memory:", "--sql", "SELECT exact, amount, flag, negative FROM read_parquet('"+strings.ReplaceAll(path, "'", "''")+"') ORDER BY i", "--format", "json", "--no-policies")
	require.Equal(t, 0, code, stderr)
	var rows []map[string]any
	decoder := json.NewDecoder(strings.NewReader(stdout))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&rows))
	require.Len(t, rows, 3)
	for i, exact := range []string{"9007199254740993", "9007199254740994", "9007199254740995"} {
		require.Equal(t, json.Number(exact), rows[i]["exact"], "a JS float decoder would round this value")
		require.Equal(t, []any{false, true, nil}[i], rows[i]["flag"])
		require.Equal(t, []json.Number{"-2", "-1", "0"}[i], rows[i]["negative"])
		require.Equal(t, map[string]any{"Width": json.Number("12"), "Scale": json.Number("4"), "Value": json.Number("120034")}, rows[i]["amount"])
	}
	for _, text := range []string{"SELECT i FROM range(10001) t(i)", "SELECT repeat('x',4000001) AS huge"} {
		stdout, stderr, code = runQuery(t, "", "--db", "duckdb://:memory:", "--sql", text, "--format", "json", "--no-policies")
		require.NotEqual(t, 0, code, stderr)
		require.Empty(t, stdout, "overflow must not emit a partial JSON result")
		require.Contains(t, stderr, "DuckDB native result exceeds row or byte budget")
	}
	stdout, stderr, code = runQuery(t, "", "--db", "duckdb://:memory:", "--sql", "SELECT 42 AS answer", "--format", "json", "--no-policies")
	require.Equal(t, 0, code, stderr)
	require.Equal(t, "[\n{\"$key\":\"\",\"answer\":42}\n]\n", stdout, "ordinary native output retains its existing shape")
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
