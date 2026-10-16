//go:build windows

package queryresults

import "os"

// isOwnedByCurrentUser is true on Windows, where a user profile directory is
// already private to its user and os.FileInfo carries no owner.
func isOwnedByCurrentUser(os.FileInfo) bool { return true }
