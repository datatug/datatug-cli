package projectvisibility

import (
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

// Core's filesystem visibility provider uses the environment-operation cache.
// Keep that owning persistence inside this test binary's temporary home.
func TestMain(m *testing.M) {
	os.Exit(hermetictest.Main(m))
}
