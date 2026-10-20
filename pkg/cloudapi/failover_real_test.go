package cloudapi

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingServer counts the requests it received per method.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// postHandshakeGarbageServer completes a real TLS handshake, reads the whole
// request, and then sends a record with an impossible version on the raw
// connection. The client fails with a tls.RecordHeaderError after the server
// has received the request.
func postHandshakeGarbageServer(t *testing.T) (baseURL string, received *atomic.Int32, next *http.Transport) {
	t.Helper()
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certSrv.Close)
	cfg := certSrv.TLS.Clone()
	cfg.MaxVersion = tls.VersionTLS12
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	received = &atomic.Int32{}
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = raw.Close() }()
				conn := tls.Server(raw, cfg)
				if conn.Handshake() != nil {
					return
				}
				req, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				_, _ = io.Copy(io.Discard, req.Body)
				received.Add(1)
				_, _ = raw.Write([]byte{0x17, 0x09, 0x09, 0x00, 0x01, 0x00})
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()
	next = certSrv.Client().Transport.(*http.Transport).Clone()
	return "https://" + ln.Addr().String() + "/v0/", received, next
}

func TestPOSTIsNotReplayedAfterAPostHandshakeTLSRecordError(t *testing.T) {
	primaryURL, received, next := postHandshakeGarbageServer(t)
	fallbackSrv, fallbackHits := countingServer(t)
	tr := NewTransport(Config{BaseURLs: []string{primaryURL, fallbackSrv.URL + "/v0/"}, Next: next})
	req, _ := http.NewRequest(http.MethodPost, primaryURL+"write", strings.NewReader("once"))
	_, err := tr.RoundTrip(req)
	var record tls.RecordHeaderError
	if !errors.As(err, &record) || record.Conn != nil {
		t.Fatalf("want a post-handshake RecordHeaderError, got %v", err)
	}
	if received.Load() != 1 || fallbackHits.Load() != 0 {
		t.Fatalf("write landed on primary=%d fallback=%d; it must land once", received.Load(), fallbackHits.Load())
	}
}

func TestGETIsReplayedAfterAPostHandshakeTLSRecordError(t *testing.T) {
	primaryURL, _, next := postHandshakeGarbageServer(t)
	fallbackSrv, fallbackHits := countingServer(t)
	tr := NewTransport(Config{BaseURLs: []string{primaryURL, fallbackSrv.URL + "/v0/"}, Next: next})
	req, _ := http.NewRequest(http.MethodGet, primaryURL+"read", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if fallbackHits.Load() != 1 {
		t.Fatalf("fallback hits = %d", fallbackHits.Load())
	}
}

func TestPOSTFailsOverWhenTheTLSHandshakeFails(t *testing.T) {
	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the untrusted server must not receive the request")
	}))
	defer untrusted.Close()
	fallbackSrv, fallbackHits := countingServer(t)
	tr := NewTransport(Config{BaseURLs: []string{untrusted.URL + "/v0/", fallbackSrv.URL + "/v0/"}, Next: &http.Transport{}})
	req, _ := http.NewRequest(http.MethodPost, untrusted.URL+"/v0/write", strings.NewReader("once"))
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if fallbackHits.Load() != 1 {
		t.Fatalf("fallback hits = %d", fallbackHits.Load())
	}
}

func TestPOSTFailsOverWhenTheHandshakeIsNotTLS(t *testing.T) {
	// A plain HTTP server answering a TLS ClientHello: the handshake fails.
	plain, _ := countingServer(t)
	fallbackSrv, fallbackHits := countingServer(t)
	primaryURL := strings.Replace(plain.URL, "http://", "https://", 1) + "/v0/"
	tr := NewTransport(Config{BaseURLs: []string{primaryURL, fallbackSrv.URL + "/v0/"}, Next: &http.Transport{}})
	req, _ := http.NewRequest(http.MethodPost, primaryURL+"write", strings.NewReader("once"))
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if fallbackHits.Load() != 1 {
		t.Fatalf("fallback hits = %d", fallbackHits.Load())
	}
}

// connected stands in for a transport that got a connection before failing.
type connected struct {
	hook func(*httptrace.ClientTrace)
	err  error
}

func (c connected) RoundTrip(req *http.Request) (*http.Response, error) {
	if trace := httptrace.ContextClientTrace(req.Context()); trace != nil {
		c.hook(trace)
	}
	return nil, c.err
}

