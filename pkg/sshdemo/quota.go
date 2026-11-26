//go:build linux

package sshdemo

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func (s *Server) checkSessionQuota(sessionID, dir string, slot Slot, pgid int, procDir string) (reason, message string) {
	var pids []int
	var err error
	if s.cfg.Isolation == IsolationUID {
		pids, err = s.ops.ListUIDProcs(slot.UID)
	} else {
		pids, err = processGroupPIDs(procDir, pgid, unix.Getpgid)
	}
	if err != nil {
		return s.quotaCheckFailed(sessionID, "memory", err)
	}
	rss, err := totalRSS(procDir, pids)
	if err != nil {
		return s.quotaCheckFailed(sessionID, "memory", err)
	}
	if rss > s.cfg.SessionMemQuota {
		return s.quotaExceeded(sessionID, "memory", EndReasonMemQuota, rss, s.cfg.SessionMemQuota)
	}

	disk, err := workspaceUsage(dir, s.cfg.SessionDiskQuota, slot.UID, unix.SetfsuidRetUid)
	if errors.Is(err, errWorkspaceScanLimit) {
		s.logger.Warn("session exceeded resource quota", "session_id", sessionID, "resource", "disk", "err", err)
		return EndReasonDiskQuota, quotaExceededNotice("disk")
	}
	if err != nil {
		return s.quotaCheckFailed(sessionID, "disk", err)
	}
	if disk > s.cfg.SessionDiskQuota {
		return s.quotaExceeded(sessionID, "disk", EndReasonDiskQuota, disk, s.cfg.SessionDiskQuota)
	}
	return "", ""
}

func (s *Server) quotaExceeded(sessionID, resource, reason string, used, limit uint64) (string, string) {
	s.logger.Warn("session exceeded resource quota",
		"session_id", sessionID, "resource", resource, "used_bytes", used, "quota_bytes", limit)
	return reason, quotaExceededNotice(resource)
}

func quotaExceededNotice(resource string) string {
	return fmt.Sprintf("This session used more than its share of %s and was ended.\r\n", resource)
}

func (s *Server) quotaCheckFailed(sessionID, resource string, err error) (string, string) {
	s.logger.Warn("session resource check failed", "session_id", sessionID, "resource", resource, "err", err)
	return EndReasonQuotaCheck, "This session ended because its resource usage could not be checked.\r\n"
}

func writeQuotaNotice(w io.Writer, conn net.Conn, message string) {
	// SSH channel flow control can block writes even when TCP is writable.
	done := make(chan struct{})
	go func() {
		_, _ = io.WriteString(w, message)
		close(done)
	}()
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = conn.Close()
	}
}

func processGroupPIDs(procDir string, group int, getpgid func(int) (int, error)) ([]int, error) {
	pids, err := listProcPIDs(procDir)
	if err != nil {
		return nil, err
	}
	var members []int
	for _, pid := range pids {
		pgid, err := getpgid(pid)
		if err != nil {
			if procGone(err) {
				continue
			}
			return nil, err
		}
		if pgid == group {
			members = append(members, pid)
		}
	}
	return members, nil
}

func totalRSS(procDir string, pids []int) (uint64, error) {
	var total uint64
	for _, pid := range pids {
		status, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "status"))
		if err != nil {
			if procGone(err) {
				continue
			}
			return 0, err
		}
		for _, line := range strings.Split(string(status), "\n") {
			value, ok := strings.CutPrefix(line, "VmRSS:")
			if !ok {
				continue
			}
			value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "kB"))
			kib, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parse VmRSS for pid %d: %w", pid, err)
			}
			total += kib * 1024
			break
		}
		// Zombies and kernel threads have no VmRSS line.
	}
	return total, nil
}

type workspaceMeasurement struct {
	bytes uint64
	err   error
}

