package apimonitor

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type testResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newTestResponseWriter() *testResponseWriter {
	return &testResponseWriter{header: make(http.Header)}
}
func (w *testResponseWriter) Header() http.Header { return w.header }
func (w *testResponseWriter) WriteHeader(code int) {
	if w.status == 0 && (code >= 200 || code == 101) {
		w.status = code
	}
}
func (w *testResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(p)
}

type optionalCalls struct {
	flush, hijack, push, readFrom int
	unwrapTarget                  http.ResponseWriter
}
type flushCap struct{ c *optionalCalls }

func (v flushCap) Flush() { v.c.flush++ }

type hijackCap struct{ c *optionalCalls }

func (v hijackCap) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	v.c.hijack++
	a, _ := net.Pipe()
	return a, bufio.NewReadWriter(bufio.NewReader(a), bufio.NewWriter(a)), nil
}

type pushCap struct{ c *optionalCalls }

func (v pushCap) Push(string, *http.PushOptions) error { v.c.push++; return nil }

type readerFromCap struct{ c *optionalCalls }

func (v readerFromCap) ReadFrom(src io.Reader) (int64, error) {
	v.c.readFrom++
	return io.Copy(io.Discard, src)
}

type unwrapCap struct{ c *optionalCalls }

func (v unwrapCap) Unwrap() http.ResponseWriter { return v.c.unwrapTarget }

func writerWithCapabilities(mask int, base *testResponseWriter, calls *optionalCalls) http.ResponseWriter {
	switch mask {
	case 1:
		return struct {
			*testResponseWriter
			flushCap
		}{base, flushCap{calls}}
	case 2:
		return struct {
			*testResponseWriter
			hijackCap
		}{base, hijackCap{calls}}
	case 3:
		return struct {
			*testResponseWriter
			flushCap
			hijackCap
		}{base, flushCap{calls}, hijackCap{calls}}
	case 4:
		return struct {
			*testResponseWriter
			pushCap
		}{base, pushCap{calls}}
	case 5:
		return struct {
			*testResponseWriter
			flushCap
			pushCap
		}{base, flushCap{calls}, pushCap{calls}}
	case 6:
		return struct {
			*testResponseWriter
			hijackCap
			pushCap
		}{base, hijackCap{calls}, pushCap{calls}}
	case 7:
		return struct {
			*testResponseWriter
			flushCap
			hijackCap
			pushCap
		}{base, flushCap{calls}, hijackCap{calls}, pushCap{calls}}
	case 8:
		return struct {
			*testResponseWriter
			readerFromCap
		}{base, readerFromCap{calls}}
	case 9:
		return struct {
			*testResponseWriter
			flushCap
			readerFromCap
		}{base, flushCap{calls}, readerFromCap{calls}}
	case 10:
		return struct {
			*testResponseWriter
			hijackCap
			readerFromCap
		}{base, hijackCap{calls}, readerFromCap{calls}}
	case 11:
		return struct {
			*testResponseWriter
			flushCap
			hijackCap
			readerFromCap
		}{base, flushCap{calls}, hijackCap{calls}, readerFromCap{calls}}
	case 12:
		return struct {
			*testResponseWriter
			pushCap
			readerFromCap
		}{base, pushCap{calls}, readerFromCap{calls}}
	case 13:
		return struct {
			*testResponseWriter
			flushCap
			pushCap
			readerFromCap
		}{base, flushCap{calls}, pushCap{calls}, readerFromCap{calls}}
	case 14:
		return struct {
			*testResponseWriter
			hijackCap
			pushCap
			readerFromCap
		}{base, hijackCap{calls}, pushCap{calls}, readerFromCap{calls}}
	case 15:
		return struct {
			*testResponseWriter
			flushCap
			hijackCap
			pushCap
			readerFromCap
		}{base, flushCap{calls}, hijackCap{calls}, pushCap{calls}, readerFromCap{calls}}
	case 16:
		return struct {
			*testResponseWriter
			unwrapCap
		}{base, unwrapCap{calls}}
	case 17:
		return struct {
			*testResponseWriter
			flushCap
			unwrapCap
		}{base, flushCap{calls}, unwrapCap{calls}}
	case 18:
		return struct {
			*testResponseWriter
			hijackCap
			unwrapCap
		}{base, hijackCap{calls}, unwrapCap{calls}}
	case 19:
		return struct {
			*testResponseWriter
			flushCap
			hijackCap
			unwrapCap
		}{base, flushCap{calls}, hijackCap{calls}, unwrapCap{calls}}
	case 20:
		return struct {
			*testResponseWriter
			pushCap
			unwrapCap
		}{base, pushCap{calls}, unwrapCap{calls}}
	case 21:
		return struct {
			*testResponseWriter
			flushCap
			pushCap
			unwrapCap
		}{base, flushCap{calls}, pushCap{calls}, unwrapCap{calls}}
	case 22:
		return struct {
			*testResponseWriter
			hijackCap
			pushCap
			unwrapCap
		}{base, hijackCap{calls}, pushCap{calls}, unwrapCap{calls}}
	case 23:
		return struct {
			*testResponseWriter
			flushCap
			hijackCap
			pushCap
			unwrapCap
		}{base, flushCap{calls}, hijackCap{calls}, pushCap{calls}, unwrapCap{calls}}
	case 24:
		return struct {
			*testResponseWriter
			readerFromCap
			unwrapCap
		}{base, readerFromCap{calls}, unwrapCap{calls}}
	case 25:
		return struct {
			*testResponseWriter
			flushCap
			readerFromCap
			unwrapCap
		}{base, flushCap{calls}, readerFromCap{calls}, unwrapCap{calls}}
	case 26:
		return struct {
			*testResponseWriter
			hijackCap
			readerFromCap
			unwrapCap
		}{base, hijackCap{calls}, readerFromCap{calls}, unwrapCap{calls}}
	case 27:
		return struct {
			*testResponseWriter
			flushCap
			hijackCap
			readerFromCap
			unwrapCap
		}{base, flushCap{calls}, hijackCap{calls}, readerFromCap{calls}, unwrapCap{calls}}
	case 28:
		return struct {
			*testResponseWriter
			pushCap
			readerFromCap
			unwrapCap
		}{base, pushCap{calls}, readerFromCap{calls}, unwrapCap{calls}}
	case 29:
		return struct {
			*testResponseWriter
			flushCap
			pushCap
			readerFromCap
			unwrapCap
		}{base, flushCap{calls}, pushCap{calls}, readerFromCap{calls}, unwrapCap{calls}}
	case 30:
		return struct {
			*testResponseWriter
			hijackCap
			pushCap
			readerFromCap
			unwrapCap
		}{base, hijackCap{calls}, pushCap{calls}, readerFromCap{calls}, unwrapCap{calls}}
	case 31:
		return struct {
			*testResponseWriter
			flushCap
			hijackCap
			pushCap
			readerFromCap
			unwrapCap
		}{base, flushCap{calls}, hijackCap{calls}, pushCap{calls}, readerFromCap{calls}, unwrapCap{calls}}
	default:
		return base
	}
}

