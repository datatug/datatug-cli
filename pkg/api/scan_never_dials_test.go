package api

import (
	"context"
	"testing"

	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
)

// A test of the scan must never dial a server: the PostgreSQL the scan is run against is the CI
// test of the scan, not a unit test. For the whole test binary of this package the open that a
// PostgreSQL scan makes is one that stops the run (TestMain), so a test that forgets to stand in
// with stubOpenSchemaScan, or a change to the order of the checks that used to refuse a scan
// before it opened anything, fails the run instead of dialing a host.
func TestTheOpenOfThisTestBinaryStopsTheRunOnAnyOpen(t *testing.T) {
	// A source that is not a PostgreSQL one: were the real open in place, it would answer with an
	// error and dial nothing. The guard stops the run for any source, and names it as the display
	// function does for a source reference, without displaying its URL.
	ref := dbcopy.BackendRef{Scheme: "sqlite", Raw: "env:DATATUG_SHOP_PG_URL", Path: "postgres://alice:" + pgSecret + "@db.example.com/shop"}

	var message any
	func() {
		defer func() { message = recover() }()
		_, _ = openSchemaScan(ref, context.Background())
	}()

	if assert.NotNil(t, message, "an open that nobody stood in for stops the run") {
		assert.Contains(t, message, "must stand in with stubOpenSchemaScan")
		assert.Contains(t, message, "env:DATATUG_SHOP_PG_URL")
		assert.NotContains(t, message, pgSecret, "and does not name the password")
		assert.NotContains(t, message, "alice")
	}
}

func TestOpenSchemaScanForTest_IsTheOpenAScanMakesNow(t *testing.T) {
	var stood int
	restore := SetOpenSchemaScanForTest(func(dbcopy.BackendRef, context.Context) (dbcopy.SchemaScanDB, error) {
		stood++
		return &fakeScanDB{}, nil
	})
	_, err := OpenSchemaScanForTest()(dbcopy.BackendRef{}, context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 1, stood, "what a test stood in with is what a scan opens through")

	restore()
	assert.Panics(t, func() { _, _ = OpenSchemaScanForTest()(dbcopy.BackendRef{Scheme: "sqlite"}, context.Background()) }, "and the guard of the test binary is back")
}
