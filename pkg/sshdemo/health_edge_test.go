//go:build linux

package sshdemo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	gossh "golang.org/x/crypto/ssh"
)

func TestCheckHealth(t *testing.T) {
	// 1. Healthy against live sshdemo.Server.
	h := startTestServer(t, nil)
	if err := CheckHealth(context.Background(), h.addr, 2*time.Second); err != nil {
		t.Fatalf("CheckHealth against live server: %v", err)
	}

	// 2. Non-SSH server fails.
	badLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = badLn.Close() }()
	go func() {
		conn, err := badLn.Accept()
		if err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\n")
		_ = conn.Close()
	}()
	if err := CheckHealth(context.Background(), badLn.Addr().String(), 2*time.Second); err == nil || !strings.Contains(err.Error(), "unexpected banner") {
		t.Fatalf("expected unexpected banner error, got %v", err)
	}

	// 3. Stalling server times out.
	stallLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = stallLn.Close() }()
	go func() {
		conn, err := stallLn.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		time.Sleep(time.Second)
	}()
	if err := CheckHealth(context.Background(), stallLn.Addr().String(), 100*time.Millisecond); err == nil {
		t.Fatal("expected timeout error on stalling server")
	}

	// 4. Refused connection fails.
	closedLn, _ := net.Listen("tcp", "127.0.0.1:0")
	closedAddr := closedLn.Addr().String()
	_ = closedLn.Close()
	if err := CheckHealth(context.Background(), closedAddr, 500*time.Millisecond); err == nil {
		t.Fatal("expected connect error on closed port")
	}

	// 5. Default addr/timeout with already-canceled context fails immediately without network wait.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckHealth(canceledCtx, "", 0); err == nil {
		t.Fatal("expected canceled error with default addr/timeout")
	}
}

func TestEncodeProxyV2_RoundTrip(t *testing.T) {
	// IPv4 round-trip parsed with go-proxyproto.
	src4 := &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 54321}
	dst4 := &net.TCPAddr{IP: net.ParseIP("198.51.100.2"), Port: 2222}
	raw4 := EncodeProxyV2(src4, dst4)
	hdr4, err := proxyproto.Read(bufio.NewReader(bytes.NewReader(raw4)))
	if err != nil {
		t.Fatalf("parse v4 proxy header: %v", err)
	}
	if hdr4.Version != 2 || hdr4.Command != proxyproto.PROXY ||
		hdr4.SourceAddr.String() != "203.0.113.10:54321" ||
		hdr4.DestinationAddr.String() != "198.51.100.2:2222" {
		t.Fatalf("unexpected v4 header: %+v", hdr4)
	}

	// IPv6 round-trip.
	src6 := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 41234}
	dst6 := &net.TCPAddr{IP: net.ParseIP("2001:db8::2"), Port: 2222}
	raw6 := EncodeProxyV2(src6, dst6)
	hdr6, err := proxyproto.Read(bufio.NewReader(bytes.NewReader(raw6)))
	if err != nil {
		t.Fatalf("parse v6 proxy header: %v", err)
	}
	if hdr6.Version != 2 || hdr6.Command != proxyproto.PROXY ||
		hdr6.SourceAddr.String() != "[2001:db8::1]:41234" ||
		hdr6.DestinationAddr.String() != "[2001:db8::2]:2222" {
		t.Fatalf("unexpected v6 header: %+v", hdr6)
	}

	// Non-TCP fallback encodes valid LOCAL/UNSPEC header.
	rawLocal := EncodeProxyV2(&net.UnixAddr{Name: "/tmp/a", Net: "unix"}, dst4)
	hdrLocal, err := proxyproto.Read(bufio.NewReader(bytes.NewReader(rawLocal)))
	if err != nil {
		t.Fatalf("parse LOCAL header: %v", err)
	}
	if hdrLocal.Version != 2 || hdrLocal.Command != proxyproto.LOCAL {
		t.Fatalf("unexpected LOCAL header: %+v", hdrLocal)
	}
}

