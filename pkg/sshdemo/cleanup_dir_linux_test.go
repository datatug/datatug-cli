package sshdemo

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCleanupDeepTree(t *testing.T) {
	for _, tc := range []struct {
		name         string
		depth, width int
	}{
		{"chain", 10_000, 0},
		{"chain within sandbox path ceiling", 512, 0},
		{"wide and deep", 155, 130},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			built := buildCleanupTree(t, root, tc.depth, tc.width)
			deletions := 0
			if err := CleanWorkspaceContents(root, func() { deletions++ }); err != nil {
				t.Fatal(err)
			}
			if want := built + built*tc.width; deletions != want {
				t.Fatalf("deleted %d entries, want %d", deletions, want)
			}
			if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
				t.Fatalf("cleanup left %d entries: %v", len(entries), err)
			}
			if built < tc.depth {
				t.Skipf("removed %d levels; host path policy rejected mkdir at level %d", built, built+1)
			}
		})
	}
}

func buildCleanupTree(t *testing.T, root string, depth, width int) int {
	t.Helper()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	for level := range depth {
		if err := unix.Mkdirat(fd, "d", 0o700); err != nil {
			if errors.Is(err, unix.ENAMETOOLONG) && depth == 10_000 {
				return level
			}
			t.Fatalf("mkdir at level %d: %v", level+1, err)
		}
		for i := range width {
			child, err := unix.Openat(fd, fmt.Sprintf("file-%03d", i), unix.O_WRONLY|unix.O_CREAT|unix.O_CLOEXEC, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_ = unix.Close(child)
		}
		next, err := unix.Openat(fd, "d", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		_ = unix.Close(fd)
		fd = next
	}
	return depth
}

func TestCleanupRewindsAfterBatchesWithShiftedEntries(t *testing.T) {
	root := t.TempDir()
	const entries = 600
	for i := range entries {
		writeFixtureFile(t, root, fmt.Sprintf("entry-%03d", i), "")
	}

	read, seek := cleanupRead, cleanupSeek
	t.Cleanup(func() { cleanupRead, cleanupSeek = read, seek })
	pastFirstBatch := false
	cleanupRead = func(f *os.File, n int) ([]string, error) {
		if pastFirstBatch {
			// Simulate shifted entries behind a cursor. A fresh scan will find them.
			return nil, io.EOF
		}
		pastFirstBatch = true
		return read(f, n)
	}
	cleanupSeek = func(f *os.File, offset int64, whence int) (int64, error) {
		pastFirstBatch = false
		return seek(f, offset, whence)
	}
	deleted := 0
	if err := CleanWorkspaceContents(root, func() { deleted++ }); err != nil {
		t.Fatal(err)
	}
	if remaining, err := os.ReadDir(root); err != nil || len(remaining) != 0 || deleted != entries {
		t.Fatalf("deleted %d, left %d, error %v", deleted, len(remaining), err)
	}
}

func TestCleanupUnlinksSpecialEntriesWithoutFollowing(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeFixtureFile(t, outside, "keep", "untouched")
	if err := os.Symlink(outside, filepath.Join(root, "symlink")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket, err := net.Listen("unix", filepath.Join(root, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = socket.Close() }()
	writeFixtureFile(t, root, "file", "remove")
	for _, mode := range []os.FileMode{0o000, 0o500} {
		dir := filepath.Join(root, fmt.Sprintf("locked-%o", mode))
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, dir, "file", "")
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanWorkspaceContents(root, func() {}); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("cleanup left %d entries: %v", len(entries), err)
	}
	raw, err := os.ReadFile(filepath.Join(outside, "keep"))
	if err != nil || string(raw) != "untouched" {
		t.Fatalf("link target was altered: %q, %v", raw, err)
	}
}

func TestCleanupIOFailures(t *testing.T) {
	for _, name := range []string{"open", "pin", "stat", "mount ID", "chmod pinned", "reopen pinned", "chmod readable", "read", "rewind", "unlink", "busy", "vanished", "descend", "climb", "device", "mount", "root", "same inode"} {
		t.Run(name, func(t *testing.T) {
			open, stat, statx := cleanupOpenat, cleanupStat, statMountID
			chmod, fchmod, read, seek, unlink := cleanupChmod, cleanupFchmod, cleanupRead, cleanupSeek, cleanupUnlink
			t.Cleanup(func() {
				cleanupOpenat, cleanupStat, statMountID = open, stat, statx
				cleanupChmod, cleanupFchmod, cleanupRead, cleanupSeek, cleanupUnlink = chmod, fchmod, read, seek, unlink
			})
			root := t.TempDir()
			buildCleanupTree(t, root, cleanupWindowSize+1, 1)
			want := errTestIO
			switch name {
			case "open", "pin", "reopen pinned", "descend", "climb":
				cleanupOpenat = func(fd int, entry string, flags int, mode uint32) (int, error) {
					switch {
					case name == "open", name == "descend" && entry == "d", name == "climb" && entry == "..":
						return -1, errTestIO
					case (name == "pin" || name == "reopen pinned") && entry == root:
						if flags&unix.O_PATH == 0 {
							return -1, unix.EACCES
						}
						if name == "pin" {
							return -1, errTestIO
						}
					case name == "reopen pinned":
						return -1, errTestIO
					}
					return open(fd, entry, flags, mode)
				}
			case "stat":
				cleanupStat = func(int, *unix.Stat_t) error { return errTestIO }
			case "mount ID":
				statMountID = func(int, string, int, int, *unix.Statx_t) error { return errTestIO }
			case "chmod pinned":
				cleanupOpenat = func(fd int, entry string, flags int, mode uint32) (int, error) {
					if flags&unix.O_PATH == 0 {
						return -1, unix.EACCES
					}
					return open(fd, entry, flags, mode)
				}
				cleanupChmod = func(string, os.FileMode) error { return errTestIO }
			case "chmod readable":
				if err := os.Chmod(root, 0o500); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = os.Chmod(root, 0o700) }()
				cleanupFchmod = func(int, uint32) error { return errTestIO }
			case "read":
				cleanupRead = func(*os.File, int) ([]string, error) { return nil, errTestIO }
			case "rewind":
				cleanupSeek = func(*os.File, int64, int) (int64, error) { return 0, errTestIO }
			case "unlink", "busy":
				if name == "busy" {
					want = ErrCleanupBoundary
				}
				cleanupUnlink = func(int, string, int) error {
					if name == "busy" {
						return unix.EBUSY
					}
					return unix.EINTR
				}
				if name == "unlink" {
					want = unix.EINTR
				}
			case "vanished":
				want = nil
				cleanupUnlink = func(fd int, entry string, flags int) error {
					err := unlink(fd, entry, flags)
					if err == nil {
						return unix.ENOENT
					}
					return err
				}
			case "device", "root", "same inode":
				want = ErrCleanupBoundary
				var calls int
				var rootInfo unix.Stat_t
				cleanupStat = func(fd int, info *unix.Stat_t) error {
					calls++
					err := stat(fd, info)
					switch {
					case calls == 1:
						rootInfo = *info
					case name == "device":
						info.Dev++
					case name == "root" && calls == 2:
						info.Ino = rootInfo.Ino
					case name == "same inode":
						info.Ino = rootInfo.Ino + 1
					}
					return err
				}
			case "mount":
				want = ErrCleanupBoundary
				calls := 0
				statMountID = func(fd int, path string, flags, mask int, info *unix.Statx_t) error {
					calls++
					err := statx(fd, path, flags, mask, info)
					if calls > 1 {
						info.Mnt_id++
					}
					return err
				}
			}
			if err := CleanWorkspaceContents(root, func() {}); !errors.Is(err, want) {
				t.Fatalf("cleanup = %v, want %v", err, want)
			}
		})
	}
	if err := CleanWorkspaceContents(filepath.Join(t.TempDir(), "absent"), func() {}); err != nil {
		t.Fatal(err)
	}
}
