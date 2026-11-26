//go:build !unix

package commands

import (
	"errors"
	"os"
)

func applyDemoSSHRlimits(demoSSHRlimits) error {
	return errors.New("ssh-session resource limits require a Unix host")
}

func fileOwnedByUID(os.FileInfo, int) bool {
	return false
}