func TestConnectedAttemptIsNotReplayedForNonIdempotentRequests(t *testing.T) {
	hooks := map[string]func(*httptrace.ClientTrace){
		"got conn":      func(tr *httptrace.ClientTrace) { tr.GotConn(httptrace.GotConnInfo{}) },
		"wrote headers": func(tr *httptrace.ClientTrace) { tr.WroteHeaders() },
	}
	errs := map[string]error{
		"dial":   dialErr(),
		"record": tls.RecordHeaderError{Msg: "bad"},
		"dns":    &net.DNSError{Err: "x"},
	}
	for hookName, hook := range hooks {
		for errName, err := range errs {
			next := &countingNext{inner: connected{hook: hook, err: err}}
			tr := NewTransport(Config{BaseURLs: []string{primary, fallback}, Next: next})
			req, _ := http.NewRequest(http.MethodPost, primary+"p", strings.NewReader("x"))
			if _, got := tr.RoundTrip(req); got == nil {
				t.Fatalf("%s/%s: err = %v", hookName, errName, got)
			}
			if next.n != 1 {
				t.Errorf("%s/%s: attempts = %d, want 1", hookName, errName, next.n)
			}
			req, _ = http.NewRequest(http.MethodGet, primary+"p", nil)
			next.n = 0
			_, _ = tr.RoundTrip(req)
			if next.n != 2 {
				t.Errorf("%s/%s GET: attempts = %d, want 2", hookName, errName, next.n)
			}
		}
	}
}

type countingNext struct {
	inner http.RoundTripper
	n     int
}

func (c *countingNext) RoundTrip(req *http.Request) (*http.Response, error) {
	c.n++
	return c.inner.RoundTrip(req)
}

// blockingDialer never connects to the host "black-hole"; it waits for the
// attempt to be cancelled, like a primary that silently drops packets.
func blockingTransport() *http.Transport {
	dialer := &net.Dialer{}
	return &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "black-hole") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return dialer.DialContext(ctx, network, addr)
	}}
}

func TestConnectTimeoutFailsOverASilentPrimary(t *testing.T) {
	fallbackSrv, fallbackHits := countingServer(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		tr := NewTransport(Config{
			BaseURLs:       []string{"http://black-hole:80/v0/", fallbackSrv.URL + "/v0/"},
			Next:           blockingTransport(),
			ConnectTimeout: 50 * time.Millisecond,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		req, _ := http.NewRequestWithContext(ctx, method, "http://black-hole:80/v0/x", strings.NewReader("b"))
		start := time.Now()
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		_ = resp.Body.Close()
		cancel()
		if time.Since(start) > 2*time.Second {
			t.Fatalf("%s: failover took %v", method, time.Since(start))
		}
	}
	if fallbackHits.Load() != 2 {
		t.Fatalf("fallback hits = %d", fallbackHits.Load())
	}
}

func TestConnectTimeoutCanBeDisabled(t *testing.T) {
	fallbackSrv, fallbackHits := countingServer(t)
	tr := NewTransport(Config{
		BaseURLs:       []string{"http://black-hole:80/v0/", fallbackSrv.URL + "/v0/"},
		Next:           blockingTransport(),
		ConnectTimeout: -1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://black-hole:80/v0/x", nil)
	if _, err := tr.RoundTrip(req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if fallbackHits.Load() != 0 {
		t.Fatal("must not fail over when the timeout is disabled and the caller gave up")
	}
}

func TestDefaultConnectTimeoutApplies(t *testing.T) {
	if got := NewTransport(Config{}).connect; got != DefaultConnectTimeout {
		t.Fatalf("connect timeout = %v", got)
	}
}

// A response that streams for longer than the connect timeout must not be cut.
func TestConnectTimeoutDoesNotCutAStreamingResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "one,")
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "two")
	}))
	defer srv.Close()
	tr := NewTransport(Config{BaseURLs: []string{srv.URL + "/v0/", fallback}, Next: &http.Transport{}, ConnectTimeout: 50 * time.Millisecond})
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v0/stream", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "one,two" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestConcurrentUse(t *testing.T) {
	f := newFakeNet(map[string][]step{
		"api.example.test":  {{err: dialErr()}, {status: 200}, {err: dialErr()}, {status: 503}},
		"api2.example.test": {{status: 200}},
	})
	tr, _ := newTransport(f, io.Discard)
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			method, body := http.MethodGet, ""
			if i%2 == 1 {
				method, body = http.MethodPost, "x"
			}
			req, _ := http.NewRequest(method, primary+"p", strings.NewReader(body))
			resp, err := tr.RoundTrip(req)
			if err != nil || resp.StatusCode >= 500 && method == http.MethodGet {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d requests failed", failures.Load())
	}
}
