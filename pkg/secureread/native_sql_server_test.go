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

// sqliteStandIn is a MySQL, Oracle or SQL Server database a test stands in with: a SQLite database in memory behind
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
	for _, accepted := range []struct{ scheme, text, want string }{
		{"mysql", "SELECT 1;", "SELECT 1"},
		{"mysql", "select id from t ;; \n", "select id from t"},
		{"mysql", "  -- a note\n/* x */ (WITH a AS (SELECT 1) SELECT * FROM a)", "-- a note\n/* x */ (WITH a AS (SELECT 1) SELECT * FROM a)"},
		{"mysql", "SELECT 'it''s; COMMIT', \"x\", `update` FROM t # a note; DELETE", "SELECT 'it''s; COMMIT', \"x\", `update` FROM t # a note; DELETE"},
		{"mysql", "SELECT a-1 FROM t WHERE a=a--1", "SELECT a-1 FROM t WHERE a=a--1"},
		{"mysql", "SELECT CAST(name AS CHAR CHARACTER SET utf8mb4) FROM t --\n", "SELECT CAST(name AS CHAR CHARACTER SET utf8mb4) FROM t --"},
		{"mysql", "SELECT `a\\b` FROM t", "SELECT `a\\b` FROM t"},
		{"postgres", "SELECT $1, name FROM customer WHERE id = $2;", "SELECT $1, name FROM customer WHERE id = $2"},
		{"postgres", `SELECT 'é', "Café" FROM t`, `SELECT 'é', "Café" FROM t`},
		{"postgres", `SELECT "update", 'insert' FROM "order" -- ends here`, `SELECT "update", 'insert' FROM "order" -- ends here`},
		{"oracle", "SELECT 1 FROM dual --no space needed\n", "SELECT 1 FROM dual --no space needed"},
		{"sqlserver", "SELECT t1.c2, 1.5e3, 0x1F FROM t1", "SELECT t1.c2, 1.5e3, 0x1F FROM t1"},
		{"sqlserver", "SELECT [update], N'insert' FROM [order] -- ends here", "SELECT [update], N'insert' FROM [order] -- ends here"},
		{"oracle", `SELECT CASE WHEN "SET" > 0 THEN 1 END FROM dual`, `SELECT CASE WHEN "SET" > 0 THEN 1 END FROM dual`},
	} {
		got, err := serverQueryText(accepted.scheme, accepted.text)
		require.NoError(t, err, accepted.text)
		assert.Equal(t, accepted.want, got)
	}
	for _, text := range []string{"SET SESSION transaction_read_only = 0", "-- only a comment", "selectx 1", "", "DELETE FROM t"} {
		_, err := serverQueryText("oracle", text)
		assert.ErrorIs(t, err, ErrNativeSQLUnsupported, text)
		if err != nil {
			assert.Contains(t, err.Error(), "on Oracle, DataTug runs a query (SELECT or WITH) and nothing else")
		}
	}
	for _, refused := range []struct{ scheme, text, holds string }{
		{"sqlserver", "SELECT 1; COMMIT; DELETE FROM t", "a second statement"},
		{"sqlserver", "SELECT 1 COMMIT DELETE FROM t", "COMMIT"},
		{"sqlserver", "SELECT 1 EXEC xp_cmdshell 'dir'", "EXEC"},
		{"sqlserver", "SELECT 1 xp_cmdshell", "XP_CMDSHELL"},
		{"sqlserver", "SELECT * FROM sys.objects WHERE 1 = sp_who", "SP_WHO"},
		{"sqlserver", "SELECT * INTO copy FROM t", "INTO"},
		{"sqlserver", "WITH c AS (SELECT 1 AS id) UPDATE customer SET name = 'x'", "UPDATE"},
		{"oracle", "WITH FUNCTION f RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN DELETE FROM t; COMMIT; RETURN 1; END; SELECT f FROM dual", "a second statement"},
		{"oracle", "WITH FUNCTION f RETURN NUMBER IS BEGIN RETURN 1 END SELECT f FROM dual", "FUNCTION"},
		{"oracle", "SELECT q'[it's]' FROM dual", "an Oracle quoted literal (q'...')"},
		{"oracle", `SELECT "id" FROM "customer" FOR UPDATE`, "UPDATE"},
		{"postgres", "SELECT $$ x $$", "a PostgreSQL dollar-quoted string"},
		{"postgres", "SELECT $tag$ x; COMMIT $tag$", "a PostgreSQL dollar-quoted string"},
		{"postgres", "SELECT $_t1$ x $_t1$", "a PostgreSQL dollar-quoted string"},
		{"postgres", `SELECT E'a\'; DELETE FROM t --'`, "a backslash in a PostgreSQL literal"},
		{"postgres", "SELECT set_config('default_transaction_read_only', 'off', false)", "SET_CONFIG"},
		{"postgres", "SELECT * INTO copy FROM t", "INTO"},
		{"postgres", "SELECT $a, $ FROM t", "a PostgreSQL dollar-quoted string"},
		{"postgres", "SELECT 1, $", "a PostgreSQL dollar-quoted string"},
		{"postgres", "WITH x AS (SELECT $é$'$é$), d AS (DELETE FROM t RETURNING 1) SELECT $é$'$é$", "a PostgreSQL dollar-quoted string"},
		{"postgres", "SELECT é FROM t", "a character that is not ASCII outside a string"},
		{"postgres", `SELECT "set_config"('default_transaction_read_only','off',false)`, "SET_CONFIG"},
		{"postgres", `SELECT pg_catalog."SET_CONFIG"('default_transaction_read_only','off',false)`, "SET_CONFIG"},
		{"postgres", `SELECT U&"!0073et_config"('default_transaction_read_only','off',false) UESCAPE '!'`, "a PostgreSQL Unicode-escaped name or string (U&)"},
		{"postgres", `SELECT u&'\0041'`, "a PostgreSQL Unicode-escaped name or string (U&)"},
		{"sqlserver", "SELECT * FROM [OPENROWSET]('SQLNCLI', 'x', 'SELECT 1')", "OPENROWSET"},
		{"sqlserver", `SELECT "xp_cmdshell" FROM t`, "XP_CMDSHELL"},
		{"mysql", "SELECT * FROM t INTO OUTFILE '/tmp/x'", "INTO"},
		{"mysql", "SELECT 1 /*!50000 , SLEEP(1) */", "a MySQL comment that the server runs"},
		{"mysql", "SELECT 1 /*M! INTO OUTFILE '/tmp/x' */", "a MySQL comment that the server runs"},
		{"mysql", "SELECT 1 /*m!100000 INTO OUTFILE '/tmp/x' */", "a MySQL comment that the server runs"},
		{"mysql", "SELECT 1 /* never closed", "a comment that is not closed"},
		{"sqlserver", "SELECT 1 /* /* */ ' */ COMMIT DELETE FROM t --'", "a comment inside a comment"},
		{"sqlserver", "SELECT 1 /* /* */ ' */ EXEC xp_cmdshell 'whoami' --'", "a comment inside a comment"},
		{"mysql", "SELECT a FROM t WHERE a=a--1 INTO OUTFILE '/tmp/x'", "INTO"},
		{"mysql", `SELECT 'a\'; DELETE' FROM t`, "a backslash in a MySQL literal"},
		{"mysql", `SELECT "a\" INTO OUTFILE '/tmp/x' -- " FROM t`, "a backslash in a MySQL literal"},
		{"sqlserver", "SELECT 1 AS CHARACTER SET IMPLICIT_TRANSACTIONS OFF", "SET"},
		{"sqlserver", "SELECT 1COMMIT SELECT 1DELETE FROM t", "COMMIT"},
		{"oracle", "SELECT 1UPDATE FROM dual", "UPDATE"},
		{"sqlserver", "SELECT 1 --x\rCOMMIT DELETE FROM t", "COMMIT"},
		{"mysql", "SELECT 'never closed", "a string or a name that is not closed"},
		{"sqlserver", "SELECT [never closed", "a string or a name that is not closed"},
	} {
		_, err := serverQueryText(refused.scheme, refused.text)
		require.ErrorIs(t, err, ErrNativeSQLUnsupported, refused.text)
		assert.Contains(t, err.Error(), "DataTug runs one query (SELECT or WITH) and nothing else, and the text holds "+refused.holds, refused.text)
	}
}

func TestRunNativeSQL_MySQLAndOracle(t *testing.T) {
	opens := standInForServers(t)
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

	_, err = executor.RunNativeSQL(context.Background(), "env:DATATUG_NATIVE_MY", "SELECT * FROM missing_table")
	assert.Error(t, err, "a query the database refuses fails")
	assert.False(t, errors.Is(err, ErrNativeSQLUnsupported))
}
