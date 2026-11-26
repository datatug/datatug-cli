//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEdgeTunnelLimits(t *testing.T) {
	for _, cfg := range []EdgeConfig{DefaultEdgeConfig().WithDefaults(), (EdgeConfig{}).WithDefaults()} {
		if cfg.MaxTunnels != 64 || cfg.MaxTunnelsPerIP != 64 {
			t.Fatalf("tunnel defaults are %d global, %d per IP", cfg.MaxTunnels, cfg.MaxTunnelsPerIP)
		}
	}
	waiting, release := make(chan struct{}, 2), make(chan struct{}, 2)
	edge := NewEdge(EdgeConfig{
		MaxTunnels: 2, MaxTunnelsPerIP: 1,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		LookupIPAddr: func(ctx context.Context, _ string) ([]net.IPAddr, error) {
			waiting <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, errors.New("fixture returns no addresses")
		},
	})
	do := func(peer string) int {
		req := httptest.NewRequest(http.MethodConnect, "https://github.com:443", nil)
		req.Host = "github.com:443"
		req.RemoteAddr = peer
		rec := httptest.NewRecorder()
		edge.ProxyHandler().ServeHTTP(rec, req)
		return rec.Code
	}
	open := func(peer string) <-chan int {
		out := make(chan int, 1)
		go func() { out <- do(peer) }()
		select {
		case <-waiting:
		case status := <-out:
			t.Fatalf("request returned %d before starting DNS", status)
		case <-time.After(time.Second):
			t.Fatal("request never reached DNS")
		}
		return out
	}
	first := open("192.0.2.1:5000")
	if got := do("192.0.2.1:5001"); got != http.StatusServiceUnavailable {
		t.Fatalf("per-IP limit returned HTTP %d", got)
	}
	second := open("192.0.2.2:5000")
	if got := do("192.0.2.3:5000"); got != http.StatusServiceUnavailable {
		t.Fatalf("global limit returned HTTP %d", got)
	}

	release <- struct{}{}
	release <- struct{}{}
	if a, b := <-first, <-second; a != http.StatusBadGateway || b != http.StatusBadGateway {
		t.Fatalf("DNS failure responses = %d, %d", a, b)
	}
	third := open("192.0.2.1:5002")
	release <- struct{}{}
	if got := <-third; got != http.StatusBadGateway {
		t.Fatalf("reopened slot returned HTTP %d", got)
	}
}

func TestEdgeLimitKeepsRemainingConnectionsForAnIP(t *testing.T) {
	limit := newConnectionLimit(4, 2)
	const ip = "192.0.2.1"
	if !limit.acquire(ip) || !limit.acquire(ip) {
		t.Fatal("two connections should fit")
	}
	limit.release(ip)
	if !limit.acquire(ip) || limit.acquire(ip) {
		t.Fatal("releasing one connection must free exactly one place")
	}
}

type closedClientHijacker struct {
	http.ResponseWriter
	client net.Conn
}

func (h closedClientHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.client, nil, nil
}

func TestEdgeClosedClientReleasesTunnel(t *testing.T) {
	upstream, target := net.Pipe()
	defer target.Close()
	client, peer := net.Pipe()
	defer client.Close()
	_ = peer.Close()
	edge := NewEdge(EdgeConfig{
		MaxTunnels: 1,
		LookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.8")}}, nil
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return upstream, nil
		},
	})
	req := httptest.NewRequest(http.MethodConnect, "https://github.com:443", nil)
	req.Host = "github.com:443"
	edge.ProxyHandler().ServeHTTP(closedClientHijacker{ResponseWriter: httptest.NewRecorder(), client: client}, req)
	if _, err := target.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("upstream remained open: %v", err)
	}
	if !edge.tunnelLimit.acquire("192.0.2.1") {
		t.Fatal("tunnel slot was not released")
	}
}

func TestEdgeConnectDialFallbackAndIdle(t *testing.T) {
	dialed := make(chan string, 2)
	upstream, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	fwd, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = fwd.Close()
		t.Fatal(err)
	}
	const dialTimeout = time.Second
	edge := NewEdge(EdgeConfig{
		DialTimeout: dialTimeout, IdleTimeout: 60 * time.Millisecond,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		LookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("2001:db8::1")}, {IP: net.ParseIP("192.0.2.8")}}, nil
		},
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			before, ok := ctx.Deadline()
			if !ok || time.Until(before) <= 0 || time.Until(before) > dialTimeout {
				t.Error("dial attempt did not get its own timeout")
			}
			dialed <- addr
			if addr == "[2001:db8::1]:443" {
				return nil, errors.New("first address unavailable")
			}
			return upstream, nil
		},
	})
	done := make(chan error, 1)
	go func() { done <- edge.Serve(ctx, fwd, proxy) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	dialProxy := func() net.Conn {
		t.Helper()
		conn, err := net.Dial("tcp", proxy.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	large := dialProxy()
	_, _ = io.WriteString(large, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\nX-Oversized: "+strings.Repeat("a", 16<<10)+"\r\n\r\n")
	if line, err := bufio.NewReader(large).ReadString('\n'); err != nil || !strings.Contains(line, "431") {
		t.Fatalf("oversized header returned %q, %v", line, err)
	}

	client := dialProxy()
	_, _ = io.WriteString(client, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n")
	reader := bufio.NewReader(client)
	if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, "200") {
		t.Fatalf("CONNECT returned %q, %v", line, err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("idle tunnel did not close: %v", err)
	}
	if first, second := <-dialed, <-dialed; first != "[2001:db8::1]:443" || second != "192.0.2.8:443" {
		t.Fatalf("dial order = %q, %q", first, second)
	}
}

func TestEdgeLoopbackHealthBypassesPublicLimit(t *testing.T) {
	supervisor := startTestServer(t, func(c *Config) { c.ProxyProtocol = true; c.ProxyProtocolTrust = []string{"192.0.2.8/32"} })
	edge := NewEdge(EdgeConfig{Upstream: supervisor.addr, MaxConns: 1, MaxConnsPerIP: 1})
	if !edge.forwardLimit.acquire("192.0.2.1") {
		t.Fatal("could not reserve the public pool")
	}
	defer edge.forwardLimit.release("192.0.2.1")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- edge.ServeForwarder(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if err := CheckHealth(ctx, ln.Addr().String(), time.Second); err != nil {
		t.Fatalf("local health probe at public capacity: %v", err)
	}
}

func TestEdgeLogsSanitizedHost(t *testing.T) {
	logs := &syncBuffer{}
	edge := NewEdge(EdgeConfig{Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	req := httptest.NewRequest(http.MethodGet, "https://github.com/private", nil)
	req.Host = "site\n\u009b\u007f" + strings.Repeat("é", 128) + ":443"
	edge.ProxyHandler().ServeHTTP(httptest.NewRecorder(), req)
	var got map[string]any
	if err := json.Unmarshal([]byte(logs.String()), &got); err != nil || got["host"] != "site���"+strings.Repeat("é", 121) {
		t.Fatalf("logged host = %v, error = %v", got["host"], err)
	}
}
