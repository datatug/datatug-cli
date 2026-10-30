package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
)

// A test of a command must never dial a PostgreSQL server: the real one is the journey test of CI. For the
// whole test binary the constructor that dbcopy opens a source through is one that stops the run, and a test
// that needs a database stands in with a fake of its own (standInForPostgres).
func init() {
	dbcopy.SetPostgresOpenerForTest(neverDialPostgres)
	dbcopy.SetServerOpenersForTest(dbcopy.ServerOpeners{
		MySQL: func(string) (dbcopy.SchemaScanDB, error) {
			panic("a test of a command opened a MySQL database for real")
		},
		Oracle: func(context.Context, string) (dbcopy.SchemaScanDB, error) {
			panic("a test of a command opened an Oracle database for real")
		},
		SQLServer: func(context.Context, string) (dbcopy.SchemaScanDB, error) {
			panic("a test of a command opened a SQL Server database for real")
		},
	})
}

// neverDialPostgres panics: a test that reaches it did not stand in, and the next thing the real constructor
// does is dial the host of the URL. The message names nothing of the URL.
func neverDialPostgres(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
	panic("a test of a command opened a PostgreSQL database for real: it must stand in with standInForPostgres and never dial")
}

// failingServerOpeners stands in for every server source's opener with one that fails the way a driver does: its
// message quotes the whole DSN it was given, password included. Nothing is dialled, and a test that reads what a
// command shows proves that none of it reaches the person.
func failingServerOpeners(t *testing.T) {
	t.Helper()
	quoting := func(dsn string) error { return errors.New(`dial "` + dsn + `" failed; password=` + sourceSecret) }
	t.Cleanup(dbcopy.SetPostgresOpenerForTest(func(dsn string, _ dal.Schema, _ dalgo2sql.DbOptions, _ ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return nil, quoting(dsn)
	}))
	t.Cleanup(dbcopy.SetServerOpenersForTest(dbcopy.ServerOpeners{
		MySQL:     func(dsn string) (dbcopy.SchemaScanDB, error) { return nil, quoting(dsn) },
		Oracle:    func(_ context.Context, dsn string) (dbcopy.SchemaScanDB, error) { return nil, quoting(dsn) },
		SQLServer: func(_ context.Context, dsn string) (dbcopy.SchemaScanDB, error) { return nil, quoting(dsn) },
	}))
}

// The opener of this test binary is the one that stops the run, which the init above cannot show: the test
// opens a PostgreSQL source, and nothing stands in.
func TestTheOpenerOfThisTestBinaryIsTheOneThatStopsTheRun(t *testing.T) {
	ref, err := dbcopy.Parse("postgres://alice:" + sourceSecret + "@db.example.com/shop")
	assert.NoError(t, err)
	assert.PanicsWithValue(t, "a test of a command opened a PostgreSQL database for real: it must stand in with standInForPostgres and never dial",
		func() { _, _ = ref.Open(t.Context()) })
}

// The journey tests of CI put the real constructors back, both the one of the scan and the one of every other
// open (`query run`, chat, serve and the copy), for the length of one test. A real opener behind only one of them
// let the first journey test panic on the stop-the-run opener of this test binary (review r1 of #339). The URL here
// is one pgx refuses to read, so the real constructor answers with an error and dials nothing; the stop-the-run
// opener would panic.
func TestTheRealPostgresOpenersAreBackForOneTest(t *testing.T) {
	const refusedByPgx = "postgres://h/db?sslmode=not-a-mode"
	ref := dbcopy.BackendRef{Scheme: "postgres", Raw: refusedByPgx, Path: refusedByPgx}
	t.Run("put back", func(t *testing.T) {
		useTheRealPostgresOpeners(t)
		assert.NotPanics(t, func() {
			_, err := ref.Open(t.Context())
			assert.Error(t, err)
			_, err = ref.OpenSchemaScan(t.Context())
			assert.Error(t, err)
		})
	})
	t.Run("and gone again when the test ends", func(t *testing.T) {
		assert.Panics(t, func() { _, _ = ref.Open(t.Context()) })
	})
}
