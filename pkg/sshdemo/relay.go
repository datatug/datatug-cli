//go:build linux

package sshdemo

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type sessionRelay struct {
	upstream *url.URL
	upAuth   string
	address  string
	limit    connectionLimit
	mu       sync.RWMutex
	sessions map[string]relaySession
	ctx      context.Context
	cancel   context.CancelFunc
	server   *http.Server
	listener *cappedListener
}

type relaySession struct {
	tokenHash [sha256.Size]byte
	ctx       context.Context
}

func randomProxyToken() string {
	var token [32]byte
	_, _ = rand.Read(token[:])
	return base64.RawURLEncoding.EncodeToString(token[:])
}

func proxyBasic(user, token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))
}

func startSessionRelay(cfg Config, ops SysOps) (*sessionRelay, error) {
	upstream, err := url.Parse(cfg.EgressProxy)
	if err != nil || upstream.Hostname() == "" || (upstream.Scheme != "http" && upstream.Scheme != "https") {
		return nil, errors.New("sshdemo: --egress-proxy requires a valid HTTP or HTTPS proxy URL")
	}
	var auth string
	if upstream.User != nil {
		password, _ := upstream.User.Password()
		auth = proxyBasic(upstream.User.Username(), password)
	}
	if cfg.EgressProxyAuthFile != "" {
		auth = proxyBasic("supervisor", randomProxyToken())
		if err := writeProxyAuthFile(cfg.EgressProxyAuthFile, auth, ops.Chown); err != nil {
			return nil, fmt.Errorf("sshdemo: publish edge proxy credential: %w", err)
		}
	}
	listener, err := listenTCP("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sshdemo: listen on loopback for session egress: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	relay := &sessionRelay{
		upstream: upstream, upAuth: auth, address: listener.Addr().String(),
		limit:    newConnectionLimit(cfg.MaxEgressTunnels, cfg.MaxSessionTunnels),
		sessions: make(map[string]relaySession), ctx: ctx, cancel: cancel,
		listener: capListener(listener, DefaultProxyMaxConns),
	}
	relay.server = newProxyServer(ctx, http.HandlerFunc(relay.handleConnect))
	go func() { _ = relay.server.Serve(relay.listener) }()
	return relay, nil
}

func (s *Server) closeRelay() {
	if s.relay != nil {
		s.relay.cancel()
		_ = s.relay.listener.Close()
		_ = s.relay.server.Close()
	}
}

func (r *sessionRelay) register(id string) (proxy string, revoke func()) {
	token := randomProxyToken()
	ctx, cancel := context.WithCancel(r.ctx)
	r.mu.Lock()
	r.sessions[id] = relaySession{tokenHash: sha256.Sum256([]byte(token)), ctx: ctx}
	r.mu.Unlock()
	u := url.URL{Scheme: "http", User: url.UserPassword(id, token), Host: r.address}
	return u.String(), func() {
		r.mu.Lock()
		delete(r.sessions, id)
		r.mu.Unlock()
		cancel()
	}
}

func (r *sessionRelay) handleConnect(w http.ResponseWriter, req *http.Request) {
	auth := req.Header.Get("Proxy-Authorization")
	basic := new(http.Request)
	basic.Header = http.Header{"Authorization": {auth}}
	id, token, _ := basic.BasicAuth()
	tokenHash := sha256.Sum256([]byte(token))
	r.mu.RLock()
	session, ok := r.sessions[id]
	r.mu.RUnlock()
	if subtle.ConstantTimeCompare(tokenHash[:], session.tokenHash[:]) != 1 || !ok {
		requireProxyAuth(w, req)
		return
	}
	if req.Method != http.MethodConnect || req.ContentLength != 0 || len(req.TransferEncoding) != 0 {
		rejectProxy(w, req, "only bodyless CONNECT is allowed", http.StatusForbidden)
		return
	}
	if !r.limit.acquire(id) {
		rejectProxy(w, req, "proxy busy", http.StatusServiceUnavailable)
		return
	}
	defer r.limit.release(id)

	ctx, cancel := context.WithCancel(session.ctx)
	defer cancel()
	stop := context.AfterFunc(req.Context(), cancel)
	defer stop()
	up, reader, err := r.connectUpstream(ctx, req.Host)
	if err != nil {
		rejectProxy(w, req, "bad gateway", http.StatusBadGateway)
		return
	}
	defer func() { _ = up.Close() }()
	establishTunnel(ctx, w, req, up, io.LimitReader(reader, int64(reader.Buffered())), DefaultEdgeIdleTimeout)
}

func (r *sessionRelay) connectUpstream(ctx context.Context, authority string) (net.Conn, *bufio.Reader, error) {
	dialCtx, cancel := context.WithTimeout(ctx, proxyRequestTimeout)
	defer cancel()
	port := r.upstream.Port()
	if port == "" {
		port = "80"
		if r.upstream.Scheme == "https" {
			port = "443"
		}
	}
	addr := net.JoinHostPort(r.upstream.Hostname(), port)
	var dialer interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	} = &net.Dialer{}
	if r.upstream.Scheme == "https" {
		dialer = &tls.Dialer{}
	}
	up, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = up.Close() })
	// Stop cancellation only after the upstream exchange is done. Tunnel
	// cancellation is then handled by establishTunnel.
	defer stop()
	_ = up.SetDeadline(time.Now().Add(proxyRequestTimeout))
	req := &http.Request{Method: http.MethodConnect, Host: authority, URL: &url.URL{}, Header: make(http.Header)}
	if r.upAuth != "" {
		req.Header.Set("Proxy-Authorization", r.upAuth)
	}
	err = req.Write(up)
	reader := bufio.NewReader(io.LimitReader(up, 8<<10))
	if err == nil {
		var resp *http.Response
		resp, err = http.ReadResponse(reader, req)
		if err == nil && resp.StatusCode == http.StatusOK {
			return up, reader, nil
		}
	}
	_ = up.Close()
	return nil, nil, errors.New("sshdemo: edge CONNECT failed")
}

var createProxyAuthTemp = os.CreateTemp

// writeProxyAuthFile publishes the header atomically even across a supervisor
// restart. The edge mounts the directory read-only and reads as GID 10001.
func writeProxyAuthFile(path, auth string, chown func(string, int, int) error) error {
	f, err := createProxyAuthTemp(filepath.Dir(path), ".proxy-auth-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, writeErr := io.WriteString(f, auth)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	if err := chown(f.Name(), -1, edgeProxyGroup); err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), 0o640); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
