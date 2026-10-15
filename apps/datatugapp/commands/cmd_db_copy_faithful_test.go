package commands

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A SQLite -> SQLite copy creates the target (its folder too), keeps the date text, and reports.
func TestDBCopyFaithful_SQLiteToSQLiteCreatesTheTargetAndKeepsValues(t *testing.T) {
	source := sqliteFixture(t,
		`CREATE TABLE p (id INTEGER PRIMARY KEY, born DATE, at DATETIME)`,
		`INSERT INTO p VALUES (1, '1968-12-08', '2020-01-02 03:04:05')`,
		`CREATE VIEW pv AS SELECT id FROM p`)
	target := filepath.Join(t.TempDir(), "new", "dir", "copy.sqlite")

	stdout, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+target, "--json")

	require.NoError(t, err)
	var report struct {
		Status string
		Totals struct{ Tables, Rows int }
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	assert.Equal(t, "ok", report.Status)
	assert.Equal(t, 1, report.Totals.Tables)
	assert.Empty(t, stderr.String())
	db := openSQLiteForTest(t, target)
	var born string
	require.NoError(t, db.QueryRow(`SELECT +born FROM p`).Scan(&born))
	assert.Equal(t, "1968-12-08", born)
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'pv'`).Scan(&n))
	assert.Equal(t, 1, n, "the view is carried between two SQLite files")
}

func TestDBCopyFaithful_TextSummaryAndConflicts(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `INSERT INTO p VALUES (1)`)
	target := "sqlite://" + filepath.Join(t.TempDir(), "t.sqlite")

	_, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", target)
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "db copy: copied 1 tables, 1 rows")

	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", target)
	require.Error(t, err)
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "--overwrite=recreate")

	stdout, _, err := runCopy(t, "db", "copy", "--from", source, "--to", target, "--overwrite", "recreate", "--json")
	require.NoError(t, err)
	assert.Contains(t, stdout.String(), `"status": "ok"`)

	stdout, _, err = runCopy(t, "db", "copy", "--from", source, "--to", target, "--json")
	require.Error(t, err)
	assert.Contains(t, stdout.String(), `"status": "failed"`)
	assert.Contains(t, stdout.String(), `"exitCode": 1`)
}

func TestDBCopyFaithful_FailOnWarningExitsThree(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `CREATE TABLE q (id INTEGER PRIMARY KEY, x TEXT)`,
		`CREATE INDEX qx ON q (lower(x))`, `INSERT INTO q VALUES (1, 'a')`)
	target := "sqlite://" + filepath.Join(t.TempDir(), "t.sqlite")

	// Between two SQLite files an expression index is carried: no warning.
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", target, "--fail-on-warning")
	require.NoError(t, err)

	// A table that is left out takes no foreign key with it, and says so.
	source = sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`, `CREATE TABLE c (id INTEGER PRIMARY KEY, p INTEGER REFERENCES p(id))`)
	target = "sqlite://" + filepath.Join(t.TempDir(), "t2.sqlite")
	stdout, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", target, "--exclude", "p", "--fail-on-warning", "--json")
	require.Error(t, err)
	assert.Equal(t, 3, exitCodeOf(t, err))
	assert.Contains(t, stdout.String(), `"status": "ok-with-warnings"`)
	assert.Contains(t, stdout.String(), `"exitCode": 3`)
	assert.Contains(t, stdout.String(), "fk-not-carried")
	assert.Empty(t, stderr.String())

	_, stderr, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+filepath.Join(t.TempDir(), "t3.sqlite"), "--exclude", "p", "--fail-on-warning")
	require.Error(t, err)
	assert.Equal(t, 3, exitCodeOf(t, err))
	assert.Contains(t, stderr.String(), "warning [fk-not-carried]")
}

