//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"crypto"
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

var errTestIO = errors.New("fixture I/O failure")

func requireErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestStatSessionsFilesystem(t *testing.T) {
	if _, err := filesystemSize(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("statfs error = %v", err)
	}
	original := statSessionsFS
	t.Cleanup(func() { statSessionsFS = original })
	statSessionsFS = func(_ string, stats *unix.Statfs_t) error {
		stats.Blocks = ^uint64(0)
		stats.Bsize = 2
		return nil
	}
	if size, err := filesystemSize("fixture"); err != nil || size != ^uint64(0) {
		t.Fatalf("overflow size = %d, error = %v", size, err)
	}
}

func TestConfigRejectsInvalidAdmissionAndCredentialRanges(t *testing.T) {
	for _, tc := range []struct {
		edit func(*Config)
		want string
	}{
		{func(c *Config) { c.MaxConnections = -1 }, "max-connections"},
		{func(c *Config) { c.MaxSessionTunnels = -1 }, "tunnel limits"},
		{func(c *Config) { c.EgressProxyAuthFile = "/unused"; c.UIDBase = edgeProxyGroup }, "proxy credential group"},
	} {
		cfg := DefaultConfig().WithDefaults()
		tc.edit(&cfg)
		requireErrorContains(t, cfg.Validate(), tc.want)
	}
	cfg := DefaultConfig().WithDefaults()
	requireErrorContains(t, cfg.validateSlotUIDs(0, cfg.UIDBase), "includes the supervisor UID")
}

type failingKeyFile struct {
	*os.File
	fail string
}

func (f failingKeyFile) Chmod(mode os.FileMode) error {
	if f.fail == "chmod" {
		return errTestIO
	}
	return f.File.Chmod(mode)
}

func (f failingKeyFile) Write(b []byte) (int, error) {
	if f.fail == "write" {
		return 0, errTestIO
	}
	return f.File.Write(b)
}

func (f failingKeyFile) Sync() error {
	if f.fail == "sync" {
		return errTestIO
	}
	return f.File.Sync()
}

func (f failingKeyFile) Close() error {
	err := f.File.Close()
	if f.fail == "close" {
		return errTestIO
	}
	return err
}

func TestHostKeyTemporaryFileFailures(t *testing.T) {
	original := openHostKeyTemp
	t.Cleanup(func() { openHostKeyTemp = original })
	for _, operation := range []string{"create", "chmod", "write", "sync", "close"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "hostkey")
			openHostKeyTemp = func(dir, pattern string) (hostKeyFile, error) {
				if operation == "create" {
					return nil, errTestIO
				}
				f, err := os.CreateTemp(dir, pattern)
				return failingKeyFile{File: f, fail: operation}, err
			}
			_, err := LoadOrGenerateHostKey(path)
			requireErrorContains(t, err, operation+" temp host key")
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatalf("incomplete key installation: %v, %v", entries, err)
			}
		})
	}
}

func TestHostKeyDirectoryAndCryptoFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.Symlink("loop", filepath.Join(dir, "loop")); err != nil {
		t.Fatal(err)
	}
	_, err := LoadOrGenerateHostKey(filepath.Join(dir, "loop"))
	requireErrorContains(t, err, "stat host key")

	// A dangling symlink reports a missing key to Stat, but cannot become a directory.
	if err := os.Symlink("missing", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	_, err = LoadOrGenerateHostKey(filepath.Join(dir, "dangling", "key"))
	requireErrorContains(t, err, "create host key dir")

	t.Run("generate", func(t *testing.T) {
		original := generateHostKey
		t.Cleanup(func() { generateHostKey = original })
		generateHostKey = func(io.Reader) (ed25519.PublicKey, ed25519.PrivateKey, error) { return nil, nil, errTestIO }
		_, err := LoadOrGenerateHostKey(path)
		requireErrorContains(t, err, "generate ed25519 host key")
	})
	t.Run("marshal", func(t *testing.T) {
		original := marshalHostKey
		t.Cleanup(func() { marshalHostKey = original })
		marshalHostKey = func(crypto.PrivateKey, string) (*pem.Block, error) { return nil, errTestIO }
		_, err := LoadOrGenerateHostKey(path)
		requireErrorContains(t, err, "marshal ed25519 host key")
	})
	t.Run("signer", func(t *testing.T) {
		original := newHostKeySigner
		t.Cleanup(func() { newHostKeySigner = original })
		newHostKeySigner = func(any) (gossh.Signer, error) { return nil, errTestIO }
		_, err := LoadOrGenerateHostKey(path)
		requireErrorContains(t, err, "build host key signer")
	})
	t.Run("read", func(t *testing.T) {
		original := readHostKey
		t.Cleanup(func() { readHostKey = original })
		readHostKey = func(string) ([]byte, error) { return nil, errTestIO }
		_, err := LoadOrGenerateHostKey(path)
		requireErrorContains(t, err, "read host key")
	})
}

