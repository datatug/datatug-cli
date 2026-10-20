package cloudapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	primary  = "https://api.example.test/v0/"
	fallback = "https://api2.example.test/v0/"
)

// step is one scripted answer of a host.
type step struct {
	status int
	err    error
}

// fakeNet answers per host from a script; the last step repeats.
type fakeNet struct {
	mu     sync.Mutex
	script map[string][]step
	calls  []call
}

type call struct {
	host, method, path, auth, body string
}

func newFakeNet(script map[string][]step) *fakeNet { return &fakeNet{script: script} }

func (f *fakeNet) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := ""
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	f.calls = append(f.calls, call{req.URL.Host, req.Method, req.URL.RequestURI(), req.Header.Get("Authorization"), body})
	steps := f.script[req.URL.Host]
	s := steps[0]
	if len(steps) > 1 {
		f.script[req.URL.Host] = steps[1:]
	}
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(strings.NewReader("body-" + req.URL.Host)), Header: http.Header{}, Request: req}, nil
}

func (f *fakeNet) hosts() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.host)
	}
	return out
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func dialErr() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
}

func newTransport(f *fakeNet, notice io.Writer) (*Transport, *clock) {
	clk := &clock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	return NewTransport(Config{BaseURLs: []string{primary, fallback}, Next: f, Now: clk.Now, Notice: notice}), clk
}

func do(t *testing.T, tr http.RoundTripper, method, path, body string) (*http.Response, error) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, primary+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	return tr.RoundTrip(req)
}

func TestGETFailsOverOnTransportErrors(t *testing.T) {
	for name, err := range map[string]error{
		"connect":   dialErr(),
		"dns":       &net.DNSError{Err: "no such host", Name: "api.example.test"},
		"timeout":   &net.OpError{Op: "read", Err: context.DeadlineExceeded},
		"plain":     errors.New("EOF"),
		"tls":       &tls.CertificateVerificationError{Err: errors.New("bad cert")},
		"tlsrecord": tls.RecordHeaderError{Msg: "bad"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeNet(map[string][]step{"api.example.test": {{err: err}}, "api2.example.test": {{status: 200}}})
			tr, _ := newTransport(f, nil)
			resp, got := do(t, tr, http.MethodGet, "projects?x=1", "")
			if got != nil {
				t.Fatalf("unexpected error: %v", got)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if h := f.hosts(); len(h) != 2 || h[1] != "api2.example.test" {
				t.Fatalf("hosts = %v", h)
			}
			if f.calls[1].path != "/v0/projects?x=1" {
				t.Fatalf("fallback path = %q", f.calls[1].path)
			}
			if f.calls[1].auth != "Bearer secret-token" {
				t.Fatalf("auth header lost on fallback: %q", f.calls[1].auth)
			}
		})
	}
}

func TestIdempotentStatusFailover(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, status := range []int{502, 503, 504, 520, 521, 522, 523, 525, 526} {
			f := newFakeNet(map[string][]step{"api.example.test": {{status: status}}, "api2.example.test": {{status: 200}}})
			tr, _ := newTransport(f, nil)
			resp, err := do(t, tr, method, "p", "")
			if err != nil || resp.StatusCode != 200 || len(f.calls) != 2 {
				t.Errorf("%s %d: resp=%v err=%v calls=%d", method, status, resp, err, len(f.calls))
			}
		}
	}
}

func TestIdempotentNoFailoverOnOtherStatuses(t *testing.T) {
	for _, status := range []int{200, 301, 400, 401, 404, 500, 524} {
		f := newFakeNet(map[string][]step{"api.example.test": {{status: status}}, "api2.example.test": {{status: 200}}})
		tr, _ := newTransport(f, nil)
		resp, err := do(t, tr, http.MethodGet, "p", "")
		if err != nil || resp.StatusCode != status || len(f.calls) != 1 {
			t.Errorf("status %d: resp=%v err=%v calls=%d", status, resp, err, len(f.calls))
		}
	}
}

func TestNonIdempotentNeverRetriedOnAmbiguousStatuses(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		for _, status := range []int{400, 401, 404, 500, 502, 503, 504, 520, 524, 525, 526} {
			f := newFakeNet(map[string][]step{"api.example.test": {{status: status}}, "api2.example.test": {{status: 200}}})
			tr, _ := newTransport(f, nil)
			resp, err := do(t, tr, method, "p", `{"a":1}`)
			if err != nil || resp.StatusCode != status || len(f.calls) != 1 {
				t.Errorf("%s %d: resp=%v err=%v calls=%d", method, status, resp, err, len(f.calls))
			}
		}
	}
}

