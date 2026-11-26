//go:build linux

package sshdemo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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
	defer sess.Close()

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
		case <-done:
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
	h := startTestServer(t, func(c *Config) {
		c.Isolation = IsolationUID
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
		ListUIDProcs:  func(int) ([]int, error) { return nil, nil },
		CredentialFor: func(int, int) *syscall.Credential { return nil },
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
		ListUIDProcs: func(uid int) ([]int, error) {
			if uid == cfg.UIDBase+1 {
				return []int{123}, nil
			}
			return nil, nil
		},
		Kill: func(int, syscall.Signal) error { return nil },
	}
	srv, err := NewServer(context.Background(), cfg, WithSysOps(ops))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "procs-left")); !os.IsNotExist(err) {
		t.Fatalf("expected only processes left for that slot, got %v", err)
	}
	if got := strings.Count(logs.String(), `"level":"ERROR","msg":"quarantining uid`); got != 3 {
		t.Fatalf("expected three quarantine diagnostics, got %d in %s", got, logs.String())
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
	cfg.MaxSessions = 1
	if _, err := NewServer(context.Background(), cfg, WithSysOps(ops)); err == nil || !strings.Contains(err.Error(), "all session slots quarantined") {
		t.Fatalf("starting with all slots quarantined = %v", err)
	}
	cfg.Isolation = IsolationProcess
	cfg.Listen = "127.0.0.1:0"
	if _, err := NewServer(context.Background(), cfg, WithSysOps(ops)); err == nil || !strings.Contains(err.Error(), "shared uid") {
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

func TestServer_WatchdogEndsOverQuotaSession(t *testing.T) {
	for _, resource := range []string{"memory", "disk"} {
		t.Run(resource, func(t *testing.T) {
			h := startTestServer(t, func(c *Config) {
				c.SaveBranch = "wait-forever"
				c.WatchdogInterval = 20 * time.Millisecond
				if resource == "memory" {
					c.SessionMemQuota = 1
				} else {
					c.SessionDiskQuota = 1
				}
			})
			sess, err := dialSSH(t, h.addr, "visitor").NewSession()
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			sess.Stdout, sess.Stderr = &stdout, &stderr
			if err := sess.Shell(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- sess.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("quota termination returned success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("watchdog did not end the session")
			}
			notice := fmt.Sprintf("This session used more than its share of %s and was ended.", resource)
			if strings.Count(stderr.String(), notice) != 1 || strings.Contains(stdout.String(), notice) {
				t.Fatalf("want one quota notice on stderr, got stderr %q, stdout %q", stderr.String(), stdout.String())
			}
			waitNoSessionDirs(t, h.sessionsDir, time.Second)
			log := waitLogContains(t, h.logs, `"end_reason":"`+resource+`-quota"`, time.Second)
			for _, field := range []string{`"level":"WARN"`, `"resource":"` + resource + `"`, `"quota_bytes":1`, `"used_bytes":`} {
				if !strings.Contains(log, field) {
					t.Fatalf("missing %s in %s", field, log)
				}
			}
		})
	}
}

func TestServer_WatchdogQuotaNoticeWhilePTYStreams(t *testing.T) {
	h := startTestServer(t, func(c *Config) {
		c.SaveBranch = "quota-stream"
		c.WatchdogInterval = 20 * time.Millisecond
		c.SessionDiskQuota = 1 << 20
	})
	sess, err := dialSSH(t, h.addr, "visitor").NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm", 24, 80, nil); err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	streaming := make(chan error, 1)
	go func() {
		_, err := io.CopyN(io.Discard, stdout, 64<<10)
		streaming <- err
		_, _ = io.Copy(io.Discard, stdout)
	}()
	select {
	case err := <-streaming:
		if err != nil {
			t.Fatalf("PTY output stopped before quota trigger: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("PTY output did not start")
	}
	entries, err := os.ReadDir(h.sessionsDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one active workspace, got %v, %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(h.sessionsDir, entries[0].Name(), "over-quota"), make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("quota termination returned success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not end the streaming session")
	}
	if got := stderr.String(); got != "This session used more than its share of disk and was ended.\r\n" {
		t.Fatalf("quota notice on stderr = %q", got)
	}
}

func TestWriteQuotaNotice_ClosesBlockedTransport(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
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