func TestEdge_TCPForwarderProxyV2EndToEndAndCaps(t *testing.T) {
	// Start sshdemo.Server with ProxyProtocol enabled, then put Edge in front of it!
	srvHarness := startTestServerWithPeer(t, net.ParseIP("192.0.2.8"), func(c *Config) {
		c.ProxyProtocol = true
		c.ProxyProtocolTrust = []string{"192.0.2.8/32", "203.0.113.7/32"}
		c.SaveBranch = "exit-now"
	})

	fwdLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fwd: %v", err)
	}
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}

	edgeCfg := DefaultEdgeConfig()
	edgeCfg.Upstream = srvHarness.addr
	edgeCfg.MaxConns = 4
	edgeCfg.MaxConnsPerIP = 2
	edgeCfg.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	edge := NewEdge(edgeCfg)

	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- edge.Serve(ctx, peerListener{Listener: fwdLn, ip: net.ParseIP("203.0.113.7")}, proxyLn)
	}()
	t.Cleanup(func() {
		cancel()
		<-serveDone
	})

	// Connect SSH client through Edge -> PROXY v2 -> sshdemo.Server!
	client := dialSSH(t, fwdLn.Addr().String(), "via-edge")
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("NewSession via edge: %v", err)
	}
	defer func() { _ = sess.Close() }()
	if err := sess.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatalf("RequestPty via edge: %v", err)
	}
	out, _ := sess.StdoutPipe()
	if err := sess.Shell(); err != nil {
		t.Fatalf("Shell via edge: %v", err)
	}
	b, _ := io.ReadAll(out)
	if err := sess.Wait(); err != nil {
		t.Fatalf("Wait via edge: %v", err)
	}
	if !strings.Contains(string(b), "SEEN:") {
		t.Fatalf("expected SEEN output through edge, got %q", string(b))
	}

	// Test forwarder per-IP cap (MaxConnsPerIP=1) with an upstream echo server.
	upLn, _ := net.Listen("tcp", "127.0.0.1:0")
	defer func() { _ = upLn.Close() }()
	go func() {
		for {
			c, err := upLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(io.Discard, conn)
			}(c)
		}
	}()
	capLn, _ := net.Listen("tcp", "127.0.0.1:0")
	capEdge := NewEdge(EdgeConfig{
		Upstream:      upLn.Addr().String(),
		MaxConns:      2,
		MaxConnsPerIP: 1,
		Logger:        slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	capCtx, capCancel := context.WithCancel(context.Background())
	capDone := make(chan error, 1)
	go func() { capDone <- capEdge.ServeForwarder(capCtx, capLn) }()
	defer func() { capCancel(); <-capDone }()

	c1, err := net.Dial("tcp", capLn.Addr().String())
	if err != nil {
		t.Fatalf("dial c1: %v", err)
	}
	defer func() { _ = c1.Close() }()
	time.Sleep(40 * time.Millisecond)

	c2, err := net.Dial("tcp", capLn.Addr().String())
	if err != nil {
		t.Fatalf("dial c2: %v", err)
	}
	defer func() { _ = c2.Close() }()
	_ = c2.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 16)
	if _, err := c2.Read(buf); !errors.Is(err, io.EOF) {
		t.Fatalf("expected excess connection to be closed immediately (EOF), got %v", err)
	}
}

