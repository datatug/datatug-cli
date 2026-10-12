//go:build linux

package dbcopy

import "golang.org/x/sys/unix"

func publishExportExclusive(stage, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}
