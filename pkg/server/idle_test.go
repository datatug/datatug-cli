package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
)

func TestIdleWatcher(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Unix(1000, 0).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	advance := func(d time.Duration) { clock.Add(int64(d)) }
	w := newIdleWatcher(time.Minute, now)
	if w.idle() {
		t.Fatal("fresh watcher is not idle")
	}
	advance(time.Minute)
	if !w.idle() {
		t.Fatal("idle after the timeout")
	}
	served := false
	h := w.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { served = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !served || w.idle() {
		t.Fatal("a request must be served and count as activity")
	}

	tick, done := make(chan time.Time), make(chan struct{})
	shutdowns := make(chan struct{}, 2)
	finished := make(chan struct{})
	go func() { w.watch(done, tick, func() { shutdowns <- struct{}{} }); close(finished) }()
	tick <- now() // not idle: keeps watching
	advance(time.Hour)
	tick <- now() // idle: one shutdown, then the watcher returns
	<-finished
	if len(shutdowns) != 1 {
		t.Fatalf("shutdowns = %d, want 1", len(shutdowns))
	}

	stopped := make(chan struct{})
	done2 := make(chan struct{})
	go func() { w.watch(done2, tick, func() { t.Error("unexpected shutdown") }); close(stopped) }()
	close(done2)
	<-stopped
}

func TestServeHTTPIdleTimeoutShutsDown(t *testing.T) {
	origListen, origInterval := listenAndServeFn, idleCheckInterval
	t.Cleanup(func() { listenAndServeFn, idleCheckInterval = origListen, origInterval })
	idleCheckInterval = 5 * time.Millisecond
	listenAndServeFn = func(srv *http.Server) error {
		down := make(chan struct{})
		srv.RegisterOnShutdown(func() { close(down) })
		select {
		case <-down:
			return http.ErrServerClosed
		case <-time.After(5 * time.Second):
			t.Error("idle watcher never shut the server down")
			return nil
		}
	}
	s := NewHttpServer()
	s.IdleTimeout = 20 * time.Millisecond
	if err := s.ServeHTTP(nil, "127.0.0.1", 8989, unrestrictedTestSession(t), api.Capabilities{}); err != nil {
		t.Fatalf("an idle shutdown is a clean exit, got %v", err)
	}
}
