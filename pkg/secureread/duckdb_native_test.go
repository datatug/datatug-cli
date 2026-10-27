//go:build cgo

package secureread

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2duckdb"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/require"
)

func TestDuckDBNativeReadsAndFailures(t *testing.T) {
	ctx := context.Background()
	executor := NewExecutor(Session{Unrestricted: true})
	result, err := executor.RunNativeSQL(ctx, "duckdb://:memory:", "SELECT ? AS answer", dal.QueryArg{Value: 42})
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)
	require.EqualValues(t, 42, result.Rows[0].Data["answer"])
	require.Equal(t, []string{"answer"}, result.ColumnOrder)

	for _, query := range []string{"SELECT 1; SELECT 2", "CREATE TABLE forbidden(id INTEGER)", "SELECT ? AS missing_argument"} {
		_, err = executor.RunNativeSQL(ctx, "duckdb://:memory:", query)
		require.Error(t, err, query)
	}
	missing := filepath.Join(t.TempDir(), "missing.duckdb")
	_, err = executor.RunNativeSQL(ctx, "duckdb://"+missing, "SELECT 1")
	require.ErrorIs(t, err, dbcopy.ErrSourceFileMissing)
	broken := filepath.Join(t.TempDir(), "broken.duckdb")
	require.NoError(t, os.WriteFile(broken, []byte("not a DuckDB database"), 0600))
	_, err = executor.RunNativeSQL(ctx, "duckdb://"+broken, "SELECT 1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "open duckdb source")
}

func TestDuckDBNativeParquetAndFileDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "items.duckdb")
	parquet := filepath.Join(t.TempDir(), "items.parquet")
	db, err := dalgo2duckdb.NewDatabase(path)
	require.NoError(t, err)
	_, err = db.SQLDB().Exec("CREATE TABLE items(id INTEGER, name VARCHAR); INSERT INTO items VALUES (1, 'DuckDB')")
	require.NoError(t, err)
	_, err = db.SQLDB().Exec("COPY items TO '" + parquet + "' (FORMAT PARQUET)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	executor := NewExecutor(Session{Unrestricted: true})
	for _, source := range []string{"duckdb://" + path, "duckdb://:memory:"} {
		query := "SELECT name FROM read_parquet('" + parquet + "') WHERE id = 1"
		result, err := executor.RunNativeSQL(ctx, source, query)
		require.NoError(t, err)
		require.Len(t, result.Rows, 1)
		require.Equal(t, "DuckDB", result.Rows[0].Data["name"])
		require.Equal(t, []string{"name"}, result.ColumnOrder)
	}
}