func TestWrapStatusAndOutcomeRemainSeparate(t *testing.T) {
	cases := []struct {
		name        string
		handler     http.HandlerFunc
		wantStatus  int
		wantOutcome string
	}{
		{"panic before headers", func(http.ResponseWriter, *http.Request) { panic("boom") }, 0, "panic"},
		{"panic after headers", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202); panic("boom") }, 202, "panic"},
		{"normal implicit status", func(http.ResponseWriter, *http.Request) {}, 200, "completed"},
		{"explicit status", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(418) }, 418, "completed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New("test")
			req := httptest.NewRequest("GET", "http://localhost/api/secret-sentinel?query-sentinel=yes#fragment-sentinel", nil)
			req.Header.Set("X-Secret", "header-sentinel")
			rw := httptest.NewRecorder()
			func() { defer func() { _ = recover() }(); m.Wrap(tc.handler).ServeHTTP(rw, req) }()
			got := m.Snapshot().Requests[0]
			if got.Status != tc.wantStatus || got.Outcome != tc.wantOutcome || got.Active {
				t.Fatalf("request = %+v", got)
			}
			if got.Path != "/api/:id" {
				t.Fatalf("path retained unsafe segment: %q", got.Path)
			}
			snapshot := fmt.Sprintf("%+v", m.Snapshot())
			for _, secret := range []string{"secret-sentinel", "query-sentinel", "fragment-sentinel", "header-sentinel", "boom"} {
				if strings.Contains(snapshot, secret) {
					t.Fatalf("retained %q in %s", secret, snapshot)
				}
			}
		})
	}
}

