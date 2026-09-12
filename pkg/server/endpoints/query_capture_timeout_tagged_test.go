//go:build darwin || linux

package endpoints

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

// holdQueryStoreLock takes the revisioned store's cross-process lock the
// way another process would, until release or the end of the test.
func holdQueryStoreLock(t *testing.T, queriesDir string) (release func()) {
	t.Helper()
	f, err := os.Open(filepath.Join(queriesDir, ".dt-query-txn", "lock"))
	if err != nil {
		t.Fatalf("open the store lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold the store lock: %v", err)
	}
	released := false
	release = func() {
		if !released {
			released = true
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
		}
	}
	t.Cleanup(release)
	return release
}

// postCaptureWithin posts req and fails the test, rather than hanging it,
// when the handler does not answer within limit.
func postCaptureWithin(t *testing.T, req captureQueryRequest, limit time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postCapture(t, req) }()
	select {
	case w := <-done:
		return w
	case <-time.After(limit):
		t.Fatalf("queries/capture did not answer within %s", limit)
		return nil
	}
}

// A capture that meets a store lock another writer holds answers 504
// TIMEOUT after captureWriteTimeout, having written nothing - never an
// unbounded wait.
func TestCaptureQuery_HeldStoreLockTimesOut(t *testing.T) {
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	req := validCaptureRequest(scope)
	if w := postCapture(t, req); w.Code != http.StatusCreated {
		t.Fatalf("setup capture: status %d; body %s", w.Code, w.Body.String())
	}
	saved := captureWriteTimeout
	captureWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { captureWriteTimeout = saved })

	release := holdQueryStoreLock(t, filepath.Join(projectDir, "queries"))
	before := treeDigest(t, projectDir)
	another := req
	another.Query.ID = "another-captured-query"
	start := time.Now()
	w := postCaptureWithin(t, another, 10*time.Second)
	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504; body %s", w.Code, w.Body.String())
	}
	if env := decodeCaptureError(t, w); env.Error.Code != "TIMEOUT" {
		t.Errorf("code %q, want TIMEOUT", env.Error.Code)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("answered after %s; the write bound is %s", elapsed, captureWriteTimeout)
	}
	if after := treeDigest(t, projectDir); !reflect.DeepEqual(before, after) {
		t.Errorf("a timed-out capture changed the project tree")
	}

	release()
	if w := postCaptureWithin(t, another, 10*time.Second); w.Code != http.StatusCreated {
		t.Fatalf("retry after the lock is released: status %d; body %s", w.Code, w.Body.String())
	}
}
