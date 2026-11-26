//go:build linux

package sshdemo

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const proxyRequestTimeout = 10 * time.Second

func newProxyServer(ctx context.Context, handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: proxyRequestTimeout,
		ReadTimeout:       proxyRequestTimeout,
		WriteTimeout:      proxyRequestTimeout,
		MaxHeaderBytes:    8 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

// rejectProxy closes without letting net/http consume an unread request body.
func rejectProxy(w http.ResponseWriter, r *http.Request, message string, status int) {
	w.Header().Set("Connection", "close")
	http.Error(w, message, status)
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		controller := http.NewResponseController(w)
		if controller.SetReadDeadline(time.Now()) == nil {
			_ = controller.Flush()
			// The response is on the wire. Aborting skips finishRequest's body drain.
			panic(http.ErrAbortHandler)
		}
	}
}

func requireProxyAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Proxy-Authenticate", `Basic realm="DataTug SSH demo"`)
	rejectProxy(w, r, "proxy authentication required", http.StatusProxyAuthRequired)
}

// cappedListener reserves a socket slot before accepting, including for
// connections that have yet to send headers and ones hijacked for CONNECT.
type cappedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

func capListener(ln net.Listener, maxConns int) *cappedListener {
	return &cappedListener{Listener: ln, slots: make(chan struct{}, maxConns), done: make(chan struct{})}
}

func (l *cappedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &cappedConn{Conn: conn, release: func() { <-l.slots }}, nil
}

func (l *cappedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type cappedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *cappedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func (c *cappedConn) CloseWrite() error {
	closeWrite(c.Conn)
	return nil
}

func establishTunnel(ctx context.Context, w http.ResponseWriter, r *http.Request, up net.Conn, upstreamBuffer io.Reader, idle time.Duration) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		rejectProxy(w, r, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hj.Hijack()
	if err != nil {
		rejectProxy(w, r, "hijacking failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = client.Close() }()

	clientConn := newIdleConn(client, idle)
	upConn := newIdleConn(up, idle)
	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	// Both HTTP parsers may have read ahead into the tunnel.
	_, clientErr := io.CopyN(upConn, buffered.Reader, int64(buffered.Reader.Buffered()))
	_, upstreamErr := io.Copy(clientConn, upstreamBuffer)
	if clientErr == nil && upstreamErr == nil {
		spliceConns(ctx, clientConn, upConn, 0)
	}
}
