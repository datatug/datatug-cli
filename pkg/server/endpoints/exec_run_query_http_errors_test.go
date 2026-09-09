package endpoints

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// This file proves the mapping exec_run_query.go's computeRunQuery adds for
// this stream (adopting dal-go/dalgo2http v0.2.0's Phase 1 HTTP bounds):
//
//	ErrResponseTooLarge                        -> RESPONSE_TOO_LARGE (413)
//	ErrAddressBlocked / ErrRedirectNotAllowed / -> SOURCE_UNAVAILABLE (503)
//	ErrInvalidConfig ("scheme errors")
//
// Each case goes through the real HTTP handler (postRunQuery ->
// runQueryHandler), asserting BOTH status and code, matching this file's
// sibling exec_run_query_test.go's own "status AND code, not English
// wording" discipline. This is the provider (server) half of the mapping
// only; browser-side HTTP execution (mode: snapshot dispatch, cancellation)
// is a separate stream's scope.

// writeRunQueryHTTPTestProject builds a self-contained project with exactly
// one registered HTTP query ("reference/widget", required string parameter
// "key") pointed at urlTemplate, and admin-full-access policies (reused
// from writePolicies) — no chinook/environment registration is needed:
// api.EligibleTargets resolves an HTTP query's only eligible target from
// its own project files (pkg/api/resolver.go's httpQuerySources), never
// from a registered catalog.
func writeRunQueryHTTPTestProject(t *testing.T, urlTemplate string) (projectDir, projectID string) {
	t.Helper()
	dir := t.TempDir()
	projectID = "run-query-http-test-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	filestore.SetProjectPath(projectID, dir)

	queriesDir := filepath.Join(dir, "queries", "reference")
	mustMkdirAll(t, queriesDir)
	mustWriteFile(t, filepath.Join(queriesDir, "widget.query.json"), `{
		"id": "widget",
		"title": "Widget",
		"type": "HTTP",
		"parameters": [{"id": "key", "type": "string", "isRequired": true}],
		"recordsets": [{"columns": [{"name": "key", "type": "string"}, {"name": "value", "type": "string"}]}]
	}`)
	mustWriteFile(t, filepath.Join(queriesDir, "widget.query.http"), urlTemplate+"\n")
	writePolicies(t, dir)

	return dir, projectID
}

// httpRunQueryRequest builds the minimal valid ExecutionRequest for
// "reference/widget" against scope: one required "key" parameter, bound
// "selection" (matching this file's sibling tests' own BindingOrigins
// shape), live mode.
func httpRunQueryRequest(scope apicontract.Scope) apicontract.ExecutionRequest {
	return apicontract.ExecutionRequest{
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		QueryID: "reference/widget",
		Parameters: map[string]apicontract.TypedValue{
			"key": apicontract.NewStringValue("gadget"),
		},
		BindingOrigins: []apicontract.BindingOriginEntry{{ParameterID: "key", Origin: apicontract.BindingOriginSelection}},
		Mode:           apicontract.ProvenanceModeLive,
	}
}

// useInsecureLoopbackHTTPQuery swaps the package-level runStructuredHTTPQuery
// var (see exec_run_query.go) for the duration of t so a "reference/widget"
// HTTP query opens its source via secureread.Executor.RunStructuredInsecureForTest
// instead of RunStructured — dal-go/dalgo2http v0.2.0 requires https:// and
// blocks dialing loopback for every descriptor loaded from a project file,
// and this file's ResponseTooLarge/RedirectNotAllowed cases need a real
// loopback httptest.Server to produce a genuine live response. Restored via
// t.Cleanup so production behavior (the default runStructuredHTTPQuery,
// https-only) is unaffected for every other test in this package.
func useInsecureLoopbackHTTPQuery(t *testing.T) {
	t.Helper()
	orig := runStructuredHTTPQuery
	runStructuredHTTPQuery = func(ctx context.Context, executor *secureread.Executor, sourceURL string, query dal.Query, variables map[string]any) (secureread.Result, error) {
		return executor.RunStructuredInsecureForTest(ctx, sourceURL, query, variables)
	}
	t.Cleanup(func() { runStructuredHTTPQuery = orig })
}

