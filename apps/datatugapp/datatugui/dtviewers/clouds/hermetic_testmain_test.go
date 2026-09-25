package clouds

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
	os.Exit(hermetictest.Main(m))
}