func TestLoopbackActiveAndDeniedRequests(t *testing.T) {
	m := New("local")
	started := make(chan struct{})
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stream" {
			w.Header().Set("X-Secret", "header-sentinel")
			_, _ = w.Write([]byte("body-sentinel"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			close(started)
			<-release
			return
		}
		http.Error(w, "body-sentinel", http.StatusForbidden)
	})
	srv := httptest.NewServer(m.Wrap(handler))
	defer srv.Close()
	m.Live(srv.URL + "/secret-sentinel?base-query=secret")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.Get(srv.URL + "/api/stream?query-sentinel=secret")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	<-started
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/path-sentinel?query-sentinel=secret#fragment-sentinel", strings.NewReader("body-sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Secret", "header-sentinel")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	snap := m.Snapshot()
	if snap.Active != 1 || snap.State != "live" {
		t.Fatalf("active snapshot = %+v", snap)
	}
	close(release)
	<-done
	snap = m.Snapshot()
	if snap.Active != 0 || snap.Total != 2 || snap.Failures != 1 {
		t.Fatalf("final snapshot = %+v", snap)
	}
	foundForbidden, foundStream := false, false
	for _, req := range snap.Requests {
		if req.Status == 403 && req.Outcome == "completed" && req.Path == "/:id" {
			foundForbidden = true
		}
		if req.Path == "/api/stream" && req.Status == 200 {
			foundStream = true
		}
	}
	if !foundForbidden || !foundStream {
		t.Fatalf("request records = %+v", snap.Requests)
	}
	if snap.BaseURL != srv.URL {
		t.Fatalf("unsafe/unexpected base URL %q", snap.BaseURL)
	}
	retained := fmt.Sprintf("%+v", snap) + Render(snap, 100, 30, 0)
	for _, secret := range []string{"secret-sentinel", "query-sentinel", "fragment-sentinel", "header-sentinel", "body-sentinel"} {
		if strings.Contains(retained, secret) {
			t.Fatalf("retained %q", secret)
		}
	}
}

