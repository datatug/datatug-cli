package commands

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcompare"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The query journey of the real PostgreSQL server of the CI job "Journey (PostgreSQL <major>)", which runs on
// every major version in realPgMajorVar's matrix: `datatug query run` (behind the preview switch)
// reads a table, with the real command, the real opener and the real DALgo adapter.
//
// What is covered of the fixes of dalgo2sql v0.26.7 that the CLI can reach:
//
//   - A not-null constraint that is not validated (PostgreSQL 18: ADD CONSTRAINT ... NOT NULL ... NOT VALID)
//     marks the column not null in the catalog while rows that were there hold NULL. DALgo orders NULLs first
//     ascending and last descending, and the adapter writes that clause unless the column is not null; it used
//     to take the unvalidated constraint for a not-null column, wrote no clause, and a limit then returned
//     the first rows by PostgreSQL's own rule (NULLs last ascending, first descending): another set of rows.
//     TestPostgresQueryJourneyOrdersNullsByDALgosRule.
//
//   - A name of 64 bytes or more on a key path (a read, a write or a delete of one record by its key) is
//     refused by the adapter instead of reaching the table or column named by its first 63 bytes. No
//     command of the CLI reads a record of a PostgreSQL source by its key: `query run`, chat, serve and the
//     saved queries read through structured queries, and the only key paths the CLI uses are the writes of
//     `db copy --to`, which no journey runs against a server yet. So no case is added for it here; the
//     adapter's own tests cover it.

// realPgMajorVar names the major version of the server of this run ("17", "18"), set by the matrix of the job.
// A run that sets it asserts the server is that version, so that a matrix that stopped changing the image would
// fail and not go on testing 17 twice.
const realPgMajorVar = "DATATUG_TEST_POSTGRES_MAJOR"

// realPgServerMajor is the major version of the server at rawURL.
func realPgServerMajor(t *testing.T, rawURL string) int {
	t.Helper()
	db, err := sql.Open("pgx", rawURL)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var number string
	require.NoError(t, db.QueryRow(`SHOW server_version_num`).Scan(&number))
	version, err := strconv.Atoi(number)
	require.NoError(t, err)
	return version / 10000
}

// realPgIDs runs `datatug query run` for query (a DTQL document) against the fixture database, as the person
// who set the switch and no policies, and returns the "id" of each row in the order they came.
func realPgIDs(t *testing.T, query string) []int {
	t.Helper()
	stdout, stderr, code := runQuery(t, query, "--db", "env:"+realPgScanVar, "-f", "-", "--format", "json", "--no-policies")
	require.Zero(t, code, "the query exits 0: %s", stderr)
	var ids []int
	for _, row := range decodeObjects(t, stdout) {
		id, ok := row["id"].(float64)
		require.True(t, ok, "a row has an id: %v", row)
		ids = append(ids, int(id))
	}
	return ids
}

func TestPostgresQueryJourneyOrdersNullsByDALgosRule(t *testing.T) {
	server := newRealPgServer(t)
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")

	major := realPgServerMajor(t, server.scanURL)
	if want := os.Getenv(realPgMajorVar); want != "" {
		require.Equal(t, want, strconv.Itoa(major), "the server of this run is the version the matrix asked for")
	}
	t.Logf("PostgreSQL %d", major)

	execAll(t, server.scanURL,
		`CREATE TABLE ranked (id integer PRIMARY KEY, score integer)`,
		`INSERT INTO ranked (id, score) VALUES (1, 30), (2, NULL), (3, 10), (4, NULL), (5, 20)`)
	if major >= 18 {
		// The not-null constraint is added when rows that hold NULL are there, and is not validated.
		execAll(t, server.scanURL, `ALTER TABLE ranked ADD CONSTRAINT ranked_score_not_null NOT NULL score NOT VALID`)
		db, err := sql.Open("pgx", server.scanURL)
		require.NoError(t, err)
		defer func() { _ = db.Close() }()
		var notNull, validated bool
		require.NoError(t, db.QueryRow(`SELECT a.attnotnull, c.convalidated
			FROM pg_catalog.pg_attribute a
			JOIN pg_catalog.pg_constraint c ON c.conrelid = a.attrelid AND c.contype = 'n' AND a.attnum = ANY (c.conkey)
			WHERE a.attrelid = 'ranked'::regclass AND a.attname = 'score'`).Scan(&notNull, &validated))
		require.True(t, notNull, "the catalog marks the column not null")
		require.False(t, validated, "though the constraint is not validated and rows hold NULL: this is what the fix is about")
	}

	// DALgo's rule: NULLs first when ascending, last when descending. The ties between the NULLs are broken by id.
	ascending := "from: {name: ranked}\norderBy:\n  - field: score\n  - field: id\nlimit: 3\n"
	descending := "from: {name: ranked}\norderBy:\n  - field: score\n    desc: true\n  - field: id\nlimit: 3\n"
	assert.Equal(t, []int{2, 4, 3}, realPgIDs(t, ascending), "ascending with a limit: the NULLs, then the smallest")
	assert.Equal(t, []int{1, 5, 3}, realPgIDs(t, descending), "descending with a limit: the largest, the NULLs last")
}

