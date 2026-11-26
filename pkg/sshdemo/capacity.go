//go:build linux

package sshdemo

import (
	"fmt"
	"math"
	"math/bits"

	"golang.org/x/sys/unix"
)

const supervisorReserve = 512 << 20

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
	requiredInodes := (total + 511) / 512
	if inodes < requiredInodes {
		return fmt.Errorf("sshdemo: sessions filesystem inode capacity (%d inodes) is below %d inodes required for slot budgets", inodes, requiredInodes)
	}
	return nil
}

func checkMemoryCapacity(cfg Config, scope cgroup) (uint64, error) {
	limit, err := scope.counter("memory.max")
	if err != nil {
		return 0, fmt.Errorf("sshdemo: require finite scope memory.max: %w", err)
	}
	slots := uint64(cfg.MaxSessions)
	perSlot := (max(limit, supervisorReserve) - supervisorReserve) / slots
	if cfg.SessionDiskQuota > perSlot || cfg.SessionMemQuota > perSlot-cfg.SessionDiskQuota {
		return 0, fmt.Errorf("sshdemo: %d sessions with %d bytes of memory and %d bytes of tmpfs each plus the %d-byte reserve exceed the cgroup memory limit (%d bytes)",
			slots, cfg.SessionMemQuota, cfg.SessionDiskQuota, supervisorReserve, limit)
	}
	return cfg.SessionMemQuota + cfg.SessionDiskQuota, nil
}
