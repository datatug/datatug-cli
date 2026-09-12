//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package incidentstore

import (
	"context"
	"fmt"
	"os"
)

func lockFileContext(context.Context, *os.File) error {
	return fmt.Errorf("incident store file locking is not supported on this platform")
}

func unlockFile(*os.File) error { return nil }