func TestDBCopyFaithful_Refusals(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	path := filepath.Join(t.TempDir(), "t.sqlite")
	target := "sqlite://" + path

	for name, args := range map[string][]string{
		"to-schema on sqlite":          {"--to-schema", "x"},
		"unknown table":                {"--include", "nope"},
		"a where that is malformed":    {"--where", "p:id:="},
		"a where on an unknown table":  {"--where", "nope:id:=:1"},
		"a where on an unknown field":  {"--where", "p:nope:=:1"},
		"a where with a bad value":     {"--where", "p:id:=:x"},
		"a where with a bad operator":  {"--where", "p:id:<>:1"},
		"a limit that is not a number": {"--limit", "p:x"},
		"a limit on an unknown table":  {"--limit", "nope:1"},
		"parallel streams below one":   {"--parallel-streams", "-1"},
		"include and exclude":          {"--include", "p", "--exclude", "p"},
		"decimals that is not a mode":  {"--decimals", "x"},
	} {
		stdout, _, err := runCopy(t, append([]string{"db", "copy", "--from", source, "--to", target, "--json"}, args...)...)
		require.Error(t, err, name)
		assert.Equal(t, 2, exitCodeOf(t, err), name)
		// --json prints an error object for a failure that comes before any copy, too.
		var report struct {
			Status   string
			ExitCode int
			Error    string
		}
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &report), name)
		assert.Equal(t, "failed", report.Status, name)
		assert.Equal(t, 2, report.ExitCode, name)
		assert.NotEmpty(t, report.Error, name)
	}

	// The same file on both sides.
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", source)
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))

	// An empty source.
	empty := sqliteFixture(t, `CREATE TABLE x (a)`, `DROP TABLE x`)
	_, stderr, err := runCopy(t, "db", "copy", "--from", empty, "--to", target)
	require.NoError(t, err, "an empty source is a note, not a failure")
	assert.Contains(t, stderr.String(), "source has no tables; nothing to copy")

	// A source that is not there.
	_, _, err = runCopy(t, "db", "copy", "--from", "sqlite://"+filepath.Join(t.TempDir(), "missing.sqlite"), "--to", target)
	require.Error(t, err)
	assert.Equal(t, 4, exitCodeOf(t, err))

	// The faithful flags are refused where they do not apply.
	for _, flag := range [][]string{{"--dry-run"}, {"--decimals", "text"}} {
		_, _, err = runCopy(t, append([]string{"db", "copy", "--from", source, "--to", "ingitdb://" + t.TempDir()}, flag...)...)
		require.Error(t, err)
		assert.Equal(t, 2, exitCodeOf(t, err))
	}
	_, err2 := os.Stat(path)
	assert.True(t, os.IsNotExist(err2), "a failed copy leaves no new file")
}

// A target path that is a URL pasted after sqlite:// is not created as a file.
func TestDBCopyFaithful_APathThatLooksLikeAURLIsNotCreated(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+filepath.Join(t.TempDir(), "x.db?password=hunter2"))
	require.Error(t, err)
	assert.Equal(t, 4, exitCodeOf(t, err))
	assert.NotContains(t, err.Error(), "hunter2")
}

func TestDBCopyFaithful_PostgresWithThePreviewOffIsRefused(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "")
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	// A dry run opens no target, but a PostgreSQL target is a preview all the same.
	for _, extra := range [][]string{{"--dry-run", "--json"}, {}} {
		_, _, err := runCopy(t, append([]string{"db", "copy", "--from", source, "--to", "postgres://u:pw@127.0.0.1:1/db"}, extra...)...)
		require.Error(t, err)
		assert.Equal(t, 4, exitCodeOf(t, err))
		assert.Equal(t, "open --to: "+dbcopy.ErrPostgresPreview.Error(), err.Error())
	}
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", "postgres://u:pw@127.0.0.1:1/db", "--dry-run", "--json")
	require.NoError(t, err, "with the switch on, a dry run does not open the target")
}

