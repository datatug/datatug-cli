//go:build linux

package sshdemo

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSlotManager_LimitsAndIsolation(t *testing.T) {
	m := NewSlotManager(3, 2, 20000, IsolationUID)
	if !m.InSlotRange(20000) || !m.InSlotRange(20002) || m.InSlotRange(19999) || m.InSlotRange(20003) {
		t.Fatal("InSlotRange boundary mismatch")
	}

	s1, err := m.Acquire("1.2.3.4")
	if err != nil || s1.Index != 0 || s1.UID != 20000 || s1.GID != 20000 {
		t.Fatalf("s1 = %+v, err = %v", s1, err)
	}
	s2, err := m.Acquire("1.2.3.4")
	if err != nil || s2.Index != 1 || s2.UID != 20001 {
		t.Fatalf("s2 = %+v, err = %v", s2, err)
	}
	// Third session from same IP hits per-IP cap.
	if _, err := m.Acquire("1.2.3.4"); !errors.Is(err, ErrPerIPLimit) {
		t.Fatalf("expected ErrPerIPLimit, got %v", err)
	}
	// Another IP gets slot 2.
	s3, err := m.Acquire("5.6.7.8")
	if err != nil || s3.Index != 2 || s3.UID != 20002 {
		t.Fatalf("s3 = %+v, err = %v", s3, err)
	}
	if m.ActiveCount() != 3 {
		t.Fatalf("ActiveCount = %d, want 3", m.ActiveCount())
	}
	// Fourth session from a third IP hits global cap.
	if _, err := m.Acquire("9.9.9.9"); !errors.Is(err, ErrGlobalLimit) {
		t.Fatalf("expected ErrGlobalLimit, got %v", err)
	}

	// Release frees both slot and per-IP count; double release is safe.
	m.Release(s1)
	m.Release(s1)
	m.Release(Slot{Index: -1})
	if m.ActiveCount() != 2 {
		t.Fatalf("ActiveCount = %d, want 2", m.ActiveCount())
	}
	s4, err := m.Acquire("1.2.3.4")
	if err != nil || s4.Index != 0 {
		t.Fatalf("re-acquire after release: %+v, %v", s4, err)
	}
	m.Release(s2)
	m.Release(s3)
	m.Release(s4)
	if m.ActiveCount() != 0 {
		t.Fatalf("ActiveCount = %d, want 0", m.ActiveCount())
	}

	// Process isolation uses supervisor's own uid/gid.
	pm := NewSlotManager(2, 2, 20000, IsolationProcess)
	ps, err := pm.Acquire("127.0.0.1")
	if err != nil || ps.UID != os.Getuid() || ps.GID != os.Getgid() {
		t.Fatalf("process slot = %+v, err = %v", ps, err)
	}
}

func TestCheckSessionsDir_UIDModeSeams(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o711); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	regularFile := filepath.Join(dir, "file")
	if err := os.WriteFile(regularFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Root-owned 0711 succeeds.
	ops := DefaultSysOps()
	ops.FileUID = func(os.FileInfo) (int, bool) { return 0, true }
	if err := checkSessionsDir(dir, IsolationUID, ops); err != nil {
		t.Fatalf("expected valid uid sessions dir, got %v", err)
	}

	// Non-root owner refused.
	opsNonRoot := ops
	opsNonRoot.FileUID = func(os.FileInfo) (int, bool) { return 1000, true }
	if err := checkSessionsDir(dir, IsolationUID, opsNonRoot); err == nil || !strings.Contains(err.Error(), "owned by root") {
		t.Fatalf("expected non-root error, got %v", err)
	}

	// Non-directory refused.
	if err := checkSessionsDir(regularFile, IsolationUID, ops); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected non-dir error, got %v", err)
	}

	// Missing directory refused in uid mode.
	if err := checkSessionsDir(filepath.Join(dir, "missing"), IsolationUID, ops); err == nil {
		t.Fatal("expected error for missing sessions dir in uid mode")
	}

	// Wrong mode (0755) refused.
	badModeDir := t.TempDir()
	_ = os.Chmod(badModeDir, 0o755)
	if err := checkSessionsDir(badModeDir, IsolationUID, ops); err == nil || !strings.Contains(err.Error(), "0711") {
		t.Fatalf("expected mode 0711 error, got %v", err)
	}

	// Process mode creates directory if absent.
	procSessions := filepath.Join(t.TempDir(), "sessions")
	if err := checkSessionsDir(procSessions, IsolationProcess, DefaultSysOps()); err != nil {
		t.Fatalf("process mode checkSessionsDir: %v", err)
	}
	if _, err := os.Stat(procSessions); err != nil {
		t.Fatalf("sessions dir not created: %v", err)
	}
}

