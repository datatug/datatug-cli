package secureread

import (
	"context"
	"os"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
)

// A test of this package must never dial a PostgreSQL server: the real one is the journey test of CI. For the
// whole test binary the constructor that dbcopy opens a source through is one that stops the run, and a
// test that needs a database stands in with a fake of its own (standInForPostgres).
func init() {
	dbcopy.SetPostgresOpenerForTest(neverDialPostgres)
	_ = os.Unsetenv(dbcopy.PostgresPreviewEnv)
}

// neverDialPostgres panics: a test that reaches it did not stand in, and the next thing the real constructor
// does is dial the host of the URL. The message names nothing of the URL.
func neverDialPostgres(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
	panic("a test of pkg/secureread opened a PostgreSQL database for real: it must stand in with standInForPostgres and never dial")
}

// The opener of this test binary is the one that stops the run, which the init above cannot show: the test opens a
// PostgreSQL source with the preview on, and nothing stands in.
func TestTheOpenerOfThisTestBinaryIsTheOneThatStopsTheRun(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	ref, err := dbcopy.Parse(pgSource)
	assert.NoError(t, err)
	assert.PanicsWithValue(t, "a test of pkg/secureread opened a PostgreSQL database for real: it must stand in with standInForPostgres and never dial",
		func() { _, _ = ref.Open(context.Background()) })
}

// The switch of the developer's shell does not reach a test: the init above clears it.
func TestThePreviewSwitchIsOffUnlessATestTurnsItOn(t *testing.T) {
	assert.ErrorIs(t, dbcopy.CheckPostgresPreview(), dbcopy.ErrPostgresPreview)
}
