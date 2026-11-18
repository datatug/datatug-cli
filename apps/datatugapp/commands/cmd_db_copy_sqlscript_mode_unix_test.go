//go:build unix

package commands

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

// assertNewFileMode checks a new file has the mode the user's umask gives it
// and no execute bit.
func assertNewFileMode(t *testing.T, info os.FileInfo) {
	t.Helper()
	old := syscall.Umask(0)
	want := os.FileMode(0o666 &^ old)
	syscall.Umask(old)
	assert.Equal(t, want, info.Mode().Perm())
	assert.Zero(t, info.Mode().Perm()&0o111)
}
