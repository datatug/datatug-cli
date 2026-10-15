package commands

import (
	"database/sql"
	"encoding/json"
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
		"where is not supported": {"--where", "p:id:=:1"},
		"limit is not supported": {"--limit", "p:1"},
		"to-schema on sqlite":    {"--to-schema", "x"},
		"unknown table":          {"--include", "nope"},
		"reload":                 {"--overwrite", "reload"},
	} {
		_, _, err := runCopy(t, append([]string{"db", "copy", "--from", source, "--to", target}, args...)...)
		require.Error(t, err, name)
		assert.Equal(t, 2, exitCodeOf(t, err), name)
	}

	// The same file on both sides.
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", source)
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))

	// An empty source.
	empty := sqliteFixture(t, `CREATE TABLE x (a)`, `DROP TABLE x`)
	_, _, err = runCopy(t, "db", "copy", "--from", empty, "--to", target)
	require.Error(t, err)
	assert.Equal(t, 1, exitCodeOf(t, err))
	assert.Contains(t, err.Error(), "no tables")

	// A source that is not there.
	_, _, err = runCopy(t, "db", "copy", "--from", "sqlite://"+filepath.Join(t.TempDir(), "missing.sqlite"), "--to", target)
	require.Error(t, err)
	assert.Equal(t, 4, exitCodeOf(t, err))

	// The faithful flags are refused where they do not apply.
	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", "ingitdb://"+t.TempDir(), "--dry-run")
	require.Error(t, err)
	assert.Equal(t, 2, exitCodeOf(t, err))
	_, err2 := os.Stat(path)
	assert.NoError(t, err2, "the target of the empty-source copy was created")
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
	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", "postgres://u:pw@127.0.0.1:1/db", "--dry-run", "--json")
	require.NoError(t, err, "a dry run does not open the target")
	_, _, err = runCopy(t, "db", "copy", "--from", source, "--to", "postgres://u:pw@127.0.0.1:1/db")
	require.Error(t, err)
	assert.Equal(t, 4, exitCodeOf(t, err))
	assert.Equal(t, "open --to: "+dbcopy.ErrPostgresPreview.Error(), err.Error())
}

func openSQLiteForTest(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
