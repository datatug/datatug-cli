package sshdemo

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

var statMountID = unix.Statx

func mountIDAt(fd int, path string, flags int) (uint64, error) {
	var mount unix.Statx_t
	if err := statMountID(fd, path, flags, unix.STATX_MNT_ID, &mount); err != nil {
		return 0, err
	}
	if mount.Mask&unix.STATX_MNT_ID == 0 {
		return 0, errors.New("kernel did not report a mount ID")
	}
	return mount.Mnt_id, nil
}

func (g cgroup) verifyNamespaceBoundary() error {
	const requirement = "SSH demo runbook requires a private writable cgroup v2 scope with nsdelegate"
	mountID, err := g.fs.mountID(g.path)
	if err != nil {
		return fmt.Errorf("sshdemo: %s: identify mount backing %s: %w", requirement, g.path, err)
	}
	mounts, err := g.fs.read("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("sshdemo: %s: read /proc/self/mountinfo: %w", requirement, err)
	}
	if !hasCgroupNSDelegate(mounts, mountID) {
		return fmt.Errorf("sshdemo: %s: mount backing %s lacks cgroup2 with the nsdelegate super option", requirement, g.path)
	}
	raw, err := g.read("memory.max")
	if err != nil {
		return fmt.Errorf("sshdemo: %s: read scope memory.max: %w", requirement, err)
	}
	err = g.write("memory.max", strings.TrimSpace(string(raw)))
	switch {
	case errors.Is(err, unix.EPERM):
		return nil
	case err == nil:
		return fmt.Errorf("sshdemo: %s: scope memory.max accepted a write of its current value; expected EPERM", requirement)
	default:
		return fmt.Errorf("sshdemo: %s: scope memory.max write probe expected EPERM: %w", requirement, err)
	}
}

func hasCgroupNSDelegate(mounts []byte, mountID uint64) bool {
	wantID := strconv.FormatUint(mountID, 10)
	for line := range strings.Lines(string(mounts)) {
		left, right, hasSeparator := strings.Cut(line, " - ")
		id, _, _ := strings.Cut(left, " ")
		if hasSeparator && id == wantID {
			fields := strings.Fields(right)
			return len(fields) == 3 && fields[0] == "cgroup2" && slices.Contains(strings.Split(fields[2], ","), "nsdelegate")
		}
	}
	return false
}
