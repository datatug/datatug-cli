package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/datatug/datatug-cli/pkg/dtroot"
)

// DirEnv overrides the state directory (default ~/datatug/daemon).
const DirEnv = "DATATUG_DAEMON_DIR"

// NewManager returns the Manager wired to the real OS.
func NewManager() *Manager {
	dir := os.Getenv(DirEnv)
	if dir == "" {
		dir = filepath.Join(dtroot.Path(), "daemon")
	}
	return &Manager{
		Dir: dir, Exe: os.Executable, Probe: probeAgent, Dial: dialLoopback, PortFree: portFree,
		Alive: processAlive, Owns: processOwns, Terminate: terminateProcess, Spawn: spawnDetached,
		Now: time.Now, Sleep: time.Sleep, StartWait: 5 * time.Second, Poll: 100 * time.Millisecond,
	}
}

var probeClient = &http.Client{Timeout: 400 * time.Millisecond}

// probeAgent says whether a DataTug agent API answers on 127.0.0.1:port, and
// its version. Anything else (a refused connection, another service) is "no".
func probeAgent(port int) (string, bool) {
	resp, err := probeClient.Get(fmt.Sprintf("http://%s:%d/datatug/agent-info", Host, port))
	if err != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var doc struct {
		Version   *string         `json:"version"`
		Principal json.RawMessage `json:"principal"`
	}
	if json.NewDecoder(resp.Body).Decode(&doc) != nil || doc.Version == nil || doc.Principal == nil {
		return "", false
	}
	return *doc.Version, true
}

// dialLoopback says whether a TCP connection to 127.0.0.1:port is accepted. Nothing is written.
func dialLoopback(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", Host, port), 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("%s:%d", Host, port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// spawnDetached starts exe in its own process group, detached from the caller's
// terminal, with stdout and stderr appended to logPath.
func spawnDetached(exe string, args []string, logPath string) (int, <-chan struct{}, error) {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.Command(exe, args...)
	cmd.Env = scrubbedEnv(os.Environ())
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = detachedAttr()
	if err = cmd.Start(); err != nil {
		return 0, nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	return cmd.Process.Pid, exited, nil
}

// childEnvNames and childEnvPrefixes are the only variables the detached server
// inherits: what a process needs to run, plus the non-secret directory settings
// serve reads. Everything else (DATATUG_OPEN_DB, tokens, passwords) stays with
// the caller; the server lives for hours and its environment is visible to ps.
var (
	childEnvNames = map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"TMPDIR": true, "TMP": true, "TEMP": true, "TZ": true, "TERM": true,
		"LANG": true, "LANGUAGE": true,
		"SYSTEMROOT": true, "WINDIR": true, "USERPROFILE": true, "APPDATA": true,
		"LOCALAPPDATA": true, "PROGRAMDATA": true, "COMSPEC": true, "PATHEXT": true,
		"DATATUG_DAEMON_DIR": true, "DATATUG_EVIDENCE_DIR": true,
		"DATATUG_POLICIES_DIR": true, "DATATUG_PERSONAL_DIR": true,
		"DATATUG_SESSION_RESULTS_DIR": true, "DATATUG_CHAT_STORE_DIR": true, "DATATUG_CHAT_PREFERENCES_DIR": true,
		// The telemetry opt-outs: the child is the same binary and must honour them.
		"DATATUG_TELEMETRY": true, "DO_NOT_TRACK": true, "CI": true,
	}
	childEnvPrefixes = []string{"LC_", "XDG_"}
)

func scrubbedEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		keep := childEnvNames[upper]
		for _, prefix := range childEnvPrefixes {
			keep = keep || strings.HasPrefix(upper, prefix)
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}
