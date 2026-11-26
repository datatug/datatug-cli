//go:build linux

package sshdemo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"charm.land/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

type badSSHChannel struct{ gossh.NewChannel }

func (badSSHChannel) Accept() (gossh.Channel, <-chan *gossh.Request, error) {
	return nil, nil, errTestIO
}

func TestChannelWrapperPreservesAcceptFailure(t *testing.T) {
	f := &filteringNewChannel{NewChannel: badSSHChannel{}}
	ch, requests, err := f.Accept()
	if ch != nil || requests != nil || !errors.Is(err, errTestIO) {
		t.Fatalf("Accept = %v, %v, %v", ch, requests, err)
	}
}

type brokenRandom struct{}

func (brokenRandom) Read([]byte) (int, error) { return 0, errTestIO }

func TestSessionStartupFailuresReleaseSlot(t *testing.T) {
	for _, tc := range []struct {
		name, log string
		setup     func(*testing.T, *testServerHarness)
	}{
		{"random", "generate session id failed", func(t *testing.T, _ *testServerHarness) {
			original := sessionRandom
			t.Cleanup(func() { sessionRandom = original })
			sessionRandom = brokenRandom{}
		}},
		{"workspace", "create session workspace failed", func(t *testing.T, h *testServerHarness) {
			if err := os.Remove(h.sessionsDir); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startTestServer(t, nil)
			tc.setup(t, h)
			client := dialSSH(t, h.addr, "visitor")
			session, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			if err := session.RequestPty("xterm", 24, 80, nil); err != nil {
				t.Fatal(err)
			}
			if err := session.Shell(); err != nil {
				t.Fatal(err)
			}
			var status *gossh.ExitError
			if err := session.Wait(); !errors.As(err, &status) || status.ExitStatus() != 1 {
				t.Fatalf("SSH status = %v", err)
			}
			if h.srv.slots.ActiveCount() != 0 {
				t.Fatal("failed SSH startup retained its slot")
			}
			waitLogContains(t, h.logs, tc.log, time.Second)
		})
	}
}

