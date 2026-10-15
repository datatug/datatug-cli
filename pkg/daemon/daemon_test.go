package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fake is a Manager whose OS seams are plain fields.
type fake struct {
	*Manager
	up        map[int]bool // ports answering as a datatug API
	free      map[int]bool
	alive     map[int]bool
	spawned   [][]string
	sleeps    int
	exited    chan struct{}
	spawnErr  error
	exeErr    error
	termErr   error
	termCalls []int
	onSleep   func()
	now       time.Time
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{up: map[int]bool{}, free: map[int]bool{}, alive: map[int]bool{}, now: time.Unix(1_800_000_000, 0)}
	for p := DefaultPort; p <= MaxPort; p++ {
		f.free[p] = true
	}
	f.Manager = &Manager{
		Dir:      filepath.Join(t.TempDir(), "daemon"),
		Exe:      func() (string, error) { return "/bin/datatug", f.exeErr },
		Probe:    func(port int) (string, bool) { return "v9", f.up[port] },
		PortFree: func(port int) bool { return f.free[port] },
		Alive:    func(pid int) bool { return f.alive[pid] },
		Terminate: func(pid int) error {
			f.termCalls = append(f.termCalls, pid)
			return f.termErr
		},
		Spawn: func(exe string, args []string, logPath string) (int, <-chan struct{}, error) {
			f.spawned = append(f.spawned, args)
			f.exited = make(chan struct{})
			if f.spawnErr != nil {
				return 0, nil, f.spawnErr
			}
			f.alive[4242] = true
			return 4242, f.exited, nil
		},
		Now: func() time.Time { return f.now },
		Sleep: func(time.Duration) {
			f.sleeps++
			if f.onSleep != nil {
				f.onSleep()
			}
		},
		StartWait: 300 * time.Millisecond, Poll: 100 * time.Millisecond,
	}
	return f
}

func (f *fake) writeRec(t *testing.T, pid, port int) {
	t.Helper()
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.writeRecord(Record{PID: pid, Port: port, StartedBy: "claude-mod", StartedAt: f.now}); err != nil {
		t.Fatal(err)
	}
}

func (f *fake) hasRecord() bool {
	_, err := os.Stat(f.path("daemon.json"))
	return err == nil
}

func TestStatusNotRunning(t *testing.T) {
	f := newFake(t)
	st := f.Status()
	if st.Running || st.URL != "" || st.Port != DefaultPort || !strings.HasSuffix(st.UIBase, "/store/http-127.0.0.1:8989") || st.LogFile != f.LogFile() {
		t.Fatalf("%+v", st)
	}
}

func TestStatusOwned(t *testing.T) {
	f := newFake(t)
	f.writeRec(t, 77, 8991)
	f.up[8991], f.alive[77] = true, true
	st := f.Status()
	if !st.Running || st.PID != 77 || st.Port != 8991 || st.StartedBy != "claude-mod" || st.URL != "http://127.0.0.1:8991" || st.Version != "v9" || st.StartedAt == "" {
		t.Fatalf("%+v", st)
	}
}

func TestStatusStaleRecordAndExternal(t *testing.T) {
	f := newFake(t)
	f.writeRec(t, 77, 8991) // process gone
	f.up[8989] = true
	st := f.Status()
	if !st.Running || st.StartedBy != "external" || st.PID != 0 || st.Port != DefaultPort {
		t.Fatalf("%+v", st)
	}
}

func TestStatusIgnoresCorruptRecord(t *testing.T) {
	f := newFake(t)
	_ = os.MkdirAll(f.Dir, 0o700)
	_ = os.WriteFile(f.path("daemon.json"), []byte("{nope"), 0o600)
	if f.Status().Running {
		t.Fatal("corrupt record must not count")
	}
}

