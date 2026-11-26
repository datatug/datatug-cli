//go:build linux

package sshdemo

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"
)

// proxyV2Signature is the 12-byte PROXY protocol v2 signature from HAProxy spec §2.2.
var proxyV2Signature = [12]byte{
	0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A,
}

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// EncodeProxyV2 builds a PROXY protocol v2 binary header carrying src and dst
// TCP addresses. If either address is not a TCP address, it encodes a valid
// LOCAL/UNSPEC v2 header.
func EncodeProxyV2(src, dst net.Addr) []byte {
	srcTCP, okSrc := src.(*net.TCPAddr)
	dstTCP, okDst := dst.(*net.TCPAddr)
	if !okSrc || !okDst || srcTCP.IP == nil || dstTCP.IP == nil {
		hdr := make([]byte, 16)
		copy(hdr[:12], proxyV2Signature[:])
		hdr[12] = 0x20 // v2, LOCAL
		hdr[13] = 0x00 // UNSPEC
		return hdr
	}

	if src4, dst4 := srcTCP.IP.To4(), dstTCP.IP.To4(); src4 != nil && dst4 != nil {
		hdr := make([]byte, 16+12)
		copy(hdr[:12], proxyV2Signature[:])
		hdr[12] = 0x21 // v2, PROXY
		hdr[13] = 0x11 // AF_INET | STREAM
		binary.BigEndian.PutUint16(hdr[14:16], 12)
		copy(hdr[16:20], src4)
		copy(hdr[20:24], dst4)
		binary.BigEndian.PutUint16(hdr[24:26], uint16(srcTCP.Port))
		binary.BigEndian.PutUint16(hdr[26:28], uint16(dstTCP.Port))
		return hdr
	}

	src16 := srcTCP.IP.To16()
	dst16 := dstTCP.IP.To16()
	hdr := make([]byte, 16+36)
	copy(hdr[:12], proxyV2Signature[:])
	hdr[12] = 0x21 // v2, PROXY
	hdr[13] = 0x21 // AF_INET6 | STREAM
	binary.BigEndian.PutUint16(hdr[14:16], 36)
	copy(hdr[16:32], src16)
	copy(hdr[32:48], dst16)
	binary.BigEndian.PutUint16(hdr[48:50], uint16(srcTCP.Port))
	binary.BigEndian.PutUint16(hdr[50:52], uint16(dstTCP.Port))
	return hdr
}

// Edge runs the TCP PROXY-v2 SSH forwarder and the allow-listing HTTP CONNECT proxy.
type Edge struct {
	cfg      EdgeConfig
	logger   *slog.Logger
	allowSet map[string]struct{}

	forwardLimit connectionLimit
	healthLimit  connectionLimit
	tunnelLimit  connectionLimit
}

// NewEdge constructs an Edge instance from cfg.
func NewEdge(cfg EdgeConfig) *Edge {
	cfg = cfg.WithDefaults()
	allowSet := make(map[string]struct{}, len(cfg.Allow))
	for _, raw := range cfg.Allow {
		h := strings.ToLower(strings.TrimSpace(raw))
		if h == "" || strings.HasSuffix(h, ".") || net.ParseIP(h) != nil {
			continue
		}
		allowSet[h] = struct{}{}
	}
	return &Edge{
		cfg:          cfg,
		logger:       cfg.Logger,
		allowSet:     allowSet,
		forwardLimit: newConnectionLimit(cfg.MaxConns, cfg.MaxConnsPerIP),
		healthLimit:  newConnectionLimit(cfg.MaxConnsPerIP, cfg.MaxConnsPerIP),
		tunnelLimit:  newConnectionLimit(cfg.MaxTunnels, cfg.MaxTunnelsPerIP),
	}
}

type connectionLimit struct {
	mu                 sync.Mutex
	maxTotal, maxPerIP int
	total              int
	perIP              map[string]int
}

func newConnectionLimit(maxTotal, maxPerIP int) connectionLimit {
	return connectionLimit{maxTotal: maxTotal, maxPerIP: maxPerIP, perIP: make(map[string]int)}
}

func (l *connectionLimit) acquire(ip string) bool {
	return l.reserve(ip) == nil
}

func (l *connectionLimit) reserve(ip string) error {
	return l.reserveForIP(ip, l.maxPerIP)
}

func (l *connectionLimit) reserveForIP(ip string, maxPerIP int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perIP[ip] >= maxPerIP {
		return ErrPerIPLimit
	}
	if l.total >= l.maxTotal {
		return ErrGlobalLimit
	}
	l.total++
	l.perIP[ip]++
	return nil
}

func (l *connectionLimit) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.total--
	if n := l.perIP[ip]; n == 1 {
		delete(l.perIP, ip)
	} else {
		l.perIP[ip] = n - 1
	}
}