func TestOptionalResponseWriterInterfacesPreservedForEveryCombination(t *testing.T) {
	for mask := 0; mask < 32; mask++ {
		t.Run(fmt.Sprintf("mask-%02d", mask), func(t *testing.T) {
			leaf := newTestResponseWriter()
			calls := &optionalCalls{unwrapTarget: leaf}
			original := writerWithCapabilities(mask, leaf, calls)
			wrapped := (&response{ResponseWriter: original}).wrap()
			_, f := wrapped.(http.Flusher)
			_, h := wrapped.(http.Hijacker)
			_, p := wrapped.(http.Pusher)
			_, q := wrapped.(io.ReaderFrom)
			_, u := wrapped.(httpUnwrapper)
			got := 0
			if f {
				got |= 1
			}
			if h {
				got |= 2
			}
			if p {
				got |= 4
			}
			if q {
				got |= 8
			}
			if u {
				got |= 16
			}
			want := (mask & 15) | 16 // Unwrap is always exposed for ResponseController traversal.
			if got != want {
				t.Fatalf("capability bits got %05b want %05b", got, want)
			}
			if f {
				wrapped.(http.Flusher).Flush()
			}
			if p {
				if err := wrapped.(http.Pusher).Push("/x", nil); err != nil {
					t.Fatal(err)
				}
			}
			if q {
				_, err := wrapped.(io.ReaderFrom).ReadFrom(strings.NewReader("x"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if !u || wrapped.(httpUnwrapper).Unwrap() != original {
				t.Fatal("Unwrap did not return the immediate original writer")
			}
			if h {
				c, _, err := wrapped.(http.Hijacker).Hijack()
				if err != nil {
					t.Fatal(err)
				}
				_ = c.Close()
			}
			if (f && calls.flush != 1) || (p && calls.push != 1) || (q && calls.readFrom != 1) || (h && calls.hijack != 1) {
				t.Fatalf("optional calls = %+v", calls)
			}
		})
	}
}

func TestRegistryAdmissionAndOverlappingInstances(t *testing.T) {
	r := &Registry{}
	first := r.NewServer("first")
	first.Live("http://127.0.0.1:8091")
	second := r.NewServer("second")
	second.Live("http://127.0.0.1:8092")
	first.Stop(nil)
	snap := r.Snapshot()
	if snap.State != "live" || snap.BaseURL != "http://127.0.0.1:8092" {
		t.Fatalf("one stop affected other instance: %+v", snap)
	}
	first.Live("http://127.0.0.1:8091")
	for i := 0; i < MaxServers-2; i++ {
		m := r.NewServer(fmt.Sprintf("s%d", i))
		m.Live("http://127.0.0.1:9000")
	}
	denied, err := r.NewServerChecked("rejected")
	if err != ErrRegistryFull || denied.Snapshot().State != "error" {
		t.Fatalf("admission result %v %+v", err, denied.Snapshot())
	}
	fullSnapshot := r.Snapshot()
	if len(r.servers) != MaxServers || fullSnapshot.Active != 0 || fullSnapshot.State != "live" || fullSnapshot.Error != ErrRegistryFull.Error() {
		t.Fatalf("registry lost a live server: %d %+v", len(r.servers), r.Snapshot())
	}
	second.Stop(nil)
	replacement, err := r.NewServerChecked("replacement")
	if err != nil {
		t.Fatal(err)
	}
	replacement.Live("http://127.0.0.1:9090")
	if len(r.servers) != MaxServers {
		t.Fatalf("registry size = %d", len(r.servers))
	}
	if got := r.Snapshot().Error; got != "" {
		t.Fatalf("admission warning did not clear after capacity returned: %q", got)
	}
}

func TestRequestAndBucketEvictionAreBounded(t *testing.T) {
	m := New("bounded")
	var mu sync.Mutex
	now := time.Unix(0, 0)
	m.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	req := httptest.NewRequest("GET", "http://localhost/api/health", nil)
	for i := 0; i < MaxRequests+20; i++ {
		mu.Lock()
		now = now.Add(BucketDuration)
		mu.Unlock()
		state := m.begin(req)
		state.complete(200, "completed")
	}
	snap := m.Snapshot()
	if len(snap.Requests) != MaxRequests || snap.Requests[0].ID != 21 {
		t.Fatalf("request retention: %d first=%d", len(snap.Requests), snap.Requests[0].ID)
	}
	if len(m.value.Buckets) != BucketCount {
		t.Fatalf("bucket retention: %d", len(m.value.Buckets))
	}
	total := uint64(0)
	for _, b := range m.value.Buckets {
		total += b.Count
	}
	if total != BucketCount {
		t.Fatalf("bucket total retained = %d", total)
	}
}

func TestConcurrentWrapAndSnapshots(t *testing.T) {
	m := New("concurrent")
	h := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/health", nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h.ServeHTTP(httptest.NewRecorder(), req)
				_ = m.Snapshot()
			}
		}()
	}
	wg.Wait()
	s := m.Snapshot()
	if s.Total != 800 || s.Active != 0 || len(s.Requests) != MaxRequests {
		t.Fatalf("concurrent totals = total %d active %d requests %d", s.Total, s.Active, len(s.Requests))
	}
}

func TestSafeBaseURLDropsUserInfoAndNonLoopbackHosts(t *testing.T) {
	for _, raw := range []string{"http://user:password@127.0.0.1:8000/secret?token=value", "https://api.example.test/key"} {
		got := safeBaseURL(raw)
		if strings.Contains(got, "password") || strings.Contains(got, "secret") || strings.Contains(got, "token") || strings.Contains(got, "key") {
			t.Fatalf("unsafe base URL %q from %q", got, raw)
		}
	}
	if got := safeBaseURL("http://127.0.0.1:8000/path?token=secret"); got != "http://127.0.0.1:8000" {
		t.Fatalf("base URL = %q", got)
	}
}

type flushErrorWriter struct {
	*testResponseWriter
	flushErr        error
	flushErrorCalls int
	flushCalls      int
}

func (w *flushErrorWriter) FlushError() error { w.flushErrorCalls++; return w.flushErr }
func (w *flushErrorWriter) Flush()            { w.flushCalls++ }

type controllerTarget struct {
	*testResponseWriter
	readDeadline, writeDeadline time.Time
	fullDuplex                  bool
}

func (w *controllerTarget) SetReadDeadline(t time.Time) error  { w.readDeadline = t; return nil }
func (w *controllerTarget) SetWriteDeadline(t time.Time) error { w.writeDeadline = t; return nil }
func (w *controllerTarget) EnableFullDuplex() error            { w.fullDuplex = true; return nil }