func TestWorkspaceCreationFailureKeepsExistingDirectory(t *testing.T) {
	sessions := t.TempDir()
	slot := Slot{}
	ops := DefaultSysOps()
	collision := filepath.Join(sessions, "existing")
	if err := os.Mkdir(collision, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := createSessionWorkspace(sessions, "existing", slot, IsolationProcess, ops)
	requireErrorContains(t, err, "mkdir")
	if info, err := os.Stat(collision); err != nil || !info.IsDir() {
		t.Fatalf("existing workspace = %v / %v", info, err)
	}

	t.Run("root permissions", func(t *testing.T) {
		original := setWorkspaceMode
		t.Cleanup(func() { setWorkspaceMode = original })
		setWorkspaceMode = func(string, os.FileMode) error { return errTestIO }
		_, _, _, err := createSessionWorkspace(sessions, "new", slot, IsolationProcess, ops)
		requireErrorContains(t, err, "chmod")
		if _, err := os.Stat(filepath.Join(sessions, "new")); !os.IsNotExist(err) {
			t.Fatalf("partial workspace remains: %v", err)
		}
	})
	t.Run("nested permissions", func(t *testing.T) {
		original := setWorkspaceMode
		t.Cleanup(func() { setWorkspaceMode = original })
		setWorkspaceMode = func(path string, mode os.FileMode) error {
			if filepath.Base(path) == "home" {
				return errTestIO
			}
			return os.Chmod(path, mode)
		}
		_, _, _, err := createSessionWorkspace(sessions, "new", slot, IsolationProcess, ops)
		requireErrorContains(t, err, "chmod")
	})
	t.Run("nested directory", func(t *testing.T) {
		original := setWorkspaceMode
		t.Cleanup(func() { setWorkspaceMode = original })
		setWorkspaceMode = func(path string, mode os.FileMode) error {
			if filepath.Base(path) == "new" {
				if err := os.WriteFile(filepath.Join(path, "home"), nil, 0o600); err != nil {
					return err
				}
			}
			return os.Chmod(path, mode)
		}
		_, _, _, err := createSessionWorkspace(sessions, "new", slot, IsolationProcess, ops)
		requireErrorContains(t, err, "mkdir")
	})
}

type fileInfoWithoutOwner struct{ os.FileInfo }

func (fileInfoWithoutOwner) Sys() any { return nil }

func TestStaleCleanupSkipsFilesAndRequiresDirectory(t *testing.T) {
	cfg := failureTestConfig(t)
	p := filepath.Join(cfg.SessionsDir, "file")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := defaultFileUID(fileInfoWithoutOwner{info}); ok {
		t.Fatal("unexpected uid for abstract FileInfo")
	}
	if _, err := NewServer(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("startup deleted a non-directory: %v", err)
	}
	cfg.SessionsDir = p
	_, err = NewServer(t.Context(), cfg)
	requireErrorContains(t, err, "not a directory")
	ops := DefaultSysOps()
	ops.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	requireErrorContains(t, checkSessionsDir(p, IsolationProcess, ops), "create sessions-dir")
}

func TestPTYDrainPendingAndUnavailableFD(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	if _, err := write.WriteString("pending"); err != nil {
		t.Fatal(err)
	}
	drainPTYMaster(read, 25*time.Millisecond)
	original := ptySyscallConn
	t.Cleanup(func() { ptySyscallConn = original })
	var called bool
	ptySyscallConn = func(*os.File) (syscall.RawConn, error) { called = true; return nil, errTestIO }
	drainPTYMaster(read, time.Second)
	if !called {
		t.Fatal("PTY fd was not checked")
	}
}

type contextOnlySession struct {
	ssh.Session
	ctx  *sessionContext
	done chan error
	pty  ssh.Pty
}

func (s contextOnlySession) Context() ssh.Context                    { return s.ctx }
func (s contextOnlySession) Pty() (ssh.Pty, <-chan ssh.Window, bool) { return s.pty, nil, true }
func (s contextOnlySession) RemoteAddr() net.Addr                    { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (s contextOnlySession) User() string                            { return "visitor" }
func (s contextOnlySession) Environ() []string                       { return nil }
func (s contextOnlySession) Exit(code int) error                     { s.done <- fmt.Errorf("exit %d", code); return nil }
func (s contextOnlySession) Write(data []byte) (int, error)          { return len(data), nil }

type isolatedSSHContext struct {
	ssh.Context
	ctx        context.Context
	connection *connectionState
}

func (c isolatedSSHContext) Done() <-chan struct{} { return c.ctx.Done() }
func (c isolatedSSHContext) Value(key any) any {
	if _, ok := key.(connectionStateKey); ok {
		return c.connection
	}
	return nil
}

func testPTY(t *testing.T) ssh.Pty {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	fd := int(master.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	num, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", num), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return ssh.Pty{Master: master, Slave: slave}
}

func TestSessionTransportClosureEndsRunningChild(t *testing.T) {
	for _, tc := range []struct {
		name           string
		closeTransport func(context.CancelFunc, chan struct{})
	}{
		{"context cancellation", func(cancel context.CancelFunc, _ chan struct{}) { cancel() }},
		{"channel closure", func(_ context.CancelFunc, done chan struct{}) { close(done) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startTestServer(t, func(c *Config) { c.SaveBranch = "wait-forever" })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server, client := net.Pipe()
			defer func() { _ = client.Close() }()
			conn := &connectionState{conn: server, shellStarted: make(chan struct{})}
			meta := &sessionMeta{activityCh: make(chan struct{}), chanDone: make(chan struct{})}
			sess := contextOnlySession{
				ctx: &sessionContext{
					Context: isolatedSSHContext{ctx: ctx, connection: conn},
					meta:    meta,
				},
				done: make(chan error, 1),
				pty:  testPTY(t),
			}
			go h.srv.handleSession(sess)
			select {
			case <-conn.shellStarted:
			case err := <-sess.done:
				t.Fatalf("child stopped before cancellation: %v", err)
			case <-time.After(time.Second):
				t.Fatal("child did not start")
			}
			tc.closeTransport(cancel, meta.chanDone)
			select {
			case err := <-sess.done:
				if err.Error() != "exit 1" {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("session did not finish")
			}
			_ = client.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("supervisor did not close the connection: %v", err)
			}
			if h.srv.slots.ActiveCount() != 0 || !strings.Contains(h.logs.String(), `"end_reason":"client-drop"`) {
				t.Fatalf("child cleanup log = %s", h.logs.String())
			}
		})
	}
}
