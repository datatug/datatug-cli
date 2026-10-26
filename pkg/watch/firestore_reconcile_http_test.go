package watch_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/watch"
	"github.com/datatug/datatug-cli/pkg/watchdaemon"
)

func TestFirestoreReconcileStaleReadyConcurrentLifecycleHTTP(t *testing.T) {
	f := watch.NewStaleReconcileHTTPTestFixture(t)
	const token = "owned-stale-reconcile-test-token"
	t.Setenv("WATCH_STALE_RECONCILE_TEST_TOKEN", token)
	auth, err := watchdaemon.NewAuthenticator([]watchdaemon.Credential{{TokenEnv: "WATCH_STALE_RECONCILE_TEST_TOKEN", Kind: "user", ID: f.Actor.ID, Namespace: f.Actor.Scope.Namespace, Project: f.Actor.Scope.Project, Environment: f.Actor.Scope.Environment, SharedEditor: true}})
	if err != nil {
		t.Fatal(err)
	}
	server := &watchdaemon.Server{Store: f.Editor, Auth: auth}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Release before the real handler runs: do not hold a transaction open
		// waiting for a writer that may need its Firestore locks.
		f.Release()
		server.ServeHTTP(w, r)
	}))
	t.Cleanup(host.Close)
	t.Logf("STALE_READY_HTTP endpoint=%s cleanup=httptest.Server.Close", host.URL)
	done := make(chan error, 1)
	go func() { _, e := f.Coordinator.ReconcileScope(t.Context(), f.Actor.Scope, 20); done <- e; close(done) }()
	defer func() { f.Release(); _ = <-done }()
	select {
	case <-f.Admitted:
	case e := <-done:
		t.Fatalf("coordinator ended before fresh admission: %v", e)
	case <-time.After(10 * time.Second):
		t.Fatal("coordinator did not reach fresh admission inside its unchanged budget")
	}
	body, err := json.Marshal(map[string]any{"id": f.Definition.ID, "lifecycle": "paused", "operationId": "pause-concurrent-stale", "expectedVersion": f.Definition.Version})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, host.URL+"/v1/watch/lifecycle", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result struct {
		Definition watch.Definition `json:"definition"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK || result.Definition.Lifecycle != "paused" || result.Definition.Version != f.Definition.Version+1 {
		t.Fatalf("real lifecycle HTTP/business result: status=%d version=%d lifecycle=%q err=%v", response.StatusCode, result.Definition.Version, result.Definition.Lifecycle, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	f.Assert()
	current, err := f.Editor.GetDefinition(t.Context(), f.Actor, f.Definition.ID)
	if err != nil || current.Version != result.Definition.Version || current.Lifecycle != "paused" {
		t.Fatal("HTTP success did not match durable lifecycle state", err)
	}
	events, err := f.Editor.Audit(t.Context(), f.Actor, f.Definition.ID)
	if err != nil || len(events) != 2 {
		t.Fatalf("creation/lifecycle audit changed: count=%d err=%v", len(events), err)
	}
}
