// Package daemon owns the one local, read-only `datatug serve` process that
// AI-agent mods use so their cards can link to the DataTug web UI
// (spec/features/cli/daemon). The CLI is the owner of that process: it records
// it in a state directory (pidfile, record, log) and stops it with Stop.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// DefaultPort is the preferred agent port; MaxPort bounds the search for a free one.
	DefaultPort = 8989
	MaxPort     = 9009
	// Host is the only address the daemon binds.
	Host = "127.0.0.1"
	// IdleTimeout is how long the daemon runs without a request before it exits.
	IdleTimeout = 2 * time.Hour

	// Fixed failure phrases: they never carry a path, URL or credential.
	ErrPhraseNoPort   = "no free port between 8989 and 9009"
	ErrPhraseExited   = "server exited during start"
	ErrPhraseTimeout  = "server was not ready within 5s"
	ErrPhraseNoBinary = "cannot locate the datatug binary"
	ErrPhraseSpawn    = "cannot launch the server"
	ErrPhraseState    = "cannot write the daemon state directory"

	startedByExternal = "external"
	lockStaleAfter    = 30 * time.Second
	stopWait          = 5 * time.Second
)

// ErrNotOwned is returned by Stop for a datatug API this CLI did not start.
var ErrNotOwned = errors.New("no daemon started by this CLI")

// ErrNotRunning is returned by Start, next to its Status, when no server runs.
type ErrNotRunning struct{ Phrase string }

func (e ErrNotRunning) Error() string { return "daemon is not running: " + e.Phrase }

// Record is what the CLI persists about the daemon it started (daemon.json).
type Record struct {
	PID         int       `json:"pid"`
	Port        int       `json:"port"`
	URL         string    `json:"url"`
	StartedAt   time.Time `json:"startedAt"`
	StartedBy   string    `json:"startedBy"`
	Version     string    `json:"version"`
	Binary      string    `json:"binary"`
	IdleTimeout string    `json:"idleTimeout"`
}

// Status is the machine-readable state printed by `daemon status|start`.
type Status struct {
	Running     bool   `json:"running"`
	URL         string `json:"url"`
	UIBase      string `json:"uiBase"`
	Port        int    `json:"port"`
	PID         int    `json:"pid"`
	StartedBy   string `json:"startedBy"`
	StartedAt   string `json:"startedAt,omitempty"`
	Version     string `json:"version"`
	LogFile     string `json:"logFile"`
	IdleTimeout string `json:"idleTimeout"`
	Error       string `json:"error,omitempty"`
}

// Manager holds the state directory and every seam over the OS, so tests drive
// the whole lifecycle without a real process or port.
type Manager struct {
	Dir      string
	Exe      func() (string, error)
	Probe    func(port int) (version string, ok bool)
	PortFree func(port int) bool
	Alive    func(pid int) bool
	// Owns says whether pid is a `datatug serve` on port (guards against a reused pid).
	Owns      func(pid, port int) bool
	Terminate func(pid int) error
	// Spawn starts the detached server and returns its pid and a channel closed when it exits.
	Spawn func(exe string, args []string, logPath string) (pid int, exited <-chan struct{}, err error)
	Now   func() time.Time
	Sleep func(time.Duration)
	// StartWait bounds the readiness wait; Poll is its interval.
	StartWait time.Duration
	Poll      time.Duration
}

func (m *Manager) path(name string) string { return filepath.Join(m.Dir, name) }

// LogFile is where the daemon's stdout and stderr go.
func (m *Manager) LogFile() string { return m.path("daemon.log") }

func (m *Manager) readRecord() (Record, bool) {
	data, err := os.ReadFile(m.path("daemon.json"))
	if err != nil {
		return Record{}, false
	}
	var rec Record
	if json.Unmarshal(data, &rec) != nil || rec.PID <= 0 || rec.Port <= 0 {
		return Record{}, false
	}
	return rec, true
}

func (m *Manager) writeRecord(rec Record) error {
	data, _ := json.MarshalIndent(rec, "", "  ") // a Record always encodes
	tmp := m.path("daemon.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path("daemon.json")); err != nil {
		return err
	}
	return os.WriteFile(m.path("daemon.pid"), []byte(strconv.Itoa(rec.PID)+"\n"), 0o600)
}

func (m *Manager) removeRecord() {
	_ = os.Remove(m.path("daemon.json"))
	_ = os.Remove(m.path("daemon.pid"))
}

func (m *Manager) statusOf(port int) Status {
	return Status{
		URL: fmt.Sprintf("http://%s:%d", Host, port), UIBase: UIBase(port),
		Port: port, LogFile: m.LogFile(), IdleTimeout: IdleTimeout.String(),
	}
}