func TestPostgresDatabaseCompareJourneyPreservesExactValuesAndChanges(t *testing.T) {
	server := newRealPgServer(t)
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	execAll(t, server.scanURL,
		`CREATE SCHEMA compare_probe`,
		`CREATE TABLE compare_probe.items (id bigint PRIMARY KEY, amount numeric(38,0), note text, payload bytea, occurred timestamp, active boolean)`,
		`CREATE TABLE public.items (id bigint PRIMARY KEY, amount numeric(38,0), note text, payload bytea, occurred timestamp, active boolean)`,
		`INSERT INTO compare_probe.items VALUES
			(1, 1234567890123456789, NULL, decode('00ff00', 'hex'), TIMESTAMP '2026-10-07 12:30:00', TRUE),
			(2, 0, '', decode('', 'hex'), NULL, FALSE)`,
		`INSERT INTO public.items VALUES
			(1, 99, NULL, decode('00ff00', 'hex'), TIMESTAMP '2026-10-07 12:30:00', TRUE),
			(2, 99, '', decode('', 'hex'), NULL, FALSE)`,
		`UPDATE compare_probe.items SET amount = 1 WHERE id = 2`)

	sqlitePath := filepath.Join(t.TempDir(), "compare.sqlite")
	sqliteDB, err := sql.Open("sqlite3", sqlitePath)
	require.NoError(t, err)
	_, err = sqliteDB.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, amount NUMERIC(38,0), note TEXT, payload BLOB, occurred TIMESTAMP, active BOOLEAN);
		INSERT INTO items VALUES (1, 1234567890123456789, NULL, X'00FF00', '2026-10-07 12:30:00', 1);
		INSERT INTO items VALUES (2, 0, '', X'', NULL, 0);`)
	require.NoError(t, err)
	require.NoError(t, sqliteDB.Close())
	sqliteRef, err := dbcopy.Parse("sqlite://" + sqlitePath)
	require.NoError(t, err)
	sqlite, err := sqliteRef.OpenForCopy(context.Background(), "")
	require.NoError(t, err)
	defer closeCompareDB(sqlite)
	ref, err := dbcopy.Parse("env:" + realPgScanVar)
	require.NoError(t, err)
	postgres, err := ref.OpenForCopy(context.Background(), "compare_probe")
	require.NoError(t, err)
	defer closeCompareDB(postgres)
	require.Equal(t, "public", effectiveCompareSchema(postgres, ""))
	require.Equal(t, "compare_probe", effectiveCompareSchema(postgres, "compare_probe"))
	_, err = dbcompare.Compare(context.Background(), "dev/sqlite", sqlite, "QA/postgres", postgres, dbcompare.Options{})
	require.ErrorContains(t, err, "right PostgreSQL schema must be explicit")

	report, err := dbcompare.Compare(context.Background(), "dev/sqlite", sqlite, "QA/postgres", postgres, dbcompare.Options{
		Details: true, DetailLimit: 10, RightSchema: "compare_probe",
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), report.Summary.Unchanged)
	require.Equal(t, int64(1), report.Summary.Changed)
	require.Zero(t, report.Summary.Added+report.Summary.Removed)
	require.Len(t, report.Relations, 1)
	require.Equal(t, int64(2), report.Relations[0].LeftRows)
	require.Equal(t, int64(2), report.Relations[0].RightRows)
	require.Len(t, report.Relations[0].Details, 1)
	require.Equal(t, "changed", report.Relations[0].Details[0].Status)
	require.Contains(t, report.Relations[0].Details[0].Fields, dbcompare.FieldDiff{Name: "amount", Before: "0", After: "1"})
}

// The checks above are what say that the rows came in DALgo's order and not PostgreSQL's, and they run against a
// real server only in CI: this proves here that they can fail, with no server, by saying what the other order is.
func TestRealPgQueryJourneyExpectationsAreNotPostgresOwnOrder(t *testing.T) {
	scores := map[int]*int{1: ptr(30), 2: nil, 3: ptr(10), 4: nil, 5: ptr(20)}
	order := func(nullsFirst, descending bool) []int {
		var ids []int
		for id := 1; id <= 5; id++ {
			ids = append(ids, id)
		}
		less := func(a, b int) bool {
			sa, sb := scores[a], scores[b]
			switch {
			case sa == nil && sb == nil:
				return a < b
			case sa == nil:
				return nullsFirst
			case sb == nil:
				return !nullsFirst
			case *sa == *sb:
				return a < b
			case descending:
				return *sa > *sb
			}
			return *sa < *sb
		}
		for i := range ids {
			for j := i + 1; j < len(ids); j++ {
				if less(ids[j], ids[i]) {
					ids[i], ids[j] = ids[j], ids[i]
				}
			}
		}
		return ids[:3]
	}
	assert.Equal(t, []int{2, 4, 3}, order(true, false), "DALgo: NULLs first ascending")
	assert.Equal(t, []int{1, 5, 3}, order(false, true), "DALgo: NULLs last descending")
	assert.NotEqual(t, order(true, false), order(false, false), "PostgreSQL's own ascending order (NULLs last) returns other rows")
	assert.NotEqual(t, order(false, true), order(true, true), "PostgreSQL's own descending order (NULLs first) returns other rows")
}

func ptr(n int) *int { return &n }
