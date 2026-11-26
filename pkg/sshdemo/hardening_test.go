//go:build linux

package sshdemo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestServer_OneSessionChannelPerConnection(t *testing.T) {
	h := startTestServer(t, nil)
	client := dialSSH(t, h.addr, "visitor")
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	rejectSecondSession := func() {
		t.Helper()
		_, err := client.NewSession()
		var openErr *gossh.OpenChannelError
		if !errors.As(err, &openErr) || openErr.Reason != gossh.Prohibited {
			t.Fatalf("second session returned %v, want Prohibited", err)
		}
	}
	rejectSecondSession()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	rejectSecondSession()
}

func TestServer_BoundsPreShellRequests(t *testing.T) {
	h := startTestServer(t, nil)
	client := dialSSH(t, h.addr, "visitor")
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	if err := sess.Setenv("LANG", strings.Repeat("é", 129)); err == nil {
		t.Fatal("accepted an env value over 256 bytes")
	}
	if err := sess.Setenv("LANG", strings.Repeat("x", 257)); err == nil {
		t.Fatal("accepted a 257-byte env value")
	}
	for i := range 8 {
		if err := sess.Setenv("LANG", strings.Repeat("x", 256)); err != nil {
			t.Fatalf("env request %d: %v", i+1, err)
		}
	}
	if err := sess.Setenv("COLORTERM", "truecolor"); err == nil {
		t.Fatal("accepted ninth env request")
	}

	if err := sess.RequestPty(strings.Repeat("a", 65), 24, 80, nil); err == nil {
		t.Fatal("accepted a 65-byte PTY terminal name")
	}
	pty := ptyReqPayload("xterm", 80, 24)
	binary.BigEndian.PutUint32(pty[len(pty)-4:], 1025)
	pty = append(pty, make([]byte, 1025)...)
	if ok, err := sess.SendRequest("pty-req", true, pty); err != nil || ok {
		t.Fatalf("oversized PTY modes request returned (%v, %v), want (false, nil)", ok, err)
	}
	pty = ptyReqPayload(strings.Repeat("x", 64), 80, 24)
	binary.BigEndian.PutUint32(pty[len(pty)-4:], 1024)
	pty = append(pty, make([]byte, 1024)...)
	if ok, err := sess.SendRequest("pty-req", true, pty); err != nil || !ok {
		t.Fatalf("PTY request at byte limits returned (%v, %v), want (true, nil)", ok, err)
	}

	var out bytes.Buffer
	sess.Stdout = &out
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Wait(); err != nil || !strings.Contains(out.String(), "SEEN:") {
		t.Fatalf("shell after refusals: err=%v out=%q", err, out.String())
	}
}

func TestServer_ShellStartDeadline(t *testing.T) {
	const timeout = 250 * time.Millisecond
	h := startTestServer(t, func(c *Config) {
		c.SaveBranch = "wait-forever"
		c.ShellStartTimeout = timeout
	})
	for _, withPTY := range []bool{false, true} {
		client := dialSSH(t, h.addr, "visitor")
		if withPTY {
			sess, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
				t.Fatal(err)
			}
		}
		done := make(chan error, 1)
		go func() { done <- client.Wait() }()
		select {
		case err := <-done:
			_ = err // only that the connection ended matters here
		case <-time.After(2 * time.Second):
			t.Fatalf("connection still open (PTY requested: %v)", withPTY)
		}
	}

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
	if ready, err := bufio.NewReader(out).ReadString('\n'); err != nil || !strings.Contains(ready, "READY:") {
		t.Fatalf("waiting for child: %q, %v", ready, err)
	}
	time.Sleep(timeout + 25*time.Millisecond)
	if _, _, err := client.SendRequest("test@demo.datatug.app", true, nil); err != nil {
		t.Fatalf("shell did not clear start deadline: %v", err)
	}
}