func TestStartReusesRunning(t *testing.T) {
	f := newFake(t)
	f.up[8989] = true
	st, err := f.Start("claude-mod")
	if err != nil || !st.Running || len(f.spawned) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestStartSpawnsReadOnlyLocalhost(t *testing.T) {
	f := newFake(t)
	f.free[8989] = false // a foreign listener
	f.onSleep = func() { f.up[8990] = true }
	st, err := f.Start("claude-mod")
	if err != nil || !st.Running || st.Port != 8990 || st.PID != 4242 || st.StartedBy != "claude-mod" {
		t.Fatalf("%+v %v", st, err)
	}
	args := strings.Join(f.spawned[0], " ")
	if args != "serve --host 127.0.0.1 --port 8990 --idle-timeout 2h0m0s" || strings.Contains(args, "allow") {
		t.Fatal(args)
	}
	pid, err := os.ReadFile(f.path("daemon.pid"))
	if err != nil || strings.TrimSpace(string(pid)) != "4242" {
		t.Fatalf("pidfile %q %v", pid, err)
	}
	if _, err = os.Stat(f.path("daemon.lock")); !os.IsNotExist(err) {
		t.Fatal("lock must be released")
	}
	rec, _ := f.readRecord()
	if rec.Version != "v9" || rec.Binary != "/bin/datatug" {
		t.Fatalf("%+v", rec)
	}
}

func TestStartReadyImmediately(t *testing.T) {
	f := newFake(t)
	f.Spawn = func(string, []string, string) (int, <-chan struct{}, error) {
		f.alive[5], f.up[8989] = true, true
		return 5, make(chan struct{}), nil
	}
	// First Status sees nothing (up is set only when spawning), so this reaches Spawn.
	st, err := f.Start("codex-mod")
	if err != nil || !st.Running || f.sleeps != 0 {
		t.Fatalf("%+v %v sleeps=%d", st, err, f.sleeps)
	}
}

func TestStartRecheckAfterLock(t *testing.T) {
	f := newFake(t)
	calls := 0
	f.Probe = func(int) (string, bool) { calls++; return "v9", calls > 1 }
	st, err := f.Start("claude-mod")
	if err != nil || !st.Running || len(f.spawned) != 0 {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestStartFailures(t *testing.T) {
	t.Run("state dir", func(t *testing.T) {
		f := newFake(t)
		blocker := filepath.Join(t.TempDir(), "file")
		_ = os.WriteFile(blocker, nil, 0o600)
		f.Dir = filepath.Join(blocker, "daemon")
		_, err := f.Start("x")
		assertNotRunning(t, err, ErrPhraseState)
	})
	t.Run("no port", func(t *testing.T) {
		f := newFake(t)
		for p := range f.free {
			f.free[p] = false
		}
		st, err := f.Start("x")
		assertNotRunning(t, err, ErrPhraseNoPort)
		if st.Error != ErrPhraseNoPort || st.Running {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("no binary", func(t *testing.T) {
		f := newFake(t)
		f.exeErr = errors.New("boom")
		_, err := f.Start("x")
		assertNotRunning(t, err, ErrPhraseNoBinary)
	})
	t.Run("spawn", func(t *testing.T) {
		f := newFake(t)
		f.spawnErr = errors.New("boom")
		_, err := f.Start("x")
		assertNotRunning(t, err, ErrPhraseSpawn)
	})
	t.Run("record cannot be written", func(t *testing.T) {
		f := newFake(t)
		inner := f.Spawn
		f.Spawn = func(exe string, args []string, log string) (int, <-chan struct{}, error) {
			// A non-empty directory where daemon.json must go makes the rename fail.
			_ = os.MkdirAll(filepath.Join(f.path("daemon.json"), "x"), 0o700)
			return inner(exe, args, log)
		}
		_, err := f.Start("x")
		assertNotRunning(t, err, ErrPhraseState)
		if len(f.termCalls) != 1 {
			t.Fatal("the spawned server must be terminated")
		}
	})
	t.Run("child exits", func(t *testing.T) {
		f := newFake(t)
		f.onSleep = func() { close(f.exited); f.onSleep = nil }
		_, err := f.Start("x")
		assertNotRunning(t, err, ErrPhraseExited)
		if f.hasRecord() {
			t.Fatal("record must be removed")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		f := newFake(t)
		_, err := f.Start("x")
		assertNotRunning(t, err, ErrPhraseTimeout)
		if f.hasRecord() || len(f.termCalls) != 1 || f.sleeps != 3 {
			t.Fatalf("record=%v term=%v sleeps=%d", f.hasRecord(), f.termCalls, f.sleeps)
		}
	})
}

func assertNotRunning(t *testing.T, err error, phrase string) {
	t.Helper()
	var nr ErrNotRunning
	if !errors.As(err, &nr) || nr.Phrase != phrase || !strings.Contains(err.Error(), phrase) {
		t.Fatalf("err = %v, want phrase %q", err, phrase)
	}
}

func TestStartLockContention(t *testing.T) {
	t.Run("loser waits for the winner", func(t *testing.T) {
		f := newFake(t)
		_ = os.MkdirAll(f.Dir, 0o700)
		_ = os.WriteFile(f.path("daemon.lock"), nil, 0o600)
		_ = os.Chtimes(f.path("daemon.lock"), f.now, f.now)
		f.onSleep = func() { f.up[8989] = true }
		st, err := f.Start("x")
		if err != nil || !st.Running || len(f.spawned) != 0 {
			t.Fatalf("%+v %v", st, err)
		}
	})
	t.Run("loser times out", func(t *testing.T) {
		f := newFake(t)
		_ = os.MkdirAll(f.Dir, 0o700)
		_ = os.WriteFile(f.path("daemon.lock"), nil, 0o600)
		_ = os.Chtimes(f.path("daemon.lock"), f.now, f.now)
		_, err := f.Start("x")
		assertNotRunning(t, err, ErrPhraseTimeout)
	})
	t.Run("stale lock is replaced", func(t *testing.T) {
		f := newFake(t)
		_ = os.MkdirAll(f.Dir, 0o700)
		_ = os.WriteFile(f.path("daemon.lock"), nil, 0o600)
		old := f.now.Add(-time.Hour)
		_ = os.Chtimes(f.path("daemon.lock"), old, old)
		f.onSleep = func() { f.up[8989] = true }
		if st, err := f.Start("x"); err != nil || !st.Running || len(f.spawned) != 1 {
			t.Fatalf("%+v %v", st, err)
		}
	})
}

func TestStop(t *testing.T) {
	t.Run("nothing running", func(t *testing.T) {
		if err := newFake(t).Stop(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("external is never touched", func(t *testing.T) {
		f := newFake(t)
		f.up[8989] = true
		if err := f.Stop(); !errors.Is(err, ErrNotOwned) || len(f.termCalls) != 0 {
			t.Fatalf("%v %v", err, f.termCalls)
		}
	})
	t.Run("stale record is removed without signalling", func(t *testing.T) {
		f := newFake(t)
		f.writeRec(t, 77, 8991)
		f.alive[77] = true // pid alive but nothing answers: a reused pid
		if err := f.Stop(); err != nil || len(f.termCalls) != 0 || f.hasRecord() {
			t.Fatalf("%v %v", err, f.termCalls)
		}
	})
	t.Run("dead pid", func(t *testing.T) {
		f := newFake(t)
		f.writeRec(t, 77, 8991)
		f.up[8991] = true
		if err := f.Stop(); err != nil || len(f.termCalls) != 0 || f.hasRecord() {
			t.Fatalf("%v %v", err, f.termCalls)
		}
	})
	t.Run("stops and cleans", func(t *testing.T) {
		f := newFake(t)
		f.writeRec(t, 77, 8991)
		f.up[8991], f.alive[77] = true, true
		f.onSleep = func() { f.alive[77] = false }
		if err := f.Stop(); err != nil || len(f.termCalls) != 1 || f.termCalls[0] != 77 || f.hasRecord() {
			t.Fatalf("%v %v", err, f.termCalls)
		}
		if _, err := os.Stat(f.path("daemon.pid")); !os.IsNotExist(err) {
			t.Fatal("pidfile must be removed")
		}
	})
	t.Run("signal error", func(t *testing.T) {
		f := newFake(t)
		f.writeRec(t, 77, 8991)
		f.up[8991], f.alive[77] = true, true
		f.termErr = errors.New("denied")
		if err := f.Stop(); err == nil || !strings.Contains(err.Error(), "denied") || !f.hasRecord() {
			t.Fatal(err)
		}
	})
	t.Run("does not stop in time", func(t *testing.T) {
		f := newFake(t)
		f.writeRec(t, 77, 8991)
		f.up[8991], f.alive[77] = true, true
		err := f.Stop()
		if err == nil || !strings.Contains(err.Error(), "did not stop") || !f.hasRecord() {
			t.Fatal(err)
		}
	})
}

func TestLogFileUnderDir(t *testing.T) {
	f := newFake(t)
	if filepath.Dir(f.LogFile()) != f.Dir {
		t.Fatal(f.LogFile())
	}
}

func TestWriteRecordFailures(t *testing.T) {
	for _, blocked := range []string{"daemon.json.tmp", "daemon.json", "daemon.pid"} {
		f := newFake(t)
		_ = os.MkdirAll(filepath.Join(f.path(blocked), "x"), 0o700)
		if err := f.writeRecord(Record{PID: 1, Port: 2}); err == nil {
			t.Errorf("%s blocked: want an error", blocked)
		}
	}
}