func TestHealthProxyPreludeWriteFailure(t *testing.T) {
	client, peer := net.Pipe()
	_ = peer.Close()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if _, ok := ctx.Deadline(); !ok || network != "tcp" || addr != DefaultHealthAddr {
			t.Fatalf("bad dial network/address %q/%q", network, addr)
		}
		return client, nil
	}
	requireErrorContains(t, checkHealthWithDialer(context.Background(), "", 0, EncodeProxyV2(nil, nil), dial), "send PROXY header")
}

type brokenHijacker struct{ *httptest.ResponseRecorder }

func (brokenHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, errTestIO }

type writeErrorConn struct{ net.Conn }

func (writeErrorConn) Write([]byte) (int, error) { return 0, errTestIO }

func TestEdgeForwarderAndHijackFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := NewEdge(EdgeConfig{}).ServeForwarder(ctx, errListener{err: net.ErrClosed}); err != nil {
		t.Fatalf("closed forwarder listener returned %v", err)
	}
	active, stop := context.WithCancel(t.Context())
	defer stop()
	if err := NewEdge(EdgeConfig{}).ServeForwarder(active, errListener{err: net.ErrClosed}); err != nil {
		t.Fatalf("externally closed forwarder listener returned %v", err)
	}
	requireErrorContains(t, RunEdge(t.Context(), EdgeConfig{ProxyAuthIP: "invalid", ProxyAuthFile: "/unused"}), "must be an IP address")

	for _, tc := range []struct {
		name string
		want string
		dial func(net.Conn) (net.Conn, error)
	}{
		{"dial", "edge upstream dial failed", func(net.Conn) (net.Conn, error) { return nil, errTestIO }},
		{"header", "edge write proxy v2 header failed", func(conn net.Conn) (net.Conn, error) { return writeErrorConn{conn}, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			down, downPeer := net.Pipe()
			defer func() { _ = downPeer.Close() }()
			up, upPeer := net.Pipe()
			defer func() { _ = up.Close() }()
			defer func() { _ = upPeer.Close() }()
			var logs syncBuffer
			edge := NewEdge(EdgeConfig{
				Logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
				DialContext: func(context.Context, string, string) (net.Conn, error) { return tc.dial(up) },
			})
			edge.forwardConn(t.Context(), down, true)
			if !strings.Contains(logs.String(), tc.want) {
				t.Fatalf("logs = %s", logs.String())
			}
		})
	}
	w := brokenHijacker{httptest.NewRecorder()}
	establishTunnel(t.Context(), w, httptest.NewRequest(http.MethodConnect, "github.com:443", nil), nil, http.NoBody, time.Second)
	if w.Code != http.StatusInternalServerError || w.Header().Get("Connection") != "close" {
		t.Fatalf("hijack failure HTTP %d, headers = %v", w.Code, w.Header())
	}
}

func failureTestConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.Isolation = IsolationProcess
	cfg.SessionsDir, cfg.StateDir = t.TempDir(), t.TempDir()
	cfg.SkipTemplate = true
	return cfg
}

