package secureread

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqliteStandIn is a MySQL or Oracle database a test stands in with: a SQLite database in memory behind
// dalgo2sql, which runs the same text query a server would.
type sqliteStandIn struct {
	dal.DB
	dbschema.SchemaReader
	sqlDB *sql.DB
}

func (s *sqliteStandIn) Close() error { return s.sqlDB.Close() }

// standInForServers makes every MySQL and Oracle open of this test return a SQLite database holding
// customer(id, name), and records the texts that reached it through the opener count.
func standInForServers(t *testing.T) (opens *int) {
	t.Helper()
	count := 0
	open := func() (dbcopy.SchemaScanDB, error) {
		count++
		sqlDB, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		_, err = sqlDB.Exec(`CREATE TABLE customer (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO customer VALUES (1, 'Ada'), (2, 'Alan')`)
		require.NoError(t, err)
		return &sqliteStandIn{DB: dalgo2sql.NewDatabase(sqlDB, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{}), sqlDB: sqlDB}, nil
	}
	t.Cleanup(dbcopy.SetServerOpenersForTest(dbcopy.ServerOpeners{
		MySQL:  func(string) (dbcopy.SchemaScanDB, error) { return open() },
		Oracle: func(context.Context, string) (dbcopy.SchemaScanDB, error) { return open() },
	}))
	return &count
}

func TestServerQueryText(t *testing.T) {
	for text, want := range map[string]string{
		"SELECT 1;":              "SELECT 1",
		"select id from t ;; \n": "select id from t",
		"  -- a note\n/* x */ (WITH a AS (SELECT 1) SELECT * FROM a)": "-- a note\n/* x */ (WITH a AS (SELECT 1) SELECT * FROM a)",
	} {
		got, err := serverQueryText("mysql", text)
		require.NoError(t, err, text)
		assert.Equal(t, want, got)
	}
	for _, text := range []string{"SET SESSION transaction_read_only = 0", "-- only a comment", "/* never closed", "selectx 1", "", "DELETE FROM t"} {
		_, err := serverQueryText("oracle", text)
		assert.ErrorIs(t, err, ErrNativeSQLUnsupported, text)
		if err != nil {
			assert.Contains(t, err.Error(), "on Oracle, DataTug runs a query (SELECT or WITH) and nothing else")
		}
	}
}

func TestRunNativeSQL_MySQLAndOracle(t *testing.T) {
	opens := standInForServers(t)
	t.Setenv(dbcopy.MySQLPreviewEnv, "1")
	t.Setenv(dbcopy.OraclePreviewEnv, "1")
	t.Setenv("DATATUG_NATIVE_MY", "mysql://alice:pw@db.example.com/shop")
	t.Setenv("DATATUG_NATIVE_ORA", "oracle://scott:pw@db.example.com/FREEPDB1")
	executor := NewExecutor(Session{Unrestricted: true})

	for _, source := range []string{"env:DATATUG_NATIVE_MY", "env:DATATUG_NATIVE_ORA"} {
		result, err := executor.RunNativeSQL(context.Background(), source, "SELECT id, name FROM customer WHERE id = ?;", dal.QueryArg{Value: 2})
		require.NoError(t, err, source)
		require.Len(t, result.Rows, 1)
		assert.Empty(t, result.Limitations, "an unrestricted session has nothing to label")
	}
	assert.Equal(t, 2, *opens)

	_, err := executor.RunNativeSQL(context.Background(), "env:DATATUG_NATIVE_MY", "UPDATE customer SET name = 'x'")
	assert.ErrorIs(t, err, ErrNativeSQLUnsupported)
	assert.Equal(t, 2, *opens, "a refused text never opens the source")

	t.Setenv(dbcopy.MySQLPreviewEnv, "")
	_, err = executor.RunNativeSQL(context.Background(), "env:DATATUG_NATIVE_MY", "SELECT 1")
	assert.EqualError(t, err, "MySQL sources are a preview and are switched off: set DATATUG_PREVIEW_MYSQL=1 to use them")

	t.Setenv(dbcopy.MySQLPreviewEnv, "1")
	_, err = executor.RunNativeSQL(context.Background(), "env:DATATUG_NATIVE_MY", "SELECT * FROM missing_table")
	assert.Error(t, err, "a query the database refuses fails")
	assert.False(t, errors.Is(err, ErrNativeSQLUnsupported))
}
