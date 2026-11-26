//go:build linux

package sshdemo

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestQuotaMeasuresProcessSetRSS(t *testing.T) {
	proc := t.TempDir()
	for pid, status := range map[int]string{
		1: "Name:\tfirst\nUid:\t20000 20000 20000 20000\nVmRSS:\t42 kB\n",
		2: "Name:\tsecond\nUid:\t20000 20000 20000 20000\nVmRSS:\t100 kB\n",
		3: "Name:\tunrelated\nUid:\t20001 20001 20001 20001\nVmRSS:\t1000 kB\n",
		4: "State:\tZ\nUid:\t20000 20000 20000 20000\n",
		5: "Name:\tvanished\n",
	} {
		writeProcStatus(t, proc, pid, status)
	}
	members, err := processGroupPIDs(proc, 20, func(pid int) (int, error) {
		switch pid {
		case 3:
			return 21, nil
		case 5:
			return 0, unix.ESRCH
		default:
			return 20, nil
		}
	})
	if err != nil || !reflect.DeepEqual(members, []int{1, 2, 4}) {
		t.Fatalf("process group members = %v, %v", members, err)
	}
	uidPIDs, err := listUIDProcsIn(proc, 20000)
	if err != nil || !reflect.DeepEqual(uidPIDs, members) {
		t.Fatalf("uid members = %v, %v", uidPIDs, err)
	}
	if got, err := totalRSS(proc, append(members, 999)); err != nil || got != 142*1024 {
		t.Fatalf("RSS = %d, %v, want %d", got, err, 142*1024)
	}

	if _, err := processGroupPIDs(filepath.Join(proc, "missing"), 20, unix.Getpgid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing proc directory: %v", err)
	}
	if _, err := processGroupPIDs(proc, 20, func(int) (int, error) { return 0, unix.EIO }); !errors.Is(err, unix.EIO) {
		t.Fatalf("unverified process membership: %v", err)
	}
	writeProcStatus(t, proc, 6, "VmRSS:\tbad kB\n")
	if _, err := totalRSS(proc, []int{6}); err == nil {
		t.Fatal("malformed VmRSS passed")
	}
	if err := os.MkdirAll(filepath.Join(proc, "7", "status"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := totalRSS(proc, []int{7}); !errors.Is(err, unix.EISDIR) {
		t.Fatalf("unreadable process status: %v", err)
	}
	if _, err := listUIDProcsIn(proc, 20000); !errors.Is(err, unix.EISDIR) {
		t.Fatalf("unreadable UID process status: %v", err)
	}
}

func TestQuotaChecksUIDModeAndFailsClosed(t *testing.T) {
	proc := t.TempDir()
	workspace := t.TempDir()
	cfg := DefaultConfig()
	slot := Slot{UID: os.Geteuid()}
	s := Server{
		cfg: cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ops: SysOps{ListUIDProcs: func(uid int) ([]int, error) {
			if uid != slot.UID {
				return nil, fmt.Errorf("expected slot UID %d, got %d", slot.UID, uid)
			}
			return []int{1}, nil
		}},
	}
	writeProcStatus(t, proc, 1, "VmRSS:\t42 kB\n")
	if reason, message := s.checkSessionQuota("id", workspace, slot, 0, proc); reason != "" || message != "" {
		t.Fatalf("under quota: %s %s", reason, message)
	}
	if reason, _ := s.checkSessionQuota("id", filepath.Join(workspace, "missing"), slot, 0, proc); reason != EndReasonQuotaCheck {
		t.Fatalf("unmeasurable workspace: %s", reason)
	}
	writeProcStatus(t, proc, 1, "VmRSS:\tbad kB\n")
	if reason, _ := s.checkSessionQuota("id", workspace, slot, 0, proc); reason != EndReasonQuotaCheck {
		t.Fatalf("unmeasurable process RSS: %s", reason)
	}
	s.ops.ListUIDProcs = func(int) ([]int, error) { return nil, unix.EIO }
	if reason, _ := s.checkSessionQuota("id", workspace, slot, 0, proc); reason != EndReasonQuotaCheck {
		t.Fatalf("unmeasurable UID process set: %s", reason)
	}
}

func writeProcStatus(t *testing.T, proc string, pid int, status string) {
	t.Helper()
	dir := filepath.Join(proc, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaWorkspaceCountsAllocatedBlocksWithoutFollowingLinks(t *testing.T) {
	workspace, external := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		filepath.Join(workspace, "tmp", "a"), filepath.Join(workspace, "b"), filepath.Join(external, "c"),
	} {
		if err := os.WriteFile(name, make([]byte, 16<<10), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sparse := filepath.Join(workspace, "sparse")
	if err := os.WriteFile(sparse, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(sparse, 128<<20); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(external, link); err != nil {
		t.Fatal(err)
	}
	var want uint64
	if err := filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		want += max(uint64(info.Sys().(*syscall.Stat_t).Blocks)*512, 1024)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := allocatedWorkspaceBytes(workspace, 1<<30); err != nil || got != want {
		t.Fatalf("disk usage = %d, %v, want %d", got, err, want)
	}
	root, err := os.Lstat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	quota := max(uint64(root.Sys().(*syscall.Stat_t).Blocks)*512, 1024)
	if got, err := allocatedWorkspaceBytes(workspace, quota); err != nil || got <= quota || got >= want {
		t.Fatalf("early quota measurement = %d, %v, want between %d and %d bytes", got, err, quota, want)
	}
	if _, err := allocatedWorkspaceBytes(link, 1<<30); err == nil {
		t.Fatal("followed symlink as workspace root")
	}
}

func TestQuotaWorkspaceEntryLimit(t *testing.T) {
	workspace := t.TempDir()
	nested := filepath.Join(workspace, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 49_998 { // The root and nested directory take the other two entries.
		dir := workspace
		if i%2 == 0 {
			dir = nested
		}
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := allocatedWorkspaceBytes(workspace, 1<<30); err != nil || got < 50_000*1024 {
		t.Fatalf("usage at entry limit = %d, %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(nested, "one-more"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := allocatedWorkspaceBytes(workspace, 1<<30); !errors.Is(err, errWorkspaceScanLimit) || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("scan above entry limit: %v", err)
	}
}

func TestQuotaWorkspaceDepthLimit(t *testing.T) {
	workspace := t.TempDir()
	deep := filepath.Join(workspace, strings.Repeat("d/", 64))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := allocatedWorkspaceBytes(workspace, 1<<30); err != nil {
		t.Fatalf("scan at depth limit: %v", err)
	}
	if err := os.Mkdir(filepath.Join(deep, "another-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := allocatedWorkspaceBytes(workspace, 1<<30); !errors.Is(err, errWorkspaceScanLimit) || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("scan above depth limit: %v", err)
	}
	s := Server{
		cfg:    DefaultConfig(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ops:    SysOps{ListUIDProcs: func(int) ([]int, error) { return nil, nil }},
	}
	if reason, notice := s.checkSessionQuota("id", workspace, Slot{UID: os.Geteuid()}, 0, ""); reason != EndReasonDiskQuota || !strings.Contains(notice, "disk") {
		t.Fatalf("depth breach = %s, %q", reason, notice)
	}
}

func TestQuotaWorkspaceDirectoryChangedBeforeOpen(t *testing.T) {
	for _, openErr := range []error{unix.ENOENT, unix.ENOTDIR, unix.ELOOP, unix.EACCES} {
		t.Run(openErr.Error(), func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.Mkdir(filepath.Join(workspace, "child"), 0o700); err != nil {
				t.Fatal(err)
			}
			dir, err := os.Open(workspace)
			if err != nil {
				t.Fatal(err)
			}
			scan := workspaceScan{openat: func(_ int, name string, flags int, _ uint32) (int, error) {
				if name != "child" || flags&unix.O_NOFOLLOW == 0 {
					t.Errorf("unexpected child open for %q with flags %#x", name, flags)
				}
				return -1, openErr
			}}
			used, err := scan.countWorkspaceDir(dir, 1<<30, 0)
			if openErr == unix.EACCES {
				if !errors.Is(err, openErr) {
					t.Fatalf("unreadable child directory: %v", err)
				}
				return
			}
			if err != nil || used < 1024 || scan.entries != 1 {
				t.Fatalf("changed directory sample = %d bytes, %d entries, %v", used, scan.entries, err)
			}
			if got, err := allocatedWorkspaceBytes(workspace, 1<<30); err != nil || got < used+1024 {
				t.Fatalf("next sample = %d, %v, want at least %d bytes", got, err, used+1024)
			}
		})
	}
}

func TestQuotaWorkspaceAccountsForFilesystemErrors(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"remove-one", "remove-two"} {
		if err := os.WriteFile(filepath.Join(workspace, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	// Populate the directory handle's read-ahead buffer, then remove the
	// entries still in it before their lstat calls.
	if _, err := dir.Readdirnames(1); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"remove-one", "remove-two"} {
		if err := os.Remove(filepath.Join(workspace, name)); err != nil {
			t.Fatal(err)
		}
	}
	scan := workspaceScan{openat: unix.Openat}
	if _, err := scan.countWorkspaceDir(dir, 1<<30, 0); err != nil {
		t.Fatalf("should skip vanished entry: %v", err)
	}
	if _, err := scan.countWorkspaceDir(dir, 1<<30, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("closed directory descriptor: %v", err)
	}

	file, err := os.CreateTemp(workspace, "not-a-directory")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scan.countWorkspaceDir(file, 1<<30, 0); !errors.Is(err, unix.ENOTDIR) {
		t.Fatalf("file used as directory: %v", err)
	}
	denied := filepath.Join(workspace, "unreadable")
	if err := os.Mkdir(denied, 0); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		if _, err := allocatedWorkspaceBytes(workspace, 1<<30); !errors.Is(err, unix.EACCES) {
			t.Fatalf("unreadable directory: %v", err)
		}
	}
}

func TestQuotaWorkspaceFilesystemUID(t *testing.T) {
	original := os.Geteuid()
	slot := original + 1
	fsuid := original
	dir := t.TempDir()
	want, err := allocatedWorkspaceBytes(dir, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	var measurementThread int
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	callerThread := unix.Gettid()
	changeUID := func(uid int) (int, error) {
		previous := fsuid
		if uid != -1 {
			fsuid = uid
		}
		if measurementThread == 0 {
			measurementThread = unix.Gettid()
		}
		if unix.Gettid() != measurementThread || measurementThread == callerThread {
			t.Error("filesystem UID changed outside the dedicated OS thread")
		}
		return previous, nil
	}
	if got, err := workspaceUsage(dir, 1<<30, slot, changeUID); err != nil || got != want || fsuid != original {
		t.Fatalf("filesystem UID measurement = %d, %v, final UID %d", got, err, fsuid)
	}

	if _, err := workspaceUsage(dir, 1<<30, slot, func(int) (int, error) { return original, unix.EPERM }); !errors.Is(err, unix.EPERM) {
		t.Fatalf("failed setfsuid: %v", err)
	}
	if _, err := workspaceUsage(dir, 1<<30, slot, func(int) (int, error) { return original, nil }); err == nil {
		t.Fatal("accepted an unchanged filesystem UID")
	}

	// Exercise the real syscall when available, and its rejected transition
	// on hosts where the test runner lacks CAP_SETUID.
	if _, err := workspaceUsage(dir, 1<<30, slot, unix.SetfsuidRetUid); err != nil &&
		!strings.Contains(err.Error(), "filesystem uid") && !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("unexpected filesystem UID result: %v", err)
	}
}
