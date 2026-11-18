//go:build !unix

package commands

import (
	"os"
	"testing"
)

// assertNewFileMode is a no-op where there is no umask; the mode bits are
// not meaningful on such platforms.
func assertNewFileMode(*testing.T, os.FileInfo) {}
