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

func denyHardLinks(goarch string, read func(string) ([]byte, error)) error {
	filter, err := hardLinkFilter(goarch)
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
		return fmt.Errorf("install hard-link seccomp filter: %w", errno)
	}
	if tid != 0 {
		return fmt.Errorf("synchronize hard-link seccomp filter: thread %d rejected TSYNC", tid)
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

func hardLinkFilter(goarch string) ([]unix.SockFilter, error) {
	var arch uint32
	var blocked []uint32
	switch goarch {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
		blocked = []uint32{86, 265} // link, linkat
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
		blocked = []uint32{37} // linkat (the ABI has no link syscall)
	default:
		return nil, fmt.Errorf("hard-link seccomp filter does not support %s", goarch)
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
	for _, nr := range blocked {
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: nr},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)})
	}
	return append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW}), nil
}
