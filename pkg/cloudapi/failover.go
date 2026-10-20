// Package cloudapi carries the DataTug cloud API client's host failover.
//
// The API is served by one Cloud Run service under several hostnames (a
// primary behind Cloudflare and a DNS-only fallback). Transport sits under an
// *http.Client and retries a request on the next host when the current one
// cannot answer. The rules match the web client's:
//
//   - GET and HEAD move on after any network error before a response and after
//     HTTP 502, 503, 504 and the Cloudflare origin statuses 520-523, 525, 526.
//   - Every other method moves on only when the request certainly never
//     reached a server: a DNS, connect or TLS-verification error, or
//     Cloudflare 521, 522 or 523 (Cloudflare could not open a connection to
//     the origin). Never after 524 (the origin got the request and was too
//     slow), an application 5xx or a 4xx. A body that cannot be replayed
//     (no GetBody) is never resent.
//   - After a failover the fallback is preferred for HoldFor (5 minutes by
//     default), then the primary is tried first again. The preference lives
//     in the process only.
package cloudapi

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultHoldFor is how long the fallback stays preferred after a failover.
const DefaultHoldFor = 5 * time.Minute

// SplitBaseURLs splits a comma-separated list of API base URLs, trimming
// blanks. A single URL gives a one-element list; an empty string gives nil.
func SplitBaseURLs(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Config configures a Transport.
type Config struct {
	// BaseURLs lists the API base URLs, primary first. Requests are built
	// against the first one; the others replace its prefix on failover.
	BaseURLs []string
	// Next performs each attempt; nil means http.DefaultTransport.
	Next http.RoundTripper
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// HoldFor is how long a fallback stays preferred; zero means DefaultHoldFor.
	HoldFor time.Duration
	// Notice receives one line, once, when the first failover happens. It
	// names hosts only, never paths, queries or credentials. Nil is silent.
	Notice io.Writer
}

// Transport is an http.RoundTripper that fails over between API base URLs.
type Transport struct {
	bases   []string // each ends with "/"
	next    http.RoundTripper
	now     func() time.Time
	holdFor time.Duration
	notice  io.Writer

	mu        sync.Mutex
	preferred int
	until     time.Time
	noticed   bool
}

// NewTransport builds a Transport. With fewer than two base URLs it only
// forwards requests, so a single-URL configuration behaves as before.
func NewTransport(cfg Config) *Transport {
	t := &Transport{next: cfg.Next, now: cfg.Now, holdFor: cfg.HoldFor, notice: cfg.Notice}
	if t.next == nil {
		t.next = http.DefaultTransport
	}
	if t.now == nil {
		t.now = time.Now
	}
	if t.holdFor <= 0 {
		t.holdFor = DefaultHoldFor
	}
	for _, base := range cfg.BaseURLs {
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		t.bases = append(t.bases, base)
	}
	return t
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if len(t.bases) < 2 {
		return t.next.RoundTrip(req)
	}
	raw := req.URL.String()
	if !strings.HasPrefix(raw, t.bases[0]) {
		return t.next.RoundTrip(req)
	}
	rest := strings.TrimPrefix(raw, t.bases[0])
	order := t.order()
	idempotent := req.Method == http.MethodGet || req.Method == http.MethodHead
	replayable := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
	for attempt := 0; ; attempt++ {
		idx := order[attempt]
		more := replayable && attempt < len(order)-1
		attemptReq, err := t.attemptRequest(req, t.bases[idx]+rest, attempt)
		if err != nil {
			return nil, err
		}
		resp, err := t.next.RoundTrip(attemptReq)
		if err != nil {
			if !more || req.Context().Err() != nil || !retryError(idempotent, err) {
				return nil, err
			}
		} else {
			failed := retryStatus(idempotent, resp.StatusCode)
			if !failed {
				t.succeeded(idx, attempt)
			}
			if !failed || !more {
				return resp, nil
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
		}
		t.announce(t.bases[idx], t.bases[order[attempt+1]])
	}
}

// order lists base indexes in the order to try them: the preferred one first
// while a failover is held, then the rest in configured order.
func (t *Transport) order() []int {
	t.mu.Lock()
	start := t.preferred
	if start != 0 && !t.now().Before(t.until) {
		start, t.preferred = 0, 0
	}
	t.mu.Unlock()
	order := []int{start}
	for i := range t.bases {
		if i != start {
			order = append(order, i)
		}
	}
	return order
}

// succeeded records which base answered. A base that answered only after a
// failover is held for holdFor; the primary answering clears the hold.
func (t *Transport) succeeded(idx, attempt int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case attempt > 0 && idx != 0:
		t.preferred, t.until = idx, t.now().Add(t.holdFor)
	case idx == 0:
		t.preferred = 0
	}
}

func (t *Transport) announce(from, to string) {
	t.mu.Lock()
	first := !t.noticed
	t.noticed = true
	t.mu.Unlock()
	if first && t.notice != nil {
		_, _ = fmt.Fprintf(t.notice, "DataTug API %s did not answer; using %s.\n", hostOf(from), hostOf(to))
	}
}

func hostOf(base string) string {
	if u, err := url.Parse(base); err == nil {
		return u.Host
	}
	return base
}

// attemptRequest clones req for one attempt against target. The original is
// never modified. Later attempts take a fresh body from GetBody.
func (t *Transport) attemptRequest(req *http.Request, target string, attempt int) (*http.Request, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL = u
	clone.Host = ""
	if attempt > 0 && req.Body != nil && req.Body != http.NoBody {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		clone.Body = body
	}
	return clone, nil
}

// retryStatus decides whether an HTTP status warrants the next host.
func retryStatus(idempotent bool, status int) bool {
	switch status {
	case 521, 522, 523: // Cloudflare could not reach the origin: nothing was processed
		return true
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 525, 526:
		return idempotent
	}
	return false
}

// retryError decides whether a transport error warrants the next host.
func retryError(idempotent bool, err error) bool {
	return idempotent || neverReachedServer(err)
}

// neverReachedServer reports errors that certainly happened before any byte
// of the request reached a server.
func neverReachedServer(err error) bool {
	var dns *net.DNSError
	var op *net.OpError
	var cert *tls.CertificateVerificationError
	var record tls.RecordHeaderError
	switch {
	case errors.As(err, &dns), errors.As(err, &cert), errors.As(err, &record):
		return true
	case errors.As(err, &op):
		return op.Op == "dial"
	}
	return false
}
