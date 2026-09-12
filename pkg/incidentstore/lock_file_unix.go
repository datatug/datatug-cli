//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package incidentstore

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func lockFileContext(ctx context.Context, file *os.File) error {
	ticker := time.NewTicker(lockRetryDelay)
	defer ticker.Stop()
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
