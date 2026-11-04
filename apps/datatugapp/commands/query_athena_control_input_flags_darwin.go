//go:build darwin

package commands

import "golang.org/x/sys/unix"

func athenaReadOnlyControlFlags(flags int) bool {
	if flags&unix.O_ACCMODE != unix.O_RDONLY {
		return false
	}
	return flags&unix.O_EVTONLY == 0
}
