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
	verify func(string) error
	read   func(string) ([]byte, error)
	write  func(string, string) error
	mkdir  func(string, os.FileMode) error
}

func defaultCgroupFS() cgroupFS {
	return cgroupFS{
		verify: verifyCgroupFS,
		read:   os.ReadFile,
		write:  writeCgroupFile,
		mkdir:  os.Mkdir,
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

// Discover the scope using namespace PIDs. A supervisor restarted via exec
// may already be in the supervisor leaf while the scope itself is empty.
func findCgroupScope(root cgroup, pid string, depth int) (cgroup, error) {
	entries, err := os.ReadDir(root.path)
	if err != nil {
		return cgroup{}, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := root.child(entry.Name())
		if strings.HasSuffix(entry.Name(), ".scope") {
			for _, candidate := range []cgroup{dir, dir.child("supervisor")} {
				raw, err := candidate.read("cgroup.procs")
				if err == nil && slices.Contains(strings.Fields(string(raw)), pid) {
					return dir, nil
				}
			}
		}
		if depth > 1 {
			scope, err := findCgroupScope(dir, pid, depth-1)
			if err == nil {
				return scope, nil
			}
		}
	}
	return cgroup{}, fmt.Errorf("no cgroup scope under %s contains PID %s", root.path, pid)
}

func (g cgroup) delegateSlots() error {
	supervisor := g.child("supervisor")
	if _, err := supervisor.ensureDir(); err != nil {
		return err
	}
	raw, err := g.read("cgroup.procs")
	if err != nil {
		return err
	}
	for _, pid := range strings.Fields(string(raw)) {
		if err := supervisor.write("cgroup.procs", pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return g.write("cgroup.subtree_control", "+memory +pids")
}

func (g cgroup) configureSlot(memoryMax uint64) error {
	for _, setting := range []struct{ file, value string }{
		{"memory.max", strconv.FormatUint(memoryMax, 10)},
		{"memory.swap.max", "0"},
		{"memory.oom.group", "1"},
		{"pids.max", strconv.Itoa(RlimitNProc)},
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

func slotCgroupName(index int) string {
	return "slot-" + strconv.Itoa(index)
}

func (s *Server) prepareCgroups(ctx context.Context) error {
	root := cgroup{path: s.cfg.CgroupRoot, fs: s.ops.cgroups}
	if root.path == "" {
		return errors.New("sshdemo: --cgroup-root is required in uid isolation mode")
	}
	if err := root.fs.verify(root.path); err != nil {
		return fmt.Errorf("sshdemo: unusable --cgroup-root %s: %w", root.path, err)
	}
	scope, err := findCgroupScope(root, strconv.Itoa(os.Getpid()), 2)
	if err != nil {
		return fmt.Errorf("sshdemo: find supervisor cgroup: %w", err)
	}
	budget, err := checkMemoryCapacity(s.cfg, scope)
	if err != nil {
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
		if entry.IsDir() && entry.Name() != "supervisor" {
			index, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "slot-"))
			if err == nil && index >= 0 && index < s.cfg.MaxSessions && entry.Name() == slotCgroupName(index) {
				continue
			}
			if err := scope.child(entry.Name()).drain(ctx, cgroupDrainTimeout); err != nil {
				return fmt.Errorf("sshdemo: drain unassigned cgroup %s: %w", entry.Name(), err)
			}
		}
	}

	s.cgroups = make([]cgroup, s.cfg.MaxSessions)
	for index := range s.cgroups {
		group := scope.child(slotCgroupName(index))
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
		if err := group.configureSlot(budget); err != nil {
			return fmt.Errorf("sshdemo: configure slot cgroup: %w", err)
		}
		if _, err := group.event("memory.events", "oom_group_kill"); err != nil {
			return fmt.Errorf("sshdemo: read slot OOM counter: %w", err)
		}
	}
	return nil
}

func (s *Server) quarantineSlot(index int, err error) bool {
	uid := s.cfg.UIDBase + index
	if s.cfg.Isolation == IsolationProcess {
		uid = os.Getuid()
	}
	s.logger.Error("quarantining session slot", "slot_index", index, "uid", uid, "err", err)
	return s.slots.quarantine(index)
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
