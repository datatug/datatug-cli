package commands

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/internal/pgstandin"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqliteFixture makes a SQLite file that holds the tables made by statements, and returns its URL.
func sqliteFixture(t *testing.T, statements ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, statement := range statements {
		_, err = db.Exec(statement)
		require.NoError(t, err, statement)
	}
	return "sqlite://" + path
}

// postgresTargetThatRecords puts a stand-in server behind the opener of PostgreSQL databases, with the preview switch on
// for the test, and returns what the server was sent.
func postgresTargetThatRecords(t *testing.T) *pgstandin.Recorder {
	t.Helper()
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	// Built here, on the goroutine of the test: the opener runs on another one.
	standIn, recorder := pgstandin.Recording(t, true)
	t.Cleanup(dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return standIn, nil
	}))
	return recorder
}

// A copy to a PostgreSQL target that holds a name the target cannot take is refused before the target is changed: the
// command exits 1 (a runtime refusal, as a target that is not empty is), names the table of the source and what to do,
// and the target was sent no statement, so the tables it held before are still there.
func TestDBCopy_ToPostgres_ARefusedNameExitsOneAndChangesNothing(t *testing.T) {
	recorder := postgresTargetThatRecords(t)
	source := sqliteFixture(t,
		`CREATE TABLE a1 (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE b2 (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE "c 3" (id INTEGER PRIMARY KEY)`)

	stdout, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", pgMarkedSource, "--overwrite", "recreate")

	require.Error(t, err)
	var coder ExitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, 1, coder.ExitCode())
	assert.Contains(t, err.Error(), `table "c 3"`)
	assert.Contains(t, err.Error(), "nothing in the target was changed")
	assert.Contains(t, err.Error(), "--exclude")
	assert.Empty(t, recorder.Statements(), "no table of the target was dropped or created")
	assertNoPgMarkers(t, "refused name", stdout.String(), stderr.String(), err.Error())
}

// A copy that is not refused goes through, and says nothing about indexes when each of them was copied.
func TestDBCopy_ToPostgres_ACopyOfAcceptableNamesCreatesTheTables(t *testing.T) {
	recorder := postgresTargetThatRecords(t)
	source := sqliteFixture(t,
		`CREATE TABLE a1 (id INTEGER PRIMARY KEY, val INTEGER)`,
		`CREATE INDEX ix_val ON a1 (val)`)

	_, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", pgMarkedSource, "--overwrite", "recreate")

	require.NoError(t, err)
	sent := strings.Join(recorder.Statements(), "\n")
	assert.Contains(t, sent, `CREATE TABLE "a1"`)
	assert.Contains(t, sent, `CREATE INDEX "ix_val" ON "a1" ("val")`)
	assert.Contains(t, stderr.String(), "db copy: replicated schema for 1/1 collections")
	assert.NotContains(t, stderr.String(), "was not copied")
}