func TestPOSTRetriesWhenRequestNeverReachedOrigin(t *testing.T) {
	for _, status := range []int{521, 522, 523} {
		f := newFakeNet(map[string][]step{"api.example.test": {{status: status}}, "api2.example.test": {{status: 201}}})
		tr, _ := newTransport(f, nil)
		resp, err := do(t, tr, http.MethodPost, "p", `{"a":1}`)
		if err != nil || resp.StatusCode != 201 || len(f.calls) != 2 {
			t.Fatalf("status %d: resp=%v err=%v calls=%d", status, resp, err, len(f.calls))
		}
		if f.calls[0].body != `{"a":1}` || f.calls[1].body != `{"a":1}` {
			t.Fatalf("body not replayed: %q then %q", f.calls[0].body, f.calls[1].body)
		}
		if f.calls[1].auth != "Bearer secret-token" {
			t.Fatalf("auth header lost: %q", f.calls[1].auth)
		}
	}
}

func TestPOSTRetriesOnlyOnErrorsBeforeTheRequestLeft(t *testing.T) {
	retry := map[string]error{
		"connect": dialErr(),
		"dns":     &net.DNSError{Err: "no such host"},
		"cert":    &tls.CertificateVerificationError{Err: errors.New("bad cert")},
		"record":  tls.RecordHeaderError{Msg: "bad"},
	}
	for name, err := range retry {
		f := newFakeNet(map[string][]step{"api.example.test": {{err: err}}, "api2.example.test": {{status: 200}}})
		tr, _ := newTransport(f, nil)
		if resp, got := do(t, tr, http.MethodPost, "p", "x"); got != nil || resp.StatusCode != 200 || len(f.calls) != 2 {
			t.Errorf("%s: resp=%v err=%v calls=%d", name, resp, got, len(f.calls))
		}
	}
	noRetry := map[string]error{
		"read":    &net.OpError{Op: "read", Err: errors.New("connection reset")},
		"timeout": &net.OpError{Op: "read", Err: context.DeadlineExceeded},
		"plain":   errors.New("EOF"),
	}
	for name, err := range noRetry {
		f := newFakeNet(map[string][]step{"api.example.test": {{err: err}}, "api2.example.test": {{status: 200}}})
		tr, _ := newTransport(f, nil)
		if _, got := do(t, tr, http.MethodPost, "p", "x"); !errors.Is(got, err) || len(f.calls) != 1 {
			t.Errorf("%s: err=%v calls=%d", name, got, len(f.calls))
		}
	}
}

func TestBodyWithoutGetBodyIsNeverResent(t *testing.T) {
	f := newFakeNet(map[string][]step{"api.example.test": {{status: 522}}, "api2.example.test": {{status: 200}}})
	tr, _ := newTransport(f, nil)
	req, _ := http.NewRequest(http.MethodPost, primary+"p", io.NopCloser(strings.NewReader("stream")))
	resp, err := tr.RoundTrip(req)
	if err != nil || resp.StatusCode != 522 || len(f.calls) != 1 {
		t.Fatalf("resp=%v err=%v calls=%d", resp, err, len(f.calls))
	}
	f = newFakeNet(map[string][]step{"api.example.test": {{err: dialErr()}}, "api2.example.test": {{status: 200}}})
	tr, _ = newTransport(f, nil)
	req, _ = http.NewRequest(http.MethodGet, primary+"p", io.NopCloser(strings.NewReader("stream")))
	if _, err := tr.RoundTrip(req); err == nil || len(f.calls) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls))
	}
}

func TestGetBodyError(t *testing.T) {
	f := newFakeNet(map[string][]step{"api.example.test": {{status: 522}}, "api2.example.test": {{status: 200}}})
	tr, _ := newTransport(f, nil)
	req, _ := http.NewRequest(http.MethodPost, primary+"p", strings.NewReader("x"))
	boom := errors.New("cannot rewind")
	req.GetBody = func() (io.ReadCloser, error) { return nil, boom }
	if _, err := tr.RoundTrip(req); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyBodyReplays(t *testing.T) {
	f := newFakeNet(map[string][]step{"api.example.test": {{status: 522}}, "api2.example.test": {{status: 200}}})
	tr, _ := newTransport(f, nil)
	req, _ := http.NewRequest(http.MethodPost, primary+"p", http.NoBody)
	resp, err := tr.RoundTrip(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

func TestCancelledContextIsNotRetried(t *testing.T) {
	f := newFakeNet(map[string][]step{"api.example.test": {{err: context.Canceled}}, "api2.example.test": {{status: 200}}})
	tr, _ := newTransport(f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, primary+"p", nil)
	if _, err := tr.RoundTrip(req); !errors.Is(err, context.Canceled) || len(f.calls) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls))
	}
}

