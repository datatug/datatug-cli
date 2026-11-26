//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSocketUIDTables(t *testing.T) {
	root := t.TempDir()
	v4Peer := netip.MustParseAddrPort("127.0.0.1:12345")
	v4Relay := netip.MustParseAddrPort("127.0.0.1:23456")
	v6Peer := netip.MustParseAddrPort("[::1]:12345")
	v6Relay := netip.MustParseAddrPort("[::1]:23456")
	row := func(peer, remote netip.AddrPort, v6 bool, uid, inode string) string {
		return fmt.Sprintf("1: %s %s 01 0:0 0:0 0 %s 0 %s\n",
			procSocketAddress(peer, v6), procSocketAddress(remote, v6), uid, inode)
	}
	writeFixtureFile(t, root, "tcp", "header\n"+
		row(v4Relay, v4Peer, false, "7", "44")+ // server half
		row(v4Peer, v4Relay, false, "8", "0")+ // expired inode
		row(v4Peer, v4Relay, false, "20000", "55"))
	writeFixtureFile(t, root, "tcp6", row(v6Peer, v6Relay, true, "20001", "66")+
		row(v4Peer, v4Relay, true, "20002", "77")+ // mapped IPv4 client
		row(v6Relay, v6Peer, true, "bad-uid", "88"))
	for _, tc := range []struct {
		peer, relay netip.AddrPort
		uid         int
		ok          bool
	}{
		{v4Peer, v4Relay, 20000, true},
		{v6Peer, v6Relay, 20001, true},
		{v6Relay, v6Peer, 0, false},
		{v4Peer, v6Relay, 0, false},
	} {
		uid, ok := socketUIDIn(root, tc.peer, tc.relay)
		if uid != tc.uid || ok != tc.ok {
			t.Errorf("%v -> %v owner = %d, %t", tc.peer, tc.relay, uid, ok)
		}
	}
	if err := os.Remove(filepath.Join(root, "tcp")); err != nil {
		t.Fatal(err)
	}
	if uid, ok := socketUIDIn(root, v4Peer, v4Relay); uid != 20002 || !ok {
		t.Fatalf("mapped IPv4 owner = %d, %t", uid, ok)
	}
}

func TestUIDRelayCapsPendingSockets(t *testing.T) {
	tcp := loopbackListener(t)
	guard := newUIDRelayListener(tcp, 20000, 2, 1)
	type identity struct {
		uid int
		ok  bool
	}
	owners := make(chan identity, 1)
	guard.lookup = func(net.Conn) (int, bool) {
		id := <-owners
		return id.uid, id.ok
	}
	srv := newProxyServer(t.Context(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requireProxyAuth(w, r)
	}))
	done := make(chan error, 1)
	go func() { done <- srv.Serve(guard) }()
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	dial := func(uid int, found bool) net.Conn {
		t.Helper()
		owners <- identity{uid, found}
		peer := proxyDial(t, tcp.Addr().String())
		t.Cleanup(func() { _ = peer.Close() })
		return peer
	}
	first := dial(20000, true)
	waitForCount := func(key string, want int) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			guard.limit.mu.Lock()
			count := guard.limit.perIP[key]
			guard.limit.mu.Unlock()
			if count == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("UID %s never reached %d sockets", key, want)
	}
	waitForCount("20000", 1)
	for _, id := range []identity{{20000, true}, {0, true}, {0, false}} {
		excess := dial(id.uid, id.ok)
		// No request bytes have been sent. The socket is still rejected.
		if _, err := excess.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("rejected socket read = %v", err)
		}
	}
	other := dial(20001, true)
	request := "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\n\r\n"
	if _, err := io.WriteString(other, request); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(other), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("other UID got HTTP %d", resp.StatusCode)
	}
	_ = first.Close()
	waitForCount("20000", 0)
	reopened := dial(20000, true)
	waitForCount("20000", 1)
	_ = reopened.Close()
}

func TestUIDRelayConnectsThroughUnixEdge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edge.sock")
	proxyLn, err := listenEdgeProxy(path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Stat(path)
	if err != nil || dir.Mode().Perm() != 0o666 {
		t.Fatalf("edge socket permissions = %v, %v", dir, err)
	}
	fwdLn := loopbackListener(t)
	edge := NewEdge(EdgeConfig{
		LookupIPAddr: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.IPv4(140, 82, 112, 3)}}, nil
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			server, client := net.Pipe()
			go func() {
				defer func() { _ = server.Close() }()
				_, _ = io.Copy(server, server)
			}()
			return client, nil
		},
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- edge.Serve(ctx, fwdLn, proxyLn) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	cfg := DefaultConfig()
	cfg.EgressProxy = (&url.URL{Scheme: "unix", Path: path}).String()
	relay, err := startSessionRelay(cfg, os.Getuid(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relay.close)
	raw, revoke := relay.register("visitor")
	defer revoke()
	proxyURL, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := proxyURL.User.Password()
	client, r, resp := rawConnect(t, relay, proxyBasic("visitor", token), "test")
	defer func() { _ = client.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	data := make([]byte, 4)
	if _, err := io.ReadFull(r, data); err != nil || string(data) != "test" {
		t.Fatalf("Unix CONNECT echoed %q, %v", data, err)
	}
}

func TestEdgeUnixSocketLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.sock")
	socket := func() *net.UnixListener {
		t.Helper()
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		return ln
	}
	ln := socket()
	defer func() { _ = ln.Close() }()
	if _, err := listenEdgeProxy(path); err == nil || !strings.Contains(err.Error(), "already serves") {
		t.Fatalf("active socket error = %v", err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := listenEdgeProxy(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = fresh.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed edge left its socket: %v", err)
	}
	if err := removeStaleProxySocket(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing socket = %v", err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenEdgeProxy(path); err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatalf("occupied path error = %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatalf("occupied path changed = %q, %v", data, err)
	}
	if _, err := listenEdgeProxy(filepath.Join(path, "missing.sock")); err == nil {
		t.Fatal("invalid parent accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ln = socket()
	ln.SetUnlinkOnClose(false)
	_ = ln.Close()
	tooLong := filepath.Join(filepath.Dir(path), strings.Repeat("p", 108))
	if err := os.Rename(path, tooLong); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleProxySocket(tooLong); err == nil {
		t.Fatal("invalid address accepted")
	}
	chmod := setProxySocketMode
	t.Cleanup(func() { setProxySocketMode = chmod })
	setProxySocketMode = func(string, os.FileMode) error { return errTestIO }
	if _, err := listenEdgeProxy(path); !errors.Is(err, errTestIO) {
		t.Fatalf("chmod error = %v", err)
	}
}

func TestEdgeRefusesTCPConnectListener(t *testing.T) {
	if !filepath.IsAbs(DefaultEdgeConfig().ProxyListen) {
		t.Fatal("default edge CONNECT listener must be a filesystem socket")
	}
	for _, addr := range []string{":3128", "127.0.0.1:0", "[::]:0", "[::1]:0"} {
		t.Run(addr, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			err := RunEdge(ctx, EdgeConfig{Listen: "127.0.0.1:0", ProxyListen: addr})
			requireErrorContains(t, err, "requires an absolute Unix socket path")
		})
	}
}