func TestServer_AuthenticatedConnectionCapUsesProxyIP(t *testing.T) {
	h := startTestServerWithPeer(t, net.ParseIP("192.0.2.8"), func(c *Config) {
		c.ProxyProtocol = true
		c.ProxyProtocolTrust = []string{"192.0.2.8/32"}
		c.MaxSessionsPerIP = 1
	})
	newTransport := func(ip string) net.Conn {
		t.Helper()
		conn, err := net.DialTimeout("tcp", h.addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		source := &net.TCPAddr{IP: net.ParseIP(ip), Port: 54321}
		if _, err := conn.Write(EncodeProxyV2(source, conn.RemoteAddr())); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	connect := func(ip string) (client *gossh.Client, banner string, err error) {
		conn := newTransport(ip)
		c, channels, requests, err := gossh.NewClientConn(conn, h.addr, &gossh.ClientConfig{
			User: "visitor", HostKeyCallback: gossh.InsecureIgnoreHostKey(),
			BannerCallback: func(msg string) error { banner = msg; return nil },
		})
		if err != nil {
			return nil, banner, err
		}
		client = gossh.NewClient(c, channels, requests)
		t.Cleanup(func() { _ = client.Close() })
		return client, banner, nil
	}
	const ip = "203.0.113.10"
	stalled := newTransport(ip)
	if banner, err := bufio.NewReader(stalled).ReadString('\n'); err != nil || !strings.HasPrefix(banner, "SSH-2.0-") {
		t.Fatalf("pre-auth connection: %q, %v", banner, err)
	}
	first, _, err := connect(ip)
	if err != nil {
		t.Fatalf("incomplete handshake consumed authenticated admission: %v", err)
	}
	if _, banner, err := connect(ip); err == nil || !strings.Contains(banner, "The DataTug demo is full") {
		t.Fatalf("connection cap returned banner %q and error %v", banner, err)
	}
	if _, _, err := connect("203.0.113.11"); err != nil {
		t.Fatalf("a different PROXY IP was refused: %v", err)
	}
	_ = first.Close()
	deadline := time.Now().Add(time.Second)
	for {
		if _, _, err := connect(ip); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed connection did not release per-IP admission")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServer_PTYOwnershipFailurePreventsChildStart(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	root, fs := fakeCgroupMount(t)
	h := startTestServer(t, func(c *Config) {
		c.Isolation = IsolationUID
		c.CgroupRoot = root
		c.SessionsDir = dir
	}, WithSysOps(SysOps{
		FileUID: func(os.FileInfo) (int, bool) { return 0, true },
		Chown:   func(string, int, int) error { return nil },
		Fchown: func(slave *os.File, uid, gid int) error {
			if _, err := slave.Stat(); err != nil {
				return err
			}
			return syscall.EPERM
		},
		cgroups:            fs,
		FilesystemCapacity: ampleSessionFilesystem,
		CredentialFor:      func(int, int) *syscall.Credential { return nil },
	}))
	sess, err := dialSSH(t, h.addr, "visitor").NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	sess.Stdout = &out
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	var exitErr *gossh.ExitError
	if err := sess.Wait(); !errors.As(err, &exitErr) || exitErr.ExitStatus() != 1 || out.Len() != 0 {
		t.Fatalf("session returned %v and %q, want exit 1 with no child output", err, out.String())
	}
	waitNoSessionDirs(t, dir, time.Second)
	waitLogContains(t, h.logs, "chown pty slave descriptor", time.Second)
}

func TestServer_StaleWorkspacesQuarantineSlots(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dir-left", "procs-left"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "dir-left", "fail-cleanup"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.StateDir = t.TempDir()
	cfg.SessionsDir = dir
	cfg.SkipTemplate = true
	cfg.Executable = os.Args[0]
	cfg.ExecPrefixArgs = []string{"-test.run=^TestHelperProcess$", "--"}
	cfg.MaxSessions = 3
	var logs syncBuffer
	cfg.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
	ops := SysOps{
		FileUID: func(info os.FileInfo) (int, bool) {
			switch info.Name() {
			case "dir-left":
				return cfg.UIDBase, true
			case "procs-left":
				return cfg.UIDBase + 1, true
			default:
				return 0, true
			}
		},
		CredentialFor: func(int, int) *syscall.Credential { return nil },
	}
	cfg.CgroupRoot, ops.cgroups = fakeCgroupMount(t)
	ops.FilesystemCapacity = ampleSessionFilesystem
	addFakeSlot(t, cfg.CgroupRoot, "slot-1")
	write := ops.cgroups.write
	ops.cgroups.write = func(path, value string) error {
		if strings.HasSuffix(path, "slot-1/cgroup.kill") {
			return syscall.EPERM
		}
		return write(path, value)
	}
	srv, err := NewServer(context.Background(), cfg, WithSysOps(ops))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "procs-left")); err != nil {
		t.Fatalf("workspace for undrained slot was altered: %v", err)
	}
	if got := strings.Count(logs.String(), `"level":"ERROR","msg":"quarantining session slot`); got != 2 {
		t.Fatalf("expected two quarantine diagnostics, got %d in %s", got, logs.String())
	}
	for range 2 {
		slot, err := srv.slots.Acquire("ip")
		if err != nil || slot.UID != cfg.UIDBase+2 {
			t.Fatalf("Acquire = %+v, %v, want only the third uid", slot, err)
		}
		if _, err := srv.slots.Acquire("ip"); !errors.Is(err, ErrGlobalLimit) {
			t.Fatalf("Acquire with only usable slot in use = %v", err)
		}
		srv.slots.Release(slot)
	}
	cfg.CgroupRoot, ops.cgroups = fakeCgroupMount(t)
	cfg.MaxSessions = 1
	if _, err := NewServer(context.Background(), cfg, WithSysOps(ops)); err == nil || !strings.Contains(err.Error(), "all session slots quarantined") {
		t.Fatalf("starting with all slots quarantined = %v", err)
	}
	cfg.Isolation = IsolationProcess
	cfg.Listen = "127.0.0.1:0"
	if _, err := NewServer(context.Background(), cfg, WithSysOps(ops)); err == nil || !strings.Contains(err.Error(), "could not verify cleanup") {
		t.Fatalf("starting with unverified cleanup for shared uid = %v", err)
	}
}

func TestStaleCleanupFailsWhenWorkspaceOwnerCannotBeVerified(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale")
	for _, ops := range []SysOps{
		{Stat: func(string) (os.FileInfo, error) { return nil, syscall.EIO }},
		{FileUID: func(os.FileInfo) (int, bool) { return 0, false }},
	} {
		srv := &Server{cfg: Config{SessionsDir: stale, Isolation: IsolationUID}, ops: ops.withDefaults()}
		if err := srv.cleanStaleSessions(context.Background()); err == nil {
			t.Fatal("expected an error if the sessions directory cannot be read")
		}
		if err := os.Mkdir(stale, 0o700); err != nil {
			t.Fatal(err)
		}
		srv.cfg.SessionsDir = dir
		if err := srv.cleanStaleSessions(context.Background()); err == nil {
			t.Fatal("expected an error if the workspace owner cannot be verified")
		}
		if err := os.Remove(stale); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStaleCleanupSkipsDirectoryReplacedBySymlink(t *testing.T) {
	dir, target := t.TempDir(), t.TempDir()
	stale := filepath.Join(dir, "stale")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	ops := DefaultSysOps()
	stat := ops.Stat
	ops.Stat = func(path string) (os.FileInfo, error) {
		if path != stale {
			t.Fatalf("unexpected stale path %q", path)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		return stat(path)
	}
	ops.FileUID = func(os.FileInfo) (int, bool) {
		t.Fatal("attempted cleanup of a symlink")
		return 0, false
	}
	srv := &Server{cfg: Config{SessionsDir: dir, Isolation: IsolationUID}, ops: ops}
	if err := srv.cleanStaleSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestWriteQuotaNotice_ClosesBlockedTransport(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	done := make(chan struct{})
	go func() {
		writeQuotaNotice(server, server, "quota exceeded")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		_ = server.Close()
		t.Fatal("blocked visitor held up quota cleanup")
	}
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("reading from closed quota transport: %v", err)
	}
}

func TestServer_ReleasesConnectionAdmissionAfterSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		branch     string
		requestPTY bool
		wantExit   int
	}{
		{"normal exit", "exit-now", true, 0},
		{"no PTY rejection", "exit-now", false, 1},
		{"idle exit", "wait-forever", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startTestServer(t, func(c *Config) {
				c.MaxConnections = 1
				c.MaxSessionsPerIP = 1
				c.MaxSessionDuration = time.Hour
				c.IdleTimeout = 100 * time.Millisecond
				c.SaveBranch = tc.branch
			})
			client := dialSSH(t, h.addr, "visitor")
			sess, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			if tc.requestPTY {
				if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := sess.Shell(); err != nil {
				t.Fatal(err)
			}
			err = sess.Wait()
			if tc.wantExit == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var exitErr *gossh.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitStatus() != tc.wantExit {
					t.Fatalf("wait = %v, want exit %d", err, tc.wantExit)
				}
			}
			// Keep the client open. The supervisor must close the transport and
			// release both counters without relying on the client's close.
			waitForNoConnectionReservations(t, h.srv)
			_ = dialSSH(t, h.addr, "next visitor")
		})
	}
}

func waitForNoConnectionReservations(t *testing.T, srv *Server) {
	t.Helper()
	limit := &srv.connectionLimit
	deadline := time.Now().Add(time.Second)
	for {
		limit.mu.Lock()
		total, ips := limit.total, len(limit.perIP)
		limit.mu.Unlock()
		if total == 0 && ips == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("finished session still holds %d connections across %d IPs", total, ips)
		}
		time.Sleep(time.Millisecond)
	}
}