func TestRelayStartupAndSecretFileFailures(t *testing.T) {
	cfg := failureTestConfig(t)
	cfg.EgressProxy = "socks5://edge.invalid"
	_, err := NewServer(t.Context(), cfg)
	requireErrorContains(t, err, "valid HTTP or HTTPS")

	cfg.EgressProxy = "http://edge.invalid"
	cfg.EgressProxyAuthFile = filepath.Join(t.TempDir(), "missing", "auth")
	_, err = NewServer(t.Context(), cfg)
	requireErrorContains(t, err, "publish edge proxy credential")

	path := filepath.Join(t.TempDir(), "auth")
	noopChown := func(string, int, int) error { return nil }
	// A read-only mount or a failed chown must never publish the credential.
	if err := writeProxyAuthFile(path, "fixture", func(string, int, int) error { return errTestIO }); !errors.Is(err, errTestIO) {
		t.Fatalf("chown failure = %v", err)
	}
	if err := writeProxyAuthFile(path, "fixture", func(name string, _, _ int) error { return os.Remove(name) }); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("chmod failure = %v", err)
	}
	t.Run("write", func(t *testing.T) {
		original := createProxyAuthTemp
		t.Cleanup(func() { createProxyAuthTemp = original })
		createProxyAuthTemp = func(dir, pattern string) (*os.File, error) {
			f, err := os.CreateTemp(dir, pattern)
			if err == nil {
				_ = f.Close()
			}
			return f, err
		}
		if err := writeProxyAuthFile(path, "fixture", noopChown); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("write failure = %v", err)
		}
	})
	t.Run("successful rotation", func(t *testing.T) {
		cfg.EgressProxyAuthFile = path
		want := ""
		for range 2 {
			relay, err := startSessionRelay(cfg, SysOps{Chown: noopChown})
			if err != nil {
				t.Fatal(err)
			}
			(&Server{relay: relay}).closeRelay()
			content, err := os.ReadFile(path)
			if err != nil || string(content) != relay.upAuth || string(content) == want {
				t.Fatal("credential failed to rotate")
			}
			stat, err := os.Stat(path)
			if err != nil || stat.Mode().Perm() != 0o640 {
				t.Fatalf("credential permissions = %v, error = %v", stat, err)
			}
			want = string(content)
		}
	})
	t.Run("listen", func(t *testing.T) {
		original := listenTCP
		t.Cleanup(func() { listenTCP = original })
		listenTCP = func(string, string) (net.Listener, error) { return nil, errTestIO }
		cfg.EgressProxyAuthFile = ""
		_, err := NewServer(t.Context(), cfg)
		requireErrorContains(t, err, "listen on loopback")
	})
}

func TestRelayRejectsNonConnectAndHandlesUpstreamFailures(t *testing.T) {
	cfg := failureTestConfig(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	cfg.EgressProxy = up.URL
	relay, err := startSessionRelay(cfg, DefaultSysOps())
	if err != nil {
		t.Fatal(err)
	}
	defer (&Server{relay: relay}).closeRelay()
	defer up.Close()
	v, revoke := relay.register("test")
	defer revoke()
	p, err := url.Parse(v)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := p.User.Password()
	auth := proxyBasic("test", token)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Proxy-Authorization", auth)
	w := httptest.NewRecorder()
	relay.handleConnect(w, req)
	if w.Code != http.StatusForbidden || w.Header().Get("Connection") != "close" {
		t.Fatalf("non-CONNECT HTTP %d", w.Code)
	}
	conn, _, resp := rawConnect(t, relay, auth, "")
	_ = conn.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("edge refusal HTTP %d", resp.StatusCode)
	}

	for _, scheme := range []string{"http", "https"} {
		u, err := url.Parse(scheme + "://127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		r := &sessionRelay{upstream: u}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if conn, _, err := r.connectUpstream(ctx, "github.com:443"); err == nil || conn != nil {
			t.Fatalf("canceled %s dial = %v, %v", scheme, conn, err)
		}
	}
}