func TestEveryHostFailingReturnsTheLastOutcome(t *testing.T) {
	last := errors.New("second down")
	f := newFakeNet(map[string][]step{"api.example.test": {{err: dialErr()}}, "api2.example.test": {{err: last}}})
	tr, _ := newTransport(f, nil)
	if _, err := do(t, tr, http.MethodGet, "p", ""); !errors.Is(err, last) {
		t.Fatalf("err = %v", err)
	}
	f = newFakeNet(map[string][]step{"api.example.test": {{status: 503}}, "api2.example.test": {{status: 502}}})
	tr, clk := newTransport(f, nil)
	resp, err := do(t, tr, http.MethodGet, "p", "")
	if err != nil || resp.StatusCode != 502 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	// A fallback that answered 502 did not prove itself, so nothing is held.
	clk.advance(time.Second)
	f.script["api.example.test"] = []step{{status: 200}}
	if _, err := do(t, tr, http.MethodGet, "p", ""); err != nil || f.hosts()[2] != "api.example.test" {
		t.Fatalf("hosts = %v err=%v", f.hosts(), err)
	}
}

func TestCircuitBreakerHoldsFallbackThenReturnsToPrimary(t *testing.T) {
	f := newFakeNet(map[string][]step{
		"api.example.test":  {{err: dialErr()}, {status: 200}},
		"api2.example.test": {{status: 200}},
	})
	tr, clk := newTransport(f, nil)
	if _, err := do(t, tr, http.MethodGet, "a", ""); err != nil {
		t.Fatal(err)
	}
	// Within five minutes the fallback is tried first, without touching the primary.
	clk.advance(4*time.Minute + 59*time.Second)
	if _, err := do(t, tr, http.MethodGet, "b", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.hosts(), ","); got != "api.example.test,api2.example.test,api2.example.test" {
		t.Fatalf("hosts = %s", got)
	}
	// After the hold the primary is tried first again and clears the hold.
	clk.advance(time.Second)
	if _, err := do(t, tr, http.MethodGet, "c", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := do(t, tr, http.MethodGet, "d", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.hosts(), ","); got != "api.example.test,api2.example.test,api2.example.test,api.example.test,api.example.test" {
		t.Fatalf("hosts = %s", got)
	}
}

func TestCircuitBreakerWhenHeldFallbackFailsPrimaryAnswers(t *testing.T) {
	f := newFakeNet(map[string][]step{
		"api.example.test":  {{err: dialErr()}, {status: 200}},
		"api2.example.test": {{status: 200}, {status: 503}},
	})
	tr, _ := newTransport(f, nil)
	for i := 0; i < 3; i++ {
		if resp, err := do(t, tr, http.MethodGet, "p", ""); err != nil || resp.StatusCode != 200 {
			t.Fatalf("request %d: resp=%v err=%v", i, resp, err)
		}
	}
	// 1: primary down, fallback ok. 2: held fallback 503, primary ok (hold cleared). 3: primary.
	if got := strings.Join(f.hosts(), ","); got != "api.example.test,api2.example.test,api2.example.test,api.example.test,api.example.test" {
		t.Fatalf("hosts = %s", got)
	}
}

func TestConfigurableHold(t *testing.T) {
	f := newFakeNet(map[string][]step{
		"api.example.test":  {{err: dialErr()}, {status: 200}},
		"api2.example.test": {{status: 200}},
	})
	clk := &clock{now: time.Unix(1000, 0)}
	tr := NewTransport(Config{BaseURLs: []string{primary, fallback}, Next: f, Now: clk.Now, HoldFor: time.Second})
	_, _ = do(t, tr, http.MethodGet, "a", "")
	clk.advance(time.Second)
	_, _ = do(t, tr, http.MethodGet, "b", "")
	if got := strings.Join(f.hosts(), ","); got != "api.example.test,api2.example.test,api.example.test" {
		t.Fatalf("hosts = %s", got)
	}
}

func TestSingleURLBehavesAsBefore(t *testing.T) {
	f := newFakeNet(map[string][]step{"api.example.test": {{status: 503}}})
	tr := NewTransport(Config{BaseURLs: []string{primary}, Next: f})
	resp, err := do(t, tr, http.MethodGet, "p", "")
	if err != nil || resp.StatusCode != 503 || len(f.calls) != 1 {
		t.Fatalf("resp=%v err=%v calls=%d", resp, err, len(f.calls))
	}
	f = newFakeNet(map[string][]step{"api.example.test": {{err: dialErr()}}})
	tr = NewTransport(Config{BaseURLs: []string{primary}, Next: f})
	if _, err := do(t, tr, http.MethodGet, "p", ""); err == nil || len(f.calls) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls))
	}
}

