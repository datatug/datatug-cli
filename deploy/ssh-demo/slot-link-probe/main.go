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
	if len(args) != 3 {
		return errors.New("expected cgroup, slot UID, and probe workspace")
	}
	uid, err := strconv.Atoi(args[1])
	if err != nil {
		return err
	}
	if err := sshdemo.EnterSlot(sshdemo.SlotEntry{CgroupDir: args[0], UID: uid, GID: uid}); err != nil {
		return err
	}
	source := filepath.Join(args[2], "source")
	if err := os.WriteFile(source, []byte("probe"), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(source) }()
	target := filepath.Join(args[2], "target")
	defer func() { _ = os.Remove(target) }()
	if err := os.Link(source, target); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("link returned %v, expected EPERM", err)
	}
	if err := unix.Linkat(unix.AT_FDCWD, source, unix.AT_FDCWD, target, 0); !errors.Is(err, unix.EPERM) {
		return fmt.Errorf("linkat returned %v, expected EPERM", err)
	}
	return nil
}
