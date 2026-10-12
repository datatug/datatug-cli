package api

import (
	"os"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
)

// A test of the API must never dial a PostgreSQL server: the real one is the journey test of CI. For the whole
// test binary the constructor that dbcopy opens a source through is one that stops the run, and the preview switch of
// the developer's own shell is cleared, so that a test that needs the switch on says so and every other test sees it off.
func init() {
	dbcopy.SetPostgresOpenerForTest(neverDialPostgres)
	_ = os.Unsetenv(dbcopy.PostgresPreviewEnv)
}

// neverDialPostgres panics: a test that reaches it did not stand in, and the next thing the real constructor does is
// dial the host of the URL. The message names nothing of the URL.
func neverDialPostgres(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
	panic("a test of the API opened a PostgreSQL database for real: it must stand in and never dial")
}

// The opener of this test binary is the one that stops the run, which the init above cannot show: the test opens a
// PostgreSQL source with the preview on, and nothing stands in.
func TestTheOpenerOfThisTestBinaryIsTheOneThatStopsTheRun(t *testing.T) {
	t.Setenv(dbcopy.PostgresPreviewEnv, "1")
	ref, err := dbcopy.Parse("postgres://alice:s3cret-dt02@127.0.0.1:1/shop")
	assert.NoError(t, err)
	assert.PanicsWithValue(t, "a test of the API opened a PostgreSQL database for real: it must stand in and never dial",
		func() { _, _ = ref.Open(t.Context()) })
}

// The switch of the developer's shell does not reach a test: the init above clears it.
func TestThePreviewSwitchIsOffUnlessATestTurnsItOn(t *testing.T) {
	assert.ErrorIs(t, dbcopy.CheckPostgresPreview(), dbcopy.ErrPostgresPreview)
}
