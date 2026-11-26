//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestStartupCapacity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SessionsDir = t.TempDir()
	cfg.StateDir = t.TempDir()
	cfg.Listen = "127.0.0.1:0"
	cfg.Isolation = IsolationProcess
	cfg.SkipTemplate = true
	exactDisk := uint64(cfg.MaxSessions) * cfg.SessionDiskQuota
	exactMem := uint64(cfg.MaxSessions)*(cfg.SessionMemQuota+cfg.SessionDiskQuota) + supervisorReserve
	for _, tc := range []struct {
		name    string
		size    uint64
		fsErr   error
		limit   string
		readErr error
		wantErr string
		warn    bool
	}{
		{name: "exact fit", size: exactDisk, limit: fmt.Sprint(exactMem)},
		{name: "disk too small", size: exactDisk - 1, wantErr: "filesystem capacity"},
		{name: "statfs error", fsErr: os.ErrPermission, wantErr: "statfs sessions-dir"},
		{name: "memory too small", size: exactDisk, limit: fmt.Sprint(exactMem - 1), wantErr: "exceed the cgroup memory limit"},
		{name: "reserve too large", size: exactDisk, limit: "1", wantErr: "exceed the cgroup memory limit"},
		{name: "absent cgroup", size: exactDisk, readErr: os.ErrNotExist, warn: true},
		{name: "unlimited cgroup", size: exactDisk, limit: "max\n", warn: true},
		{name: "unreadable cgroup", size: exactDisk, readErr: os.ErrPermission, wantErr: "read cgroup memory limit"},
		{name: "malformed cgroup", size: exactDisk, limit: "garbage", wantErr: "parse cgroup memory limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			cfg.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			ops := SysOps{
				FilesystemSize: func(path string) (uint64, error) {
					if path != cfg.SessionsDir {
						t.Fatalf("statfs path = %s", path)
					}
					return tc.size, tc.fsErr
				},
				ReadMemoryMax: func() ([]byte, error) { return []byte(tc.limit), tc.readErr },
			}
			s, err := NewServer(t.Context(), cfg, WithSysOps(ops))
			if tc.wantErr != "" {
				if s != nil || err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("server = %v, error = %v, want %s", s, err, tc.wantErr)
				}
				return
			}
			if err != nil || strings.Contains(logs.String(), "skipping memory capacity check") != tc.warn {
				t.Fatalf("startup error = %v, logs = %s", err, &logs)
			}
		})
	}
	cfg.SessionDiskQuota = math.MaxUint64
	if _, err := NewServer(t.Context(), cfg, WithSysOps(SysOps{FilesystemSize: func(string) (uint64, error) { return math.MaxUint64, nil }})); err == nil || !strings.Contains(err.Error(), "filesystem capacity") {
		t.Fatalf("overflowing quotas returned %v", err)
	}
}

func TestSupervisorChecksDirectorylessUIDs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.StateDir, cfg.SessionsDir = t.TempDir(), t.TempDir()
	cfg.MaxSessions = 3
	cfg.SkipTemplate = true
	var inspected []int
	var killed int
	ops := SysOps{
		FileUID: func(os.FileInfo) (int, bool) { return 0, true },
		ListUIDProcs: func(uid int) ([]int, error) {
			inspected = append(inspected, uid)
			switch uid {
			case cfg.UIDBase:
				return nil, os.ErrPermission
			case cfg.UIDBase + 1:
				if killed == 0 {
					return []int{123}, nil
				}
			}
			return nil, nil
		},
		Kill: func(pid int, signal syscall.Signal) error {
			if pid != 123 || signal != syscall.SIGKILL {
				t.Fatalf("kill(%d, %v)", pid, signal)
			}
			killed++
			return nil
		},
	}
	if err := os.Chmod(cfg.SessionsDir, 0o711); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(t.Context(), cfg, WithSysOps(ops))
	if err != nil {
		t.Fatal(err)
	}
	if killed != 1 || len(inspected) != 4 || inspected[3] != cfg.UIDBase+2 {
		t.Fatalf("inspected = %v, killed = %d", inspected, killed)
	}
	slot, err := srv.slots.Acquire("a")
	if err != nil || slot.UID != cfg.UIDBase+1 {
		t.Fatalf("slot = %+v, err = %v", slot, err)
	}
	cfg.MaxSessions = 1
	if _, err := NewServer(t.Context(), cfg, WithSysOps(ops)); err == nil || !strings.Contains(err.Error(), "all session slots quarantined") {
		t.Fatalf("startup with no safe UIDs returned %v", err)
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
	cfg.MaxSessionTunnels = 1
	cfg.MaxEgressTunnels = 2
	cfg.EgressProxy = "http://" + sharedUser + ":" + sharedSecret + "@" + up.Listener.Addr().String()
	relay, err := startSessionRelay(cfg, DefaultSysOps())
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

func TestEdgeAuthenticatesSupervisor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge-auth")
	auth := proxyBasic("supervisor", "test-token")
	if err := writeProxyAuthFile(path, auth, func(string, int, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
	edge := NewEdge(EdgeConfig{
		ProxyAuthIP:   "172.31.250.3",
		ProxyAuthFile: path,
		MaxTunnels:    3, MaxTunnelsPerIP: 1,
		LookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("DNS test") },
	})
	do := func(auth string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodConnect, "github.com:443", nil)
		req.RemoteAddr = "172.31.250.3:1234"
		req.Header.Set("Proxy-Authorization", auth)
		rec := httptest.NewRecorder()
		edge.ProxyHandler().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("proxy HTTP %d, want %d", rec.Code, want)
		}
	}
	do("", http.StatusProxyAuthRequired)
	if !edge.tunnelLimit.acquire("172.31.250.3") {
		t.Fatal("reserve first tunnel")
	}
	defer edge.tunnelLimit.release("172.31.250.3")
	do(auth, http.StatusBadGateway) // authenticated demo uses relay cap instead of edge per-IP cap
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	do(auth, http.StatusProxyAuthRequired)
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
