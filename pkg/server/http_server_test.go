package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// unrestrictedTestSession is a valid, --no-policies-equivalent
// secureread.Session for tests that only exercise unrelated server plumbing
// (ping, agent-info, projects_summary) and are not themselves about access
// policy enforcement.
func unrestrictedTestSession(t *testing.T) secureread.Session {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession(NoPolicies): %v", err)
	}
	return session
}

// TestNewDatatugStoreFactory replaces the old recover()-around-the-panic
// coverage: storage.NewDatatugStore used to be wired to a closure that always
// panicked with "implement me" (pkg/server/http_server.go), so any test that
// exercised it had to recover from that panic instead of asserting real
// behaviour. It now returns a working filestore-backed storage.Store.
func TestNewDatatugStoreFactory(t *testing.T) {
	projDir := t.TempDir()
	projectFile := filepath.Join(projDir, "datatug-project.json")
	if err := os.WriteFile(projectFile, []byte(`{"id":"proj1","title":"Project One"}`), 0o600); err != nil {
		t.Fatalf("failed to write fixture project file: %v", err)
	}
	pathsByID := map[string]string{"proj1": projDir}
	factory := newDatatugStoreFactory(pathsByID)

	store, err := factory("proj1")
	if err != nil {
		t.Fatalf("factory(%q) returned error: %v", "proj1", err)
	}
	if store == nil {
		t.Fatal("factory returned a nil store")
	}

	ctx := context.Background()
	briefs, err := store.GetProjects(ctx)
	if err != nil {
		t.Fatalf("store.GetProjects() returned error: %v", err)
	}
	if len(briefs) != 1 {
		t.Fatalf("expected 1 project brief, got %d", len(briefs))
	}
}

// demoProjectDir resolves the read-only demo project fixture used across the
// datatug repos on this machine. It is not vendored into datatug-cli, so CI
// runners (and any other checkout without a sibling datatug-demo-projects
// clone) skip the test instead of failing.
func demoProjectDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot resolve home dir: %v", err)
	}
	dir := filepath.Join(home, "projects", "datatug", "datatug-demo-projects", "demo-project-1")
	if _, err := os.Stat(filepath.Join(dir, "datatug-project.json")); err != nil {
		t.Skipf("demo project fixture not present at %s (expected on the dev VM, not in CI): %v", dir, err)
	}
	return dir
}

// freeTCPPort asks the OS for an ephemeral port, then releases it so
// ServeHTTP (which only accepts host/port, not a pre-bound listener) can bind
// it a moment later.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free TCP port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("failed to close probe listener: %v", err)
	}
	return port
}

// testHTTPClient is used for every HTTP request this package's ServeHTTP
// tests make against a server startServeHTTPWithSession started, instead of
// http.DefaultClient/http.Get/http.Post. http.DefaultClient's Transport
// keeps a completed connection open for keep-alive reuse; the server's
// graceful net/http.Server.Shutdown (called from every such test's
// t.Cleanup) can only finish once every connection it tracks has become
// idle from BOTH sides. TestServeHTTP_ProjectSummary_ReturnsSummary failed
// once in CI with "Shutdown: context deadline exceeded" and passed
// immediately on rerun - the signature of a race in that idle-connection
// bookkeeping, not a real, reproducible hang (Go's own httptest package
// works around the identical class of race by tracking and force-closing
// every client connection itself on Close, rather than trusting Shutdown's
// idle-connection handling alone - see httptest.Server.Close). Disabling
// keep-alives here means every response closes its connection as soon as
// it is read, so Shutdown never has anything to wait on from the client
// side at all.
var testHTTPClient = &http.Client{
	Transport: &http.Transport{DisableKeepAlives: true},
}

func waitForServer(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := testHTTPClient.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready: %v", url, lastErr)
}

// startServeHTTP starts a real HTTP server on a free port serving pathsByID,
// returning the base URL once it is accepting connections and registering
// cleanup (graceful shutdown) via t.Cleanup.
func startServeHTTP(t *testing.T, pathsByID map[string]string) string {
	t.Helper()
	return startServeHTTPWithSession(t, pathsByID, unrestrictedTestSession(t))
}

