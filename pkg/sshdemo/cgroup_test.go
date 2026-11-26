//go:build linux

package sshdemo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

var fakeSlotFiles = map[string]string{
	"cgroup.kill":      "0",
	"cgroup.events":    "frozen 0\npopulated 0\n",
	"cgroup.procs":     "",
	"memory.max":       "max",
	"memory.current":   "0",
	"memory.reclaim":   "",
	"memory.events":    "oom 0\noom_kill 0\noom_group_kill 0\n",
	"memory.swap.max":  "max",
	"memory.oom.group": "0",
	"pids.max":         "max",
}

func writeFixtureFile(t *testing.T, dir, file, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func addFakeSlot(t *testing.T, root, slot string) string {
	t.Helper()
	dir := filepath.Join(root, slot)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, data := range fakeSlotFiles {
		writeFixtureFile(t, dir, file, data)
	}
	return dir
}

func fakeCgroupMount(t *testing.T) (string, cgroupFS) {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, root, "cgroup.controllers", "memory pids")
	scope := root
	writeFixtureFile(t, scope, "cgroup.procs", fmt.Sprintf("%d\n1\n", os.Getpid()))
	writeFixtureFile(t, scope, "cgroup.subtree_control", "")
	writeFixtureFile(t, scope, "memory.max", strconv.FormatUint(8<<30, 10))
	writeFixtureFile(t, scope, "pids.max", "max\n")
	fs := defaultCgroupFS()
	fs.verify = func(string) error { return nil }
	fs.mountID = func(string) (uint64, error) { return 42, nil }
	fs.read = func(path string) ([]byte, error) {
		if path == "/proc/self/mountinfo" {
			return []byte("42 1 0:2 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw,nsdelegate\n"), nil
		}
		return os.ReadFile(path)
	}
	fs.write = func(path, value string) error {
		if path == filepath.Join(root, "memory.max") {
			return unix.EPERM
		}
		return writeCgroupFile(path, value)
	}
	fs.mkdir = func(path string, perm os.FileMode) error {
		if err := os.Mkdir(path, perm); err != nil {
			return err
		}
		if filepath.Base(path) == "supervisor" {
			return os.WriteFile(filepath.Join(path, "cgroup.procs"), nil, 0o644)
		}
		for file, data := range fakeSlotFiles {
			if err := os.WriteFile(filepath.Join(path, file), []byte(data), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	return root, fs
}

func ampleSessionFilesystem(string) (uint64, uint64, error) {
	return math.MaxUint64, math.MaxUint64, nil
}

func noSharedTemp(string) (os.FileInfo, error) { return nil, os.ErrNotExist }

func fakeCgroupServer(t *testing.T) (Config, SysOps) {
	t.Helper()
	cfg := failureTestConfig(t)
	cfg.Isolation = IsolationUID
	cfg.MaxSessions = 2
	if err := os.Chmod(cfg.SessionsDir, 0o711); err != nil {
		t.Fatal(err)
	}
	ops := SysOps{
		FilesystemCapacity: ampleSessionFilesystem,
		StatSharedTemp:     noSharedTemp,
		FileUID:            func(os.FileInfo) (int, bool) { return 0, true },
		FileGID:            fakeSlotGID(cfg.UIDBase),
		Chown:              func(string, int, int) error { return nil },
		Fchown:             func(*os.File, int, int) error { return nil },
	}
	cfg.CgroupRoot, ops.cgroups = fakeCgroupMount(t)
	return cfg, ops
}

func TestCgroupFilesystemType(t *testing.T) {
	original := statCgroupFS
	t.Cleanup(func() { statCgroupFS = original })
	statCgroupFS = func(_ string, info *unix.Statfs_t) error {
		info.Type = unix.CGROUP2_SUPER_MAGIC
		return nil
	}
	if err := verifyCgroupFS("mount"); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupCapacityFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, limit, want string
		remove, overflow  bool
	}{
		{"exact", strconv.FormatUint(supervisorReserve+DefaultCleanupMemLimit+2*(DefaultSessionMemQuota+DefaultSessionDiskQuota), 10), "", false, false},
		{"one byte short", strconv.FormatUint(supervisorReserve+DefaultCleanupMemLimit+2*(DefaultSessionMemQuota+DefaultSessionDiskQuota)-1, 10), "exceed the cgroup memory limit", false, false},
		{"reserve larger", "1", "exceed the cgroup memory limit", false, false},
		{"missing", "", "require finite scope memory.max", true, false},
		{"unlimited", "max\n", "require finite scope memory.max", false, false},
		{"malformed", "garbage", "require finite scope memory.max", false, false},
		{"overflow", strconv.FormatUint(math.MaxUint64, 10), "exceed the cgroup memory limit", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ops := fakeCgroupServer(t)
			scope := cfg.CgroupRoot
			writeFixtureFile(t, scope, "memory.max", tc.limit)
			if tc.remove {
				if err := os.Remove(filepath.Join(scope, "memory.max")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.overflow {
				cfg.SessionMemQuota = math.MaxUint64
			}
			_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			if tc.want != "" {
				requireErrorContains(t, err, tc.want)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCgroupPIDCapacityFailsClosed(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	group := cgroup{path: cfg.CgroupRoot, fs: ops.cgroups}
	exact := cfg.MaxSessions*RlimitNProc + cfg.CleanupPIDsLimit + supervisorPIDReserve
	for _, tc := range []struct{ value, want string }{
		{"max\n", ""},
		{strconv.Itoa(exact), ""},
		{strconv.Itoa(exact - 1), "scope pids.max"},
		{"256", "scope pids.max"},
		{"bad", "invalid scope pids.max"},
	} {
		writeFixtureFile(t, group.path, "pids.max", tc.value)
		err := checkPIDCapacity(cfg, group)
		if tc.want == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			requireErrorContains(t, err, tc.want)
		}
	}
	if err := os.Remove(filepath.Join(group.path, "pids.max")); err != nil {
		t.Fatal(err)
	}
	_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
	requireErrorContains(t, err, "read scope pids.max")
}

func TestUIDStartupChecksSharedTmpfsCapacity(t *testing.T) {
	const total = uint64(2 * (DefaultSessionMemQuota + DefaultSessionDiskQuota))
	const roundedSize = total + (total+9)/10
	const requiredInodes = total/512 + 2*5 + 1
	for _, tc := range []struct {
		name         string
		size, inodes uint64
		want         string
	}{
		{"exact fit", roundedSize, requiredInodes, ""},
		{"below combined caps", total - 1, requiredInodes, "filesystem capacity"},
		{"missing ten percent headroom", roundedSize - 1, requiredInodes, "headroom"},
		{"one inode short", roundedSize, requiredInodes - 1, "inode capacity"},
		{"previous sizing", roundedSize, total / 1024, "inode capacity"},
		{"zero reported inodes", roundedSize, 0, "inode capacity"},
		{"changed concurrency", roundedSize, requiredInodes, "filesystem capacity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ops := fakeCgroupServer(t)
			if tc.name == "changed concurrency" {
				cfg.MaxSessions++
			}
			ops.FilesystemCapacity = func(path string) (uint64, uint64, error) {
				if path != cfg.SessionsDir {
					t.Fatalf("statfs checked %s", path)
				}
				return tc.size, tc.inodes, nil
			}
			_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			if tc.want != "" {
				requireErrorContains(t, err, tc.want)
				raw, err := os.ReadFile(filepath.Join(cfg.CgroupRoot, "cgroup.subtree_control"))
				if err != nil || len(raw) != 0 {
					t.Fatalf("failed sizing changed delegation: %q, %v", raw, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCgroupStartupUsesScopeAndDrainsSlots(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	scope := cfg.CgroupRoot
	existing := addFakeSlot(t, cfg.CgroupRoot, "slot-0")
	retired := addFakeSlot(t, cfg.CgroupRoot, "slot-9")
	var moves []string
	write := ops.cgroups.write
	ops.cgroups.write = func(path, value string) error {
		if path == filepath.Join(scope, "supervisor", "cgroup.procs") {
			moves = append(moves, value)
			if value == "1" {
				return syscall.ESRCH
			}
		}
		return write(path, value)
	}
	s, err := NewServer(t.Context(), cfg, WithSysOps(ops))
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 2 || moves[0] != strconv.Itoa(os.Getpid()) || moves[1] != "1" {
		t.Fatalf("unexpected supervisor moves: %v", moves)
	}
	assertFile := func(path, want string) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v, want %q", path, got, err, want)
		}
	}
	for _, path := range []string{existing, s.cgroups[1].path} {
		assertFile(filepath.Join(path, "cgroup.kill"), "1")
		assertFile(filepath.Join(path, "memory.max"), strconv.FormatUint(DefaultSessionMemQuota+DefaultSessionDiskQuota, 10))
		assertFile(filepath.Join(path, "memory.swap.max"), "0")
		assertFile(filepath.Join(path, "memory.oom.group"), "1")
		assertFile(filepath.Join(path, "pids.max"), strconv.Itoa(RlimitNProc))
	}
	assertFile(filepath.Join(retired, "cgroup.kill"), "1")
	assertFile(filepath.Join(scope, "cgroup.subtree_control"), "+memory +pids")
	for file, want := range map[string]string{
		"cgroup.kill": "1", "memory.max": strconv.Itoa(DefaultCleanupMemLimit),
		"memory.swap.max": "0", "memory.oom.group": "1", "pids.max": strconv.Itoa(DefaultCleanupPIDsLimit),
	} {
		assertFile(filepath.Join(scope, "cleanup", file), want)
	}
	// Restart in the supervisor leaf.
	writeFixtureFile(t, scope, "cgroup.procs", "")
	writeFixtureFile(t, filepath.Join(scope, "supervisor"), "cgroup.procs", strconv.Itoa(os.Getpid()))
	if _, err := NewServer(t.Context(), cfg, WithSysOps(ops)); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupStartupErrors(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		edit       func(*testing.T, *Config, *cgroupFS)
	}{
		{"relative root", "--cgroup-root must be absolute", func(_ *testing.T, c *Config, _ *cgroupFS) { c.CgroupRoot = "scope" }},
		{"invalid cleanup limit", "--cleanup-pids-limit must be positive", func(_ *testing.T, c *Config, _ *cgroupFS) { c.CleanupPIDsLimit = -1 }},
		{"not cgroupfs", "expected a cgroup v2", func(_ *testing.T, _ *Config, f *cgroupFS) { f.verify = verifyCgroupFS }},
		{"missing root", "unusable --cgroup-root", func(_ *testing.T, c *Config, f *cgroupFS) {
			c.CgroupRoot = filepath.Join(c.CgroupRoot, "missing")
			f.verify = verifyCgroupFS
		}},
		{"foreign scope", "is not the supervisor's cgroup scope", func(t *testing.T, c *Config, _ *cgroupFS) {
			writeFixtureFile(t, c.CgroupRoot, "cgroup.procs", "")
		}},
		{"missing delegation files", "delegate session cgroups", func(t *testing.T, c *Config, _ *cgroupFS) {
			if err := os.Remove(filepath.Join(c.CgroupRoot, "cgroup.subtree_control")); err != nil {
				t.Fatal(err)
			}
		}},
		{"supervisor mkdir", "delegate session cgroups", func(_ *testing.T, _ *Config, f *cgroupFS) {
			f.mkdir = func(string, os.FileMode) error { return errTestIO }
		}},
		{"move denied", "delegate session cgroups", func(_ *testing.T, _ *Config, f *cgroupFS) {
			f.write = func(string, string) error { return syscall.EPERM }
		}},
		{"scope procs read", "delegate session cgroups", func(_ *testing.T, c *Config, f *cgroupFS) {
			procs := filepath.Join(c.CgroupRoot, "cgroup.procs")
			read := f.read
			f.read = func(path string) ([]byte, error) {
				if path == procs {
					return nil, errTestIO
				}
				return read(path)
			}
		}},
		{"scope read dir", "read", func(t *testing.T, c *Config, f *cgroupFS) {
			scope := c.CgroupRoot
			write := f.write
			f.write = func(path, value string) error {
				if filepath.Base(path) == "cgroup.subtree_control" {
					return os.Rename(scope, scope+".gone")
				}
				return write(path, value)
			}
		}},
		{"slot mkdir", "create slot cgroup", func(_ *testing.T, _ *Config, f *cgroupFS) {
			mkdir := f.mkdir
			f.mkdir = func(p string, m os.FileMode) error {
				if filepath.Base(p) == "slot-0" {
					return errTestIO
				}
				return mkdir(p, m)
			}
		}},
		{"cleanup mkdir", "create cleanup cgroup", func(_ *testing.T, _ *Config, f *cgroupFS) {
			mkdir := f.mkdir
			f.mkdir = func(path string, mode os.FileMode) error {
				if filepath.Base(path) == "cleanup" {
					return errTestIO
				}
				return mkdir(path, mode)
			}
		}},
		{"cleanup drain", "drain cleanup cgroup", func(t *testing.T, cfg *Config, _ *cgroupFS) {
			cleanup := addFakeSlot(t, cfg.CgroupRoot, "cleanup")
			writeFixtureFile(t, cleanup, "cgroup.events", "bad")
		}},
		{"cleanup limit", "configure cleanup cgroup", func(_ *testing.T, _ *Config, f *cgroupFS) {
			write := f.write
			f.write = func(path, value string) error {
				if strings.HasSuffix(path, "cleanup/pids.max") {
					return errTestIO
				}
				return write(path, value)
			}
		}},
		{"slot limit", "configure slot cgroup", func(_ *testing.T, _ *Config, f *cgroupFS) {
			write := f.write
			f.write = func(p, v string) error {
				if filepath.Base(p) == "memory.swap.max" {
					return errTestIO
				}
				return write(p, v)
			}
		}},
		{"fresh slot drain", "no usable session cgroups", func(_ *testing.T, _ *Config, f *cgroupFS) {
			write := f.write
			f.write = func(p, v string) error {
				if filepath.Base(p) == "cgroup.kill" {
					return errTestIO
				}
				return write(p, v)
			}
		}},
		{"only slot quarantined", "no usable session cgroups", func(t *testing.T, c *Config, f *cgroupFS) {
			c.MaxSessions = 1
			slot := addFakeSlot(t, c.CgroupRoot, "slot-0")
			writeFixtureFile(t, slot, "cgroup.events", "bad")
		}},
		{"unassigned slot drain", "drain unassigned cgroup", func(t *testing.T, c *Config, _ *cgroupFS) {
			slot := addFakeSlot(t, c.CgroupRoot, "slot-3")
			writeFixtureFile(t, slot, "cgroup.events", "bad")
		}},
		{"missing OOM event", "read slot OOM counter", func(_ *testing.T, _ *Config, fs *cgroupFS) {
			read := fs.read
			fs.read = func(path string) ([]byte, error) {
				if filepath.Base(path) == "memory.events" {
					return []byte("oom 0"), nil
				}
				return read(path)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ops := fakeCgroupServer(t)
			tc.edit(t, &cfg, &ops.cgroups)
			_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			requireErrorContains(t, err, tc.want)
		})
	}
}

func TestSlotReuseWaitsForResidualMemory(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	var logs syncBuffer
	cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	existing := addFakeSlot(t, cfg.CgroupRoot, "slot-0")
	file := filepath.Join(existing, "memory.current")
	threshold := strconv.FormatUint(cfg.SlotReuseMemoryThreshold, 10)
	writeFixtureFile(t, existing, "memory.current", threshold)
	s, err := NewServer(t.Context(), cfg, WithSysOps(ops))
	if err != nil {
		t.Fatal(err)
	}
	slot, err := s.slots.Acquire("first")
	if err != nil || slot.Index != 1 || s.slots.quarantinedCount() != 1 || !s.slots.awaitingMemory(0) {
		t.Fatalf("slot at threshold was usable: acquired %+v, %v", slot, err)
	}
	s.quarantineSlot(1, errTestIO)
	s.slots.Release(slot)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	s.checkSlotReuse(0, true)
	if _, err := s.slots.Acquire("first"); !errors.Is(err, ErrGlobalLimit) {
		t.Fatalf("returned a slot with unreadable charges or failed cleanup: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.recheckSlots(ctx, time.Millisecond)
	}()
	defer func() { cancel(); <-done }()
	writeFixtureFile(t, existing, "new.current", strconv.FormatUint(cfg.SlotReuseMemoryThreshold-1, 10))
	if err := os.Rename(filepath.Join(existing, "new.current"), file); err != nil {
		t.Fatal(err)
	}
	waitLogContains(t, &logs, "session slot available again", time.Second)
	slot, err = s.slots.Acquire("first")
	if err != nil || slot.Index != 0 || s.slots.quarantinedCount() != 1 {
		t.Fatalf("reclaim returned slot %+v, %v", slot, err)
	}
}

func TestCgroupDrainAndMemoryCounters(t *testing.T) {
	g := cgroup{path: t.TempDir(), fs: defaultCgroupFS()}
	writeFixtureFile(t, g.path, "cgroup.kill", "")
	writeFixtureFile(t, g.path, "cgroup.events", "populated 1")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := g.drain(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("drain returned %v", err)
	}
	read := g.fs.read
	polls := 0
	g.fs.read = func(path string) ([]byte, error) {
		if filepath.Base(path) == "cgroup.events" {
			polls++
			if polls == 2 {
				return []byte("populated 0"), nil
			}
		}
		return read(path)
	}
	if err := g.drain(t.Context(), time.Second); err != nil || polls != 2 {
		t.Fatalf("polls = %d, drain = %v", polls, err)
	}
	if got := g.memoryNearLimit(); got {
		t.Fatal("missing counter reported pressure")
	}
	writeFixtureFile(t, g.path, "memory.current", "89")
	if got := g.memoryNearLimit(); got {
		t.Fatal("missing maximum reported pressure")
	}
	writeFixtureFile(t, g.path, "memory.max", "100")
	if got := g.memoryNearLimit(); got {
		t.Fatal("89% reported pressure")
	}
	writeFixtureFile(t, g.path, "memory.current", "90\n")
	if got := g.memoryNearLimit(); !got {
		t.Fatal("90% did not report pressure")
	}
	if g.oomGroupKilledSince(0) {
		t.Fatal("missing counter reported OOM")
	}
	writeFixtureFile(t, g.path, "memory.events", "oom_group_kill 2")
	if g.oomGroupKilledSince(2) || !g.oomGroupKilledSince(1) {
		t.Fatal("OOM reported without counter delta")
	}
}

func TestSessionSlotEntryFailureWithholdsPTYInput(t *testing.T) {
	for _, tc := range []struct {
		branch, log string
	}{
		{"slot-entry-timeout", "context deadline exceeded"},
		{"slot-entry-error", "read slot entry acknowledgement: EOF"},
		{"slot-entry-wrong-pid", "is absent from slot"},
	} {
		t.Run(tc.branch, func(t *testing.T) {
			cfg, ops := fakeCgroupServer(t)
			h := startTestServer(t, func(c *Config) {
				c.Isolation, c.CgroupRoot, c.SessionsDir = cfg.Isolation, cfg.CgroupRoot, cfg.SessionsDir
				c.SaveBranch = tc.branch
			}, WithSysOps(ops))

			sess, err := dialSSH(t, h.addr, "visitor").NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sess.Close() }()
			if err := sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{gossh.ECHO: 1}); err != nil {
				t.Fatal(err)
			}
			out, err := sess.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			in, err := sess.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.Shell(); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(out)
			if tc.branch == "slot-entry-timeout" {
				line, err := reader.ReadString('\n')
				if err != nil || !strings.HasPrefix(line, "PID:") {
					t.Fatalf("child PID report = %q, %v", line, err)
				}
				pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PID:")))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
						t.Errorf("timed-out child PID %d was not reaped: %v", pid, err)
					}
				})
				if _, err := io.WriteString(in, "gated visitor input\n"); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := io.ReadAll(reader)
			if err != nil || strings.Contains(string(raw), "gated visitor input") {
				t.Fatalf("unexpected PTY output = %q, %v", raw, err)
			}
			var exitErr *gossh.ExitError
			if err := sess.Wait(); !errors.As(err, &exitErr) || exitErr.ExitStatus() != 1 {
				t.Fatalf("session wait = %v", err)
			}
			waitLogContains(t, h.logs, tc.log, time.Second)
			if !h.srv.slots.isQuarantined(0) {
				t.Fatal("slot with failed entry was returned to the pool")
			}
			waitNoSessionDirs(t, filepath.Join(cfg.SessionsDir, "slot-0"), time.Second)
		})
	}
}

func TestSlotEntryAbandonedOnChannelOrConnectionClose(t *testing.T) {
	open := make(chan struct{})
	closed := make(chan struct{})
	close(closed)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !slotEntryAbandoned(ctx, open) || !slotEntryAbandoned(t.Context(), closed) ||
		slotEntryAbandoned(t.Context(), open) {
		t.Fatal("slot entry must distinguish a disconnect from an entry failure")
	}
}

func TestSessionDisconnectDuringSlotEntryReusesSlot(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	h := startTestServer(t, func(c *Config) {
		c.Isolation, c.CgroupRoot, c.SessionsDir = cfg.Isolation, cfg.CgroupRoot, cfg.SessionsDir
		c.MaxSessions = 1
		c.SaveBranch = "slot-entry-timeout"
	}, WithSysOps(ops))

	client := dialSSH(t, h.addr, "visitor")
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || !strings.HasPrefix(line, "PID:") {
		t.Fatalf("child startup: %q, %v", line, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	waitLogContains(t, h.logs, `"end_reason":"client-drop"`, time.Second)
	if _, err := h.srv.slots.Acquire("next visitor"); err != nil {
		t.Fatalf("slot remained unavailable after a disconnect: %v", err)
	}
}

func TestSessionSlotEntryPipeAndSpawnFailures(t *testing.T) {
	for _, tc := range []struct {
		name, log string
		setup     func(*testing.T, *Config)
	}{
		{"pipe", "create slot entry acknowledgement pipe", func(t *testing.T, _ *Config) {
			original := slotReadyPipe
			t.Cleanup(func() { slotReadyPipe = original })
			slotReadyPipe = func() (*os.File, *os.File, error) { return nil, nil, errTestIO }
		}},
		{"spawn", "no such file or directory", func(t *testing.T, cfg *Config) {
			cfg.Executable = filepath.Join(t.TempDir(), "missing-executable")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ops := fakeCgroupServer(t)
			h := startTestServer(t, func(c *Config) {
				c.Isolation, c.CgroupRoot, c.SessionsDir = cfg.Isolation, cfg.CgroupRoot, cfg.SessionsDir
				tc.setup(t, c)
			}, WithSysOps(ops))
			sess, err := dialSSH(t, h.addr, "visitor").NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sess.Close() }()
			if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
				t.Fatal(err)
			}
			if err := sess.Shell(); err != nil {
				t.Fatal(err)
			}
			var exitErr *gossh.ExitError
			if err := sess.Wait(); !errors.As(err, &exitErr) || exitErr.ExitStatus() != 1 {
				t.Fatalf("session wait = %v", err)
			}
			waitLogContains(t, h.logs, tc.log, time.Second)
		})
	}
}

func TestSessionCgroupMemoryNotices(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	var stderr syncBuffer
	h := startTestServer(t, func(c *Config) {
		c.Isolation, c.CgroupRoot, c.SessionsDir = cfg.Isolation, cfg.CgroupRoot, cfg.SessionsDir
		c.SaveBranch = "quota-stream"
		c.WatchdogInterval = 10 * time.Millisecond
	}, WithSysOps(ops))
	client := dialSSH(t, h.addr, "visitor")
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	sess.Stderr = &stderr
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	ready := make(chan struct{})
	go func() {
		_, _ = io.CopyN(&stdout, out, 64<<10)
		close(ready)
		_, _ = io.Copy(io.Discard, out)
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("no PTY traffic")
	}
	slot := h.srv.cgroups[0]
	current := strconv.FormatUint(DefaultSessionMemQuota+DefaultSessionDiskQuota, 10)
	// Write the fake counter atomically while the supervisor reads it.
	writeFixtureFile(t, slot.path, "new.current", current)
	if err := os.Rename(filepath.Join(slot.path, "new.current"), filepath.Join(slot.path, "memory.current")); err != nil {
		t.Fatal(err)
	}
	waitLogContains(t, &stderr, memoryPressureNotice, time.Second)
	if strings.Contains(stdout.String(), "memory/storage allowance") {
		t.Fatal("notice leaked to PTY stdout")
	}
	writeFixtureFile(t, slot.path, "memory.events", "oom_group_kill 1\n")
	// The fixture has no real OOM killer; end the helper as the kernel would.
	if err := syscall.Kill(helperPIDFromWorkspace(t, filepath.Join(cfg.SessionsDir, "slot-0")), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	var exitErr *gossh.ExitError
	if err := sess.Wait(); !errors.As(err, &exitErr) || exitErr.ExitStatus() != 1 {
		t.Fatalf("session wait = %v", err)
	}
	if got := stderr.String(); got != memoryPressureNotice+oomGroupNotice {
		t.Fatalf("stderr = %q", got)
	}
	waitLogContains(t, h.logs, `"end_reason":"memory-quota"`, time.Second)
	if !h.srv.slots.awaitingMemory(0) {
		t.Fatal("slot with retained memory was returned to the pool")
	}
	waitForNoConnectionReservations(t, h.srv)
	waitNoSessionDirs(t, filepath.Join(cfg.SessionsDir, "slot-0"), time.Second)
}

func helperPIDFromWorkspace(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("workspace = %v, %v", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, entries[0].Name(), "seen.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report helperReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report.PID
}

func TestSessionCgroupDrainFailureQuarantines(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	h := startTestServer(t, func(c *Config) {
		c.Isolation, c.CgroupRoot, c.SessionsDir = cfg.Isolation, cfg.CgroupRoot, cfg.SessionsDir
		c.MaxSessions = 1
		c.SaveBranch = "wait-forever"
	}, WithSysOps(ops))

	sess, err := dialSSH(t, h.addr, "visitor").NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "READY:") {
		t.Fatalf("child startup: %q, %v", line, err)
	}
	slot := h.srv.cgroups[0]
	if err := os.Remove(filepath.Join(slot.path, "cgroup.kill")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(in, "q\n"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Wait(); err != nil {
		t.Fatal(err)
	}
	waitLogContains(t, h.logs, "quarantining session slot", time.Second)
	if _, err := h.srv.slots.Acquire("visitor"); !errors.Is(err, ErrGlobalLimit) {
		t.Fatalf("allocation after an unverified drain: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(cfg.SessionsDir, "slot-0"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("workspace must remain untouched after failed drain: %v, %v", entries, err)
	}
}

func TestSessionCgroupGracefulShutdownWithMissingNoticeCounter(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	h := startTestServer(t, func(c *Config) {
		c.Isolation, c.CgroupRoot, c.SessionsDir = cfg.Isolation, cfg.CgroupRoot, cfg.SessionsDir
		c.SaveBranch = "graceful-shutdown"
	}, WithSysOps(ops))
	if err := os.Remove(filepath.Join(h.srv.cgroups[0].path, "memory.events")); err != nil {
		t.Fatal(err)
	}
	sess, err := dialSSH(t, h.addr, "visitor").NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "READY:") {
		t.Fatalf("child startup: %q, %v", line, err)
	}
	waitLogContains(t, h.logs, "read slot OOM counter failed", time.Second)
	h.stop()
	waitLogContains(t, h.logs, `"end_reason":"shutdown","exit_status":0`, time.Second)
	waitNoSessionDirs(t, filepath.Join(cfg.SessionsDir, "slot-0"), time.Second)
}

func TestSlotReuseReclaimsBeforeDeciding(t *testing.T) {
	const high = "16777216"
	for _, tc := range []struct {
		name, remaining string
		writeErr        error
		retry, warned   bool
		want            slotQuarantine
	}{
		{"reclaimed", "0", nil, false, false, slotUsable},
		{"partial reclaim", "0", unix.EAGAIN, false, false, slotUsable},
		{"retained charges", high, unix.EAGAIN, false, false, slotAwaitingMemory},
		{"periodic reclaim", "0", nil, true, false, slotUsable},
		{"reclaim error with retained charges", high, unix.EIO, true, true, slotAwaitingMemory},
		{"reclaim error with released charges", "0", unix.EIO, true, true, slotUsable},
		{"unreadable after reclaim", "bad", nil, false, false, slotAwaitingMemory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ops := fakeCgroupServer(t)
			var logs syncBuffer
			cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			s, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			if err != nil {
				t.Fatal(err)
			}

			reads, writes := 0, 0
			s.cgroups[0].fs.read = func(path string) ([]byte, error) {
				if filepath.Base(path) != "memory.current" {
					t.Fatalf("unexpected read from %s", path)
				}
				reads++
				if reads == 1 {
					return []byte(high), nil
				}
				return []byte(tc.remaining), nil
			}
			s.cgroups[0].fs.write = func(path, value string) error {
				if filepath.Base(path) != "memory.reclaim" || reads != 1 || value != high {
					t.Fatalf("unexpected reclaim: write %s %s after %d reads", path, value, reads)
				}
				writes++
				return tc.writeErr
			}
			if tc.retry {
				s.slots.setSlotState(0, slotAwaitingMemory)
			}
			s.checkSlotReuse(0, tc.retry)
			if reads != 2 || writes != 1 || s.slots.quarantined[0] != tc.want {
				t.Fatalf("reads = %d, writes = %d, slot state = %v, want %v", reads, writes, s.slots.quarantined[0], tc.want)
			}
			if warned := strings.Contains(logs.String(), "reclaim residual session slot memory"); warned != tc.warned {
				t.Fatalf("reclaim warning = %v, want %v: %s", warned, tc.warned, logs.String())
			}
		})
	}
}
