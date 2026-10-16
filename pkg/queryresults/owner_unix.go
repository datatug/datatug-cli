//go:build !windows

package queryresults

import (
	"os"
	"syscall"
)

// isOwnedByCurrentUser reports whether the user running the command owns
// info's file.
func isOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