func TestRevocationClosesPendingUpstreamHandshake(t *testing.T) {
	edge := loopbackListener(t)
	ready := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		up, err := edge.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer func() { _ = up.Close() }()
		_ = up.SetDeadline(time.Now().Add(2 * time.Second))
		req, err := http.ReadRequest(bufio.NewReader(up))
		if err != nil || req.Method != http.MethodConnect {
			closed <- fmt.Errorf("edge request %v, %w", req, err)
			return
		}
		close(ready)
		_, err = up.Read(make([]byte, 1))
		closed <- err
	}()
	cfg := failureTestConfig(t)
	cfg.EgressProxy = "http://" + edge.Addr().String()
	relay, err := startSessionRelay(cfg, DefaultSysOps())
	if err != nil {
		t.Fatal(err)
	}
	defer (&Server{relay: relay}).closeRelay()
	v, revoke := relay.register("pending")
	defer revoke()
	p, err := url.Parse(v)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := p.User.Password()
	client := proxyDial(t, relay.address)
	_, _ = fmt.Fprintf(client, "CONNECT github.com:443 HTTP/1.1\r\nHost: github.com:443\r\nProxy-Authorization: %s\r\n\r\n", proxyBasic("pending", token))
	select {
	case <-ready:
	case err := <-closed:
		t.Fatalf("no upstream request: %v", err)
	case <-time.After(time.Second):
		t.Fatal("upstream CONNECT was not forwarded")
	}
	revoke()
	if err := <-closed; !errors.Is(err, io.EOF) {
		t.Fatalf("revoked edge socket read = %v", err)
	}
}

func TestSupervisorSetupAndShutdownFailures(t *testing.T) {
	cfg := failureTestConfig(t)
	t.Run("executable", func(t *testing.T) {
		original := resolveExecutable
		t.Cleanup(func() { resolveExecutable = original })
		resolveExecutable = func() (string, error) { return "", errTestIO }
		_, err := NewServer(t.Context(), cfg)
		requireErrorContains(t, err, "resolve executable")
	})
	t.Run("wish", func(t *testing.T) {
		original := newWishServer
		t.Cleanup(func() { newWishServer = original })
		newWishServer = func(...ssh.Option) (*ssh.Server, error) { return nil, errTestIO }
		_, err := NewServer(t.Context(), cfg)
		requireErrorContains(t, err, "create wish server")
	})
	t.Run("fatal accept", func(t *testing.T) {
		original := listenTCP
		t.Cleanup(func() { listenTCP = original })
		listenTCP = func(string, string) (net.Listener, error) { return errListener{err: errTestIO}, nil }
		if err := RunServer(t.Context(), cfg); !errors.Is(err, errTestIO) {
			t.Fatalf("RunServer = %v", err)
		}
	})
	t.Run("forwarding is disabled", func(t *testing.T) {
		s, err := NewServer(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if s.sshServer.LocalPortForwardingCallback(nil, "localhost", 80) || s.sshServer.ReversePortForwardingCallback(nil, "localhost", 80) {
			t.Fatal("supervisor enabled SSH forwarding")
		}
	})
	t.Run("shutdown deadline", func(t *testing.T) {
		s, err := NewServer(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		// A listener accepted by Wish remains active while its Accept
		// call is deliberately held. Shutdown must honor context cancellation.
		ln := &heldListener{accepted: make(chan struct{}), done: make(chan struct{})}
		go func() { _ = s.Serve(ln) }()
		<-ln.accepted
		defer close(ln.done)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unresponsive shutdown", func(t *testing.T) {
		if err := waitForShutdown(make(chan error), time.Millisecond); err != nil {
			t.Fatal(err)
		}
	})
}

type heldListener struct {
	accepted, done chan struct{}
}

func (l *heldListener) Accept() (net.Conn, error) {
	close(l.accepted)
	<-l.done
	return nil, net.ErrClosed
}

func (*heldListener) Close() error   { return nil }
func (*heldListener) Addr() net.Addr { return &net.TCPAddr{} }