// TestExecRunQuery_HTTPSource_ResponseTooLarge_MapsTo413 proves
// dalgo2http.ErrResponseTooLarge (a live response over the adapter's 2 MiB
// cap) reaches the caller as 413 RESPONSE_TOO_LARGE, not a raw decode error
// or a bare 500.
func TestExecRunQuery_HTTPSource_ResponseTooLarge_MapsTo413(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t) // srv is a loopback httptest.Server (plain HTTP)
	const overCap = 2*1024*1024 + 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, overCap))
	}))
	defer srv.Close()

	projectDir, projectID := writeRunQueryHTTPTestProject(t, srv.URL+"/big?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	status, env := postRunQuery(t, httpRunQueryRequest(scope))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (code=%q message=%q)", status, http.StatusRequestEntityTooLarge, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeResponseTooLarge) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeResponseTooLarge)
	}
	if strings.Contains(env.Error.Message, srv.URL) {
		t.Errorf("message must not leak the raw source URL: %q", env.Error.Message)
	}
}

// TestExecRunQuery_HTTPSource_RedirectNotAllowed_MapsToSourceUnavailable
// proves dalgo2http.ErrRedirectNotAllowed (the live endpoint tried to
// redirect; redirects are always refused) reaches the caller as 503
// SOURCE_UNAVAILABLE.
func TestExecRunQuery_HTTPSource_RedirectNotAllowed_MapsToSourceUnavailable(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t) // srv is a loopback httptest.Server (plain HTTP)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()

	projectDir, projectID := writeRunQueryHTTPTestProject(t, srv.URL+"/redirect?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	status, env := postRunQuery(t, httpRunQueryRequest(scope))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (code=%q message=%q)", status, http.StatusServiceUnavailable, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeSourceUnavailable) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeSourceUnavailable)
	}
	if strings.Contains(env.Error.Message, srv.URL) {
		t.Errorf("message must not leak the raw source URL: %q", env.Error.Message)
	}
}

// TestExecRunQuery_HTTPSource_AddressBlocked_MapsToSourceUnavailable proves
// dalgo2http.ErrAddressBlocked (the guarded dialer refusing a link-local /
// cloud-metadata address) reaches the caller as 503 SOURCE_UNAVAILABLE, with
// no resolved-IP or dial-address detail in the message — this case needs no
// loopback test server: the descriptor's own https:// scheme is valid, and
// the guarded dialer blocks the literal IP before any network activity, so
// it runs through the real, unmodified production path (no
// useInsecureLoopbackHTTPQuery swap).
func TestExecRunQuery_HTTPSource_AddressBlocked_MapsToSourceUnavailable(t *testing.T) {
	// 169.254.169.254 is the cloud-metadata address (link-local range),
	// always blocked by dalgo2http's guarded dialer regardless of
	// InsecureAllowLoopback (only loopback is ever relaxed by that flag).
	projectDir, projectID := writeRunQueryHTTPTestProject(t, "https://169.254.169.254/latest/meta-data/?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	status, env := postRunQuery(t, httpRunQueryRequest(scope))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (code=%q message=%q)", status, http.StatusServiceUnavailable, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeSourceUnavailable) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeSourceUnavailable)
	}
	if strings.Contains(env.Error.Message, "169.254.169.254") {
		t.Errorf("message must not leak the resolved/blocked address: %q", env.Error.Message)
	}
}

// TestExecRunQuery_HTTPSource_SchemeError_MapsToSourceUnavailable proves a
// project descriptor whose .query.http file uses plain http:// (dalgo2http
// v0.2.0's config-time "urlTemplate must use https://" ErrInvalidConfig —
// the stream brief's "scheme errors") reaches the caller as 503
// SOURCE_UNAVAILABLE rather than a 400 echoing the adapter's own internal
// wording. No network is involved: NewDB's own config validation rejects
// this before any request is attempted, so this too runs through the real,
// unmodified production path.
func TestExecRunQuery_HTTPSource_SchemeError_MapsToSourceUnavailable(t *testing.T) {
	projectDir, projectID := writeRunQueryHTTPTestProject(t, "http://example.com/widgets?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	status, env := postRunQuery(t, httpRunQueryRequest(scope))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (code=%q message=%q)", status, http.StatusServiceUnavailable, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeSourceUnavailable) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeSourceUnavailable)
	}
}