// ServeForwarder accepts TCP connections on ln, dials e.cfg.Upstream, writes a
// PROXY protocol v2 header, and splices both ways. Loopback uses a reserved
// pool and a LOCAL header so health checks work at public capacity.
func (e *Edge) ServeForwarder(ctx context.Context, ln net.Listener) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		clientConn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		ip := clientIPFromAddr(clientConn.RemoteAddr())
		loopback := net.ParseIP(ip).IsLoopback()
		limit := &e.forwardLimit
		if loopback {
			limit = &e.healthLimit
		}
		if !limit.acquire(ip) {
			e.logger.Warn("edge forwarder cap reached", "client_ip", safeLogValue(ip))
			_ = clientConn.Close()
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer limit.release(ip)
			defer func() { _ = clientConn.Close() }()
			e.forwardConn(ctx, clientConn, loopback)
		}()
	}
}

func (e *Edge) forwardConn(ctx context.Context, clientConn net.Conn, loopback bool) {
	dialCtx, cancel := context.WithTimeout(ctx, e.cfg.DialTimeout)
	upConn, err := e.cfg.DialContext(dialCtx, "tcp", e.cfg.Upstream)
	cancel()
	if err != nil {
		e.logger.Warn("edge upstream dial failed", "upstream", e.cfg.Upstream, "err", err)
		return
	}
	defer func() { _ = upConn.Close() }()

	hdr := EncodeProxyV2(clientConn.RemoteAddr(), clientConn.LocalAddr())
	if loopback {
		hdr = EncodeProxyV2(nil, nil)
	}
	_ = upConn.SetWriteDeadline(time.Now().Add(e.cfg.DialTimeout))
	if _, err := upConn.Write(hdr); err != nil {
		e.logger.Warn("edge write proxy v2 header failed", "err", err)
		return
	}
	_ = upConn.SetWriteDeadline(time.Time{})

	spliceConns(ctx, clientConn, upConn, 5*time.Second)
}

// ProxyHandler returns the http.Handler implementing the allow-listing CONNECT proxy.
func (e *Edge) ProxyHandler() http.Handler {
	return http.HandlerFunc(e.handleProxy)
}

func (e *Edge) handleProxy(w http.ResponseWriter, r *http.Request) {
	// Never log request paths or query strings; log only host + result.
	logHost := safeLogValue(hostOnly(r.Host))
	logResult := func(result string) {
		e.logger.Info("egress proxy request", "host", logHost, "result", result)
	}
	if r.Method != http.MethodConnect || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		logResult("denied-method")
		rejectProxy(w, r, "method not allowed", http.StatusForbidden)
		return
	}

	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		logResult("denied-port")
		rejectProxy(w, r, "forbidden", http.StatusForbidden)
		return
	}

	if host == "" || strings.HasSuffix(host, ".") || net.ParseIP(host) != nil {
		logResult("denied-host")
		rejectProxy(w, r, "forbidden", http.StatusForbidden)
		return
	}

	lowerHost := strings.ToLower(host)
	if _, ok := e.allowSet[lowerHost]; !ok {
		logResult("denied-host")
		rejectProxy(w, r, "forbidden", http.StatusForbidden)
		return
	}

	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	maxPerIP := e.tunnelLimit.maxPerIP
	if net.ParseIP(ip).Equal(net.ParseIP(e.cfg.ProxyAuthIP)) && e.cfg.ProxyAuthIP != "" {
		auth, err := os.ReadFile(e.cfg.ProxyAuthFile)
		if err != nil || len(auth) == 0 || !sameProxyAuth(r.Header.Get("Proxy-Authorization"), string(auth)) {
			logResult("denied-auth")
			requireProxyAuth(w, r)
			return
		}
		maxPerIP = e.tunnelLimit.maxTotal
	}
	if e.tunnelLimit.reserveForIP(ip, maxPerIP) != nil {
		logResult("tunnel-limit")
		rejectProxy(w, r, "proxy busy", http.StatusServiceUnavailable)
		return
	}
	defer e.tunnelLimit.release(ip)

	resolveCtx, cancelResolve := context.WithTimeout(r.Context(), e.cfg.DialTimeout)
	ips, err := e.cfg.LookupIPAddr(resolveCtx, lowerHost)
	cancelResolve()
	if err != nil || len(ips) == 0 {
		logResult("resolve-failed")
		rejectProxy(w, r, "bad gateway", http.StatusBadGateway)
		return
	}
	for _, ipAddr := range ips {
		if isForbiddenEgressIP(ipAddr.IP) {
			logResult("denied-private-ip")
			rejectProxy(w, r, "forbidden", http.StatusForbidden)
			return
		}
	}

	var upConn net.Conn
	for _, addr := range ips {
		target := net.JoinHostPort(addr.IP.String(), "443")
		dialCtx, cancelDial := context.WithTimeout(r.Context(), e.cfg.DialTimeout)
		upConn, err = e.cfg.DialContext(dialCtx, "tcp", target)
		cancelDial()
		if err == nil {
			break
		}
	}
	if err != nil {
		logResult("dial-failed")
		rejectProxy(w, r, "bad gateway", http.StatusBadGateway)
		return
	}
	defer func() { _ = upConn.Close() }()

	logResult("allowed")
	establishTunnel(r.Context(), w, r, upConn, http.NoBody, e.cfg.IdleTimeout)
}

