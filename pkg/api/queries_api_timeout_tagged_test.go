//go:build datatug_query_capture && (darwin || linux)

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdStoreLock takes the revisioned store's cross-process lock the way
// another process would, for the rest of the test.
func holdStoreLock(t *testing.T, queriesDir string) {
	t.Helper()
	f, err := os.Open(filepath.Join(queriesDir, ".dt-query-txn", "lock"))
	if err != nil {
		t.Fatalf("open the store lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold the store lock: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
}

// within runs call and fails the test, rather than hanging it, when it does
// not return within limit.
func within(t *testing.T, limit time.Duration, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(limit):
		t.Fatalf("the write did not return within %s", limit)
		return nil
	}
}

// A legacy write that meets a store lock another writer holds gives up
// after legacyWriteTimeout with a timeout error and writes nothing.
func TestLegacyWrites_HeldStoreLockTimesOut(t *testing.T) {
	projectID, queriesDir := servedWritableProject(t, adminSession(t), true)
	if _, err := CreateQuery(context.Background(), createRequest(projectID, legacyQuery("", "q1"))); err != nil {
		t.Fatalf("setup create: %v", err)
	}
	path := filepath.Join(queriesDir, "q1.query.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := legacyWriteTimeout
	legacyWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { legacyWriteTimeout = saved })
	holdStoreLock(t, queriesDir)

	changed := legacyQuery("", "q1")
	changed.Title = "changed"
	calls := map[string]func() error{
		"update": func() error {
			_, err := UpdateQuery(context.Background(), updateRequest(projectID, "q1", changed))
			return err
		},
		"delete": func() error { return DeleteQuery(context.Background(), deleteRef(projectID, "q1")) },
	}
	for name, call := range calls {
		start := time.Now()
		err := within(t, 10*time.Second, call)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "nothing was saved") {
			t.Errorf("%s: expected a timeout saying nothing was saved, got %v", name, err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("%s: returned after %s; the write bound is %s", name, elapsed, legacyWriteTimeout)
		}
		assertFileContent(t, path, string(content))
	}
}
