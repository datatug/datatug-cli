package sshdemo

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

var (
	setNoNewPrivileges = unix.Prctl
	seccompSetFilter   = setSeccompFilter
)

func setSeccompFilter(program *unix.SockFprog) (uintptr, syscall.Errno) {
	tid, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER,
		unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(program)))
	return tid, errno
}

func installSlotFilter(goarch string, read func(string) ([]byte, error)) error {
	filter, err := slotFilter(goarch)
	if err != nil {
		return err
	}
	// PR_SET_NO_NEW_PRIVS applies to the calling thread. TSYNC propagates
	// that flag and the filter to the rest of the thread group.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := setNoNewPrivileges(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	tid, errno := seccompSetFilter(&program)
	if errno != 0 {
		return fmt.Errorf("install slot seccomp filter: %w", errno)
	}
	if tid != 0 {
		return fmt.Errorf("synchronize slot seccomp filter: thread %d rejected TSYNC", tid)
	}
	const statusPath = "/proc/self/status"
	status, err := read(statusPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", statusPath, err)
	}
	for line := range strings.Lines(string(status)) {
		if mode, ok := strings.CutPrefix(line, "Seccomp:"); ok && strings.TrimSpace(mode) == "2" {
			return nil
		}
	}
	return fmt.Errorf("%s does not report seccomp filter mode", statusPath)
}

func slotFilter(goarch string) ([]unix.SockFilter, error) {
	var arch uint32
	var blocked []uint32
	var pageRefs []uint32
	var socket, socketpair, clone uint32
	var setsockopt []uint32
	switch goarch {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
		blocked = []uint32{86, 265} // link, linkat
		// System V IPC and POSIX message queues outlive their creator in the
		// container's shared IPC namespace and stay charged to the slot.
		blocked = append(blocked, 29, 30, 31, 67, 64, 65, 66, 220, 68, 69, 70, 71, 240)
		// listen and bind: a socket in one slot that never reads keeps data
		// another slot sent charged to that slot after it has ended. Without
		// bind, two slots cannot agree on ports for a TCP simultaneous open.
		blocked = append(blocked, 50, 49)
		// sendfile, splice, tee, vmsplice and x32's own vmsplice number.
		pageRefs = []uint32{40, 275, 276, 278, 532}
		socket, socketpair, clone = 41, 53, 56
		setsockopt = []uint32{54, 541} // x32 has its own setsockopt number
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
		blocked = []uint32{37} // linkat (the ABI has no link syscall)
		blocked = append(blocked, 194, 196, 195, 197, 190, 193, 191, 192, 186, 189, 188, 187, 180)
		blocked = append(blocked, 201, 200) // listen, bind
		pageRefs = []uint32{71, 76, 77, 75} // sendfile, splice, tee, vmsplice
		socket, socketpair, clone = 198, 199, 220
		setsockopt = []uint32{208}
	default:
		return nil, fmt.Errorf("slot seccomp filter does not support %s", goarch)
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4}, // seccomp_data.arch
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: arch},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0}, // seccomp_data.nr
	}
	if goarch == "amd64" {
		// x32 uses the same audit arch and ORs this bit into syscall numbers.
		filter = append(filter, unix.SockFilter{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: ^uint32(1 << 30)})
	}
	// io_uring_setup, io_uring_enter and io_uring_register share syscall numbers.
	blocked = append(blocked, 425, 426, 427)
	const eperm = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	for _, nr := range blocked {
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: nr},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: eperm})
	}
	// Pipes can hold references to page cache another slot faulted in, which
	// keeps that slot's memory unreclaimable. EINVAL makes Go fall back to a
	// plain copy.
	const einval = unix.SECCOMP_RET_ERRNO | uint32(unix.EINVAL)
	for _, nr := range pageRefs {
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: nr},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: einval})
	}
	// IP_LOCAL_PORT_RANGE would let a socket choose its own port at connect
	// time, which is bind by another name.
	for _, nr := range setsockopt {
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 6, K: nr},
			unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 24}, // seccomp_data.args[1] (level), low word
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 3, K: unix.SOL_IP},
			unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 32}, // seccomp_data.args[2] (optname), low word
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: unix.IP_LOCAL_PORT_RANGE},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: eperm},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	}
	// clone3 passes its flags in memory, out of the filter's reach. Docker's
	// profile already answers ENOSYS; this keeps that true under any profile.
	// Callers fall back to clone, whose flags are checked below.
	const enosys = unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)
	filter = append(filter,
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: 435},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: enosys})
	// CLONE_PARENT makes the new process the supervisor's child: it would
	// never be reaped and would keep its PID charged to the slot. Flags are
	// argument 0 on both ABIs.
	filter = append(filter,
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 4, K: clone},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16}, // seccomp_data.args[0], low word
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, Jf: 1, K: unix.CLONE_PARENT},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: eperm},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	)
	return append(filter,
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: socket},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 3, K: socketpair},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16}, // seccomp_data.args[0], low word
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: unix.AF_UNIX},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: eperm},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	), nil
}
