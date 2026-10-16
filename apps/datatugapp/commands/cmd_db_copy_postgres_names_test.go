package commands

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

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

// A copy to a PostgreSQL target that holds a name the target cannot take (more than 63 bytes) is refused before
// the target is changed: the command exits 1 and names the table of the source. --dry-run never opens the target,
// so the check is the one every copy makes before its first write.
func TestDBCopy_ToPostgres_ARefusedNameExitsOneAndChangesNothing(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	long := "t_" + strings.Repeat("x", 70)
	source := sqliteFixture(t,
		`CREATE TABLE a1 (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE "`+long+`" (id INTEGER PRIMARY KEY)`)

	_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", pgMarkedSource, "--dry-run")

	require.Error(t, err)
	var coder ExitCoder
	require.ErrorAs(t, err, &coder)
	assert.Equal(t, 1, coder.ExitCode())
	assert.Contains(t, err.Error(), `table "`+long+`"`)
	assert.Contains(t, err.Error(), "nothing was changed")
}

// A table name with a space is quoted, not refused, and the plan of a dry run shows keys and indexes.
func TestDBCopy_ToPostgres_ADryRunShowsTheTablesKeysAndIndexes(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	source := sqliteFixture(t,
		`CREATE TABLE "c 3" (id INTEGER PRIMARY KEY, val INTEGER)`,
		`CREATE TABLE a1 (id INTEGER PRIMARY KEY, val INTEGER, c INTEGER REFERENCES "c 3"(id))`,
		`CREATE INDEX ix_val ON a1 (val)`)

	stdout, _, err := runCopy(t, "db", "copy", "--from", source, "--to", pgMarkedSource, "--dry-run", "--json")

	require.NoError(t, err)
	out := stdout.String()
	assert.Contains(t, out, `"status": "dry-run"`)
	assert.Contains(t, out, `CREATE TABLE \"public\".\"c 3\"`)
	assert.Contains(t, out, `CREATE INDEX \"ix_val\" ON \"public\".\"a1\" (\"val\")`)
	assert.Contains(t, out, `FOREIGN KEY (\"c\") REFERENCES \"public\".\"c 3\" (\"id\")`)
	assert.Contains(t, out, `"foreignKeys": 1`)
}
