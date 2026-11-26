package sshdemo

import (
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

// EdgeConfig configures `datatug demo ssh-edge` (spec D4).
type EdgeConfig struct {
	Listen        string
	Upstream      string
	ProxyListen   string
	Allow         []string
	MaxConns      int
	MaxConnsPerIP int
	IdleTimeout   time.Duration
	DialTimeout   time.Duration

	Logger       *slog.Logger
	LookupIPAddr func(ctx context.Context, host string) ([]net.IPAddr, error)
	DialContext  func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DefaultEdgeConfig returns an EdgeConfig populated with specification defaults.
func DefaultEdgeConfig() EdgeConfig {
	allow := make([]string, len(DefaultEdgeAllow))
	copy(allow, DefaultEdgeAllow)
	return EdgeConfig{
		Listen:        DefaultEdgeListen,
		Upstream:      DefaultEdgeUpstream,
		ProxyListen:   DefaultEdgeProxyListen,
		Allow:         allow,
		MaxConns:      DefaultEdgeMaxConns,
		MaxConnsPerIP: DefaultEdgeMaxConnsPerIP,
		IdleTimeout:   DefaultEdgeIdleTimeout,
		DialTimeout:   10 * time.Second,
	}
}

// WithDefaults returns a copy of c with zero-valued fields replaced by defaults.
func (c EdgeConfig) WithDefaults() EdgeConfig {
	if c.Listen == "" {
		c.Listen = DefaultEdgeListen
	}
	if c.Upstream == "" {
		c.Upstream = DefaultEdgeUpstream
	}
	if c.ProxyListen == "" {
		c.ProxyListen = DefaultEdgeProxyListen
	}
	if c.Allow == nil {
		c.Allow = append([]string(nil), DefaultEdgeAllow...)
	}
	if c.MaxConns <= 0 {
		c.MaxConns = DefaultEdgeMaxConns
	}
	if c.MaxConnsPerIP <= 0 {
		c.MaxConnsPerIP = DefaultEdgeMaxConnsPerIP
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultEdgeIdleTimeout
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = 10 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if c.LookupIPAddr == nil {
		c.LookupIPAddr = net.DefaultResolver.LookupIPAddr
	}
	if c.DialContext == nil {
		d := &net.Dialer{Timeout: c.DialTimeout}
		c.DialContext = d.DialContext
	}
	return c
}

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

	connMu    sync.Mutex
	totalConn int
	perIPConn map[string]int
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
		cfg:       cfg,
		logger:    cfg.Logger,
		allowSet:  allowSet,
		perIPConn: make(map[string]int),
	}
}

func (e *Edge) acquireConn(ip string) bool {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	if e.totalConn >= e.cfg.MaxConns || e.perIPConn[ip] >= e.cfg.MaxConnsPerIP {
		return false
	}
	e.totalConn++
	e.perIPConn[ip]++
	return true
}

func (e *Edge) releaseConn(ip string) {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	if e.totalConn > 0 {
		e.totalConn--
	}
	if n := e.perIPConn[ip]; n <= 1 {
		delete(e.perIPConn, ip)
	} else {
		e.perIPConn[ip] = n - 1
	}
}

// ServeForwarder accepts TCP connections on ln, dials e.cfg.Upstream, writes a
// PROXY protocol v2 header containing the client's address, and splices both ways.
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
		if !e.acquireConn(ip) {
			e.logger.Warn("edge forwarder cap reached", "client_ip", ip)
			_ = clientConn.Close()
			continue
		}

		wg.Add(1)
		go func(c net.Conn, clientIP string) {
			defer wg.Done()
			defer e.releaseConn(clientIP)
			defer func() { _ = c.Close() }()
			e.forwardConn(ctx, c)
		}(clientConn, ip)
	}
}

func (e *Edge) forwardConn(ctx context.Context, clientConn net.Conn) {
	dialCtx, cancel := context.WithTimeout(ctx, e.cfg.DialTimeout)
	upConn, err := e.cfg.DialContext(dialCtx, "tcp", e.cfg.Upstream)
	cancel()
	if err != nil {
		e.logger.Warn("edge upstream dial failed", "upstream", e.cfg.Upstream, "err", err)
		return
	}
	defer func() { _ = upConn.Close() }()

	hdr := EncodeProxyV2(clientConn.RemoteAddr(), clientConn.LocalAddr())
	_ = upConn.SetWriteDeadline(time.Now().Add(e.cfg.DialTimeout))
	if _, err := upConn.Write(hdr); err != nil {
		e.logger.Warn("edge write proxy v2 header failed", "err", err)
		return
	}
	_ = upConn.SetWriteDeadline(time.Time{})

	spliceConns(ctx, clientConn, upConn)
}

