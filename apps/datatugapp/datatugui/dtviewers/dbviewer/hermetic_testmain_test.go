package dbviewer

import (
	"os"
	"testing"

	"github.com/datatug/datatug-cli/internal/hermetictest"
)

func TestMain(m *testing.M) {
	os.Exit(hermetictest.Main(m))
}
