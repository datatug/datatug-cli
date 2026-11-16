package endpoints

import (
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

// TestMain keeps every test in this package from reading or writing the
// real developer's home, XDG config, or XDG cache directories — see
// internal/hermetictest for why and scripts/check-hermetic-tests.sh for the
// CI gate that catches a regression here.
func TestMain(m *testing.M) {
	// The agent harness a developer runs the tests under must not make every
	// exec/run_query read strict; a test that wants a strict read says so.
	serveCallerIsAgent = func() bool { return false }
	os.Exit(hermetictest.Main(m))
}
