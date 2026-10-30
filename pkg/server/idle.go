package server

import (
	"net/http"
	"sync/atomic"
	"time"
)

// idleCheckInterval is how often an idle watcher looks at the clock.
var idleCheckInterval = time.Second

// idleWatcher shuts a server down once timeout has passed with no request
// (spec/features/cli/daemon#req:idle-lifetime). Every request, including the
// daemon's own status probes, counts as activity.
type idleWatcher struct {
	timeout time.Duration
	now     func() time.Time
	last    atomic.Int64 // unix nanoseconds of the latest request
}

func newIdleWatcher(timeout time.Duration, now func() time.Time) *idleWatcher {
	w := &idleWatcher{timeout: timeout, now: now}
	w.touch()
	return w
}

func (w *idleWatcher) touch() { w.last.Store(w.now().UnixNano()) }

func (w *idleWatcher) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.touch()
		next.ServeHTTP(rw, r)
	})
}

func (w *idleWatcher) idle() bool {
	return w.now().Sub(time.Unix(0, w.last.Load())) >= w.timeout
}

// watch calls shutdown once, on the first tick at which the server is idle, or
// returns when done is closed.
func (w *idleWatcher) watch(done <-chan struct{}, tick <-chan time.Time, shutdown func()) {
	for {
		select {
		case <-done:
			return
		case <-tick:
			if w.idle() {
				shutdown()
				return
			}
		}
	}
}
