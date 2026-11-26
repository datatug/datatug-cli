package sshdemo

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

type slotFilterCase struct {
	goarch string
	arch   uint32
	number uint32
	arg0   uint32
	want   uint32
}

func TestSlotFilterArchitectures(t *testing.T) {
	const denied = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	cases := []slotFilterCase{
		{"amd64", unix.AUDIT_ARCH_X86_64, 86, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 265, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 86 | 1<<30, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 265 | 1<<30, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 37, 0, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_I386, 9, 0, unix.SECCOMP_RET_KILL_PROCESS},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 37, 0, denied},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 86, 0, unix.SECCOMP_RET_ALLOW},
		{"arm64", unix.AUDIT_ARCH_X86_64, 86, 0, unix.SECCOMP_RET_KILL_PROCESS},
		{"amd64", unix.AUDIT_ARCH_X86_64, 41, unix.AF_UNIX, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 41, unix.AF_INET, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_X86_64, 41, unix.AF_INET6, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_X86_64, 53, unix.AF_UNIX, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 53, unix.AF_INET, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_X86_64, 53, unix.AF_INET6, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_X86_64, 41 | 1<<30, unix.AF_UNIX, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 41 | 1<<30, unix.AF_INET, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_X86_64, 41 | 1<<30, unix.AF_INET6, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_X86_64, 53 | 1<<30, unix.AF_UNIX, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 53 | 1<<30, unix.AF_INET, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_X86_64, 53 | 1<<30, unix.AF_INET6, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_X86_64, 425, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 425 | 1<<30, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 426, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 426 | 1<<30, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 427, 0, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 427 | 1<<30, 0, denied},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 198, unix.AF_UNIX, denied},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 198, unix.AF_INET, unix.SECCOMP_RET_ALLOW},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 198, unix.AF_INET6, unix.SECCOMP_RET_ALLOW},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 199, unix.AF_UNIX, denied},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 199, unix.AF_INET, unix.SECCOMP_RET_ALLOW},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 199, unix.AF_INET6, unix.SECCOMP_RET_ALLOW},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 425, 0, denied},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 426, 0, denied},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 427, 0, denied},
	}
	// System V IPC (shm, sem, msg) and mq_open, by architecture.
	for _, nr := range []uint32{29, 30, 31, 67, 64, 65, 66, 220, 68, 69, 70, 71, 240} {
		cases = append(cases, slotFilterCase{"amd64", unix.AUDIT_ARCH_X86_64, nr, 0, denied},
			slotFilterCase{"amd64", unix.AUDIT_ARCH_X86_64, nr | 1<<30, 0, denied})
	}
	for _, nr := range []uint32{194, 196, 195, 197, 190, 193, 191, 192, 186, 189, 188, 187, 180} {
		cases = append(cases, slotFilterCase{"arm64", unix.AUDIT_ARCH_AARCH64, nr, 0, denied})
	}
	cases = append(cases, slotFilterCase{"amd64", unix.AUDIT_ARCH_X86_64, 50, 0, denied},
		slotFilterCase{"amd64", unix.AUDIT_ARCH_X86_64, 50 | 1<<30, 0, denied},
		slotFilterCase{"arm64", unix.AUDIT_ARCH_AARCH64, 201, 0, denied})
	const einval = unix.SECCOMP_RET_ERRNO | uint32(unix.EINVAL)
	for _, nr := range []uint32{40, 275, 276, 278, 532} {
		cases = append(cases, slotFilterCase{"amd64", unix.AUDIT_ARCH_X86_64, nr, 0, einval},
			slotFilterCase{"amd64", unix.AUDIT_ARCH_X86_64, nr | 1<<30, 0, einval})
	}
	for _, nr := range []uint32{71, 76, 77, 75} {
		cases = append(cases, slotFilterCase{"arm64", unix.AUDIT_ARCH_AARCH64, nr, 0, einval})
	}
	const threadFlags = unix.CLONE_VM | unix.CLONE_FS | unix.CLONE_FILES | unix.CLONE_SIGHAND | unix.CLONE_SYSVSEM | unix.CLONE_THREAD
	for _, c := range []struct {
		goarch string
		arch   uint32
		nr     uint32
	}{{"amd64", unix.AUDIT_ARCH_X86_64, 56}, {"amd64", unix.AUDIT_ARCH_X86_64, 56 | 1<<30}, {"arm64", unix.AUDIT_ARCH_AARCH64, 220}} {
		cases = append(cases,
			slotFilterCase{c.goarch, c.arch, c.nr, threadFlags, unix.SECCOMP_RET_ALLOW},
			slotFilterCase{c.goarch, c.arch, c.nr, uint32(unix.SIGCHLD), unix.SECCOMP_RET_ALLOW},
			slotFilterCase{c.goarch, c.arch, c.nr, unix.CLONE_PARENT | uint32(unix.SIGCHLD), denied},
			slotFilterCase{c.goarch, c.arch, c.nr, unix.CLONE_PARENT | threadFlags, denied})
	}
	for _, tc := range cases {
		filter, err := slotFilter(tc.goarch)
		if err != nil {
			t.Fatal(err)
		}
		raw := make([]bpf.RawInstruction, len(filter))
		for i, ins := range filter {
			raw[i] = bpf.RawInstruction{Op: ins.Code, Jt: ins.Jt, Jf: ins.Jf, K: ins.K}
		}
		ins, ok := bpf.Disassemble(raw)
		if !ok {
			t.Fatal("filter contains unknown instructions")
		}
		vm, err := bpf.NewVM(ins)
		if err != nil {
			t.Fatal(err)
		}
		var data [64]byte
		binary.BigEndian.PutUint32(data[:4], tc.number)
		binary.BigEndian.PutUint32(data[4:], tc.arch)
		binary.BigEndian.PutUint32(data[16:], tc.arg0)
		binary.BigEndian.PutUint32(data[20:], unix.AF_UNIX)
		got, err := vm.Run(data[:])
		if err != nil || uint32(got) != tc.want {
			t.Fatalf("%s syscall %d audit arch %x arg0 %d = %x, %v; want %x", tc.goarch, tc.number, tc.arch, tc.arg0, got, err, tc.want)
		}
	}
}