func workspaceUsage(dir string, quota uint64, uid int, setFSUID func(int) (int, error)) (uint64, error) {
	if uid == os.Geteuid() {
		return allocatedWorkspaceBytes(dir, quota)
	}

	// The supervisor has CAP_SETUID but lacks CAP_DAC_OVERRIDE. Switch only
	// this worker's filesystem UID so it can traverse the private workspace.
	// If credentials cannot be restored, exiting the locked goroutine retires
	// its OS thread instead of returning it to the runtime.
	result := make(chan workspaceMeasurement, 1)
	go func() {
		var measurement workspaceMeasurement
		defer func() { result <- measurement }()
		runtime.LockOSThread()
		previous, err := setFSUID(uid)
		if err != nil {
			runtime.UnlockOSThread()
			measurement.err = err
			return
		}
		defer func() {
			_, _ = setFSUID(previous)
			if restored, err := setFSUID(-1); err == nil && restored == previous {
				runtime.UnlockOSThread()
			}
		}()
		if current, err := setFSUID(-1); err != nil || current != uid {
			measurement.err = fmt.Errorf("cannot use filesystem uid %d", uid)
			return
		}
		measurement.bytes, measurement.err = allocatedWorkspaceBytes(dir, quota)
	}()
	measurement := <-result
	return measurement.bytes, measurement.err
}

const (
	workspaceDirFlags      = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	workspaceMinEntryBytes = 1 << 10
	workspaceMaxEntries    = 50_000
	workspaceMaxDepth      = 64
)

var errWorkspaceScanLimit = errors.New("workspace scan limit exceeded")

type workspaceScan struct {
	entries int
	openat  func(int, string, int, uint32) (int, error)
}

func allocatedWorkspaceBytes(dir string, quota uint64) (uint64, error) {
	fd, err := unix.Openat(unix.AT_FDCWD, dir, workspaceDirFlags, 0)
	if err != nil {
		return 0, err
	}
	scan := workspaceScan{openat: unix.Openat}
	return scan.countWorkspaceDir(os.NewFile(uintptr(fd), dir), quota, 0)
}

func (scan *workspaceScan) countWorkspaceDir(dir *os.File, quota uint64, depth int) (uint64, error) {
	defer func() { _ = dir.Close() }()
	fd := int(dir.Fd())
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, err
	}
	total := scan.chargeEntry(&stat)
	for total <= quota {
		names, err := dir.Readdirnames(64)
		if err != nil && err != io.EOF {
			return 0, err
		}
		for _, name := range names {
			used, err := scan.workspaceEntryBytes(fd, name, quota-total, depth)
			if err != nil {
				return 0, err
			}
			total += used
			if total > quota {
				return total, nil
			}
		}
		if err == io.EOF {
			break
		}
	}
	return total, nil
}

func (scan *workspaceScan) workspaceEntryBytes(fd int, name string, quota uint64, parentDepth int) (uint64, error) {
	// lstat relative to the open directory. Neither this lookup nor
	// opening a subdirectory can follow a link, even during renames.
	var stat unix.Stat_t
	if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, workspaceChildLookupError(err)
	}
	if scan.entries == workspaceMaxEntries {
		return 0, fmt.Errorf("%w: more than %d entries", errWorkspaceScanLimit, workspaceMaxEntries)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return scan.chargeEntry(&stat), nil
	}
	if parentDepth == workspaceMaxDepth {
		return 0, fmt.Errorf("%w: directory depth exceeds %d", errWorkspaceScanLimit, workspaceMaxDepth)
	}
	childFD, err := scan.openat(fd, name, workspaceDirFlags, 0)
	if err != nil {
		return 0, workspaceChildLookupError(err)
	}
	return scan.countWorkspaceDir(os.NewFile(uintptr(childFD), name), quota, parentDepth+1)
}

func (scan *workspaceScan) chargeEntry(stat *unix.Stat_t) uint64 {
	scan.entries++
	return max(uint64(stat.Blocks)*512, workspaceMinEntryBytes)
}

func workspaceChildLookupError(err error) error {
	switch err {
	case unix.ENOENT, unix.ENOTDIR, unix.ELOOP:
		// A child may have disappeared or changed type since it was listed.
		return nil
	default:
		return err
	}
}