// ProxyHandler returns the http.Handler implementing the allow-listing CONNECT proxy.
func (e *Edge) ProxyHandler() http.Handler {
	return http.HandlerFunc(e.handleProxy)
}

func (e *Edge) handleProxy(w http.ResponseWriter, r *http.Request) {
	// Never log request paths or query strings; log only host + result.
	if r.Method != http.MethodConnect {
		e.logger.Info("egress proxy request", "host", hostOnly(r.Host), "result", "denied-method")
		http.Error(w, "method not allowed", http.StatusForbidden)
		return
	}

	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		e.logger.Info("egress proxy request", "host", hostOnly(r.Host), "result", "denied-port")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if host == "" || strings.HasSuffix(host, ".") || net.ParseIP(host) != nil {
		e.logger.Info("egress proxy request", "host", host, "result", "denied-host")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	lowerHost := strings.ToLower(host)
	if _, ok := e.allowSet[lowerHost]; !ok {
		e.logger.Info("egress proxy request", "host", lowerHost, "result", "denied-host")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	resolveCtx, cancelResolve := context.WithTimeout(r.Context(), e.cfg.DialTimeout)
	ips, err := e.cfg.LookupIPAddr(resolveCtx, lowerHost)
	cancelResolve()
	if err != nil || len(ips) == 0 {
		e.logger.Info("egress proxy request", "host", lowerHost, "result", "resolve-failed")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	for _, ipAddr := range ips {
		if isForbiddenEgressIP(ipAddr.IP) {
			e.logger.Info("egress proxy request", "host", lowerHost, "result", "denied-private-ip")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	target := net.JoinHostPort(ips[0].IP.String(), "443")
	dialCtx, cancelDial := context.WithTimeout(r.Context(), e.cfg.DialTimeout)
	upConn, err := e.cfg.DialContext(dialCtx, "tcp", target)
	cancelDial()
	if err != nil {
		e.logger.Info("egress proxy request", "host", lowerHost, "result", "dial-failed")
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upConn.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, bufRW, err := hj.Hijack()
	if err != nil {
		_ = upConn.Close()
		return
	}
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = upConn.Close() }()

	if _, err := io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	e.logger.Info("egress proxy request", "host", lowerHost, "result", "allowed")

	wrappedClient := newIdleConn(clientConn, e.cfg.IdleTimeout)
	wrappedUp := newIdleConn(upConn, e.cfg.IdleTimeout)

	// Flush any bytes already buffered by net/http's bufio.Reader.
	if bufRW != nil && bufRW.Reader.Buffered() > 0 {
		buffered := make([]byte, bufRW.Reader.Buffered())
		if n, rErr := bufRW.Reader.Read(buffered); n > 0 && rErr == nil {
			if _, wErr := wrappedUp.Write(buffered[:n]); wErr != nil {
				return
			}
		}
	}

	spliceConns(r.Context(), wrappedClient, wrappedUp)
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
	if !addr.IsValid() {
		return true
	}
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

func spliceConns(ctx context.Context, a, b net.Conn) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = a.Close()
			_ = b.Close()
		case <-done:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		closeWrite(b)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		closeWrite(a)
	}()
	wg.Wait()
	close(done)
}

// Run starts both the TCP forwarder and the HTTP CONNECT proxy and blocks until
// ctx is canceled or either server returns a fatal error.
func (e *Edge) Run(ctx context.Context) error {
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

	httpSrv := &http.Server{
		Handler:           e.ProxyHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- e.ServeForwarder(runCtx, fwdLn)
	}()
	go func() {
		err := httpSrv.Serve(proxyLn)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	var firstErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		firstErr = err
		cancel()
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	_ = fwdLn.Close()

	// Drain remaining worker error(s).
	if firstErr == nil {
		for i := 0; i < 2; i++ {
			if err := <-errCh; err != nil && firstErr == nil {
				firstErr = err
			}
		}
	} else {
		<-errCh
	}
	return firstErr
}

// RunEdge constructs an Edge from cfg and runs it until ctx is canceled.
func RunEdge(ctx context.Context, cfg EdgeConfig) error {
	return NewEdge(cfg).Run(ctx)
}
