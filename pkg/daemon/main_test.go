package daemon

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

// TestMain keeps these tests off the real home (see internal/hermetictest). The
// same binary doubles as the detached "server" the spawn tests start: with
// its first argument is serve it only reports its environment and sleeps.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		// Helper mode: report the environment this process was given, then idle.
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(hermetictest.Main(m))
}
