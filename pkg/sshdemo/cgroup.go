//go:build linux

package sshdemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	cgroupDrainTimeout = 5 * time.Second
	slotEntryTimeout   = 5 * time.Second
)

type cgroupFS struct {
	verify  func(string) error
	mountID func(string) (uint64, error)
	read    func(string) ([]byte, error)
	write   func(string, string) error
	mkdir   func(string, os.FileMode) error
}

func defaultCgroupFS() cgroupFS {
	return cgroupFS{
		verify:  verifyCgroupFS,
		mountID: func(path string) (uint64, error) { return mountIDAt(unix.AT_FDCWD, path, 0) },
		read:    os.ReadFile,
		write:   writeCgroupFile,
		mkdir:   os.Mkdir,
	}
}

var statCgroupFS = unix.Statfs

func verifyCgroupFS(path string) error {
	var info unix.Statfs_t
	if err := statCgroupFS(path, &info); err != nil {
		return err
	}
	if info.Type != unix.CGROUP2_SUPER_MAGIC {
		return errors.New("expected a cgroup v2 filesystem")
	}
	return nil
}

func writeCgroupFile(path, value string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = file.WriteString(value)
	return errors.Join(err, file.Close())
}

type cgroup struct {
	path string
	fs   cgroupFS
}

func (g cgroup) read(name string) ([]byte, error) {
	return g.fs.read(filepath.Join(g.path, name))
}

func (g cgroup) write(name, value string) error {
	if err := g.fs.write(filepath.Join(g.path, name), value); err != nil {
		return fmt.Errorf("write %s/%s: %w", g.path, name, err)
	}
	return nil
}

func (g cgroup) counter(name string) (uint64, error) {
	raw, err := g.read(name)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
}