// Status reports the daemon without starting or stopping anything.
func (m *Manager) Status() Status {
	if rec, ok := m.readRecord(); ok {
		if version, up := m.Probe(rec.Port); up && m.Alive(rec.PID) {
			st := m.statusOf(rec.Port)
			st.Running, st.PID, st.StartedBy, st.Version = true, rec.PID, rec.StartedBy, version
			st.StartedAt = rec.StartedAt.UTC().Format(time.RFC3339)
			return st
		}
	}
	if version, up := m.Probe(DefaultPort); up {
		st := m.statusOf(DefaultPort)
		st.Running, st.StartedBy, st.Version = true, startedByExternal, version
		return st
	}
	// Not running: the link base still names the port a start would prefer.
	st := m.statusOf(DefaultPort)
	st.URL = ""
	return st
}

// Start ensures the daemon runs. The returned Status is always set; the error
// is ErrNotRunning when no server could be brought up.
func (m *Manager) Start(caller string) (Status, error) {
	if st := m.Status(); st.Running {
		return st, nil
	}
	fallback := m.statusOf(DefaultPort)
	if rec, ok := m.readRecord(); ok && m.Alive(rec.PID) && m.Owns(rec.PID, rec.Port) {
		// Our server is alive but did not answer in time: wait for it, never start a second one.
		if st, up := m.waitRunning(); up {
			return st, nil
		}
		return fail(fallback, ErrPhraseTimeout)
	}
	m.removeRecord()
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return fail(fallback, ErrPhraseState)
	}
	release, owner := m.lock()
	if !owner {
		// Another starter is at work: only wait for it.
		if st, ok := m.waitRunning(); ok {
			return st, nil
		}
		return fail(fallback, ErrPhraseTimeout)
	}
	defer release()
	// Re-check: the other starter may have finished before the lock was taken.
	if st := m.Status(); st.Running {
		return st, nil
	}
	port := 0
	for p := DefaultPort; p <= MaxPort && port == 0; p++ {
		if m.PortFree(p) {
			port = p
		}
	}
	if port == 0 {
		return fail(fallback, ErrPhraseNoPort)
	}
	st := m.statusOf(port)
	exe, err := m.Exe()
	if err != nil {
		return fail(st, ErrPhraseNoBinary)
	}
	args := []string{"serve", "--host", Host, "--port", strconv.Itoa(port), "--idle-timeout", IdleTimeout.String()}
	pid, exited, err := m.Spawn(exe, args, m.LogFile())
	if err != nil {
		return fail(st, ErrPhraseSpawn)
	}
	rec := Record{
		PID: pid, Port: port, URL: st.URL, StartedAt: m.Now().UTC(), StartedBy: caller,
		Binary: exe, IdleTimeout: IdleTimeout.String(),
	}
	if err = m.writeRecord(rec); err != nil {
		_ = m.Terminate(pid)
		return fail(st, ErrPhraseState)
	}
	for waited := time.Duration(0); ; waited += m.Poll {
		if version, up := m.Probe(port); up {
			rec.Version = version
			_ = m.writeRecord(rec)
			return m.Status(), nil
		}
		select {
		case <-exited:
			m.removeRecord()
			return fail(st, ErrPhraseExited)
		default:
		}
		if waited >= m.StartWait {
			_ = m.Terminate(pid)
			m.removeRecord()
			return fail(st, ErrPhraseTimeout)
		}
		m.Sleep(m.Poll)
	}
}

func fail(st Status, phrase string) (Status, error) {
	st.Error = phrase
	return st, ErrNotRunning{Phrase: phrase}
}

func (m *Manager) waitRunning() (Status, bool) {
	for waited := time.Duration(0); ; waited += m.Poll {
		if st := m.Status(); st.Running {
			return st, true
		}
		if waited >= m.StartWait {
			return Status{}, false
		}
		m.Sleep(m.Poll)
	}
}

// lock takes daemon.lock exclusively. A lock older than lockStaleAfter is a
// dead starter's and is replaced. owner is false when another starter holds it.
func (m *Manager) lock() (release func(), owner bool) {
	name := m.path("daemon.lock")
	if info, err := os.Stat(name); err == nil && m.Now().Sub(info.ModTime()) > lockStaleAfter {
		_ = os.Remove(name)
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}, false
	}
	_ = f.Close()
	return func() { _ = os.Remove(name) }, true
}

// Stop ends the daemon this CLI started. A datatug API it did not start is
// never touched (ErrNotOwned); any other process is never signalled.
func (m *Manager) Stop() error {
	rec, ok := m.readRecord()
	if !ok {
		if _, up := m.Probe(DefaultPort); up {
			return ErrNotOwned
		}
		return nil
	}
	if !m.Alive(rec.PID) || !m.Owns(rec.PID, rec.Port) {
		// Stale record, or a reused pid that is not our `serve`: never signalled. A server
		// that is ours but no longer answers is still stopped below.
		m.removeRecord()
		return nil
	}
	if err := m.Terminate(rec.PID); err != nil {
		return fmt.Errorf("stop daemon pid %d: %w", rec.PID, err)
	}
	for waited := time.Duration(0); m.Alive(rec.PID); waited += m.Poll {
		if waited >= stopWait {
			return fmt.Errorf("daemon pid %d did not stop within %s", rec.PID, stopWait)
		}
		m.Sleep(m.Poll)
	}
	m.removeRecord()
	return nil
}
