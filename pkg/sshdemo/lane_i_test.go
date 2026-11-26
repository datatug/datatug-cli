//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestStartupFilesystemCapacity(t *testing.T) {
	cfg := failureTestConfig(t)
	exact := uint64(cfg.MaxSessions) * cfg.SessionDiskQuota
	for _, tc := range []struct {
		name string
		size uint64
		err  error
		want string
	}{
		{"exact fit", exact, nil, ""},
		{"disk too small", exact - 1, nil, "filesystem capacity"},
		{"statfs error", 0, os.ErrPermission, "statfs sessions-dir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := SysOps{FilesystemCapacity: func(path string) (uint64, uint64, error) {
				if path != cfg.SessionsDir {
					t.Fatalf("statfs path = %s", path)
				}
				return tc.size, 0, tc.err
			}}
			_, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			if tc.want != "" {
				requireErrorContains(t, err, tc.want)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestGlobalPreShellConnectionAdmission(t *testing.T) {
	h := startTestServerWithPeer(t, net.ParseIP("192.0.2.8"), func(c *Config) {
		c.ProxyProtocol = true
		c.ProxyProtocolTrust = []string{"192.0.2.8/32"}
		c.MaxConnections = 2
		c.ShellStartTimeout = 500 * time.Millisecond
	})
	dial := func(ip string) (*gossh.Client, string, error) {
		conn, err := net.DialTimeout("tcp", h.addr, time.Second)
		if err != nil {
			return nil, "", err
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write(EncodeProxyV2(&net.TCPAddr{IP: net.ParseIP(ip), Port: 31415}, conn.RemoteAddr())); err != nil {
			return nil, "", err
		}
		var banner string
		client, channels, requests, err := gossh.NewClientConn(conn, h.addr, &gossh.ClientConfig{
			User: "visitor", HostKeyCallback: gossh.InsecureIgnoreHostKey(),
			BannerCallback: func(msg string) error { banner = msg; return nil },
		})
		if err != nil {
			return nil, banner, err
		}
		return gossh.NewClient(client, channels, requests), banner, nil
	}
	for _, ip := range []string{"198.51.100.1", "198.51.100.2"} {
		client, _, err := dial(ip)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
	}
	if _, banner, err := dial("198.51.100.3"); err == nil || banner != demoFullMessage {
		t.Fatalf("at global capacity err = %v, banner = %q", err, banner)
	}
	if n := h.srv.slots.ActiveCount(); n != 0 {
		t.Fatalf("authenticated connections used %d shell slots", n)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		client, _, err := dial("198.51.100.3")
		if err == nil {
			_ = client.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pre-shell deadlines did not release capacity")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func loopbackListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func proxyDial(t *testing.T, host string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func rawConnect(t *testing.T, relay *sessionRelay, auth, extra string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn := proxyDial(t, relay.address)
	req := "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n"
	if auth != "" {
		req += "Proxy-Authorization: " + auth + "\r\n"
	}
	if _, err := io.WriteString(conn, req+"\r\n"+extra); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return conn, reader, resp
}

func TestEdgeHTTPConnectionCapAndRejection(t *testing.T) {
	proxyLn := loopbackListener(t)
	fwdLn := loopbackListener(t)
	accepted := make(chan struct{}, 2)
	notified := notifyingListener{Listener: proxyLn, accepted: accepted}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	edge := NewEdge(EdgeConfig{MaxProxyConns: 1})
	go func() { done <- edge.Serve(ctx, fwdLn, notified) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	a := proxyDial(t, proxyLn.Addr().String())
	<-accepted
	b := proxyDial(t, proxyLn.Addr().String())
	select {
	case <-accepted:
		t.Fatal("edge accepted a second HTTP connection at capacity")
	case <-time.After(40 * time.Millisecond):
	}
	// Send headers only. A rejected request must respond and close promptly.
	if _, err := io.WriteString(a, "POST / HTTP/1.1\r\nHost: example.com\r\nContent-Length: 20\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(a)
	resp, err := http.ReadResponse(r, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !resp.Close {
		t.Fatalf("rejection = HTTP %d, close = %t", resp.StatusCode, resp.Close)
	}
	if _, err := r.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("after rejection read = %v", err)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("edge did not admit waiting HTTP connection")
	}
	_ = b.Close()
}

type notifyingListener struct {
	net.Listener
	accepted chan<- struct{}
}

func (l notifyingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted <- struct{}{}
	}
	return conn, err
}

func TestSessionRelayFairnessAndRevocation(t *testing.T) {
	const sharedUser, sharedSecret = "edge", "upstream-test"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodConnect || req.Host != "github.com:443" ||
			req.Header.Get("Proxy-Authorization") != proxyBasic(sharedUser, sharedSecret) {
			http.Error(w, "incorrect edge request", http.StatusForbidden)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\nready")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw)
	}))
	defer up.Close()
	cfg := DefaultConfig()
	cfg.Isolation = IsolationProcess
	cfg.MaxSessionTunnels = 1
	cfg.MaxEgressTunnels = 2
	cfg.EgressProxy = "http://" + sharedUser + ":" + sharedSecret + "@" + up.Listener.Addr().String()
	relay, err := startSessionRelay(cfg, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer (&Server{relay: relay}).closeRelay()

	register := func(id string) (string, func()) {
		t.Helper()
		u, revoke := relay.register(id)
		p, err := url.Parse(u)
		if err != nil || p.Hostname() != "127.0.0.1" {
			t.Fatal("invalid relay URL")
		}
		token, _ := p.User.Password()
		return proxyBasic(p.User.Username(), token), revoke
	}
	auth1, revoke1 := register("session1")
	defer revoke1()
	auth2, revoke2 := register("session2")
	defer revoke2()
	auth3, revoke3 := register("session3")
	defer revoke3()
	for _, auth := range []string{"", proxyBasic("session1", "wrong")} {
		conn, _, resp := rawConnect(t, relay, auth, "")
		_ = conn.Close()
		if !resp.Close || resp.StatusCode != http.StatusProxyAuthRequired {
			t.Fatalf("auth rejection HTTP %d, close = %t", resp.StatusCode, resp.Close)
		}
	}

	c1, rd1, resp := rawConnect(t, relay, auth1, "ping")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first tunnel HTTP %d", resp.StatusCode)
	}
	data := make([]byte, 9)
	if _, err := io.ReadFull(rd1, data); err != nil || string(data) != "readyping" {
		t.Fatalf("tunnel read-ahead = %q, error = %v", data, err)
	}
	rejectAtCapacity := func(auth string) {
		t.Helper()
		conn, _, resp := rawConnect(t, relay, auth, "")
		_ = conn.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("at capacity HTTP %d", resp.StatusCode)
		}
	}
	rejectAtCapacity(auth1)
	c2, _, resp := rawConnect(t, relay, auth2, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second session HTTP %d", resp.StatusCode)
	}
	defer func() { _ = c2.Close() }()
	rejectAtCapacity(auth3)

	revoke1()
	if _, err := rd1.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("revoked tunnel read = %v", err)
	}
	_ = c1.Close()
	conn, _, resp := rawConnect(t, relay, auth1, "")
	_ = conn.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("revoked credential HTTP %d", resp.StatusCode)
	}
}

func TestCappedListenerCloseAtCapacity(t *testing.T) {
	tcp := loopbackListener(t)
	listener := capListener(tcp, 1)
	peer := proxyDial(t, tcp.Addr().String())
	defer func() { _ = peer.Close() }()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = accepted.Close() }()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept at capacity after Close = %v", err)
	}
}
