package commands

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the journey of `datatug db copy --to postgres://...` against a REAL PostgreSQL server, in the job
// "Journey (PostgreSQL <major>)" of .github/workflows/golangci.yml (see scan_journey_postgres_real_test.go for how the
// server and the role are made, and scripts/check-journey-postgres.sh for how a skip fails the job): the real command,
// the real opener and the real DALgo driver, from a SQLite file into a target that already holds tables with rows.

// pgLabels returns the label column of table, ordered by id, read straight from the server.
func pgLabels(t *testing.T, rawURL, table string) []string {
	t.Helper()
	db, err := sql.Open("pgx", rawURL)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT label FROM ` + table + ` ORDER BY id`)
	require.NoError(t, err, "the table %s is still there", table)
	defer func() { _ = rows.Close() }()
	var labels []string
	for rows.Next() {
		var label string
		require.NoError(t, rows.Scan(&label))
		labels = append(labels, label)
	}
	require.NoError(t, rows.Err())
	return labels
}

// A copy that holds a name the target cannot take is refused before the target is changed: both tables the target held,
// each of them named by the source, are still there with their rows. Then a copy of a clean fixture goes through and
// replaces them. The names are those of a table with a space and of a table of 64 bytes, in the third place of the source,
// where the tables before it are the ones a copy with --overwrite=recreate would have dropped first.
func TestPostgresCopyJourneyRefusesANameAndLeavesTheTargetAsItWas(t *testing.T) {
	server := newRealPgServer(t)
	execAll(t, server.scanURL,
		`CREATE TABLE dt_copy_first (id integer PRIMARY KEY, label text)`,
		`INSERT INTO dt_copy_first VALUES (1, 'one'), (2, 'two')`,
		`CREATE TABLE dt_copy_second (id integer PRIMARY KEY, label text)`,
		`INSERT INTO dt_copy_second VALUES (1, 'uno'), (2, 'dos'), (3, 'tres')`)

	clean := []string{
		`CREATE TABLE dt_copy_first (id INTEGER PRIMARY KEY, label TEXT)`,
		`INSERT INTO dt_copy_first VALUES (1, 'new one'), (2, 'new two'), (3, 'new three')`,
		`CREATE TABLE dt_copy_second (id INTEGER PRIMARY KEY, label TEXT)`,
		`CREATE INDEX dt_copy_second_label ON dt_copy_second (label)`,
		`INSERT INTO dt_copy_second VALUES (1, 'new uno')`,
	}
	t.Run("a table with a name of 64 bytes is refused", func(t *testing.T) {
		third := "dt_copy_" + strings.Repeat("x", 56)
		require.Len(t, third, 64)
		source := sqliteFixture(t, append(append([]string{}, clean...),
			`CREATE TABLE "`+third+`" (id INTEGER PRIMARY KEY, label TEXT)`)...)

		_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", server.scanURL, "--overwrite", "recreate")

		require.Error(t, err, "the copy is refused")
		var coder ExitCoder
		require.ErrorAs(t, err, &coder)
		assert.Equal(t, 1, coder.ExitCode())
		assert.Contains(t, err.Error(), `table "`+third+`"`)
		assert.Contains(t, err.Error(), "nothing was changed")
		assert.Equal(t, []string{"one", "two"}, pgLabels(t, server.scanURL, "dt_copy_first"), "the first table is still there with its rows")
		assert.Equal(t, []string{"uno", "dos", "tres"}, pgLabels(t, server.scanURL, "dt_copy_second"), "the second table is still there with its rows")
	})

	t.Run("a clean fixture is copied", func(t *testing.T) {
		source := sqliteFixture(t, clean...)

		_, stderr, err := runCopy(t, "db", "copy", "--from", source, "--to", server.scanURL, "--overwrite", "recreate")

		require.NoError(t, err)
		assert.Contains(t, stderr.String(), "db copy: copied 2 tables, 4 rows")
		assert.Equal(t, []string{"new one", "new two", "new three"}, pgLabels(t, server.scanURL, "dt_copy_first"))
		assert.Equal(t, []string{"new uno"}, pgLabels(t, server.scanURL, "dt_copy_second"))
	})

	t.Run("a table with a space in its name is quoted and copied", func(t *testing.T) {
		source := sqliteFixture(t,
			`CREATE TABLE "dt_copy space" (id INTEGER PRIMARY KEY, label TEXT)`,
			`INSERT INTO "dt_copy space" VALUES (1, 'spaced')`)

		_, _, err := runCopy(t, "db", "copy", "--from", source, "--to", server.scanURL, "--overwrite", "recreate")

		require.NoError(t, err)
		assert.Equal(t, []string{"spaced"}, pgLabels(t, server.scanURL, `"dt_copy space"`))
	})
}
