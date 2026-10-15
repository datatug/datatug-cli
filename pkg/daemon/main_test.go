package daemon

import (
	"os"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

// TestMain keeps these tests off the real home (see internal/hermetictest). The
// same binary doubles as the detached "server" the spawn tests start: with
// DAEMON_TEST_HELPER set it only sleeps.
func TestMain(m *testing.M) {
	if os.Getenv("DAEMON_TEST_HELPER") != "" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(hermetictest.Main(m))
}