func hostOnly(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
}

// isForbiddenEgressIP returns true for loopback, private, link-local,
// unspecified, multicast, carrier-grade NAT, or invalid IP addresses.
func isForbiddenEgressIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() ||
		cgnatPrefix.Contains(addr)
}

type idleConn struct {
	net.Conn
	timeout time.Duration
}

func newIdleConn(c net.Conn, timeout time.Duration) *idleConn {
	ic := &idleConn{Conn: c, timeout: timeout}
	ic.bump()
	return ic
}

func (c *idleConn) bump() {
	if c.timeout > 0 {
		_ = c.Conn.SetDeadline(time.Now().Add(c.timeout))
	}
}

func (c *idleConn) Read(p []byte) (int, error) {
	c.bump()
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.bump()
	}
	return n, err
}

func (c *idleConn) Write(p []byte) (int, error) {
	c.bump()
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.bump()
	}
	return n, err
}

type closeWriter interface {
	CloseWrite() error
}

func closeWrite(c net.Conn) {
	if ic, ok := c.(*idleConn); ok {
		c = ic.Conn
	}
	if cw, ok := c.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}

type connDeadlineWriter struct {
	conn    net.Conn
	timeout time.Duration
}

func (w connDeadlineWriter) Write(p []byte) (int, error) {
	if err := w.conn.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil {
		return 0, err
	}
	return w.conn.Write(p)
}

func spliceConns(ctx context.Context, a, b net.Conn, writeTimeout time.Duration) {
	done := make(chan struct{}, 2)
	copyDirection := func(dst, src net.Conn) {
		var output io.Writer = dst
		if writeTimeout > 0 {
			output = connDeadlineWriter{conn: dst, timeout: writeTimeout}
		}
		_, _ = io.Copy(output, src)
		closeWrite(dst)
		done <- struct{}{}
	}
	go copyDirection(a, b)
	go copyDirection(b, a)

	remaining := 2
	select {
	case <-ctx.Done():
	case <-done:
		remaining--
		// Allow a short half-close drain, then force both copy loops to return.
		drain := time.NewTimer(500 * time.Millisecond)
		defer drain.Stop()
		select {
		case <-done:
			remaining--
		case <-drain.C:
		case <-ctx.Done():
		}
	}
	_ = a.Close()
	_ = b.Close()
	for range remaining {
		<-done
	}
}

// Run starts both the TCP forwarder and the HTTP CONNECT proxy and blocks until
// ctx is canceled or either server returns a fatal error.
func (e *Edge) Run(ctx context.Context) error {
	if (e.cfg.ProxyAuthIP == "") != (e.cfg.ProxyAuthFile == "") ||
		(e.cfg.ProxyAuthIP != "" && net.ParseIP(e.cfg.ProxyAuthIP) == nil) {
		return errors.New("ssh-edge: --proxy-auth-ip must be an IP address paired with --proxy-auth-file")
	}
	fwdLn, err := net.Listen("tcp", e.cfg.Listen)
	if err != nil {
		return fmt.Errorf("ssh-edge: listen %s: %w", e.cfg.Listen, err)
	}
	proxyLn, err := net.Listen("tcp", e.cfg.ProxyListen)
	if err != nil {
		_ = fwdLn.Close()
		return fmt.Errorf("ssh-edge: listen proxy %s: %w", e.cfg.ProxyListen, err)
	}
	return e.Serve(ctx, fwdLn, proxyLn)
}

// Serve runs the TCP forwarder on fwdLn and the HTTP CONNECT proxy on proxyLn
// until ctx is canceled.
func (e *Edge) Serve(ctx context.Context, fwdLn, proxyLn net.Listener) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	httpSrv := newProxyServer(runCtx, e.ProxyHandler())

	errCh := make(chan error, 2)
	go func() {
		errCh <- e.ServeForwarder(runCtx, fwdLn)
	}()
	go func() {
		err := httpSrv.Serve(capListener(proxyLn, e.cfg.MaxProxyConns))
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	var firstErr error
	remaining := 2
	select {
	case <-ctx.Done():
	case err := <-errCh:
		firstErr = err
		remaining--
	}
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	_ = fwdLn.Close()

	// Drain remaining worker error(s).
	for range remaining {
		firstErr = cmp.Or(firstErr, <-errCh)
	}
	return firstErr
}

// RunEdge constructs an Edge from cfg and runs it until ctx is canceled.
func RunEdge(ctx context.Context, cfg EdgeConfig) error {
	edge := NewEdge(cfg)
	slog.SetDefault(edge.logger)
	return edge.Run(ctx)
}