func (g cgroup) event(file, key string) (uint64, error) {
	raw, err := g.read(file)
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(raw)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == key {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("%s/%s lacks %s", g.path, file, key)
}

func (g cgroup) child(name string) cgroup {
	return cgroup{path: filepath.Join(g.path, name), fs: g.fs}
}

func (g cgroup) ensureDir() (existed bool, err error) {
	err = g.fs.mkdir(g.path, 0o755)
	if errors.Is(err, os.ErrExist) {
		return true, nil
	}
	return false, err
}

func (g cgroup) delegateSlots() error {
	supervisor := g.child("supervisor")
	raw, err := g.read("cgroup.procs")
	if err != nil {
		return err
	}
	procs := strings.Fields(string(raw))
	pid := strconv.Itoa(os.Getpid())
	if !slices.Contains(procs, pid) {
		// A restarted supervisor may already be in the leaf.
		current, err := supervisor.read("cgroup.procs")
		if err != nil || !slices.Contains(strings.Fields(string(current)), pid) {
			return fmt.Errorf("%s is not the supervisor's cgroup scope", g.path)
		}
	}
	if _, err := supervisor.ensureDir(); err != nil {
		return err
	}
	for _, pid := range procs {
		if err := supervisor.write("cgroup.procs", pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return g.write("cgroup.subtree_control", "+memory +pids")
}

func (g cgroup) configureLimits(memoryMax uint64, pidsMax int) error {
	for _, setting := range []struct{ file, value string }{
		{"memory.max", strconv.FormatUint(memoryMax, 10)},
		{"memory.swap.max", "0"},
		{"memory.oom.group", "1"},
		{"pids.max", strconv.Itoa(pidsMax)},
	} {
		if err := g.write(setting.file, setting.value); err != nil {
			return err
		}
	}
	return nil
}

func (g cgroup) drain(ctx context.Context, timeout time.Duration) error {
	if err := g.write("cgroup.kill", "1"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(15 * time.Millisecond)
	defer tick.Stop()
	for {
		populated, err := g.event("cgroup.events", "populated")
		if err != nil {
			return err
		}
		if populated == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func slotName(index int) string {
	return "slot-" + strconv.Itoa(index)
}

func (s *Server) prepareCgroups(ctx context.Context) error {
	scope := cgroup{path: s.cfg.CgroupRoot, fs: s.ops.cgroups}
	if err := scope.fs.verify(scope.path); err != nil {
		return fmt.Errorf("sshdemo: unusable --cgroup-root %s: %w", scope.path, err)
	}
	budget, err := checkMemoryCapacity(s.cfg, scope)
	if err != nil {
		return err
	}
	if err := scope.verifyNamespaceBoundary(); err != nil {
		return err
	}
	if err := checkPIDCapacity(s.cfg, scope); err != nil {
		return err
	}
	if err := s.checkFilesystemCapacity(budget); err != nil {
		return err
	}
	if err := scope.delegateSlots(); err != nil {
		return fmt.Errorf("sshdemo: delegate session cgroups: %w", err)
	}

	// A restart may have fewer slots than its predecessor. Drain unused
	// siblings too, so their processes cannot outlive the old UID assignment.
	entries, err := os.ReadDir(scope.path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "supervisor" && entry.Name() != "cleanup" {
			index, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "slot-"))
			if err == nil && index >= 0 && index < s.cfg.MaxSessions && entry.Name() == slotName(index) {
				continue
			}
			if err := scope.child(entry.Name()).drain(ctx, cgroupDrainTimeout); err != nil {
				return fmt.Errorf("sshdemo: drain unassigned cgroup %s: %w", entry.Name(), err)
			}
		}
	}

	s.cgroups = make([]cgroup, s.cfg.MaxSessions)
	for index := range s.cgroups {
		group := scope.child(slotName(index))
		s.cgroups[index] = group
		existed, err := group.ensureDir()
		if err != nil {
			return fmt.Errorf("sshdemo: create slot cgroup: %w", err)
		}
		// Also verify that a fresh slot exposes cgroup.kill and cgroup.events.
		if err := group.drain(ctx, cgroupDrainTimeout); err != nil {
			if !existed || s.quarantineSlot(index, err) {
				return fmt.Errorf("sshdemo: no usable session cgroups: %w", err)
			}
			continue
		}
		if err := group.configureLimits(budget, RlimitNProc); err != nil {
			return fmt.Errorf("sshdemo: configure slot cgroup: %w", err)
		}
		if _, err := group.event("memory.events", "oom_group_kill"); err != nil {
			return fmt.Errorf("sshdemo: read slot OOM counter: %w", err)
		}
	}

	s.cleanupGroup = scope.child("cleanup")
	if _, err := s.cleanupGroup.ensureDir(); err != nil {
		return fmt.Errorf("sshdemo: create cleanup cgroup: %w", err)
	}
	if err := s.cleanupGroup.drain(ctx, cgroupDrainTimeout); err != nil {
		return fmt.Errorf("sshdemo: drain cleanup cgroup: %w", err)
	}
	if err := s.cleanupGroup.configureLimits(s.cfg.CleanupMemLimit, s.cfg.CleanupPIDsLimit); err != nil {
		return fmt.Errorf("sshdemo: configure cleanup cgroup: %w", err)
	}
	return nil
}

func (s *Server) quarantineSlot(index int, err error) bool {
	uid := s.cfg.UIDBase + index
	if s.cfg.Isolation == IsolationProcess {
		uid = os.Getuid()
	}
	allQuarantined := s.slots.quarantine(index)
	s.logger.Error("quarantining session slot", "slot_index", index, "uid", uid, "err", err,
		"quarantined_slots", s.slots.quarantinedCount(), "max_sessions", s.cfg.MaxSessions)
	return allQuarantined
}

// checkSlotReuse runs after successful drainage and cleanup, or while retrying
// a slot held solely for outstanding memory charges.
func (s *Server) checkSlotReuse(index int, retry bool) {
	s.slotReuseMu.Lock()
	defer s.slotReuseMu.Unlock()
	pending := s.slots.awaitingMemory(index)
	if retry && !pending {
		return
	}
	group := s.cgroups[index]
	current, err := group.counter("memory.current")
	threshold := s.cfg.SlotReuseMemoryThreshold
	if err == nil && current >= threshold {
		// Reclaim can return EAGAIN after dropping part of the requested
		// amount. The current counter decides whether the slot is usable.
		if err := group.write("memory.reclaim", strconv.FormatUint(current, 10)); err != nil && !errors.Is(err, unix.EAGAIN) {
			s.logger.Warn("reclaim residual session slot memory", "slot_index", index, "err", err)
		}
		current, err = group.counter("memory.current")
	}
	// A drained slot has no live tasks, but zombies still hold PID charges
	// until they are reaped; a slot is reusable only with none left.
	var tasks uint64
	if err == nil && current < threshold {
		tasks, err = group.counter("pids.current")
	}
	if err == nil && current < threshold && tasks == 0 {
		s.slots.setSlotState(index, slotUsable)
		if pending {
			s.logger.Info("session slot available again", "slot_index", index, "memory_current", current,
				"quarantined_slots", s.slots.quarantinedCount(), "max_sessions", s.cfg.MaxSessions)
		}
		return
	}
	if !pending {
		s.slots.setSlotState(index, slotAwaitingMemory)
		s.logger.Warn("quarantining session slot pending memory release", "slot_index", index,
			"memory_current", current, "pids_current", tasks, "threshold", threshold, "err", err,
			"quarantined_slots", s.slots.quarantinedCount(), "max_sessions", s.cfg.MaxSessions)
	}
}

func (s *Server) recheckSlots(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for i := range s.cgroups {
				s.checkSlotReuse(i, true)
			}
		}
	}
}

var slotReadyPipe = os.Pipe

// waitForSlotEntry keeps the PTY input gate closed until the child reports its
// privilege drop and cgroupfs independently confirms its PID.
func (g cgroup) waitForSlotEntry(ctx context.Context, ready *os.File, pid int) error {
	defer func() { _ = ready.Close() }()
	ctx, cancel := context.WithTimeout(ctx, slotEntryTimeout)
	defer cancel()

	verified := make(chan error, 1)
	go func() {
		verified <- g.verifyChildSlotEntry(ready, pid)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-verified:
		return errors.Join(err, ctx.Err())
	}
}

func (g cgroup) verifyChildSlotEntry(ready *os.File, pid int) error {
	var b [1]byte
	if _, err := io.ReadFull(ready, b[:]); err != nil {
		return fmt.Errorf("read slot entry acknowledgement: %w", err)
	}
	raw, err := g.read("cgroup.procs")
	if err != nil {
		return fmt.Errorf("read slot cgroup.procs: %w", err)
	}
	if !slices.Contains(strings.Fields(string(raw)), strconv.Itoa(pid)) {
		return fmt.Errorf("PID %d is absent from slot %s", pid, g.path)
	}
	return nil
}
