//go:build linux

package sshdemo

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	gossh "golang.org/x/crypto/ssh"
)

type peerListener struct {
	net.Listener
	ip net.IP
}

func (l peerListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	peer := *conn.RemoteAddr().(*net.TCPAddr)
	peer.IP = l.ip
	return peerConn{Conn: conn, peer: &peer}, nil
}

type peerConn struct {
	net.Conn
	peer *net.TCPAddr
}

func (c peerConn) RemoteAddr() net.Addr { return c.peer }

func TestServerProxyProtocolPeerPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		peer   net.IP
		trust  []string
		header string
		allow  bool
	}{
		{name: "loopback LOCAL", trust: []string{"198.51.100.0/24"}, header: "LOCAL", allow: true},
		{name: "loopback PROXY", trust: []string{"192.0.2.8/32"}, header: "PROXY"},
		{name: "loopback v1", trust: []string{"192.0.2.8/32"}, header: "v1"},
		{name: "loopback missing", trust: []string{"192.0.2.8/32"}, header: "missing"},

		{name: "trusted IPv4 edge", peer: net.ParseIP("192.0.2.8"), trust: []string{" 192.0.2.0/24 "}, header: "PROXY", allow: true},
		{name: "trusted IPv6 edge", peer: net.ParseIP("2001:db8::1"), trust: []string{"192.0.2.0/24", "2001:db8::/64"}, header: "PROXY", allow: true},
		{name: "trusted edge missing header", peer: net.ParseIP("192.0.2.8"), trust: []string{"192.0.2.0/24"}, header: "missing"},
		{name: "untrusted demo peer", peer: net.ParseIP("172.31.250.3"), trust: []string{"172.31.250.2/32"}, header: "PROXY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startTestServerWithPeer(t, tc.peer, func(c *Config) {
				c.ProxyProtocol = true
				c.ProxyProtocolTrust = tc.trust
				c.HandshakeTimeout = 100 * time.Millisecond
			})
			conn, err := net.Dial("tcp", h.addr)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			switch tc.header {
			case "LOCAL":
				_, _ = conn.Write(EncodeProxyV2(nil, nil))
			case "PROXY":
				source := &net.TCPAddr{IP: net.ParseIP("203.0.113.4"), Port: 4000}
				_, _ = conn.Write(EncodeProxyV2(source, conn.RemoteAddr()))
			case "v1":
				_, _ = io.WriteString(conn, "PROXY UNKNOWN\r\n")
			case "missing":
				_, _ = io.WriteString(conn, "SSH-2.0-test\r\n")
			}
			banner, err := bufio.NewReader(conn).ReadString('\n')
			if got := err == nil && strings.HasPrefix(banner, "SSH-2.0-"); got != tc.allow {
				t.Fatalf("banner = %q, error = %v, want accepted = %t", banner, err, tc.allow)
			}
		})
	}

	if _, err := NewServer(t.Context(), Config{ProxyProtocol: true}); err == nil || !strings.Contains(err.Error(), "--proxy-protocol-trust") {
		t.Fatalf("missing CIDR error = %v", err)
	}
	if _, err := NewServer(t.Context(), Config{ProxyProtocol: true, ProxyProtocolTrust: []string{"invalid"}}); err == nil || !strings.Contains(err.Error(), "--proxy-protocol-trust") {
		t.Fatalf("invalid CIDR error = %v", err)
	}
	policy, err := proxyConnectionPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy(proxyproto.ConnPolicyOptions{Upstream: &net.UnixAddr{}}); !errors.Is(err, proxyproto.ErrInvalidUpstream) {
		t.Fatalf("non-TCP peer error = %v", err)
	}
}

func TestDemoRunnersSetDefaultJSONLogger(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, name := range []string{"server", "edge"} {
		t.Run(name, func(t *testing.T) {
			logs := &syncBuffer{}
			logger := slog.New(slog.NewJSONHandler(logs, nil))
			switch name {
			case "server":
				_ = RunServer(t.Context(), Config{Isolation: "invalid", Logger: logger})
			case "edge":
				_ = RunEdge(t.Context(), EdgeConfig{Listen: "127.0.0.1:-1", Logger: logger})
			}
			slog.Info("library startup notice")
			var record map[string]any
			if err := json.Unmarshal([]byte(logs.String()), &record); err != nil || record["msg"] != "library startup notice" {
				t.Fatalf("default logger output = %q, %v", logs.String(), err)
			}
		})
	}
}

func TestServerLogsSanitizedSSHUser(t *testing.T) {
	h := startTestServer(t, nil)
	prefix := "visitor\t\u009b\u007f"
	user := prefix + strings.Repeat("é", 128)
	client := dialSSH(t, h.addr, user)
	sess, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	_ = sess.Wait()
	want := "visitor���" + strings.Repeat("é", 118)
	waitLogContains(t, h.logs, `"ssh_user":"`+want+`"`, time.Second)
}

func TestServerDeadlineTerminatesProcessGroup(t *testing.T) {
	for _, cooperative := range []bool{true, false} {
		name := "forced"
		if cooperative {
			name = "cooperative"
		}
		t.Run(name, func(t *testing.T) {
			const grace = 80 * time.Millisecond
			type sentSignal struct {
				pid int
				sig syscall.Signal
				at  time.Time
			}
			signals := make(chan sentSignal, 4)
			ops := DefaultSysOps()
			originalKill := ops.Kill
			ops.Kill = func(pid int, sig syscall.Signal) error {
				signals <- sentSignal{pid, sig, time.Now()}
				if !cooperative && sig == syscall.SIGTERM {
					return nil
				}
				return originalKill(pid, sig)
			}
			h := startTestServer(t, func(c *Config) {
				c.SaveBranch = "graceful-shutdown"
				c.MaxSessionDuration = 300 * time.Millisecond
				c.ShutdownGrace = grace
			}, WithSysOps(ops))
			client := dialSSH(t, h.addr, "deadline")
			timeout := time.AfterFunc(3*time.Second, func() { _ = client.Close() })
			defer timeout.Stop()
			sess, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sess.Close() }()
			if err := sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}); err != nil {
				t.Fatal(err)
			}
			out, err := sess.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.Shell(); err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, out); err != nil {
				t.Fatal(err)
			}
			waitErr := sess.Wait()
			if cooperative && waitErr != nil {
				t.Fatalf("cooperative child wait = %v", waitErr)
			}
			if !cooperative && waitErr == nil {
				t.Fatal("forced child reported success")
			}
			readSignal := func() sentSignal {
				t.Helper()
				select {
				case sig := <-signals:
					return sig
				case <-time.After(time.Second):
					t.Fatal("missing process-group signal")
					return sentSignal{}
				}
			}
			term, kill := readSignal(), readSignal()
			if term.sig != syscall.SIGTERM || kill.sig != syscall.SIGKILL || term.pid >= 0 || kill.pid != term.pid {
				t.Fatalf("signals sent to process group: %+v, %+v", term, kill)
			}
			if !cooperative && kill.at.Sub(term.at) < grace {
				t.Fatalf("kill came before the grace period elapsed: %v", kill.at.Sub(term.at))
			}
			waitLogContains(t, h.logs, `"end_reason":"max-duration"`, time.Second)
		})
	}
}
