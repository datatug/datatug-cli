package faithful

import (
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

// TestMain keeps every test of this package from reading or writing the developer's home, XDG config
// or XDG cache directories (see internal/hermetictest).
func TestMain(m *testing.M) {
	os.Exit(hermetictest.Main(m))
}