func openSQLiteForTest(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A SQLite file that a failed copy created is removed again; one that was there before is left alone.
func TestDBCopyFaithful_AFailedCopyLeavesNoNewFile(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE p (id INTEGER PRIMARY KEY)`)
	target := filepath.Join(t.TempDir(), "t.sqlite")
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+target, "--include", "nope")
	require.Error(t, err)
	_, statErr := os.Stat(target)
	assert.True(t, os.IsNotExist(statErr), "the failed copy left %s", target)

	require.NoError(t, os.WriteFile(target, nil, 0o600))
	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+target, "--include", "nope")
	require.Error(t, err)
	_, statErr = os.Stat(target)
	assert.NoError(t, statErr, "a file that was there is not removed")
}

// emptyPG is a PostgreSQL source with no server and no table: every query answers with no row.
type emptyPGConnector struct{}

func (emptyPGConnector) Connect(context.Context) (driver.Conn, error) { return emptyPGConn{}, nil }
func (emptyPGConnector) Driver() driver.Driver                        { return nil }

type emptyPGConn struct{}

func (emptyPGConn) Prepare(string) (driver.Stmt, error) { return nil, io.ErrUnexpectedEOF }
func (emptyPGConn) Close() error                        { return nil }
func (emptyPGConn) Begin() (driver.Tx, error)           { return emptyPGTx{}, nil }
func (emptyPGConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return emptyPGTx{}, nil
}
func (emptyPGConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return emptyPGRows{}, nil
}
func (emptyPGConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

type emptyPGTx struct{}

func (emptyPGTx) Commit() error   { return nil }
func (emptyPGTx) Rollback() error { return nil }

type emptyPGRows struct{}

func (emptyPGRows) Columns() []string         { return []string{"a", "b", "c", "d", "e"} }
func (emptyPGRows) Close() error              { return nil }
func (emptyPGRows) Next([]driver.Value) error { return io.EOF }

func TestDBCopyFaithful_APostgresSourceWithNoTables(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	t.Cleanup(dbcopy.SetSQLOpenerForTest(func(string, string) (*sql.DB, error) { return sql.OpenDB(emptyPGConnector{}), nil }))
	target := "sqlite://" + filepath.Join(t.TempDir(), "t.sqlite")

	_, _, err := runCopy(t, "db", "copy", "--from", "postgres://u:pw@127.0.0.1:1/db", "--to", target)
	require.Error(t, err)
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), `PostgreSQL source schema "public" has no tables`)

	_, _, err = runCopy(t, "db", "copy", "--from", "postgres://u:pw@127.0.0.1:1/db", "--from-schema", "sales", "--to", target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `PostgreSQL source schema "sales" has no tables`)
}

// --decimals reaches the copy: with text a DECIMAL_TEXT whose values numeric would spell differently stays text.
func TestDBCopyFaithful_DecimalsFlagReachesTheCopy(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	source := sqliteFixture(t, `CREATE TABLE n (id INTEGER PRIMARY KEY, v DECIMAL_TEXT(10,2))`, `INSERT INTO n VALUES (1, '.5'), (2, '1.50')`)
	warnings := func(extra ...string) string {
		stdout, _, err := runCopy(t, append([]string{"db", "copy", "--from", source, "--to", "postgres://u:pw@127.0.0.1:1/db", "--dry-run", "--json"}, extra...)...)
		require.NoError(t, err)
		return stdout.String()
	}
	assert.Contains(t, warnings(), `"values-normalized"`)
	assert.NotContains(t, warnings(), `"type-widened"`)
	withText := warnings("--decimals", "text")
	assert.Contains(t, withText, `"type-widened"`)
	assert.NotContains(t, withText, `"values-normalized"`)
}

func TestDBCopyFaithful_DecimalsTextIsRefusedWhereItDoesNotApply(t *testing.T) {
	source := sqliteFixture(t, `CREATE TABLE n (id INTEGER PRIMARY KEY, v DECIMAL_TEXT(10,2))`)
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", "sqlite://"+filepath.Join(t.TempDir(), "t.sqlite"), "--decimals", "text")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "SQLite to PostgreSQL")
}
