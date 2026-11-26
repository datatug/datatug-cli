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
	listener net.Listener
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

func startSessionRelay(cfg Config, uidBase, uidCount int) (*sessionRelay, error) {
	upstream, err := url.Parse(cfg.EgressProxy)
	if err != nil || !validRelayUpstream(upstream) {
		return nil, errors.New("sshdemo: --egress-proxy requires a unix, HTTP or HTTPS proxy URL")
	}
	var auth string
	if upstream.User != nil {
		password, _ := upstream.User.Password()
		auth = proxyBasic(upstream.User.Username(), password)
	}
	listener, err := listenTCP("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sshdemo: listen on loopback for session egress: %w", err)
	}
	var admitted net.Listener
	if cfg.Isolation == IsolationUID {
		admitted = newUIDRelayListener(listener, uidBase, uidCount, cfg.MaxRelaySocketsPerUID)
	} else {
		admitted = capListener(listener, DefaultProxyMaxConns)
	}
	ctx, cancel := context.WithCancel(context.Background())
	relay := &sessionRelay{
		upstream: upstream, upAuth: auth, address: listener.Addr().String(),
		limit:    newConnectionLimit(cfg.MaxEgressTunnels, cfg.MaxSessionTunnels),
		sessions: make(map[string]relaySession), ctx: ctx, cancel: cancel,
		listener: admitted,
	}
	relay.server = newProxyServer(ctx, http.HandlerFunc(relay.handleConnect))
	go func() { _ = relay.server.Serve(relay.listener) }()
	return relay, nil
}

func validRelayUpstream(u *url.URL) bool {
	if u.Scheme == "unix" {
		return u.Host == "" && filepath.IsAbs(u.Path) && u.RawQuery == "" && u.Fragment == ""
	}
	return u.Hostname() != "" && (u.Scheme == "http" || u.Scheme == "https")
}

func (s *Server) closeRelay() {
	if s.relay != nil {
		s.relay.close()
	}
}

func (r *sessionRelay) close() {
	r.cancel()
	_ = r.listener.Close()
	_ = r.server.Close()
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
	up, err := r.dialUpstream(dialCtx)
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

func (r *sessionRelay) dialUpstream(ctx context.Context) (net.Conn, error) {
	var dialer net.Dialer
	if r.upstream.Scheme == "unix" {
		return dialer.DialContext(ctx, "unix", r.upstream.Path)
	}

	port := r.upstream.Port()
	if r.upstream.Scheme == "https" {
		if port == "" {
			port = "443"
		}
		return (&tls.Dialer{NetDialer: &dialer}).DialContext(ctx, "tcp", net.JoinHostPort(r.upstream.Hostname(), port))
	}
	if port == "" {
		port = "80"
	}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(r.upstream.Hostname(), port))
}
