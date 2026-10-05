package dbcopy

import (
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/stretchr/testify/assert"
)

// neverDialPostgres is the constructor of a PostgreSQL database for the scan in this package's
// tests, unless a test says otherwise. It panics: a test that reaches it did not stand in with a
// fake, and the next thing the real one does is dial the host the URL names. The message names
// nothing of the URL.
func neverDialPostgres(string, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
	panic("a test of pkg/dbcopy opened a PostgreSQL database for real: it must stand in with stubNewPostgresDatabase and never dial")
}

// neverDialPostgresWithOptions is the same for the constructor that BackendRef opens a source with.
func neverDialPostgresWithOptions(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
	panic("a test of pkg/dbcopy opened a PostgreSQL database for real: it must stand in with stubPostgresOpener and never dial")
}

// useRealPostgresConstructors lets one test run the real constructors on purpose, for a URL that
// pgx refuses before it dials: the test says so by name, and the stub goes back when it ends.
func useRealPostgresConstructors(t *testing.T) {
	t.Helper()
	stubNewPostgresDatabase(t, dalgo2postgres.NewDatabase)
	stubPostgresOpener(t, dalgo2postgres.NewDatabaseWithOptions)
}

// The test binary of this package has the constructors that stop the run (TestMain), which a test
// that calls them cannot show: it calls the variables themselves, with a URL that pgx refuses to
// parse. The real constructors answer such a URL with an error and dial nothing, so the assertions
// fail, without a connection, if the defaults of TestMain are ever removed.
func TestTheConstructorsOfThisTestBinaryAreTheOnesThatStopTheRun(t *testing.T) {
	const refusedByPgx = "postgres://h:notaport/db"
	assert.PanicsWithValue(t, "a test of pkg/dbcopy opened a PostgreSQL database for real: it must stand in with stubNewPostgresDatabase and never dial",
		func() { _, _ = newPostgresDatabase(refusedByPgx) })
	assert.PanicsWithValue(t, "a test of pkg/dbcopy opened a PostgreSQL database for real: it must stand in with stubPostgresOpener and never dial",
		func() {
			_, _ = newPostgresDatabaseWithOptions(refusedByPgx, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{})
		})
}

// A test that opens a source with no stand-in is stopped by the run itself, whichever way in it
// takes: Open reaches the constructor only when the preview is on.
func TestAnOpenWithNoStandInStopsTheRun(t *testing.T) {
	previewOn(t)
	_, err := Parse("postgres://alice:" + markerPassword + "@h:notaport/db")
	assert.Error(t, err, "a URL pgx cannot read is refused by Parse already")
	ref, err := Parse("postgres://alice:" + markerPassword + "@db.example.com/shop")
	assert.NoError(t, err)
	assert.Panics(t, func() { _, _ = ref.Open(t.Context()) })
	assert.Panics(t, func() { _, _ = ref.OpenSchemaScan(t.Context()) })
}

// The switch of the developer's shell does not reach a test: TestMain clears it.
func TestThePreviewSwitchIsOffUnlessATestTurnsItOn(t *testing.T) {
	assert.ErrorIs(t, CheckPostgresPreview(), ErrPostgresPreview)
}

// The hook that the tests of other packages use puts a stand-in in place of the constructor, forgets the
// handles of the process, and puts everything back.
func TestSetPostgresOpenerForTest_ReplacesTheConstructorForTheLengthOfATest(t *testing.T) {
	previewOn(t)
	ref, err := Parse("postgres://alice:" + markerPassword + "@db.example.com/shop")
	assert.NoError(t, err)
	fake := &fakeOpener{}
	restore := SetPostgresOpenerForTest(fake.open)
	first, err := ref.Open(t.Context())
	assert.NoError(t, err)
	again, err := ref.Open(t.Context())
	assert.NoError(t, err)
	assert.Same(t, first, again)
	assert.Len(t, fake.dsns, 1)

	restore()
	assert.Panics(t, func() { _, _ = ref.Open(t.Context()) }, "the constructor of the test binary is back")
	restoredAgain := SetPostgresOpenerForTest(fake.open)
	reopened, err := ref.Open(t.Context())
	assert.NoError(t, err)
	assert.NotSame(t, first, reopened, "the handles of the earlier test are forgotten")
	restoredAgain()
}
