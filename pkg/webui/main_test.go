package webui

import (
	"os"
	"testing"
)

// TestMain runs the package's tests with no trusted release key, so the archives the tests build (unsigned) install;
// signing_test.go sets keys explicitly.
func TestMain(m *testing.M) {
	trustedKeys = nil
	os.Exit(m.Run())
}