func TestEdge_CONNECTProxyAllowListAndPrivateIPRefusal(t *testing.T) {
	// Local echo backend standing in for github.com:443 via injected DialContext seam.
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer func() { _ = echoLn.Close() }()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	resolvedIPs := map[string][]net.IPAddr{
		"github.com":            {{IP: net.ParseIP("140.82.121.4")}},
		"api.github.com":        {{IP: net.ParseIP("127.0.0.1")}},       // loopback -> refused
		"codeload.github.com":   {{IP: net.ParseIP("10.0.0.5")}},        // private -> refused
		"cloud.openvaultdb.com": {{IP: net.ParseIP("169.254.169.254")}}, // link-local -> refused
	}
	var dialFail bool
	var resolveErr error
	logs := &syncBuffer{}

	edge := NewEdge(EdgeConfig{
		Allow:       []string{"github.com", "api.github.com", "codeload.github.com", "cloud.openvaultdb.com"},
		IdleTimeout: 150 * time.Millisecond,
		Logger:      slog.New(slog.NewJSONHandler(logs, nil)),
		LookupIPAddr: func(_ context.Context, host string) ([]net.IPAddr, error) {
			if resolveErr != nil {
				return nil, resolveErr
			}
			return resolvedIPs[host], nil
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if dialFail {
				return nil, errors.New("dial boom")
			}
			return (&net.Dialer{}).DialContext(ctx, network, echoLn.Addr().String())
		},
	})

	ts := httptest.NewServer(edge.ProxyHandler())
	defer ts.Close()
	proxyAddr := strings.TrimPrefix(ts.URL, "http://")

	sendRaw := func(req string) (string, net.Conn) {
		t.Helper()
		conn, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			t.Fatalf("dial proxy: %v", err)
		}
		if _, err := io.WriteString(conn, req); err != nil {
			t.Fatalf("write req: %v", err)
		}
		br := bufio.NewReader(conn)
		status, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read status: %v", err)
		}
		for {
			line, err := br.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
		return strings.TrimSpace(status), conn
	}

	// 1. Allowed host (case-insensitive) on 443 succeeds, echoes bytes, and hits idle timeout!
	status, conn := sendRaw("CONNECT GitHub.COM:443 HTTP/1.1\r\nHost: GitHub.COM:443\r\n\r\n")
	if !strings.Contains(status, "200 Connection Established") {
		_ = conn.Close()
		t.Fatalf("expected 200 Connection Established, got %q", status)
	}
	_, _ = io.WriteString(conn, "ping")
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "ping" {
		_ = conn.Close()
		t.Fatalf("tunnel echo failed: %q / %v", reply, err)
	}
	// Wait for idle timeout to close tunnel.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(reply); err == nil {
		t.Fatal("expected tunnel to close on idle timeout")
	}
	_ = conn.Close()

	// 2. Disallowed requests -> all 403.
	denied := []struct {
		name string
		req  string
	}{
		{"disallowed-host", "CONNECT evil.example.com:443 HTTP/1.1\r\nHost: evil.example.com:443\r\n\r\n"},
		{"trailing-dot", "CONNECT github.com.:443 HTTP/1.1\r\nHost: github.com.:443\r\n\r\n"},
		{"ipv4-literal", "CONNECT 140.82.121.4:443 HTTP/1.1\r\nHost: 140.82.121.4:443\r\n\r\n"},
		{"ipv6-literal", "CONNECT [2001:db8::1]:443 HTTP/1.1\r\nHost: [2001:db8::1]:443\r\n\r\n"},
		{"port-80", "CONNECT github.com:80 HTTP/1.1\r\nHost: github.com:80\r\n\r\n"},
		{"missing-port", "CONNECT github.com HTTP/1.1\r\nHost: github.com\r\n\r\n"},
		{"plain-get", "GET http://github.com/secret/token-path HTTP/1.1\r\nHost: github.com\r\n\r\n"},
		{"private-loopback", "CONNECT api.github.com:443 HTTP/1.1\r\nHost: api.github.com:443\r\n\r\n"},
		{"private-rfc1918", "CONNECT codeload.github.com:443 HTTP/1.1\r\nHost: codeload.github.com:443\r\n\r\n"},
		{"private-linklocal", "CONNECT cloud.openvaultdb.com:443 HTTP/1.1\r\nHost: cloud.openvaultdb.com:443\r\n\r\n"},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			st, c := sendRaw(tc.req)
			_ = c.Close()
			if !strings.Contains(st, "403") {
				t.Fatalf("%s: got %q, want 403", tc.name, st)
			}
		})
	}

	// Verify logs never record request paths!
	if strings.Contains(logs.String(), "secret/token-path") {
		t.Fatalf("proxy logs leaked request path: %s", logs.String())
	}

	// 3. Dial failure -> 502.
	dialFail = true
	st, c := sendRaw("CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n")
	_ = c.Close()
	if !strings.Contains(st, "502") {
		t.Fatalf("dial failure got %q, want 502", st)
	}
	dialFail = false

	// 4. Resolve failure -> 502.
	resolveErr = errors.New("dns down")
	st, c = sendRaw("CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n")
	_ = c.Close()
	if !strings.Contains(st, "502") {
		t.Fatalf("resolve failure got %q, want 502", st)
	}
	resolveErr = nil

	// 5. Verify all forbidden IP classifications directly.
	for _, s := range []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.1.1",
		"0.0.0.0", "224.0.0.1", "100.64.0.1", "::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1",
	} {
		if !isForbiddenEgressIP(net.ParseIP(s)) {
			t.Fatalf("expected %s to be forbidden", s)
		}
	}
	if !isForbiddenEgressIP(nil) {
		t.Fatal("nil IP must be forbidden")
	}
	if isForbiddenEgressIP(net.ParseIP("140.82.121.4")) {
		t.Fatal("public IP 140.82.121.4 should be allowed")
	}

	// 6. Non-hijackable ResponseWriter returns 500.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("CONNECT", "https://github.com:443", nil)
	req.Host = "github.com:443"
	edge.ProxyHandler().ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("non-hijackable status = %d, want 500", rec.Code)
	}

	// 7. Edge allow-list ignores invalid entries (trailing dot, IP literal, empty).
	e2 := NewEdge(EdgeConfig{Allow: []string{"", "127.0.0.1", "github.com.", "github.com"}})
	if len(e2.allowSet) != 1 {
		t.Fatalf("allowSet = %v, want only github.com", e2.allowSet)
	}

	// 8. RunEdge starts and shuts down cleanly on cancel, and rejects bad listen addresses.
	runCtx, runCancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- RunEdge(runCtx, EdgeConfig{
			Listen:      "127.0.0.1:0",
			ProxyListen: "127.0.0.1:0",
			Upstream:    "127.0.0.1:1",
			Logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		})
	}()
	time.Sleep(60 * time.Millisecond)
	runCancel()
	if err := <-runDone; err != nil {
		t.Fatalf("RunEdge cancel error: %v", err)
	}
	if err := RunEdge(context.Background(), EdgeConfig{Listen: "127.0.0.1:-1", ProxyListen: "127.0.0.1:0"}); err == nil {
		t.Fatal("expected bad listen error")
	}
	if err := RunEdge(context.Background(), EdgeConfig{Listen: "127.0.0.1:0", ProxyListen: "127.0.0.1:-1"}); err == nil {
		t.Fatal("expected bad proxy-listen error")
	}
	proxyLn2, _ := net.Listen("tcp", "127.0.0.1:0")
	if err := NewEdge(EdgeConfig{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}).Serve(context.Background(), errListener{err: errors.New("fwd boom")}, proxyLn2); err == nil {
		t.Fatal("expected Serve error from failing forwarder listener")
	}
}

