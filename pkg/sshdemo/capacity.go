//go:build linux

package sshdemo

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	supervisorReserve    = 512 << 20
	supervisorPIDReserve = 256
)

var statSessionsFS = unix.Statfs

func filesystemCapacity(path string) (size, inodes uint64, err error) {
	var stats unix.Statfs_t
	if err := statSessionsFS(path, &stats); err != nil {
		return 0, 0, err
	}
	high, size := bits.Mul64(stats.Blocks, uint64(stats.Bsize))
	if high != 0 {
		return math.MaxUint64, stats.Files, nil
	}
	return size, stats.Files, nil
}

func (s *Server) checkFilesystemCapacity(slotMemory uint64) error {
	cfg := s.cfg
	size, inodes, err := s.ops.FilesystemCapacity(cfg.SessionsDir)
	if err != nil {
		return fmt.Errorf("sshdemo: statfs sessions-dir %s: %w", cfg.SessionsDir, err)
	}
	slots := uint64(cfg.MaxSessions)
	if cfg.Isolation == IsolationProcess {
		if cfg.SessionDiskQuota > size/slots {
			return fmt.Errorf("sshdemo: %d sessions with %d bytes of disk each exceed sessions filesystem capacity (%d bytes)",
				slots, cfg.SessionDiskQuota, size)
		}
		return nil
	}

	// checkMemoryCapacity has established that the total fits in uint64
	// with at least supervisorReserve bytes left for the roundups below.
	total := slotMemory * slots
	headroom := (total + 9) / 10
	if total > size || headroom > size-total {
		return fmt.Errorf("sshdemo: sessions filesystem capacity (%d bytes) does not fit %d bytes of slot budgets plus %d bytes of headroom",
			size, total, headroom)
	}
	const bytesPerInode = 512
	// Reserve the tmpfs root plus each slot parent, workspace, and its
	// initial home, repo and tmp directories created by the supervisor.
	const supervisorInodesPerSlot = 5
	requiredInodes := ((slotMemory+bytesPerInode-1)/bytesPerInode+supervisorInodesPerSlot)*slots + 1
	if inodes < requiredInodes {
		return fmt.Errorf("sshdemo: sessions filesystem inode capacity (%d inodes) is below %d inodes required for slot budgets and supervisor directories", inodes, requiredInodes)
	}
	return nil
}

func checkMemoryCapacity(cfg Config, scope cgroup) (uint64, error) {
	limit, err := scope.counter("memory.max")
	if err != nil {
		return 0, fmt.Errorf("sshdemo: require finite scope memory.max: %w", err)
	}
	slots := uint64(cfg.MaxSessions)
	available := max(limit, supervisorReserve) - supervisorReserve
	perSlot := (available - min(available, cfg.CleanupMemLimit)) / slots
	if cfg.SessionDiskQuota > perSlot || cfg.SessionMemQuota > perSlot-cfg.SessionDiskQuota {
		return 0, fmt.Errorf("sshdemo: %d sessions with %d bytes of memory and %d bytes of tmpfs each, a %d-byte supervisor reserve and a %d-byte cleanup limit exceed the cgroup memory limit (%d bytes)",
			slots, cfg.SessionMemQuota, cfg.SessionDiskQuota, supervisorReserve, cfg.CleanupMemLimit, limit)
	}
	return cfg.SessionMemQuota + cfg.SessionDiskQuota, nil
}

func checkPIDCapacity(cfg Config, scope cgroup) error {
	raw, err := scope.read("pids.max")
	if err != nil {
		return fmt.Errorf("sshdemo: read scope pids.max: %w", err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "max" {
		return nil
	}
	limit, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return fmt.Errorf("sshdemo: invalid scope pids.max: %w", err)
	}
	reserve := uint64(cfg.CleanupPIDsLimit) + supervisorPIDReserve
	if limit < reserve || uint64(cfg.MaxSessions) > (limit-reserve)/RlimitNProc {
		return fmt.Errorf("sshdemo: scope pids.max (%d) cannot fit %d slots of %d PIDs plus %d cleanup and %d supervisor PIDs",
			limit, cfg.MaxSessions, RlimitNProc, cfg.CleanupPIDsLimit, supervisorPIDReserve)
	}
	return nil
}
