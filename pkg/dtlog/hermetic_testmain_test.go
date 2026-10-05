package dtlog

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
// It also clears the three variables that turn telemetry off. CI sets CI, and
// a developer may have DO_NOT_TRACK exported; the tests of the sending paths
// must not depend on either. A test of the off switch sets them itself with
// t.Setenv.
func TestMain(m *testing.M) {
	for _, name := range []string{EnvTelemetry, EnvDoNotTrack, EnvCI} {
		_ = os.Unsetenv(name)
	}
	os.Exit(hermetictest.Main(m))
}
