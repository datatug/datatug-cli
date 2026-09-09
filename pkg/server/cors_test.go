package server

import (
	"net/http"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// getWithOrigin GETs requestURL with an explicit Origin header set, the way
// a real cross-origin browser fetch (rather than net/http's own client,
// which never sets one) would.
func getWithOrigin(t *testing.T, requestURL, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", origin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s (Origin: %s): %v", requestURL, origin, err)
	}
	return resp
}

// TestServeHTTP_CORS_DatatugApp is the regression test for half of lane
// C5's finding (datatug-apps PR #59, verified with curl): the agent's
// origin check (sneat-go-core/security.VerifyOrigin, invoked by every
// apicore.Execute request via httpserver.AccessControlAllowOrigin) never
// knew about the production "https://datatug.app" web UI — cmd_serve.go's
// serveAgentURLs prints exactly that URL and expects to reach this agent
// directly from the browser, and it was refused with "bad origin".
// ServeHTTP now calls security.AddKnownHosts("datatug.app") at startup
// (http_server.go).
//
// The other half of that finding — plain http://127.0.0.1:<port> as an
// origin, rather than http://localhost:<port> (which already worked
// unconditionally: security.IsLocalhostHost matches "localhost" at any
// port) — turned out to need a sneat-go-core change to fix for real
// (AddKnownHosts's own addKnownOrigins only ever adds the http:// variant
// when IsLocalhostHost(host) is true, and that function never recognizes
// "127.0.0.1" as a loopback synonym of "localhost"); see the doc comment on
// the security.AddKnownHosts call in http_server.go. Not attempted here —
// flagged as a follow-up instead, since it needs either an upstream fix or
// a local override of the swappable apicore.VerifyRequest var, a bigger
// change than this stream's actual numbered task list called for.
//
// This exercises /datatug/projects/project_summary rather than
// /datatug/ping: AccessControlAllowOrigin only runs inside
// apicore.Execute's VerifyRequest, and Ping (like a few other simple
// handlers) is wired directly, bypassing Execute entirely — it would pass
// regardless of this fix.
func TestServeHTTP_CORS_DatatugApp(t *testing.T) {
	const projectID = "cors-project"
	pathsByID := authHookProjectFixture(t, projectID)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "agent1", NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	summaryURL := baseURL + "/datatug/projects/project_summary?id=" + projectID

	t.Run("https://datatug.app is allowed", func(t *testing.T) {
		resp := getWithOrigin(t, summaryURL, "https://datatug.app")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://datatug.app" {
			t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "https://datatug.app")
		}
	})

	t.Run("an unrelated origin is still refused", func(t *testing.T) {
		resp := getWithOrigin(t, summaryURL, "https://evil.example.com")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (the origin check must still refuse strangers)", resp.StatusCode)
		}
	})
}

// The OPTIONS-preflight side of the same fix (globalOptionsHandler ->
// endpoints.IsSupportedOrigin, a datatug-cli-owned check separate from
// sneat-go-core's security.VerifyOrigin tested above) is covered directly
// in pkg/server/endpoints/validators_test.go (TestIsSupportedOrigin), which
// now also asserts http(s)://127.0.0.1:<port> — a real browser
// POST/PUT/DELETE with a JSON body is preflighted, and that OPTIONS request
// must pass this check too before the browser ever sends the real request
// this file's tests cover.
