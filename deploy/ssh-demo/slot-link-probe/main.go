//go:build linux && sshdemoprobe

// The Compose integration test mounts this binary into the test container.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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
	if _, err := os.ReadDir(filepath.Dir(otherWorkspace)); !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("list other slot parent: %v, expected permission error", err)
	}
	if _, err := os.Stat(otherWorkspace); !errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("reach other slot workspace: %v, expected permission error", err)
	}
	return nil
}

func buildDeepWorkspace(workspace string) error {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(workspace, flags, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for level := range 10_000 {
		if err := unix.Mkdirat(fd, "d", 0o700); err != nil {
			return fmt.Errorf("mkdir at level %d: %w", level+1, err)
		}
		next, err := unix.Openat(fd, "d", flags, 0)
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
		fd = next
	}
	fmt.Println("created 10000 directories")
	return nil
}
