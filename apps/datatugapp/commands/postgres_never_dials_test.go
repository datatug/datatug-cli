package commands

import (
	"os"
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
	_ = os.Unsetenv(dbcopy.PostgresPreviewEnv)
}

// neverDialPostgres panics: a test that reaches it did not stand in, and the next thing the real constructor
// does is dial the host of the URL. The message names nothing of the URL.
func neverDialPostgres(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
	panic("a test of a command opened a PostgreSQL database for real: it must stand in with standInForPostgres and never dial")
}

// The opener of this test binary is the one that stops the run, which the init above cannot show: the test
// opens a PostgreSQL source with the preview on, and nothing stands in.
func TestTheOpenerOfThisTestBinaryIsTheOneThatStopsTheRun(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	ref, err := dbcopy.Parse("postgres://alice:" + sourceSecret + "@db.example.com/shop")
	assert.NoError(t, err)
	assert.PanicsWithValue(t, "a test of a command opened a PostgreSQL database for real: it must stand in with standInForPostgres and never dial",
		func() { _, _ = ref.Open(t.Context()) })
}

// The switch of the developer's shell does not reach a test: the init above clears it.
func TestThePreviewSwitchIsOffUnlessATestTurnsItOn(t *testing.T) {
	assert.ErrorIs(t, dbcopy.CheckPostgresPreview(), dbcopy.ErrPostgresPreview)
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
		t.Setenv(dbcopy.PostgresPreviewEnv, "1")
		useTheRealPostgresOpeners(t)
		assert.NotPanics(t, func() {
			_, err := ref.Open(t.Context())
			assert.Error(t, err)
			_, err = ref.OpenSchemaScan(t.Context())
			assert.Error(t, err)
		})
	})
	t.Run("and gone again when the test ends", func(t *testing.T) {
		t.Setenv(dbcopy.PostgresPreviewEnv, "1")
		assert.Panics(t, func() { _, _ = ref.Open(t.Context()) })
	})
}