func TestServerRejectsSymlinkSessionsDir(t *testing.T) {
	target := t.TempDir()
	if err := os.Chmod(target, 0o711); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "sessions")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, isolation := range []string{IsolationUID, IsolationProcess} {
		t.Run(isolation, func(t *testing.T) {
			cfg := failureTestConfig(t)
			cfg.Isolation = isolation
			cfg.SessionsDir = link + "/"
			ops := DefaultSysOps()
			ops.FileUID = func(os.FileInfo) (int, bool) { return 0, true }
			_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			requireErrorContains(t, err, "not a directory")
		})
	}
}

func TestUIDModeSeams_WorkspaceOwnershipAndStaleCleanup(t *testing.T) {
	if _, ok := defaultFileUID(nil); ok {
		t.Fatal("defaultFileUID(nil) should return false")
	}
	if fi, err := os.Stat(t.TempDir()); err == nil {
		if uid, ok := defaultFileUID(fi); !ok || uid != os.Getuid() {
			t.Fatalf("defaultFileUID = (%d, %v), want (%d, true)", uid, ok, os.Getuid())
		}
	}

	// 2. createSessionWorkspace in uid mode calls chown on dir, home, repo and tmp.
	sessDir := t.TempDir()
	var chowned []string
	ops := DefaultSysOps()
	ops.Chown = func(p string, uid, gid int) error {
		if uid != 20003 || gid != 20003 {
			t.Fatalf("unexpected chown uid:gid %d:%d", uid, gid)
		}
		chowned = append(chowned, filepath.Base(p))
		return nil
	}
	slot := Slot{Index: 3, UID: 20003, GID: 20003}
	dir, homeDir, repoDir, err := createSessionWorkspace(sessDir, "abcd1234", slot, IsolationUID, ops)
	if err != nil {
		t.Fatalf("createSessionWorkspace: %v", err)
	}
	// Innermost first: without CAP_DAC_OVERRIDE nothing can be done inside dir once it is the slot uid's.
	if !slices.Equal(chowned, []string{"home", "repo", "tmp", "abcd1234"}) {
		t.Fatalf("chowned = %v, want [home repo tmp abcd1234]", chowned)
	}
	for _, p := range []string{dir, homeDir, repoDir, filepath.Join(dir, "tmp")} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("workspace %s perm = %v / %v, want 0700", p, fi, err)
		}
	}

	// Chown failure cleans up partial workspace.
	failOps := ops
	failOps.Chown = func(string, int, int) error { return errors.New("eperm") }
	if _, _, _, err := createSessionWorkspace(sessDir, "failws", slot, IsolationUID, failOps); err == nil {
		t.Fatal("expected chown error")
	}
	if _, err := os.Stat(filepath.Join(sessDir, "failws")); !os.IsNotExist(err) {
		t.Fatal("failed workspace should have been removed")
	}

	// Stale cleanup includes retired slots and refuses owners below UIDBase.
	staleRoot := t.TempDir()
	inRangeDir := filepath.Join(staleRoot, "stale-in-range")
	outRangeDir := filepath.Join(staleRoot, "stale-out-of-range")
	_ = os.MkdirAll(filepath.Join(inRangeDir, "home"), 0o700)
	_ = os.WriteFile(filepath.Join(inRangeDir, "home", "tmp.txt"), []byte("x"), 0o600)
	_ = os.MkdirAll(outRangeDir, 0o700)
	_ = os.WriteFile(filepath.Join(outRangeDir, "keep.txt"), []byte("keep"), 0o600)

	var logBuf bytes.Buffer
	staleOps := DefaultSysOps()
	staleOps.FileUID = func(fi os.FileInfo) (int, bool) {
		if fi.Name() == "stale-in-range" {
			return 20001, true
		}
		return 20004, true // retired slot above the active range
	}
	srv := &Server{
		cfg: Config{
			SessionsDir:    staleRoot,
			Isolation:      IsolationUID,
			UIDBase:        20000,
			MaxSessions:    4,
			CleanupTimeout: 10 * time.Second,
			ExecPrefixArgs: []string{"-test.run=^TestHelperProcess$", "--"},
		},
		executable:   os.Args[0],
		logger:       slog.New(slog.NewJSONHandler(&logBuf, nil)),
		ops:          staleOps,
		slots:        NewSlotManager(4, 2, 20000, IsolationUID),
		cleanupGroup: cgroup{path: t.TempDir()},
		cleanupQueue: make(chan struct{}, 1),
	}
	if err := srv.cleanStaleSessions(context.Background()); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(srv.cleanupGroup.path, "cleanup-seen.json"))
	if err != nil || !strings.Contains(string(raw), `"--slot-uid","20004","--slot-gid","20004"`) {
		t.Fatalf("cleanup helper args = %q, %v", raw, err)
	}
	if _, err := os.Stat(inRangeDir); !os.IsNotExist(err) {
		t.Fatalf("in-range stale dir should have been removed: %v", err)
	}
	if _, err := os.Stat(outRangeDir); !os.IsNotExist(err) {
		t.Fatalf("retired slot workspace should have been removed: %v", err)
	}
	if err := os.Mkdir(outRangeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv.ops.FileUID = func(os.FileInfo) (int, bool) { return 1000, true }
	requireErrorContains(t, srv.cleanStaleSessions(t.Context()), "UID 1000 below --uid-base 20000")
	srv.ops.FileUID = func(os.FileInfo) (int, bool) { return 20004, true }
	writeFixtureFile(t, outRangeDir, "fail-cleanup", "")
	requireErrorContains(t, srv.cleanStaleSessions(t.Context()), "could not verify cleanup")
}

