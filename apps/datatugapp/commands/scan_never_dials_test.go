package commands

import (
	"context"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
)

// A test of the scan must never dial a server: the PostgreSQL the scan is run against is
// the CI test of the scan, not a unit test. Every scan of PostgreSQL opens its source
// through one seam (api.SetOpenSchemaScanForTest), and for the whole test binary that seam is
// a function that stops the run, loudly, on any open. A test that wants a database stands in
// with a fake of its own (usePostgres), and the seam goes back to this when the test ends.
func init() {
	api.SetOpenSchemaScanForTest(neverDial)
}

// neverDial is the open of a PostgreSQL source, unless a test says otherwise. It panics: the
// scan opens in a goroutine of its own, where a test cannot stop itself, and a panic there
// ends the run with this message. The source is named as the display function names it.
func neverDial(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
	panic("a test opened the PostgreSQL source " + ref.Display() + " for real: it must stand in with a fake (usePostgres) and never dial")
}

func TestNeverDialStopsTheRunOnAnyRealOpen(t *testing.T) {
	ref := dbcopy.BackendRef{Scheme: "postgres", Raw: "env:DATATUG_SHOP_PG_URL", Path: "postgres://alice:" + scanPgSecret + "@db.example.com/shop"}

	var message any
	func() {
		defer func() { message = recover() }()
		_, _ = neverDial(ref, context.Background())
	}()

	if assert.NotNil(t, message, "an open that nobody stood in for stops the run") {
		assert.Contains(t, message, "must stand in with a fake")
		assert.Contains(t, message, "env:DATATUG_SHOP_PG_URL")
		assert.NotContains(t, message, scanPgSecret, "and does not name the password")
		assert.NotContains(t, message, "alice")
	}
}

// The test binary of this package has the open that stops the run (the init above), which the test
// above cannot show: it calls the function itself. This one asks the scan what it opens through,
// and a source that is not a PostgreSQL one, which the real open answers with an error and dials
// nothing for, still stops the run: so the test fails if that init is ever deleted.
func TestTheOpenOfThisTestBinaryIsTheOneThatStopsTheRun(t *testing.T) {
	ref := dbcopy.BackendRef{Scheme: "sqlite", Raw: "env:DATATUG_SHOP_PG_URL"}

	assert.Panics(t, func() { _, _ = api.OpenSchemaScanForTest()(ref, context.Background()) })
}
