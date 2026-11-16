//go:build !windows && !plan9

package localprojects

import "syscall"

const noFollowFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