func TestSlotFilterFailsClosed(t *testing.T) {
	prctl, seccomp := setNoNewPrivileges, seccompSetFilter
	t.Cleanup(func() { setNoNewPrivileges, seccompSetFilter = prctl, seccomp })
	requireErrorContains(t, installSlotFilter("unsupported", os.ReadFile), "does not support")
	for _, tc := range []struct {
		name         string
		prctlErr     error
		tid          uintptr
		errno        syscall.Errno
		status, want string
		readErr      error
	}{
		{name: "set no_new_privs", prctlErr: unix.EPERM, want: "set no_new_privs"},
		{name: "install", errno: unix.EINVAL, want: "install slot seccomp filter"},
		{name: "TSYNC", tid: 42, want: "thread 42 rejected TSYNC"},
		{name: "status read", readErr: errTestIO, want: "read /proc/self/status"},
		{name: "wrong mode", status: "Name:\tdatatug\nSeccomp:\t1\n", want: "does not report seccomp filter mode"},
		{name: "success", status: "Name:\tdatatug\nSeccomp:\t2\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setNoNewPrivileges = func(option int, value, a3, a4, a5 uintptr) error {
				if option != unix.PR_SET_NO_NEW_PRIVS || value != 1 || a3|a4|a5 != 0 {
					t.Fatal("incorrect prctl call")
				}
				return tc.prctlErr
			}
			seccompSetFilter = func(p *unix.SockFprog) (uintptr, syscall.Errno) {
				if p.Len == 0 || p.Filter == nil {
					t.Fatal("missing filter")
				}
				return tc.tid, tc.errno
			}
			err := installSlotFilter("amd64", func(path string) ([]byte, error) {
				if path != "/proc/self/status" {
					t.Fatalf("read %q", path)
				}
				return []byte(tc.status), tc.readErr
			})
			if tc.want != "" {
				requireErrorContains(t, err, tc.want)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSlotFilterKernel(t *testing.T) {
	const probeEnv = "DATATUG_TEST_SECCOMP_PROBE"
	dir := os.Getenv(probeEnv)
	if dir == "" {
		// An invalid filter exercises the syscall without changing this
		// test runner's seccomp policy.
		if _, errno := setSeccompFilter(&unix.SockFprog{}); errno == 0 {
			t.Fatal("the kernel accepted an empty seccomp program")
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestSlotFilterKernel$")
		cmd.Env = append(os.Environ(), probeEnv+"="+t.TempDir())
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seccomp probe: %v\n%s", err, output)
		}
		return
	}

	// Keep a second OS thread alive across filter installation.
	started, tryLink := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	source, destination := filepath.Join(dir, "source"), filepath.Join(dir, "destination")
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		close(started)
		<-tryLink
		result <- unix.Linkat(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, 0)
	}()
	<-started
	if err := installSlotFilter(runtime.GOARCH, os.ReadFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("allowed"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(tryLink)
	if err := <-result; !errors.Is(err, unix.EPERM) {
		t.Fatalf("linkat on existing thread = %v, want EPERM", err)
	}
	if _, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0); !errors.Is(err, unix.EPERM) {
		t.Fatalf("socket(AF_UNIX) = %v, want EPERM", err)
	}
	if _, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0); !errors.Is(err, unix.EPERM) {
		t.Fatalf("socketpair(AF_UNIX) = %v, want EPERM", err)
	}
	if _, err := unix.SysvShmGet(unix.IPC_PRIVATE, 1<<20, unix.IPC_CREAT|0o600); !errors.Is(err, unix.EPERM) {
		t.Fatalf("shmget = %v, want EPERM", err)
	}
	for _, nr := range []uintptr{unix.SYS_SEMGET, unix.SYS_MSGGET} {
		if _, _, errno := unix.Syscall(nr, unix.IPC_PRIVATE, 1, unix.IPC_CREAT|0o600); errno != unix.EPERM {
			t.Fatalf("syscall %d = %v, want EPERM", nr, errno)
		}
	}
	// CLONE_PARENT|CLONE_THREAD without CLONE_SIGHAND is invalid, so a broken
	// filter yields EINVAL from the kernel instead of creating a process.
	if _, _, errno := unix.RawSyscall(unix.SYS_CLONE, unix.CLONE_PARENT|unix.CLONE_THREAD, 0, 0); errno != unix.EPERM {
		t.Fatalf("clone(CLONE_PARENT) = %v, want EPERM", errno)
	}
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(pipe[0]); _ = unix.Close(pipe[1]) }()
	src, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	if _, err := unix.Sendfile(pipe[1], int(src.Fd()), nil, 4); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("sendfile to a pipe = %v, want EINVAL", err)
	}
	if _, err := unix.Vmsplice(pipe[1], []unix.Iovec{{Base: &[]byte("x")[0], Len: 1}}, 0); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("vmsplice = %v, want EINVAL", err)
	}
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket(AF_INET) = %v", err)
	}
	if err := unix.Listen(fd, 1); !errors.Is(err, unix.EPERM) {
		t.Fatalf("listen = %v, want EPERM", err)
	}
	_ = unix.Close(fd)

	if runtime.GOARCH == "amd64" {
		// On amd64 os.Link issues the separate link syscall.
		if err := os.Link(source, destination); !errors.Is(err, unix.EPERM) {
			t.Fatalf("link = %v, want EPERM", err)
		}
	}
}
