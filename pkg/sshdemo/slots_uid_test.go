//go:build linux

package sshdemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	if err := os.Mkdir(filepath.Join(sessDir, "slot-3"), 0o710); err != nil {
		t.Fatal(err)
	}
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
	if _, err := os.Stat(filepath.Join(sessDir, "slot-3", "failws")); !os.IsNotExist(err) {
		t.Fatal("failed workspace should have been removed")
	}

}

func TestUIDModeChecksSharedTemporaryDirectories(t *testing.T) {
	cfg, ops := fakeCgroupServer(t)
	tmp := t.TempDir()
	if err := os.Chmod(tmp, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	ops.StatSharedTemp = func(path string) (os.FileInfo, error) {
		if path != "/dev/shm" {
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
	go func() { _, err := s.cleanWorkspace(ctx, workspace, cfg.UIDBase, cfg.UIDBase); done <- err }()
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
	_, err = s.cleanWorkspace(t.Context(), workspace, cfg.UIDBase, cfg.UIDBase)
	requireErrorContains(t, err, "directory not empty")
	if err := os.Remove(filepath.Join(workspace, "keep")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cleanWorkspace(t.Context(), workspace, cfg.UIDBase, cfg.UIDBase); err != nil {
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

func fakeSlotGID(base int) func(os.FileInfo) (int, bool) {
	return func(fi os.FileInfo) (int, bool) {
		index, err := strconv.Atoi(strings.TrimPrefix(fi.Name(), "slot-"))
		return base + index, err == nil
	}
}

type fakeModeInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (f fakeModeInfo) Mode() os.FileMode { return f.mode }

func TestLockSharedDevices(t *testing.T) {
	modes := map[string]os.FileMode{"/dev/pts/ptmx": os.ModeCharDevice | 0o666, "/dev/ptmx": os.ModeCharDevice | 0o666, "/dev/mqueue": os.ModeDir | os.ModeSticky | 0o777}
	stat := func(path string) (os.FileInfo, error) {
		mode, ok := modes[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return fakeModeInfo{mode: mode}, nil
	}
	var chmods []string
	chmod := func(path string, mode os.FileMode) error {
		chmods = append(chmods, fmt.Sprintf("%s %04o", path, mode))
		modes[path] = modes[path]&os.ModeType | mode
		return nil
	}
	if err := lockSharedDevices(stat, chmod); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/dev/pts/ptmx 0600", "/dev/ptmx 0600", "/dev/mqueue 0755"}; fmt.Sprint(chmods) != fmt.Sprint(want) {
		t.Fatalf("chmods = %v, want %v", chmods, want)
	}
	chmods = nil
	if err := lockSharedDevices(stat, chmod); err != nil || len(chmods) != 0 {
		t.Fatalf("already locked: err = %v, chmods = %v", err, chmods)
	}

	open := func(path string) (os.FileInfo, error) {
		if path == "/dev/pts/ptmx" {
			return fakeModeInfo{mode: os.ModeCharDevice | 0o666}, nil
		}
		return nil, os.ErrNotExist
	}
	for _, tc := range []struct {
		name  string
		stat  func(string) (os.FileInfo, error)
		chmod func(string, os.FileMode) error
		want  string
	}{
		{"stat", func(string) (os.FileInfo, error) { return nil, errTestIO }, chmod, "stat shared device /dev/pts/ptmx"},
		{"chmod", open, func(string, os.FileMode) error { return errTestIO }, "restrict shared device /dev/pts/ptmx"},
		{"still open", open, func(string, os.FileMode) error { return nil }, "still accessible to other uids"},
		{"restat", func() func(string) (os.FileInfo, error) {
			calls := 0
			return func(path string) (os.FileInfo, error) {
				calls++
				if calls > 1 {
					return nil, errTestIO
				}
				return open(path)
			}
		}(), func(string, os.FileMode) error { return nil }, "stat shared device /dev/pts/ptmx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := lockSharedDevices(tc.stat, tc.chmod); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	t.Run("server refuses to start", func(t *testing.T) {
		cfg, ops := fakeCgroupServer(t)
		ops.StatSharedTemp = open
		ops.ChmodShared = func(string, os.FileMode) error { return errTestIO }
		_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
		requireErrorContains(t, err, "restrict shared device /dev/pts/ptmx")
	})
}
