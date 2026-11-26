//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUIDRelayBoundsUnidentifiedSockets(t *testing.T) {
	ln := loopbackListener(t)
	guard := newUIDRelayListener(ln, os.Getuid(), 1, 1)
	started := make(chan struct{}, maxUnidentifiedRelaySockets)
	unblock := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(unblock) })
	guard.lookup = func(net.Conn) (int, bool) {
		started <- struct{}{}
		<-unblock
		return os.Getuid(), true
	}
	done := make(chan error, 1)
	go func() {
		conn, err := guard.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	defer func() { _ = guard.Close() }()
	for range maxUnidentifiedRelaySockets {
		conn := proxyDial(t, ln.Addr().String())
		defer func() { _ = conn.Close() }()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("lookup never started")
		}
	}
	excess := proxyDial(t, ln.Addr().String())
	defer func() { _ = excess.Close() }()
	if _, err := excess.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("excess connection read = %v", err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v", err)
	}
	release.Do(func() { close(unblock) })
	deadline := time.Now().Add(time.Second)
	for len(guard.unidentified) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("lookup reservations survived Close")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUIDRelayReportsListenerErrors(t *testing.T) {
	guard := newUIDRelayListener(errListener{err: errTestIO}, 0, 1, 1)
	if conn, err := guard.Accept(); conn != nil || !errors.Is(err, errTestIO) {
		t.Fatalf("Accept = %v, %v", conn, err)
	}
	_ = guard.Close()
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
	path := filepath.Join(privateSocketDir(t), "edge.sock")
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
	path := filepath.Join(privateSocketDir(t), "proxy.sock")
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
	if _, err := listenEdgeProxy(tooLong); err == nil {
		t.Fatal("edge bound an overlong socket path")
	}
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

func privateSocketDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestEdgeRejectsUnprotectedSocketDirectories(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "symlink")
	if err := os.Symlink(privateSocketDir(t), link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{public, link} {
		path := filepath.Join(dir, "proxy.sock")
		_, err := listenEdgeProxy(path)
		requireErrorContains(t, err, "must be a directory with no permissions for others")
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("edge created an unprotected socket: %v", err)
		}
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
