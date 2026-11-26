//go:build linux && sshdemoprobe

// The Compose integration test mounts this binary into the test container.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/datatug/datatug-cli/pkg/sshdemo"
	"golang.org/x/sys/unix"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 4 {
		return errors.New("expected probe mode, cgroup, slot UID, and workspace")
	}
	uid, err := strconv.Atoi(args[2])
	if err != nil {
		return err
	}
	if err := sshdemo.EnterSlot(sshdemo.SlotEntry{CgroupDir: args[1], UID: uid, GID: uid}); err != nil {
		return err
	}
	switch args[0] {
	case "isolation":
		if len(args) != 5 {
			return errors.New("expected other slot's workspace")
		}
		return checkIsolation(args[3], args[4])
	case "deep":
		return buildDeepWorkspace(args[3])
	default:
		return fmt.Errorf("unknown probe mode %q", args[0])
	}
}

func checkIsolation(workspace, otherWorkspace string) error {

	source := filepath.Join(workspace, "source")
	if err := os.WriteFile(source, []byte("probe"), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(source) }()
	target := filepath.Join(workspace, "target")
	defer func() { _ = os.Remove(target) }()
	if err := os.Link(source, target); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("link returned %v, expected EPERM", err)
	}
	if err := unix.Linkat(unix.AT_FDCWD, source, unix.AT_FDCWD, target, 0); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("linkat returned %v, expected EPERM", err)
	}

	if _, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("socket(AF_UNIX) returned %v, expected EPERM", err)
	}
	if _, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("socketpair(AF_UNIX) returned %v, expected EPERM", err)
	}
	if _, err := unix.SysvShmGet(unix.IPC_PRIVATE, 1<<20, unix.IPC_CREAT|0o600); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("shmget returned %v, expected EPERM", err)
	}
	if _, err := os.ReadDir(filepath.Dir(otherWorkspace)); !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("list other slot parent: %v, expected permission error", err)
	}
	if _, err := os.Stat(otherWorkspace); !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("reach other slot workspace: %v, expected permission error", err)
	}
	return nil
}

const probeDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

func buildDeepWorkspace(workspace string) error {
	root, err := unix.Open(workspace, probeDirFlags, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(root) }()
	if err := buildDeepChain(root, "d", 2_500); err != nil {
		return fmt.Errorf("short-name tree: %w", err)
	}
	if err := buildDeepChain(root, strings.Repeat("l", 255), 10); err != nil {
		return fmt.Errorf("long-name tree: %w", err)
	}
	if err := buildReadOnlyBoundary(root); err != nil {
		return fmt.Errorf("read-only boundary tree: %w", err)
	}
	fmt.Println("created 10000 short-name levels and 40 long-name levels")
	return nil
}

// Build four reachable segments, putting the existing tree under each new
// segment by rename so every operation fits AppArmor's path buffer.
func buildDeepChain(root int, name string, segmentDepth int) error {
	const segment = "probe-segment"
	bottom, err := buildChainSegment(root, name, name, segmentDepth)
	if err != nil {
		return err
	}
	_ = unix.Close(bottom)
	for range 3 {
		bottom, err := buildChainSegment(root, segment, name, segmentDepth)
		if err != nil {
			return err
		}
		err = unix.Renameat(root, name, bottom, name)
		_ = unix.Close(bottom)
		if err != nil {
			return fmt.Errorf("nest chain segment: %w", err)
		}
		if err := unix.Renameat(root, segment, root, name); err != nil {
			return fmt.Errorf("rename chain root: %w", err)
		}
	}
	return nil
}

func buildChainSegment(root int, top, name string, depth int) (int, error) {
	fd, entry := root, top
	for level := range depth {
		next, err := makeProbeDirectory(fd, entry)
		if fd != root {
			_ = unix.Close(fd)
		}
		if err != nil {
			return -1, fmt.Errorf("create segment level %d: %w", level+1, err)
		}
		fd, entry = next, name
	}
	return fd, nil
}

func makeProbeDirectory(parent int, name string) (int, error) {
	if err := unix.Mkdirat(parent, name, 0o700); err != nil {
		return -1, err
	}
	return unix.Openat(parent, name, probeDirFlags, 0)
}

// Eight 255-byte levels put the last directory just past cleanup's 2 KiB path
// budget; making it read-only and non-empty means cleanup must restore its
// permissions before it can move it.
func buildReadOnlyBoundary(root int) error {
	name := strings.Repeat("r", 255)
	bottom, err := buildChainSegment(root, name, name, 8)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(bottom) }()
	if err := unix.Mkdirat(bottom, "c", 0o700); err != nil {
		return err
	}
	return unix.Fchmod(bottom, 0o500)
}
