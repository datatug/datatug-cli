package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/openvaultdb"
	"github.com/julienschmidt/httprouter"
)

func newProxyTestRouter(t *testing.T, upstream *httptest.Server) *httprouter.Router {
	t.Helper()
	router := httprouter.New()
	err := registerOpenVaultDBProxy(router, OpenVaultDBProxyOptions{
		Origin: "https://datatug.app", SessionToken: "session",
		Targets: map[string]openvaultdb.Target{"crm": {BaseURL: upstream.URL, DatabaseID: "vault", Token: "bearer"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func newProxyHTTPRequest(method, path, origin, token, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(agentTokenHeader, token)
	return r
}

func TestOpenVaultDBProxyRequiresOriginAndSession(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { upstreamCalled = true }))
	defer upstream.Close()
	router := newProxyTestRouter(t, upstream)
	for _, tc := range []struct{ origin, token string }{{"https://evil.test", "session"}, {"https://datatug.app", ""}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/query", tc.origin, tc.token, `{"target":"crm","dtql":"from: {name: customers}"}`))
		if w.Code != http.StatusForbidden || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d headers=%v", w.Code, w.Header())
		}
	}
	if upstreamCalled {
		t.Fatal("unauthorized browser request reached upstream")
	}
}

func TestOpenVaultDBProxyUsesFixedTargetAndDoesNotLeakBearer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/databases/vault/dtql" || r.Header.Get("Authorization") != "Bearer bearer" {
			t.Fatalf("upstream path/auth = %q/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"records":[]}`))
	}))
	defer upstream.Close()
	router := newProxyTestRouter(t, upstream)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/query", "https://datatug.app", "session", `{"target":"crm","dtql":"from: {name: customers}"}`))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "bearer") {
		t.Fatalf("status/body = %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/query", "https://datatug.app", "session", `{"target":"crm","url":"https://evil.test","dtql":"x"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("browser URL field accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestOpenVaultDBProxyRejectsIdentityAndDatabaseOverride(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid request reached upstream") }))
	defer upstream.Close()
	router := newProxyTestRouter(t, upstream)
	for _, body := range []string{
		`{"target":"crm","request":{"subject":{"realm":"x","kind":"user","id":"u"},"operations":[{"resource":{"databaseId":"vault"}}]}}`,
		`{"target":"crm","request":{"operations":[{"resource":{"databaseId":"other"}}]}}`,
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/explain", "https://datatug.app", "session", body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status/body = %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOpenVaultDBProxyPreflightIsClosed(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	router := newProxyTestRouter(t, upstream)
	for _, origin := range []string{"https://evil.test", "https://datatug.app"} {
		r := httptest.NewRequest(http.MethodOptions, "/datatug/ovdb/query", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Access-Control-Request-Method", http.MethodPost)
		r.Header.Set("Access-Control-Request-Headers", "content-type, x-datatug-agent-token")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		want := http.StatusForbidden
		if origin == "https://datatug.app" {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Fatalf("origin %q: got %d want %d", origin, w.Code, want)
		}
	}
}

func TestOpenVaultDBProxyForwardsFrozenUpdateAndEvidence(t *testing.T) {
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"data_revision_conflict","message":"conflict"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"apiVersion":"dtql.org/authorization/v1","resource":{"databaseId":"vault","path":"/customers/101","rowId":"101"},"exists":true,"dataRevision":"r1","fields":[]}`))
	}))
	defer upstream.Close()
	router := newProxyTestRouter(t, upstream)

	update := `{"target":"crm","request":{"apiVersion":"dtql.org/authorization/v1","id":"u1","action":"update","resource":{"databaseId":"vault","path":"/customers/101","table":"customers","rowId":"101","columns":[["name"]]},"mutation":{"changes":[{"op":"set","path":["name"],"value":"Ada"}],"ifDataRevision":"r0"},"executionClass":"dtql"}}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/update", "https://datatug.app", "session", update))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "data_revision_conflict") {
		t.Fatalf("update response = %d %s", w.Code, w.Body.String())
	}

	evidence := `{"target":"crm","request":{"apiVersion":"dtql.org/authorization/v1","resource":{"databaseId":"vault","path":"/customers/101","rowId":"101"},"requiredFields":[["name"]]}}`
	w = httptest.NewRecorder()
	router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/evidence", "https://datatug.app", "session", evidence))
	if w.Code != http.StatusOK {
		t.Fatalf("evidence response = %d %s", w.Code, w.Body.String())
	}
	want := []string{
		"PATCH /v1/databases/vault/records/customers/101 application/vnd.dtql.operation+json",
		"POST /v1/databases/vault/access/evidence application/json",
	}
	if len(requests) != len(want) || requests[0] != want[0] || requests[1] != want[1] {
		t.Fatalf("upstream requests = %#v, want %#v", requests, want)
	}
}

func TestOpenVaultDBProxyRejectsDuplicateAndCaseAliasKeys(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("ambiguous request reached upstream") }))
	defer upstream.Close()
	router := newProxyTestRouter(t, upstream)
	for _, body := range []string{
		`{"target":"crm","target":"crm","dtql":"x"}`,
		`{"Target":"crm","dtql":"x"}`,
		`{"target":"crm","request":{"operations":[],"operations":[]}}`,
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/query", "https://datatug.app", "session", body))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("ambiguous body accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestOpenVaultDBTargetDiscoveryDisclosesOnlyIDs(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	router := newProxyTestRouter(t, upstream)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/targets", "https://datatug.app", "session", ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"databaseId":"vault"`) ||
		strings.Contains(w.Body.String(), upstream.URL) || strings.Contains(w.Body.String(), "bearer") {
		t.Fatalf("unsafe discovery response: %d %s", w.Code, w.Body.String())
	}
}

func TestOpenVaultDBProxyBoundsRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("oversized request reached upstream") }))
	defer upstream.Close()
	router := newProxyTestRouter(t, upstream)
	body := `{"target":"crm","dtql":"` + strings.Repeat("x", openvaultdb.MaxRequestBytes) + `"}`
	w := httptest.NewRecorder()
	router.ServeHTTP(w, newProxyHTTPRequest(http.MethodPost, "/datatug/ovdb/query", "https://datatug.app", "session", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d", w.Code)
	}
}
