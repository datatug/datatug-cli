package sshdemo

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

type slotEntryOps struct {
	write         func(string, string) error
	read          func(string) ([]byte, error)
	readDir       func(string) ([]os.DirEntry, error)
	setgroups     func([]int) error
	setgid        func(int) error
	setuid        func(int) error
	installFilter func(string, func(string) ([]byte, error)) error
}

var enterSlotOps = slotEntryOps{
	write:   writeCgroupFile,
	read:    os.ReadFile,
	readDir: os.ReadDir,
	// syscall uses the Go runtime's all-thread syscalls or libc's setxid
	// wrappers, depending on whether the binary links cgo.
	setgroups:     syscall.Setgroups,
	setgid:        syscall.Setgid,
	setuid:        syscall.Setuid,
	installFilter: installSlotFilter,
}

// EnterSlot migrates the calling process to its slot and permanently drops
// privileges on all threads. The child must exit on any error.
func EnterSlot(entry SlotEntry) error {
	if entry.CgroupDir == "" || entry.UID <= 0 || entry.GID <= 0 ||
		uint64(entry.UID) >= math.MaxUint32 || uint64(entry.GID) >= math.MaxUint32 {
		return errors.New("slot entry requires a cgroup directory and non-root UID and GID")
	}

	ops := enterSlotOps
	if err := ops.write(filepath.Join(entry.CgroupDir, "cgroup.procs"), "0"); err != nil {
		return fmt.Errorf("join slot cgroup: %w", err)
	}
	if err := verifySlotMembership(entry.CgroupDir, ops); err != nil {
		return err
	}
	if err := ops.setgroups(nil); err != nil {
		return fmt.Errorf("clear supplementary groups: %w", err)
	}
	if err := ops.setgid(entry.GID); err != nil {
		return fmt.Errorf("set slot GID: %w", err)
	}
	if err := ops.setuid(entry.UID); err != nil {
		return fmt.Errorf("set slot UID: %w", err)
	}
	if err := verifySlotThreadIDs(entry, ops); err != nil {
		return err
	}
	if err := ops.setuid(0); !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("verify setuid(0) is denied: got %v, want EPERM", err)
	}
	if err := verifySlotMembership(entry.CgroupDir, ops); err != nil {
		return err
	}
	return ops.installFilter(runtime.GOARCH, ops.read)
}

func verifySlotMembership(dir string, ops slotEntryOps) error {
	const procCgroup = "/proc/self/cgroup"
	raw, err := ops.read(procCgroup)
	if err != nil {
		return fmt.Errorf("read %s: %w", procCgroup, err)
	}
	if want := "0::/" + filepath.Base(dir); !slices.Contains(strings.Split(string(raw), "\n"), want) {
		return fmt.Errorf("%s does not report slot %s", procCgroup, dir)
	}
	return nil
}

func verifySlotThreadIDs(entry SlotEntry, ops slotEntryOps) error {
	const tasksPath = "/proc/self/task"
	tasks, err := ops.readDir(tasksPath)
	if err != nil {
		return fmt.Errorf("list process threads: %w", err)
	}
	if len(tasks) == 0 {
		return errors.New("no process threads found")
	}
	uid, gid := strconv.Itoa(entry.UID), strconv.Itoa(entry.GID)
	for _, task := range tasks {
		path := filepath.Join(tasksPath, task.Name(), "status")
		raw, err := ops.read(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if !slotStatusIDsMatch(string(raw), "Uid:", uid) {
			return fmt.Errorf("%s: real, effective and saved UIDs must equal %s", path, uid)
		}
		if !slotStatusIDsMatch(string(raw), "Gid:", gid) {
			return fmt.Errorf("%s: real, effective and saved GIDs must equal %s", path, gid)
		}
	}
	return nil
}

func slotStatusIDsMatch(status, label, id string) bool {
	for line := range strings.Lines(status) {
		if values, found := strings.CutPrefix(line, label); found {
			fields := strings.Fields(values)
			return len(fields) >= 3 && fields[0] == id && fields[1] == id && fields[2] == id
		}
	}
	return false
}
