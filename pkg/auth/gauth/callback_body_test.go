package gauth

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type independentPipeListener struct {
	conn   net.Conn
	used   bool
	closed chan struct{}
	once   sync.Once
}

func (l *independentPipeListener) Accept() (net.Conn, error) {
	if !l.used {
		l.used = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *independentPipeListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }
func (l *independentPipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}
}

type independentTrackedConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *independentTrackedConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 42000}
}
func (c *independentTrackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

// Real net/http server over an in-memory pipe: no socket, browser or cloud I/O.
func TestGoogleCallbackIncompleteBodyCleanup(t *testing.T) {
	prodServe, prodShutdown := authServerServe, srvShutdown
	_, _ = callbackFixture(t)
	server, client := net.Pipe()
	tracked := &independentTrackedConn{Conn: server, closed: make(chan struct{})}
	listener := &independentPipeListener{conn: tracked, closed: make(chan struct{})}
	defer func() { _ = client.Close() }()
	defer func() { _ = tracked.Close() }()
	defer func() { _ = listener.Close() }()
	authListen = func(string, string) (net.Listener, error) { return listener, nil }
	handled := make(chan struct{})
	var handlerOnce sync.Once
	authServerServe = func(s *http.Server, l net.Listener) error {
		original := s.Handler
		s.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			original.ServeHTTP(w, r)
			handlerOnce.Do(func() { close(handled) })
		})
		return prodServe(s, l)
	}
	srvShutdown = prodShutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	openBrowser = func(context.Context, string) error {
		// Invalid state is sufficient to trigger normal net/http body cleanup.
		if _, err := io.WriteString(client, "GET /oauth2callback?state=invalid&code=fixture HTTP/1.1\r\nHost: localhost:8080\r\nContent-Length: 1\r\n\r\n"); err != nil {
			return err
		}
		go func() { _, _ = io.Copy(io.Discard, client) }()
		<-handled
		cancel()
		return nil
	}
	_, err := getTokenFromWeb(ctx, &oauth2.Config{RedirectURL: "http://localhost:8080/oauth2callback"})
	if err == nil {
		t.Fatal("cancel accepted")
	}
	select {
	case <-tracked.closed:
	default:
		t.Error("flow returned after bounded shutdown but accepted incomplete-body connection remains open")
	}
	_ = client.Close()
	select {
	case <-tracked.closed:
	case <-time.After(time.Second):
		t.Fatal("probe cleanup failed")
	}
}
