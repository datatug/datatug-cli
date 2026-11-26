//go:build unix && !linux

package commands

import "golang.org/x/sys/unix"

// No POSIX message-queue limit outside Linux (the demo runs on Linux only);
// lowering RLIMIT_MEMLOCK to 0 a second time changes nothing.
const demoSSHRlimitMsgQueue = unix.RLIMIT_MEMLOCK
