//go:build linux

package sshdemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func cleanupTestConfig(t *testing.T) (Config, SysOps) {
	t.Helper()
	cfg, ops := fakeCgroupServer(t)
	cfg.Executable = os.Args[0]
	cfg.ExecPrefixArgs = []string{"-test.run=^TestHelperProcess$", "--"}
	return cfg, ops
}

func TestCleanupRetryAfterBudgetOrOOM(t *testing.T) {
	for _, marker := range []string{"cleanup-timeout", "cleanup-oom"} {
		t.Run(marker, func(t *testing.T) {
			cfg, ops := cleanupTestConfig(t)
			cfg.MaxSessions = 1
			cfg.CleanupTimeout = 300 * time.Millisecond
			s, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			if err != nil {
				t.Fatal(err)
			}
			slot, err := s.slots.Acquire("visitor")
			if err != nil {
				t.Fatal(err)
			}
			workspace := makeStaleWorkspace(t, cfg.SessionsDir, "slot-0", "workspace")
			writeFixtureFile(t, workspace, marker, "")
			writeFixtureFile(t, workspace, "remaining", "")
			job := &cleanupJob{slot: slot, dirs: []string{workspace}, noProgress: 2}
			s.runCleanupJob(t.Context(), job)
			s.slots.Release(slot)
			if s.slots.quarantined[0] != slotAwaitingCleanup || s.slots.quarantinedCount() != 1 || job.noProgress != 0 {
				t.Fatalf("state = %v, consecutive runs without progress = %d", s.slots.quarantined[0], job.noProgress)
			}
			if _, err := s.slots.Acquire("visitor"); !errors.Is(err, ErrGlobalLimit) {
				t.Fatalf("dirty slot allocation = %v, want ErrGlobalLimit", err)
			}
			if _, err := os.Stat(filepath.Join(workspace, marker)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("deletion did not survive the interrupted cleanup: %v", err)
			}
			s.retryCleanupJobs(t.Context())
			if _, err := os.Stat(workspace); err != nil {
				t.Fatalf("cleanup retried before backoff: %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() {
				defer close(done)
				job.retryAfter = time.Now().Add(100 * time.Millisecond)
				s.recheckCleanup(ctx, time.Millisecond)
			}()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(time.Second)
			for {
				slot, err = s.slots.Acquire("visitor")
				if err == nil {
					if slot.Index != 0 {
						t.Fatalf("wrong slot returned to service: %+v", slot)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("slot remained unavailable after retry: %v", err)
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("workspace still exists after retry: %v", err)
			}
		})
	}
}

func TestCleanupQuarantinesInvariantOrThreeRunsWithoutProgress(t *testing.T) {
	for _, tc := range []struct {
		marker string
		runs   int
	}{
		{"cleanup-invariant", 1},
		{"fail-cleanup", 3},
	} {
		t.Run(tc.marker, func(t *testing.T) {
			cfg, ops := cleanupTestConfig(t)
			s, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			if err != nil {
				t.Fatal(err)
			}
			workspace := makeStaleWorkspace(t, cfg.SessionsDir, "slot-0", "workspace")
			writeFixtureFile(t, workspace, tc.marker, "")
			job := &cleanupJob{slot: Slot{UID: cfg.UIDBase, GID: cfg.UIDBase}, dirs: []string{workspace}}
			s.queueCleanup(job)
			for run := 1; run <= tc.runs; run++ {
				job.retryAfter = time.Now()
				s.retryCleanupJobs(t.Context())
				if run < tc.runs && s.slots.quarantined[0] != slotAwaitingCleanup {
					t.Fatalf("run %d prematurely quarantined slot", run)
				}
			}
			if s.slots.quarantined[0] != slotQuarantined || len(s.pendingCleanup) != 0 {
				t.Fatalf("slot state = %v, %d queued retries", s.slots.quarantined[0], len(s.pendingCleanup))
			}
		})
	}
}

func TestStartupQueuesActiveAndRetiredWorkspaces(t *testing.T) {
	cfg, ops := cleanupTestConfig(t)
	cfg.CleanupTimeout = 300 * time.Millisecond
	active := makeStaleWorkspace(t, cfg.SessionsDir, "slot-0", "active")
	undrained := makeStaleWorkspace(t, cfg.SessionsDir, "slot-1", "undrained")
	retired := makeStaleWorkspace(t, cfg.SessionsDir, "slot-3", "retired")
	addFakeSlot(t, cfg.CgroupRoot, "slot-1")
	addFakeSlot(t, cfg.CgroupRoot, "slot-3")
	writeFixtureFile(t, retired, "cleanup-timeout", "")
	ops.FileUID = func(fi os.FileInfo) (int, bool) {
		switch fi.Name() {
		case "active":
			return cfg.UIDBase, true
		case "retired":
			return cfg.UIDBase + 3, true
		default:
			return 0, true
		}
	}
	write := ops.cgroups.write
	ops.cgroups.write = func(path, value string) error {
		if strings.HasSuffix(path, "slot-1/cgroup.kill") {
			return errTestIO
		}
		return write(path, value)
	}
	s, err := NewServer(t.Context(), cfg, WithSysOps(ops))
	if err != nil {
		t.Fatal(err)
	}
	if s.slots.quarantined[0] != slotAwaitingCleanup || s.slots.quarantined[1] != slotQuarantined || len(s.pendingCleanup) != 2 {
		t.Fatalf("startup slot states = %v with %d queued jobs", s.slots.quarantined, len(s.pendingCleanup))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.retryCleanupJobs(ctx)
	if len(s.pendingCleanup) != 2 {
		t.Fatal("canceled retry removed queued workspaces")
	}
	s.retryCleanupJobs(t.Context())
	if s.slots.quarantined[0] != slotUsable || len(s.pendingCleanup) != 1 {
		t.Fatalf("after first retry: slot states = %v with %d queued jobs", s.slots.quarantined, len(s.pendingCleanup))
	}
	if _, err := os.Stat(active); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active stale workspace was not removed: %v", err)
	}
	if _, err := os.Stat(undrained); err != nil {
		t.Fatalf("undrained workspace was altered: %v", err)
	}
	s.pendingCleanup[3].retryAfter = time.Now()
	s.retryCleanupJobs(t.Context())
	if _, err := os.Stat(retired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired workspace survived retry: %v", err)
	}

	// Failures of a retired cleanup's invariants remain local to that workspace.
	writeFixtureFile(t, makeStaleWorkspace(t, cfg.SessionsDir, "slot-3", "retired"), "cleanup-invariant", "")
	if err := s.cleanStaleSlot(3); err != nil {
		t.Fatal(err)
	}
	s.retryCleanupJobs(t.Context())
	if len(s.pendingCleanup) != 0 {
		t.Fatalf("retired invariant violation left %d queued jobs", len(s.pendingCleanup))
	}
}

func makeStaleWorkspace(t *testing.T, sessions, parent, name string) string {
	t.Helper()
	dir := filepath.Join(sessions, parent)
	if err := os.Mkdir(dir, 0o710); err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	workspace := filepath.Join(dir, name)
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func TestStartupSlotParentChecks(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"owner", "must be a root-owned directory"},
		{"gid", "must be a root-owned directory"},
		{"mode", "must be a root-owned directory"},
		{"symlink", "must be a root-owned directory"},
		{"legacy layout", "required slot layout"},
		{"stat parent", "stat slot parent"},
		{"stat workspace", "stat stale workspace"},
		{"owner workspace", "owned by slot UID"},
		{"ownership setup", "set slot parent ownership"},
		{"mode setup", "set slot parent permissions"},
		{"create parent", "create slot parent"},
		{"read slot parent", "read stale slot directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, ops := fakeCgroupServer(t)
			parent := filepath.Join(cfg.SessionsDir, "slot-0")
			stat := os.Lstat
			switch tc.name {
			case "legacy layout":
				if err := os.Mkdir(filepath.Join(cfg.SessionsDir, "flat-workspace"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), parent); err != nil {
					t.Fatal(err)
				}
			case "create parent":
				cfg.SessionsDir = filepath.Join(t.TempDir(), "missing")
				s := &Server{cfg: cfg, ops: ops}
				requireErrorContains(t, s.ensureSlotParent(0), tc.want)
				return
			case "ownership setup":
				ops.Chown = func(string, int, int) error { return errTestIO }
			case "mode setup":
				chmod := setWorkspaceMode
				t.Cleanup(func() { setWorkspaceMode = chmod })
				setWorkspaceMode = func(string, os.FileMode) error { return errTestIO }
			case "read slot parent":
				makeStaleWorkspace(t, cfg.SessionsDir, "slot-0", "workspace")
				ops.Stat = func(path string) (os.FileInfo, error) {
					if path != parent {
						return stat(path)
					}
					fi, err := stat(path)
					if err != nil {
						return nil, err
					}
					return fi, os.RemoveAll(path)
				}
			default:
				workspace := makeStaleWorkspace(t, cfg.SessionsDir, "slot-0", "workspace")
				ops.Stat = func(path string) (os.FileInfo, error) {
					if (tc.name == "stat parent" && path == parent) || (tc.name == "stat workspace" && path == workspace) {
						return nil, errTestIO
					}
					return stat(path)
				}
				switch tc.name {
				case "owner":
					ops.FileUID = func(fi os.FileInfo) (int, bool) {
						if fi.Name() == "slot-0" {
							return 1000, true
						}
						return 0, true
					}
				case "gid":
					ops.FileGID = fakeSlotGID(cfg.UIDBase + 1)
				case "mode":
					if err := os.Chmod(parent, 0o711); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			requireErrorContains(t, err, tc.want)
		})
	}
}

func TestProcessStartupStaleCleanup(t *testing.T) {
	cfg := failureTestConfig(t)
	cfg.Executable = os.Args[0]
	cfg.ExecPrefixArgs = []string{"-test.run=^TestHelperProcess$", "--"}
	workspace := filepath.Join(cfg.SessionsDir, "stale")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, workspace, "fail-cleanup", "")
	_, err := NewServer(t.Context(), cfg)
	requireErrorContains(t, err, "could not verify cleanup")
	if err := os.Remove(filepath.Join(workspace, "fail-cleanup")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(t.Context(), cfg, WithSysOps(SysOps{})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale workspace still exists: %v", err)
	}
}

func TestReadStaleSessionsDirectoryFailure(t *testing.T) {
	cfg := failureTestConfig(t)
	cfg.SessionsDir = filepath.Join(cfg.SessionsDir, "missing")
	s := &Server{cfg: cfg}
	requireErrorContains(t, s.cleanStaleSessions(t.Context()), "read sessions-dir for stale cleanup")
}

type nonUnixSlotInfo struct{ os.FileInfo }

func (nonUnixSlotInfo) Sys() any { return nil }

func TestDefaultSlotFileGID(t *testing.T) {
	fi, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := int(fi.Sys().(*syscall.Stat_t).Gid)
	if got, ok := defaultFileGID(fi); !ok || got != want {
		t.Fatalf("actual file gid = %d, %v, want %d", got, ok, want)
	}
	if got, ok := defaultFileGID(nonUnixSlotInfo{fi}); ok {
		t.Fatalf("non-Unix fileinfo returned gid %d", got)
	}
}