// startServeHTTPWithSession is startServeHTTP with an explicit
// secureread.Session and every write/opaque-SQL capability off, for tests
// that exercise policy enforcement but not the write-capability gate itself
// (see write_capability_test.go for that).
func startServeHTTPWithSession(t *testing.T, pathsByID map[string]string, session secureread.Session) string {
	t.Helper()
	return startServeHTTPWithSessionAndCapabilities(t, pathsByID, session, api.Capabilities{})
}

// startServeHTTPWithSessionAndCapabilities is startServeHTTPWithSession with
// an explicit api.Capabilities, for tests that exercise --allow-writes/
// --allow-opaque-sql themselves.
func startServeHTTPWithSessionAndCapabilities(t *testing.T, pathsByID map[string]string, session secureread.Session, caps api.Capabilities) string {
	t.Helper()
	port := freeTCPPort(t)

	httpServer := NewHttpServer()
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- httpServer.ServeHTTP(pathsByID, "127.0.0.1", port, session, caps)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		if err := <-serveErrCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("ServeHTTP: %v", err)
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForServer(t, baseURL+"/datatug/ping")
	return baseURL
}

// assertPingAndAgentInfo proves the store wiring (item 2) and the real
// Handler (apicore.Execute, item 2) no longer panic: it replaces the old
// recover()-around-the-panic style coverage with real assertions against a
// live server.
func assertPingAndAgentInfo(t *testing.T, baseURL string) {
	t.Helper()

	t.Run("ping", func(t *testing.T) {
		resp, err := testHTTPClient.Get(baseURL + "/datatug/ping")
		if err != nil {
			t.Fatalf("GET /datatug/ping: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != "pong" {
			t.Fatalf("expected body %q, got %q", "pong", string(body))
		}
	})

	t.Run("agent-info", func(t *testing.T) {
		resp, err := testHTTPClient.Get(baseURL + "/datatug/agent-info")
		if err != nil {
			t.Fatalf("GET /datatug/agent-info: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var info struct {
			Version       string  `json:"version"`
			UptimeMinutes float64 `json:"uptimeMinutes"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if info.Version == "" {
			t.Fatal("expected a non-empty agent version")
		}
	})
}

// TestServeHTTP_PingAndAgentInfo starts a real server with a generated
// project registered (mirroring `datatug serve --project <dir>`) so this
// runs everywhere, including CI, without depending on a sibling
// datatug-demo-projects checkout.
func TestServeHTTP_PingAndAgentInfo(t *testing.T) {
	projDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projDir, "datatug-project.json"), []byte(`{"id":"synthetic-project","title":"Synthetic Project"}`), 0o600); err != nil {
		t.Fatalf("failed to write fixture project file: %v", err)
	}
	baseURL := startServeHTTP(t, map[string]string{"synthetic-project": projDir})
	assertPingAndAgentInfo(t, baseURL)

	t.Run("projects_summary uses the real store wiring", func(t *testing.T) {
		resp, err := testHTTPClient.Get(baseURL + "/datatug/projects/projects_summary?storage=files")
		if err != nil {
			t.Fatalf("GET /datatug/projects/projects_summary: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var briefs []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&briefs); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(briefs) != 1 || briefs[0].ID != "synthetic-project" {
			t.Fatalf("expected the synthetic project brief, got %+v", briefs)
		}
	})
}

// TestServeHTTP_PingAndAgentInfoWithDemoProject is the brief's literal
// acceptance check: it starts the server on a free port with the real demo
// project (~/projects/datatug/datatug-demo-projects/demo-project-1)
// registered and GETs /datatug/ping and /datatug/agent-info. It skips on any
// checkout without that sibling clone (CI included) - see demoProjectDir.
func TestServeHTTP_PingAndAgentInfoWithDemoProject(t *testing.T) {
	dir := demoProjectDir(t)
	baseURL := startServeHTTP(t, map[string]string{"datatug-demo-project": dir})
	assertPingAndAgentInfo(t, baseURL)
}
