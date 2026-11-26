package sshdemo

import (
	"fmt"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCgroupNamespaceBoundary(t *testing.T) {
	const (
		mountLine = "42 1 0:2 /path /sys/fs/cgroup rw - "
		otherLine = "41 1 0:2 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw,nsdelegate\n"
	)
	for _, tc := range []struct {
		name, mount   string
		want          string
		write, readID error
		readMount     error
		readMemory    error
	}{
		{name: "delegated", mount: mountLine + "cgroup2 cgroup2 rw,nsdelegate,memory_recursiveprot", write: unix.EPERM},
		{name: "wrong mount", mount: otherLine, want: "lacks cgroup2 with the nsdelegate"},
		{name: "wrong fs", mount: mountLine + "ext4 /dev/root rw,nsdelegate", want: "lacks cgroup2 with the nsdelegate"},
		{name: "wrong option", mount: mountLine + "cgroup2 cgroup2 rw,nsdelegatefake", want: "lacks cgroup2 with the nsdelegate"},
		{name: "short record", mount: mountLine + "cgroup2", want: "lacks cgroup2 with the nsdelegate"},
		{name: "mountinfo denied", readMount: errTestIO, want: "read /proc/self/mountinfo"},
		{name: "mount ID unavailable", readID: errTestIO, want: "identify mount"},
		{name: "memory unavailable", mount: mountLine + "cgroup2 cgroup2 rw,nsdelegate", readMemory: errTestIO, want: "read scope memory.max"},
		{name: "writable scope", mount: mountLine + "cgroup2 cgroup2 rw,nsdelegate", want: "accepted a write of its current value"},
		{name: "read-only scope", mount: mountLine + "cgroup2 cgroup2 ro,nsdelegate", write: unix.EROFS, want: "write probe expected EPERM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := cgroup{path: "/scope", fs: cgroupFS{
				mountID: func(string) (uint64, error) { return 42, tc.readID },
				read: func(path string) ([]byte, error) {
					if path == "/proc/self/mountinfo" {
						return []byte(tc.mount), tc.readMount
					}
					return []byte("1234\n"), tc.readMemory
				},
				write: func(path, value string) error {
					if path != filepath.Join("/scope", "memory.max") || value != "1234" {
						t.Fatalf("unexpected write %s %q", path, value)
					}
					return tc.write
				},
			}}
			err := g.verifyNamespaceBoundary()
			if tc.want != "" {
				requireErrorContains(t, err, tc.want)
				requireErrorContains(t, err, "SSH demo runbook")
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCgroupStartupRejectsSuccessfulScopeWrite(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	ops.cgroups.write = func(path, value string) error {
		if path != filepath.Join(cfg.CgroupRoot, "memory.max") {
			return fmt.Errorf("unexpected write to %s with value %s", path, value)
		}
		return nil
	}
	_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
	requireErrorContains(t, err, "scope memory.max accepted a write")
}

func TestMountID(t *testing.T) {
	if _, err := defaultCgroupFS().mountID(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	stat := statMountID
	t.Cleanup(func() { statMountID = stat })
	statMountID = func(int, string, int, int, *unix.Statx_t) error { return nil }
	_, err := defaultCgroupFS().mountID(t.TempDir())
	requireErrorContains(t, err, "did not report a mount ID")
}
