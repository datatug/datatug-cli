package commands

import "golang.org/x/sys/unix"

// POSIX message queues outlive their creator; /dev/mqueue is also root-only.
const demoSSHRlimitMsgQueue = unix.RLIMIT_MSGQUEUE
