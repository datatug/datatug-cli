package api

import (
	"context"
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
)

// TestMain keeps every test in this package from reading or writing the
// real developer's home, XDG config, or XDG cache directories — see
// internal/hermetictest for why and scripts/check-hermetic-tests.sh for the
// CI gate that catches a regression here.
//
// It also gives the whole test binary an open for a PostgreSQL scan that stops the run: no test
// of this package dials a server (the real server is the CI test of the scan), so a test that
// does not stand in with stubOpenSchemaScan is stopped, not let through to a host.
func TestMain(m *testing.M) {
	openSchemaScan = neverDialInTests
	os.Exit(hermetictest.Main(m))
}

// neverDialInTests is the open of a PostgreSQL source in this package's tests, unless a test says
// otherwise. It panics: the scan opens in a goroutine of its own, where a test cannot stop itself,
// and a panic there ends the run with this message. The source is named as the display function
// names it, never by its URL.
func neverDialInTests(ref dbcopy.BackendRef, _ context.Context) (dbcopy.SchemaScanDB, error) {
	panic("a test opened the PostgreSQL source " + ref.Display() + " for real: it must stand in with stubOpenSchemaScan and never dial")
}
