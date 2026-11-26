//go:build linux

package sshdemo

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	containerMemoryMax = "/sys/fs/cgroup/memory.max"
	supervisorReserve  = 512 << 20
)

var statSessionsFS = unix.Statfs

func filesystemSize(path string) (uint64, error) {
	var stats unix.Statfs_t
	if err := statSessionsFS(path, &stats); err != nil {
		return 0, err
	}
	high, size := bits.Mul64(stats.Blocks, uint64(stats.Bsize))
	if high != 0 {
		return math.MaxUint64, nil
	}
	return size, nil
}

func (s *Server) checkCapacity() error {
	cfg := s.cfg
	size, err := s.ops.FilesystemSize(cfg.SessionsDir)
	if err != nil {
		return fmt.Errorf("sshdemo: statfs sessions-dir %s: %w", cfg.SessionsDir, err)
	}
	slots := uint64(cfg.MaxSessions)
	if cfg.SessionDiskQuota > size/slots {
		return fmt.Errorf("sshdemo: %d sessions with %d bytes of disk each exceed sessions filesystem capacity (%d bytes)",
			slots, cfg.SessionDiskQuota, size)
	}

	raw, err := s.ops.ReadMemoryMax()
	if errors.Is(err, os.ErrNotExist) || (err == nil && strings.TrimSpace(string(raw)) == "max") {
		s.logger.Warn("cgroup memory limit absent or unlimited; skipping memory capacity check", "path", containerMemoryMax)
		return nil
	}
	if err != nil {
		return fmt.Errorf("sshdemo: read cgroup memory limit: %w", err)
	}
	limit, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return fmt.Errorf("sshdemo: parse cgroup memory limit: %w", err)
	}
	perSlot := (max(limit, supervisorReserve) - supervisorReserve) / slots
	if cfg.SessionDiskQuota > perSlot || cfg.SessionMemQuota > perSlot-cfg.SessionDiskQuota {
		return fmt.Errorf("sshdemo: %d sessions with %d bytes of memory and %d bytes of tmpfs each plus the %d-byte reserve exceed the cgroup memory limit (%d bytes)",
			slots, cfg.SessionMemQuota, cfg.SessionDiskQuota, supervisorReserve, limit)
	}
	return nil
}