type controllerUnwrapper struct {
	*testResponseWriter
	child http.ResponseWriter
}

func (w *controllerUnwrapper) Unwrap() http.ResponseWriter { return w.child }

func TestResponseControllerTraversesImmediateUnwrapAndFlushError(t *testing.T) {
	flushErr := fmt.Errorf("flush sentinel")
	flushWriter := &flushErrorWriter{testResponseWriter: newTestResponseWriter(), flushErr: flushErr}
	wrapped := (&response{ResponseWriter: flushWriter}).wrap()
	if err := http.NewResponseController(wrapped).Flush(); err != flushErr {
		t.Fatalf("Flush error = %v, want original %v", err, flushErr)
	}
	if flushWriter.flushErrorCalls != 1 || flushWriter.flushCalls != 0 {
		t.Fatalf("flush calls = error %d, plain %d", flushWriter.flushErrorCalls, flushWriter.flushCalls)
	}

	target := &controllerTarget{testResponseWriter: newTestResponseWriter()}
	original := &controllerUnwrapper{testResponseWriter: newTestResponseWriter(), child: target}
	proxy := (&response{ResponseWriter: original}).wrap()
	if got := proxy.(httpUnwrapper).Unwrap(); got != original {
		t.Fatal("proxy did not expose immediate writer")
	}
	controller := http.NewResponseController(proxy)
	readAt, writeAt := time.Unix(10, 0), time.Unix(20, 0)
	if err := controller.SetReadDeadline(readAt); err != nil {
		t.Fatal(err)
	}
	if err := controller.SetWriteDeadline(writeAt); err != nil {
		t.Fatal(err)
	}
	if err := controller.EnableFullDuplex(); err != nil {
		t.Fatal(err)
	}
	if !target.readDeadline.Equal(readAt) || !target.writeDeadline.Equal(writeAt) || !target.fullDuplex {
		t.Fatalf("controller capabilities did not reach leaf: %+v", target)
	}
}

type hijackResponseWriter struct{ *testResponseWriter }

func (w *hijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	server, client := net.Pipe()
	_ = client.Close()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

func TestHijackedRequestRemainsActiveUntilConnectionClose(t *testing.T) {
	m := New("hijack")
	base := &hijackResponseWriter{newTestResponseWriter()}
	transferred := make(chan net.Conn, 1)
	m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatal(err)
		}
		transferred <- conn
	})).ServeHTTP(base, httptest.NewRequest("GET", "http://localhost/api/stream", nil))
	conn := <-transferred
	active := m.Snapshot()
	if active.Active != 1 || active.Requests[0].Outcome != "hijacked" || !active.Requests[0].Active || active.Requests[0].Status != http.StatusSwitchingProtocols {
		t.Fatalf("after handler return = %+v", active)
	}
	closed := make(chan struct{})
	go func() { defer close(closed); _ = conn.Close() }()
	<-closed
	final := m.Snapshot()
	if final.Active != 0 || final.Requests[0].Outcome != "hijacked" || final.Requests[0].Active || final.Requests[0].Duration < 0 {
		t.Fatalf("after conn close = %+v", final)
	}
}

func TestPanicAfterHijackRemainsFailureAndRetainsStatus(t *testing.T) {
	m := New("hijack-panic")
	base := &hijackResponseWriter{newTestResponseWriter()}
	transferred := make(chan net.Conn, 1)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected handler panic")
			}
		}()
		m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusSwitchingProtocols)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				panic(err)
			}
			transferred <- conn
			panic("panic sentinel")
		})).ServeHTTP(base, httptest.NewRequest("GET", "http://localhost/api/stream", nil))
	}()
	conn := <-transferred
	active := m.Snapshot()
	if active.Active != 1 || active.Failures != 1 || active.Requests[0].Outcome != "panic" || active.Requests[0].Status != http.StatusSwitchingProtocols {
		t.Fatalf("after panic = %+v", active)
	}
	_ = conn.Close()
	final := m.Snapshot()
	if final.Active != 0 || final.Failures != 1 || final.Requests[0].Outcome != "panic" || final.Requests[0].Status != http.StatusSwitchingProtocols {
		t.Fatalf("after close = %+v", final)
	}
}

