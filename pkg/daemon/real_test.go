package daemon

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func portOf(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, p, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var port int
	for _, c := range p {
		port = port*10 + int(c-'0')
	}
	return port
}

func TestProbeAgent(t *testing.T) {
	serve := func(status int, body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/datatug/agent-info" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
	}
	good := serve(200, `{"version":"1.2.3","principal":{"id":"admin"}}`)
	defer good.Close()
	if v, ok := probeAgent(portOf(t, good)); !ok || v != "1.2.3" {
		t.Fatalf("%q %v", v, ok)
	}
	for name, srv := range map[string]*httptest.Server{
		"not 200":      serve(500, `{"version":"1","principal":{}}`),
		"not JSON":     serve(200, `<html>`),
		"no version":   serve(200, `{"principal":{}}`),
		"no principal": serve(200, `{"version":"1"}`),
	} {
		if _, ok := probeAgent(portOf(t, srv)); ok {
			t.Errorf("%s must not count as a datatug API", name)
		}
		srv.Close()
	}
	closed := serve(200, ``)
	port := portOf(t, closed)
	closed.Close()
	if _, ok := probeAgent(port); ok {
		t.Fatal("refused connection must not count")
	}
}

func TestPortFree(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if portFree(port) {
		t.Fatal("bound port reported free")
	}
	_ = l.Close()
	if !portFree(port) {
		t.Fatal("released port reported busy")
	}
}

func TestSpawnAliveTerminate(t *testing.T) {
	t.Setenv("DAEMON_TEST_HELPER", "1")
	logPath := filepath.Join(t.TempDir(), "daemon.log")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pid, exited, err := spawnDetached(exe, []string{"serve"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !processAlive(pid) {
		t.Fatal("spawned process must be alive")
	}
	if err = terminateProcess(pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit after SIGTERM")
	}
	if processAlive(pid) {
		t.Fatal("terminated process must be gone")
	}
	if _, err = os.Stat(logPath); err != nil {
		t.Fatal("log file must exist")
	}
	if !processAlive(1) { // not ours: EPERM still means alive
		t.Fatal("pid 1 is alive")
	}
}

func TestSpawnErrors(t *testing.T) {
	if _, _, err := spawnDetached("/bin/true", nil, filepath.Join(t.TempDir(), "missing", "daemon.log")); err == nil {
		t.Fatal("unwritable log must fail")
	}
	if _, _, err := spawnDetached(filepath.Join(t.TempDir(), "no-such-binary"), nil, filepath.Join(t.TempDir(), "daemon.log")); err == nil {
		t.Fatal("missing binary must fail")
	}
}

func TestNewManager(t *testing.T) {
	t.Setenv(DirEnv, "/x/daemon")
	m := NewManager()
	if m.Dir != "/x/daemon" || m.StartWait != 5*time.Second || m.Poll <= 0 {
		t.Fatalf("%+v", m)
	}
	t.Setenv(DirEnv, "")
	if m = NewManager(); !strings.HasSuffix(m.Dir, filepath.Join("datatug", "daemon")) {
		t.Fatal(m.Dir)
	}
}

func TestProcessOwns(t *testing.T) {
	// The test binary is not a `serve --port N`; a helper started as one is.
	if processOwns(os.Getpid(), 8989) {
		t.Fatal("the test process is not a daemon")
	}
	t.Setenv("DAEMON_TEST_HELPER", "1")
	exe, _ := os.Executable()
	pid, exited, err := spawnDetached(exe, []string{"serve", "--host", "127.0.0.1", "--port", "8995"}, filepath.Join(t.TempDir(), "l.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = terminateProcess(pid); <-exited }()
	if !processOwns(pid, 8995) || processOwns(pid, 8996) {
		t.Fatal("must match exactly the recorded port")
	}
	if processOwns(999999, 8995) {
		t.Fatal("a missing pid is not ours")
	}
}