func TestRequestsOutsideTheBaseAreForwarded(t *testing.T) {
	f := newFakeNet(map[string][]step{"other.example.test": {{err: dialErr()}}})
	tr, _ := newTransport(f, nil)
	req, _ := http.NewRequest(http.MethodGet, "https://other.example.test/x", nil)
	if _, err := tr.RoundTrip(req); err == nil || len(f.calls) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls))
	}
}

func TestBaseWithoutTrailingSlashAndDefaults(t *testing.T) {
	f := newFakeNet(map[string][]step{"api.example.test": {{err: dialErr()}}, "api2.example.test": {{status: 200}}})
	tr := NewTransport(Config{BaseURLs: []string{strings.TrimSuffix(primary, "/"), strings.TrimSuffix(fallback, "/")}, Next: f})
	if tr.holdFor != DefaultHoldFor || tr.now == nil {
		t.Fatalf("defaults not applied: %v", tr.holdFor)
	}
	if _, err := do(t, tr, http.MethodGet, "p", ""); err != nil || len(f.calls) != 2 {
		t.Fatalf("err=%v calls=%d", err, len(f.calls))
	}
	if NewTransport(Config{}).next != http.DefaultTransport {
		t.Fatal("default transport not used")
	}
}

func TestNoticeIsOneLineAboutHostsOnly(t *testing.T) {
	f := newFakeNet(map[string][]step{"api.example.test": {{err: dialErr()}}, "api2.example.test": {{status: 200}}})
	var out bytes.Buffer
	tr, clk := newTransport(f, &out)
	for i := 0; i < 3; i++ {
		clk.advance(10 * time.Minute) // every request fails over again; the notice stays single
		if _, err := do(t, tr, http.MethodGet, "secret/path?token=abc", ""); err != nil {
			t.Fatal(err)
		}
	}
	want := "DataTug API api.example.test did not answer; using api2.example.test.\n"
	if out.String() != want {
		t.Fatalf("notice = %q", out.String())
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "token") {
		t.Fatal("notice leaks request data")
	}
}

func TestInvalidBaseURL(t *testing.T) {
	f := newFakeNet(map[string][]step{"api.example.test": {{err: dialErr()}}})
	var out bytes.Buffer
	tr := NewTransport(Config{BaseURLs: []string{primary, "http://[::1/"}, Next: f, Notice: &out})
	if _, err := do(t, tr, http.MethodGet, "p", ""); err == nil {
		t.Fatal("expected a URL error")
	}
	if !strings.Contains(out.String(), "http://[::1/") {
		t.Fatalf("notice = %q", out.String())
	}
}

func TestSplitBaseURLs(t *testing.T) {
	if got := SplitBaseURLs(""); got != nil {
		t.Fatalf("got %v", got)
	}
	if got := SplitBaseURLs(" a , ,b,"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
	if got := SplitBaseURLs(primary); len(got) != 1 || got[0] != primary {
		t.Fatalf("got %v", got)
	}
}

// TestRealServersAndRealConnectError drives the transport through a real
// http.Transport: the primary port refuses connections, the fallback is an
// httptest server that checks the replayed body and the auth header.
func TestRealServersAndRealConnectError(t *testing.T) {
	var gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotAuth = string(b), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	deadURL := closed.URL
	closed.Close()

	tr := NewTransport(Config{BaseURLs: []string{deadURL + "/v0/", srv.URL + "/v0/"}, Next: &http.Transport{}})
	client := &http.Client{Transport: tr}
	req, _ := http.NewRequest(http.MethodPost, deadURL+"/v0/thing", strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer t")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || gotBody != "payload" || gotAuth != "Bearer t" {
		t.Fatalf("status=%d body=%q auth=%q", resp.StatusCode, gotBody, gotAuth)
	}
}

// TestFakeDialer fails the primary at the dial step of a real http.Transport.
func TestFakeDialer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	dialer := &net.Dialer{}
	next := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "primary.invalid") {
			return nil, &net.OpError{Op: "dial", Net: network, Err: errors.New("no route")}
		}
		return dialer.DialContext(ctx, network, addr)
	}}
	tr := NewTransport(Config{BaseURLs: []string{"http://primary.invalid:80/v0/", srv.URL + "/v0/"}, Next: next})
	req, _ := http.NewRequest(http.MethodPost, "http://primary.invalid:80/v0/x", strings.NewReader("b"))
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}