func TestPrivacyRedactsRawBracesAndUnknownMethodEverywhere(t *testing.T) {
	m := New("privacy")
	req := httptest.NewRequest("GET", "http://localhost/api/{SECRETPATH}?SECRETTOKEN=query", nil)
	req.Method = "SECRETTOKEN"
	m.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), req)
	snap := m.Snapshot()
	if snap.Requests[0].Path != "/api/:id" || snap.Requests[0].Method != "OTHER" {
		t.Fatalf("unsafe request metadata: %+v", snap.Requests[0])
	}
	retained := fmt.Sprintf("%+v", snap) + Render(snap, 100, 30, 0)
	for _, secret := range []string{"SECRETTOKEN", "SECRETPATH", "query"} {
		if strings.Contains(retained, secret) {
			t.Fatalf("retained %q: %s", secret, retained)
		}
	}

	routeRequest := httptest.NewRequest("GET", "http://localhost/api/value", nil)
	routeRequest.Pattern = "GET /api/{safeName}"
	if got := safePath(routeRequest); got != "/api/:safeName" {
		t.Fatalf("route metadata path = %q", got)
	}
}

func TestPendingRegistrySlotsAreReservedDuringConcurrentBind(t *testing.T) {
	r := &Registry{}
	servers := make([]*Monitor, MaxServers)
	for i := range servers {
		servers[i] = r.NewServer(fmt.Sprintf("pending-%d", i))
	}
	if got := r.Snapshot().State; got != "starting" {
		t.Fatalf("pending aggregate state = %q", got)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	var denied *Monitor
	go func() { defer wg.Done(); servers[0].Live("http://127.0.0.1:8000") }()
	go func() { defer wg.Done(); denied = r.NewServer("concurrent") }()
	wg.Wait()
	if denied == nil || denied.admitted {
		t.Fatalf("full registry admitted extra server: %+v", denied)
	}
	if len(r.servers) != MaxServers {
		t.Fatalf("pending registry size changed: %d", len(r.servers))
	}
	for _, m := range servers {
		if m == servers[0] {
			continue
		}
		if m.Snapshot().State != "starting" {
			t.Fatalf("pending monitor was evicted or changed: %+v", m.Snapshot())
		}
	}
}

type unwrapOnlyWriter struct {
	http.ResponseWriter
	next http.ResponseWriter
}

func (w *unwrapOnlyWriter) Unwrap() http.ResponseWriter { return w.next }

type flushCommitWriter struct {
	*testResponseWriter
	writeHeaderCalls int
	flushErrorCalls  int
}

func (w *flushCommitWriter) WriteHeader(code int) {
	w.writeHeaderCalls++
	w.testResponseWriter.WriteHeader(code)
}
func (w *flushCommitWriter) FlushError() error {
	w.flushErrorCalls++
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return nil
}
func (w *flushCommitWriter) Flush() {}

func TestResponseControllerHijackThroughUnwrapChainRemainsTracked(t *testing.T) {
	m := New("nested-hijack")
	now := time.Unix(100, 0)
	m.now = func() time.Time { return now }
	leaf := &hijackResponseWriter{newTestResponseWriter()}
	middle := &unwrapOnlyWriter{ResponseWriter: leaf, next: leaf}
	original := &unwrapOnlyWriter{ResponseWriter: middle, next: middle}
	transferred := make(chan net.Conn, 1)
	m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("ResponseController.Hijack: %v", err)
			return
		}
		transferred <- conn
	})).ServeHTTP(original, httptest.NewRequest("GET", "http://localhost/api/stream", nil))
	conn := <-transferred
	active := m.Snapshot()
	if active.Active != 1 || active.Requests[0].Outcome != "hijacked" || !active.Requests[0].Active {
		t.Fatalf("controller hijack was not recorded: %+v", active)
	}
	now = now.Add(time.Second)
	_ = conn.Close()
	closed := m.Snapshot()
	if closed.Active != 0 || closed.Requests[0].Outcome != "hijacked" || closed.Requests[0].Duration != time.Second {
		t.Fatalf("first close snapshot = %+v", closed)
	}
	now = now.Add(5 * time.Second)
	_ = conn.Close()
	repeated := m.Snapshot()
	if repeated.Duration != closed.Duration || repeated.Requests[0].Duration != time.Second {
		t.Fatalf("repeated close changed finalized duration: first=%+v repeated=%+v", closed, repeated)
	}
}