func TestUIDModeChecksSharedTemporaryDirectories(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	tmp := t.TempDir()
	if err := os.Chmod(tmp, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	ops.StatSharedTemp = func(path string) (os.FileInfo, error) {
		if path == "/tmp" {
			return nil, os.ErrNotExist
		}
		return os.Stat(tmp)
	}
	_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
	requireErrorContains(t, err, "shared temporary path /dev/shm is writable by others")
	if err := os.Chmod(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(t.Context(), cfg, WithSysOps(ops)); err != nil {
		t.Fatal(err)
	}
	ops.StatSharedTemp = func(string) (os.FileInfo, error) { return nil, errTestIO }
	_, err = NewServer(t.Context(), cfg, WithSysOps(ops))
	requireErrorContains(t, err, "stat shared temporary path /tmp")
}

func TestUIDCleanupWaitsForQueue(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	cfg.Executable = "/bin/true"
	cfg.ExecPrefixArgs = nil
	s, err := NewServer(t.Context(), cfg, WithSysOps(ops))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	s.cleanupQueue <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.cleanWorkspace(ctx, workspace, cfg.UIDBase, cfg.UIDBase) }()
	select {
	case err := <-done:
		t.Fatalf("cleanup bypassed a busy queue: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued helper returned %v", err)
	}
	<-s.cleanupQueue
	writeFixtureFile(t, workspace, "keep", "")
	requireErrorContains(t, s.cleanWorkspace(t.Context(), workspace, cfg.UIDBase, cfg.UIDBase), "directory not empty")
	if err := os.Remove(filepath.Join(workspace, "keep")); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanWorkspace(t.Context(), workspace, cfg.UIDBase, cfg.UIDBase); err != nil {
		t.Fatal(err)
	}
}

func TestChildReapGrace(t *testing.T) {
	waited := make(chan error, 1)
	requireErrorContains(t, waitForChildReap(waited, time.Millisecond), "timed out waiting to reap session child")
	waited <- errTestIO // Child exit status is independent from reap verification.
	if err := waitForChildReap(waited, time.Second); err != nil {
		t.Fatal(err)
	}
}
