package dbcopy

import (
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

// TestMain keeps every test in this package from reading or writing the
// real developer's home, XDG config, or XDG cache directories — see
// internal/hermetictest for why and scripts/check-hermetic-tests.sh for the
// CI gate that catches a regression here.
//
// It also gives the whole test binary an opener of PostgreSQL databases that stops the run: no
// test of this package dials a server (the real server is the journey test of CI), so a test that
// does not stand in with stubPostgresOpener or stubNewPostgresDatabase is stopped, not let
// through to a host. And it clears the preview switch of the developer's own shell, so that a
// test that needs it on says so (previewOn) and every other test sees it off.
func TestMain(m *testing.M) {
	newPostgresDatabase = neverDialPostgres
	newPostgresDatabaseWithOptions = neverDialPostgresWithOptions
	_ = os.Unsetenv(PostgresPreviewEnv)
	os.Exit(hermetictest.Main(m))
}