func TestCheckHealthProxyLocal(t *testing.T) {
	h := startTestServer(t, func(cfg *Config) { cfg.ProxyProtocol = true; cfg.ProxyProtocolTrust = []string{"192.0.2.8/32"} })
	// A plain probe never sees a banner: the server waits for the PROXY header.
	if err := CheckHealth(context.Background(), h.addr, 300*time.Millisecond); err == nil {
		t.Fatal("plain CheckHealth against a --proxy-protocol server: want timeout error")
	}
	if err := CheckHealthProxyLocal(context.Background(), h.addr, 2*time.Second); err != nil {
		t.Fatalf("CheckHealthProxyLocal: %v", err)
	}
}

func TestEdge_UpstreamHalfCloseReleasesForwardingSlot(t *testing.T) {
	h := startTestServer(t, func(cfg *Config) {
		cfg.ProxyProtocol = true
		cfg.ProxyProtocolTrust = []string{"192.0.2.8/32"}
		cfg.HandshakeTimeout = 500 * time.Millisecond
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	edge := NewEdge(EdgeConfig{
		Upstream: h.addr, MaxConns: 1, MaxConnsPerIP: 1,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- edge.ServeForwarder(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	if banner, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(banner, "SSH-2.0-") {
		t.Fatalf("forwarded banner: %q, %v", banner, err)
	}
	// Leave the client's write half open after the supervisor times out.
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("waiting for upstream EOF: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := CheckHealth(ctx, ln.Addr().String(), time.Second); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("half-open client kept the only forwarding slot")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEdge_ForwardingWriteDeadlineReleasesBlockedCopy(t *testing.T) {
	client, edgeClient := net.Pipe()
	upstream, edgeUpstream := net.Pipe()
	defer client.Close()
	defer upstream.Close()
	defer edgeClient.Close()
	defer edgeUpstream.Close()

	done := make(chan struct{})
	go func() {
		spliceConns(context.Background(), edgeClient, edgeUpstream, 30*time.Millisecond)
		close(done)
	}()
	if _, err := upstream.Write([]byte("the client never reads this")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwarding goroutine still holds a slot")
	}

	writer := connDeadlineWriter{conn: edgeClient, timeout: time.Second}
	if _, err := writer.Write([]byte("closed")); err == nil {
		t.Fatal("expected a write deadline error on the closed connection")
	}
}
