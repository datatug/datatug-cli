package server

import (
	"github.com/datatug/datatug-cli/pkg/api"
	"net/http"
	"os"
	"path/filepath"
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
	resp, err := testHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s (Origin: %s): %v", requestURL, origin, err)
	}
	return resp
}

func optionsWithOrigin(t *testing.T, requestURL, origin, method string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, requestURL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", method)
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	resp, err := testHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS %s (Origin: %s): %v", requestURL, origin, err)
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
// Local metadata execution now uses the same strict origin predicate as the
// request guard and newer contract routes. Authentication/content checks remain
// delegated to apicore.Execute.
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

	t.Run("https://app.incidentius.com is allowed", func(t *testing.T) {
		resp := getWithOrigin(t, summaryURL, "https://app.incidentius.com")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.incidentius.com" {
			t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "https://app.incidentius.com")
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

func TestServeHTTP_CORS_IncidentiusPreflight(t *testing.T) {
	baseURL := startServeHTTPWithSession(t, nil, unrestrictedTestSession(t))

	t.Run("exact production origin is allowed", func(t *testing.T) {
		resp := optionsWithOrigin(t, baseURL+"/datatug/queries/create_query", "https://app.incidentius.com", http.MethodPost)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.incidentius.com" {
			t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "https://app.incidentius.com")
		}
		if got := resp.Header.Get("Access-Control-Allow-Methods"); got != http.MethodPost {
			t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, http.MethodPost)
		}
	})

	t.Run("near miss is refused", func(t *testing.T) {
		resp := optionsWithOrigin(t, baseURL+"/datatug/queries/create_query", "https://app.incidentius.com:443", http.MethodPost)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
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

// IPv4 loopback pages use the agent origin policy for legacy metadata too.
func TestServeHTTP_CORS_127001_Allowed(t *testing.T) {
	const projectID = "cors-127001-project"
	pathsByID := authHookProjectFixture(t, projectID)
	session, err := secureread.NewSession(secureread.SessionOptions{As: "agent1", NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	summaryURL := baseURL + "/datatug/projects/project_summary?id=" + projectID

	resp := getWithOrigin(t, summaryURL, "http://127.0.0.1:4200")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 for admitted IPv4 loopback", resp.StatusCode)
	}
}

func TestServeHTTP_CORS_MetadataAndSessionUseSameStrictOrigin(t *testing.T) {
	const projectID = "origin-parity"
	paths := authHookProjectFixture(t, projectID)
	envDir := filepath.Join(paths[projectID], "environments", "local")
	if err := os.MkdirAll(envDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "local.env.json"), []byte(`{"id":"local","dbServers":[{"driver":"sqlite3","catalogs":["source"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "agent1", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	base := startServeHTTPWithSessionAndCapabilities(t, paths, session, api.Capabilities{EvidencePrivateDir: t.TempDir()})
	for _, path := range []string{"/datatug/projects/project_summary?id=" + projectID, "/datatug/session-context/capabilities?storage=local&project=" + projectID} {
		for _, origin := range []string{"http://127.0.0.1:18991", "http://localhost:18991", "http://127.0.0.1:18991/path", "http://127.0.0.1:18991@evil.example", "https://evil.example"} {
			resp := getWithOrigin(t, base+path, origin)
			want := http.StatusForbidden
			if origin == "http://127.0.0.1:18991" || origin == "http://localhost:18991" {
				want = http.StatusOK
			}
			if resp.StatusCode != want {
				t.Errorf("%s origin=%q status=%d want=%d", path, origin, resp.StatusCode, want)
			}
			if want == http.StatusOK && resp.Header.Get("Access-Control-Allow-Origin") != origin {
				t.Errorf("%s missing approved CORS header", path)
			}
			if want == http.StatusForbidden && resp.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Errorf("%s reflected forbidden origin", path)
			}
			_ = resp.Body.Close()
		}
	}
}