func TestResponseControllerSuccessfulFlushErrorDoesNotForwardSecondStatus(t *testing.T) {
	writer := &flushCommitWriter{testResponseWriter: newTestResponseWriter()}
	wrapped := (&response{ResponseWriter: writer}).wrap()
	if err := http.NewResponseController(wrapped).Flush(); err != nil {
		t.Fatal(err)
	}
	if writer.flushErrorCalls != 1 || writer.writeHeaderCalls != 1 || writer.status != http.StatusOK {
		t.Fatalf("flush status forwarding: calls=%d flush=%d status=%d", writer.writeHeaderCalls, writer.flushErrorCalls, writer.status)
	}
}

func TestResponseControllerFlushThroughUnwrapRecordsCommittedStatus(t *testing.T) {
	m := New("nested-flush")
	recorder := &flushCommitWriter{testResponseWriter: newTestResponseWriter()}
	leaf := http.ResponseWriter(recorder)
	middle := &unwrapOnlyWriter{ResponseWriter: leaf, next: leaf}
	original := &unwrapOnlyWriter{ResponseWriter: middle, next: middle}
	m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController.Flush: %v", err)
		}
		w.WriteHeader(http.StatusTeapot)
	})).ServeHTTP(original, httptest.NewRequest("GET", "http://localhost/api/flush", nil))
	snapshot := m.Snapshot()
	request := snapshot.Requests[0]
	if recorder.status != http.StatusOK || recorder.writeHeaderCalls != 1 || request.Status != http.StatusOK || request.Outcome != "completed" || snapshot.Failures != 0 {
		t.Fatalf("nested flush status diverged: actual=%d writes=%d request=%+v snapshot=%+v", recorder.status, recorder.writeHeaderCalls, request, snapshot)
	}
}

func TestResponseControllerFlushErrorThroughUnwrapPreservesError(t *testing.T) {
	flushErr := fmt.Errorf("nested flush sentinel")
	leaf := &flushErrorWriter{testResponseWriter: newTestResponseWriter(), flushErr: flushErr}
	middle := &unwrapOnlyWriter{ResponseWriter: leaf, next: leaf}
	original := &unwrapOnlyWriter{ResponseWriter: middle, next: middle}
	wrapped := (&response{ResponseWriter: original}).wrap()
	if err := http.NewResponseController(wrapped).Flush(); err != flushErr {
		t.Fatalf("nested FlushError = %v, want %v", err, flushErr)
	}
	if leaf.flushErrorCalls != 1 || leaf.flushCalls != 0 {
		t.Fatalf("nested flush calls = error %d, plain %d", leaf.flushErrorCalls, leaf.flushCalls)
	}
}

type cyclicUnwrapWriter struct {
	http.ResponseWriter
	calls int
}

func (w *cyclicUnwrapWriter) Unwrap() http.ResponseWriter { w.calls++; return w }

func TestCapabilityDiscoveryBoundsCyclicUnwrapChain(t *testing.T) {
	cycle := &cyclicUnwrapWriter{ResponseWriter: httptest.NewRecorder()}
	wrapped := (&response{ResponseWriter: cycle}).wrap()
	if _, ok := wrapped.(httpFlushError); !ok {
		t.Fatal("indeterminate Flush capability was not intercepted")
	}
	if _, ok := wrapped.(http.Hijacker); !ok {
		t.Fatal("indeterminate Hijack capability was not intercepted")
	}
	if err := http.NewResponseController(wrapped).Flush(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("cyclic Flush error = %v", err)
	}
	if _, _, err := http.NewResponseController(wrapped).Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("cyclic Hijack error = %v", err)
	}
	if cycle.calls > 128 {
		t.Fatalf("capability discovery did not remain bounded: %d unwraps", cycle.calls)
	}
}

func TestCapabilityDiscoveryRefusesUnresolvedDeepUnwrapChain(t *testing.T) {
	leaf := &hijackResponseWriter{newTestResponseWriter()}
	var chain http.ResponseWriter = leaf
	for i := 0; i < 64; i++ {
		chain = &unwrapOnlyWriter{ResponseWriter: chain, next: chain}
	}
	wrapped := (&response{ResponseWriter: chain}).wrap()
	controller := http.NewResponseController(wrapped)
	if _, _, err := controller.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("deep Hijack error = %v", err)
	}
	if _, ok := wrapped.(httpFlushError); !ok {
		t.Fatal("deep Flush was not intercepted")
	}
	if err := controller.Flush(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("deep Flush error = %v", err)
	}
}
