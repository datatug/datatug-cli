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

func TestHardLinkFilterArchitectures(t *testing.T) {
	const denied = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	for _, tc := range []struct {
		goarch string
		arch   uint32
		number uint32
		want   uint32
	}{
		{"amd64", unix.AUDIT_ARCH_X86_64, 86, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 265, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 86 | 1<<30, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 265 | 1<<30, denied},
		{"amd64", unix.AUDIT_ARCH_X86_64, 37, unix.SECCOMP_RET_ALLOW},
		{"amd64", unix.AUDIT_ARCH_I386, 9, unix.SECCOMP_RET_KILL_PROCESS},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 37, denied},
		{"arm64", unix.AUDIT_ARCH_AARCH64, 86, unix.SECCOMP_RET_ALLOW},
		{"arm64", unix.AUDIT_ARCH_X86_64, 86, unix.SECCOMP_RET_KILL_PROCESS},
	} {
		filter, err := hardLinkFilter(tc.goarch)
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
		var data [8]byte
		binary.BigEndian.PutUint32(data[:4], tc.number)
		binary.BigEndian.PutUint32(data[4:], tc.arch)
		got, err := vm.Run(data[:])
		if err != nil || uint32(got) != tc.want {
			t.Fatalf("%s syscall %d audit arch %x = %x, %v; want %x", tc.goarch, tc.number, tc.arch, got, err, tc.want)
		}
	}
}

func TestHardLinkFilterFailsClosed(t *testing.T) {
	prctl, seccomp := setNoNewPrivileges, seccompSetFilter
	t.Cleanup(func() { setNoNewPrivileges, seccompSetFilter = prctl, seccomp })
	requireErrorContains(t, denyHardLinks("unsupported", os.ReadFile), "does not support")
	for _, tc := range []struct {
		name         string
		prctlErr     error
		tid          uintptr
		errno        syscall.Errno
		status, want string
		readErr      error
	}{
		{name: "set no_new_privs", prctlErr: unix.EPERM, want: "set no_new_privs"},
		{name: "install", errno: unix.EINVAL, want: "install hard-link seccomp filter"},
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
			err := denyHardLinks("amd64", func(path string) ([]byte, error) {
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

func TestHardLinkFilterKernel(t *testing.T) {
	const probeEnv = "DATATUG_TEST_SECCOMP_PROBE"
	dir := os.Getenv(probeEnv)
	if dir == "" {
		// An invalid filter exercises the syscall without changing this
		// test runner's seccomp policy.
		if _, errno := setSeccompFilter(&unix.SockFprog{}); errno == 0 {
			t.Fatal("the kernel accepted an empty seccomp program")
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestHardLinkFilterKernel$")
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
	if err := denyHardLinks(runtime.GOARCH, os.ReadFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("allowed"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(tryLink)
	if err := <-result; !errors.Is(err, unix.EPERM) {
		t.Fatalf("linkat on existing thread = %v, want EPERM", err)
	}
	if runtime.GOARCH == "amd64" {
		// On amd64 os.Link issues the separate link syscall.
		if err := os.Link(source, destination); !errors.Is(err, unix.EPERM) {
			t.Fatalf("link = %v, want EPERM", err)
		}
	}
}
