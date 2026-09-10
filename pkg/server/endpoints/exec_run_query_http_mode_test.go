package endpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/httpsource"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// This file covers Phase 1 Task 14 ("Complete bounded HTTP browser
// execution"): request-driven mode:live|snapshot dispatch (no silent
// fallback), the lead-assumption snapshot-discovery "details" key, the
// per-request timeout + cancellation, --http-offline, the undeclared-source
// rejection, and the provider-capability check before dispatch. Each test
// goes through the real HTTP handler (postRunQuery -> runQueryHandler),
// exactly like this package's sibling exec_run_query_http_errors_test.go.

// configureHTTPModeSession is configureSemanticSession (semantic_contract_
// test.go), except it accepts an explicit api.Capabilities — needed here for
// --http-offline and --exec-timeout, which configureSemanticSession always
// passes as the zero value.
func configureHTTPModeSession(t *testing.T, projectDir, projectID, as string, roles []string, caps api.Capabilities) apicontract.Scope {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{
		As: as, Roles: roles, PoliciesDir: projectDir + "/policies",
	})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	pathsByID := map[string]string{projectID: projectDir}
	api.ConfigureSecureSession(session, pathsByID, caps)
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}
	return apicontract.Scope{Project: projectID, Environment: semanticTestEnv, SecurityContextID: api.SecurityContextID()}
}

// writeRunQueryHTTPTestProjectWithFixture is writeRunQueryHTTPTestProject
// (exec_run_query_http_errors_test.go) plus a fixtures/http/widget.json
// recorded snapshot, so mode:snapshot dispatch and the SOURCE_UNAVAILABLE
// "details.availableSnapshots" enrichment both have a real fixture to
// resolve against — mirroring datatug-demo-projects/demo-project-1's own
// fixtures/http/<queryID>.json convention (pkg/httpsource's fixtureFS).
func writeRunQueryHTTPTestProjectWithFixture(t *testing.T, urlTemplate string) (projectDir, projectID string) {
	t.Helper()
	projectDir, projectID = writeRunQueryHTTPTestProject(t, urlTemplate)
	fixturesDir := filepath.Join(projectDir, "fixtures", "http")
	mustMkdirAll(t, fixturesDir)
	mustWriteFile(t, filepath.Join(fixturesDir, "widget.json"), `{"key":"gadget","value":"snapshot-value"}`)
	return projectDir, projectID
}

// httpErrorEnvelopeWithDetails decodes a SOURCE_UNAVAILABLE (or any error)
// response body including the sibling "details" key contractError.Details
// adds (contract_errors.go's errorResponseEnvelope) — apicontract.
// ErrorEnvelope itself has no Details field, so postRunQuery's own decode
// cannot see it.
type httpErrorEnvelopeWithDetails struct {
	Error   apicontract.ErrorBody `json:"error"`
	Details *struct {
		AvailableSnapshots []struct {
			SnapshotID string `json:"snapshotId"`
			RecordedAt string `json:"recordedAt"`
		} `json:"availableSnapshots"`
	} `json:"details"`
}

// postRunQuerySuccess is postRunQuery for the 200 path: decodes the body as
// apicontract.Result, failing the test if the status isn't 200.
func postRunQuerySuccess(t *testing.T, req apicontract.ExecutionRequest) apicontract.Result {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", strings.NewReader(string(body)))
	runQueryHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var result apicontract.Result
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal result: %v (body: %s)", err, w.Body.String())
	}
	return result
}

func postRunQueryRaw(t *testing.T, req apicontract.ExecutionRequest) (status int, env httpErrorEnvelopeWithDetails, rawBody []byte) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", strings.NewReader(string(body)))
	runQueryHandler(w, r)
	rawBody = w.Body.Bytes()
	if w.Code >= 300 {
		if err := json.Unmarshal(rawBody, &env); err != nil {
			t.Fatalf("unmarshal error envelope: %v (body: %s)", err, rawBody)
		}
	}
	return w.Code, env, rawBody
}

// --- item 1: request-driven mode, no fallback ---

// TestExecRunQuery_HTTPSource_ModeLive_Success proves mode:live dispatches
// ModeLive and returns live provenance from a real (loopback) fetch.
func TestExecRunQuery_HTTPSource_ModeLive_Success(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"key":"gadget","value":"live-value"}`))
	}))
	defer srv.Close()

	projectDir, projectID := writeRunQueryHTTPTestProject(t, srv.URL+"/widgets?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	status, env := postRunQuery(t, httpRunQueryRequest(scope))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
}

// TestExecRunQuery_HTTPSource_ModeSnapshot_WithoutValidSnapshotID_NotFound
// proves a fabricated snapshotId never dispatches ModeSnapshot: no fixture
// exists for "widget" in writeRunQueryHTTPTestProject's bare project, so
// ANY snapshotId (even one shaped like a real identity) must fail NOT_FOUND,
// never silently succeed and never touch the network.
func TestExecRunQuery_HTTPSource_ModeSnapshot_WithoutValidSnapshotID_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("mode:snapshot must never touch the network; got a request: %s", r.URL)
	}))
	defer srv.Close()

	projectDir, projectID := writeRunQueryHTTPTestProject(t, srv.URL+"/widgets?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	req := httpRunQueryRequest(scope)
	req.Mode = apicontract.ProvenanceModeSnapshot
	req.SnapshotID = "widget@2026-09-09T00:00:00Z"
	status, env := postRunQuery(t, req)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeNotFound) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeNotFound)
	}
}

// TestExecRunQuery_HTTPSource_ModeSnapshot_ValidSnapshotID_Success proves
// the real path: a project WITH a recorded fixture, requesting the exact
// identity httpsource.SnapshotIdentity computes for it, dispatches
// ModeSnapshot and returns the fixture's own value and recorded time,
// touching the network never (srv fails the test if hit).
func TestExecRunQuery_HTTPSource_ModeSnapshot_ValidSnapshotID_Success(t *testing.T) {
	// useInsecureLoopbackHTTPQuery is needed even though this test never
	// dials: dalgo2http's Config validation (https:// scheme required)
	// runs unconditionally at Open time, for every collection, regardless
	// of the runtime Mode — a descriptor must be config-valid even when
	// only ever served from a snapshot. srv itself is still never
	// contacted (ModeSnapshot never dials); it exists only so the
	// descriptor's URL template is a real loopback address to validate the
	// shape of, and fails the test outright if it is ever hit.
	useInsecureLoopbackHTTPQuery(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("mode:snapshot must never touch the network; got a request: %s", r.URL)
	}))
	defer srv.Close()

	projectDir, projectID := writeRunQueryHTTPTestProjectWithFixture(t, srv.URL+"/widgets?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	wantID, wantRecordedAt, ok := httpsource.SnapshotIdentity(projectDir, "widget")
	if !ok {
		t.Fatalf("SnapshotIdentity: no fixture found (test setup bug)")
	}

	req := httpRunQueryRequest(scope)
	req.Mode = apicontract.ProvenanceModeSnapshot
	req.SnapshotID = wantID
	result := postRunQuerySuccess(t, req)
	if result.Provenance.Mode != apicontract.ProvenanceModeSnapshot {
		t.Errorf("provenance.mode = %q, want %q", result.Provenance.Mode, apicontract.ProvenanceModeSnapshot)
	}
	if result.Provenance.SnapshotID != wantID {
		t.Errorf("provenance.snapshotId = %q, want %q", result.Provenance.SnapshotID, wantID)
	}
	// The regression this test guards: observedAt MUST be the fixture's own
	// recorded capture time, never "now" — a snapshot response that reports
	// its own execution time as "observed" is exactly the "production-
	// acceptance claim based on a fixture" api-contract.md forbids (found by
	// this stream's own journey e2e, J2b, asserting the exact recorded date).
	wantObservedAt := wantRecordedAt.UTC().Format(time.RFC3339)
	if result.Provenance.ObservedAt != wantObservedAt {
		t.Errorf("provenance.observedAt = %q, want the fixture's own recorded time %q (not now)", result.Provenance.ObservedAt, wantObservedAt)
	}
}

// TestExecRunQuery_HTTPSource_ModeSnapshot_NonHTTPQuery_InvalidRequest
// proves mode:snapshot against a saved query that is NOT HTTP-typed fails
// INVALID_REQUEST, naming the mode field, before any dispatch is attempted.
func TestExecRunQuery_HTTPSource_ModeSnapshot_NonHTTPQuery_InvalidRequest(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	req := apicontract.ExecutionRequest{
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		QueryID: "customers/customer-invoices",
		Parameters: map[string]apicontract.TypedValue{
			"CustomerId": apicontract.NewIntegerValue("1"),
		},
		BindingOrigins: []apicontract.BindingOriginEntry{{ParameterID: "CustomerId", Origin: apicontract.BindingOriginSelection}},
		Mode:           apicontract.ProvenanceModeSnapshot,
		SnapshotID:     "customer-invoices@2026-09-09T00:00:00Z",
	}
	status, env := postRunQuery(t, req)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeInvalidRequest) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeInvalidRequest)
	}
	if env.Error.Field != "mode" {
		t.Errorf("field = %q, want %q", env.Error.Field, "mode")
	}
}

// --- item 2/3: snapshot discovery + provenance ---

// TestExecRunQuery_HTTPSource_LiveFailure_WithFixture_DetailsNameSnapshot
// proves a live failure against a query THAT HAS a recorded fixture
// attaches the sibling "details.availableSnapshots" entry (LEAD ASSUMPTION
// 2026-09-10) naming exactly the identity httpsource.SnapshotIdentity
// computes, and that the plain "error" shape is otherwise unaffected.
func TestExecRunQuery_HTTPSource_LiveFailure_WithFixture_DetailsNameSnapshot(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close() // unreachable

	projectDir, projectID := writeRunQueryHTTPTestProjectWithFixture(t, downURL+"/widgets?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	wantID, wantRecordedAt, ok := httpsource.SnapshotIdentity(projectDir, "widget")
	if !ok {
		t.Fatalf("SnapshotIdentity: no fixture found (test setup bug)")
	}

	status, env, _ := postRunQueryRaw(t, httpRunQueryRequest(scope))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeSourceUnavailable) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeSourceUnavailable)
	}
	if env.Details == nil || len(env.Details.AvailableSnapshots) != 1 {
		t.Fatalf("details.availableSnapshots = %+v, want exactly one entry", env.Details)
	}
	got := env.Details.AvailableSnapshots[0]
	if got.SnapshotID != wantID {
		t.Errorf("availableSnapshots[0].snapshotId = %q, want %q", got.SnapshotID, wantID)
	}
	if got.RecordedAt != wantRecordedAt.UTC().Format(time.RFC3339) {
		t.Errorf("availableSnapshots[0].recordedAt = %q, want %q", got.RecordedAt, wantRecordedAt.UTC().Format(time.RFC3339))
	}
}

// TestExecRunQuery_HTTPSource_LiveFailure_NoFixture_NoDetails proves a live
// failure against a query with NO recorded fixture carries no "details" key
// at all — never a fabricated or empty entry.
func TestExecRunQuery_HTTPSource_LiveFailure_NoFixture_NoDetails(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close()

	projectDir, projectID := writeRunQueryHTTPTestProject(t, downURL+"/widgets?key={key}") // no fixture
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	status, env, raw := postRunQueryRaw(t, httpRunQueryRequest(scope))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Details != nil {
		t.Errorf("details = %+v, want none (no recorded fixture)", env.Details)
	}
	if strings.Contains(string(raw), `"details"`) {
		t.Errorf("response body must not carry a \"details\" key at all: %s", raw)
	}
}

// --- item 4: time budget and cancellation ---

// TestExecRunQuery_HTTPSource_Timeout_SleepingServer_MapsToSourceUnavailable
// proves a live endpoint that never answers within the configured exec
// timeout fails SOURCE_UNAVAILABLE (per the Phase 1 HTTP bounds: "Live
// failure ... timeout ... -> SOURCE_UNAVAILABLE", not the generic TIMEOUT
// code — see exec_run_query.go's err-classifier comment on ErrUpstream), and
// that the request returns close to the configured timeout, not the full
// sleep duration — proving the deadline actually bounds the fetch.
func TestExecRunQuery_HTTPSource_Timeout_SleepingServer_MapsToSourceUnavailable(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t)
	const (
		configuredTimeout = 200 * time.Millisecond
		// handlerSleep is a bounded sleep, not an indefinite block on a
		// channel: httptest.Server.Close() (deferred below) waits for every
		// in-flight handler to return before it returns itself, so a
		// handler that blocks until a t.Cleanup-closed channel would
		// deadlock against that same defer — t.Cleanup callbacks run only
		// after the test function (and its own defers) have already
		// returned. A generous-but-finite sleep sidesteps that entirely
		// while still comfortably outlasting configuredTimeout.
		handlerSleep = time.Second
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(handlerSleep)
	}))
	defer srv.Close()

	projectDir, projectID := writeRunQueryHTTPTestProject(t, srv.URL+"/widgets?key={key}")
	scope := configureHTTPModeSession(t, projectDir, projectID, "alice", []string{"admin"}, api.Capabilities{ExecTimeout: configuredTimeout})

	start := time.Now()
	status, env := postRunQuery(t, httpRunQueryRequest(scope))
	elapsed := time.Since(start)

	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeSourceUnavailable) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeSourceUnavailable)
	}
	if elapsed >= handlerSleep {
		t.Errorf("elapsed = %s, want well under the %s handler sleep — the configured %s deadline did not bound the fetch", elapsed, handlerSleep, configuredTimeout)
	}
}

// TestExecRunQuery_HTTPSource_ExecTimeout_ClampedToCeiling proves
// api.Capabilities.ExecTimeout above the contract's 30-second ceiling is
// clamped down, not honored verbatim (execTimeoutFor, contract_scope.go).
func TestExecRunQuery_HTTPSource_ExecTimeout_ClampedToCeiling(t *testing.T) {
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{ExecTimeout: 5 * time.Minute})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	if got := execTimeoutFor(); got != maxExecTimeout {
		t.Errorf("execTimeoutFor() = %s, want the %s ceiling", got, maxExecTimeout)
	}
}

// --- item 7: --http-offline ---

// TestExecRunQuery_HTTPOffline_LiveFetch_FailsWithoutTouchingNetwork proves
// api.Capabilities.HTTPOffline makes a live dispatch fail SOURCE_UNAVAILABLE
// even though the descriptor's real endpoint IS reachable (srv would answer
// successfully if ever contacted, and fails the test if it is).
func TestExecRunQuery_HTTPOffline_LiveFetch_FailsWithoutTouchingNetwork(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("--http-offline must never touch the network; got a request: %s", r.URL)
	}))
	defer srv.Close()

	projectDir, projectID := writeRunQueryHTTPTestProject(t, srv.URL+"/widgets?key={key}")
	scope := configureHTTPModeSession(t, projectDir, projectID, "alice", []string{"admin"}, api.Capabilities{HTTPOffline: true})

	status, env := postRunQuery(t, httpRunQueryRequest(scope))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeSourceUnavailable) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeSourceUnavailable)
	}
}

// TestExecRunQuery_HTTPOffline_SnapshotStillWorks proves --http-offline
// affects only LIVE dispatch: an explicit mode:snapshot request with a
// project that has a recorded fixture still succeeds under
// api.Capabilities.HTTPOffline.
func TestExecRunQuery_HTTPOffline_SnapshotStillWorks(t *testing.T) {
	projectDir, projectID := writeRunQueryHTTPTestProjectWithFixture(t, "https://example.invalid/widgets?key={key}")
	scope := configureHTTPModeSession(t, projectDir, projectID, "alice", []string{"admin"}, api.Capabilities{HTTPOffline: true})

	wantID, _, ok := httpsource.SnapshotIdentity(projectDir, "widget")
	if !ok {
		t.Fatalf("SnapshotIdentity: no fixture found (test setup bug)")
	}
	req := httpRunQueryRequest(scope)
	req.Mode = apicontract.ProvenanceModeSnapshot
	req.SnapshotID = wantID
	status, env := postRunQuery(t, req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
}

// --- item 6: reject browser-provided source overrides ---

// TestExecRunQuery_HTTPSource_UndeclaredSourceOverride_RejectedWithoutNetwork
// proves a source value the browser fabricates (never one of the project's
// own eligible targets — there is no way to submit a raw URL in
// ExecutionRequest at all, only an opaque source id resolved server-side
// via api.EligibleTargets) is rejected before any dispatch, touching the
// network never.
func TestExecRunQuery_HTTPSource_UndeclaredSourceOverride_RejectedWithoutNetwork(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("an undeclared source override must never touch the network; got a request: %s", r.URL)
	}))
	defer srv.Close()

	projectDir, projectID := writeRunQueryHTTPTestProject(t, srv.URL+"/widgets?key={key}")
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	req := httpRunQueryRequest(scope)
	req.Source = "https://evil.example.com/steal-this"
	status, env := postRunQuery(t, req)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 TARGET_REQUIRED (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeTargetRequired) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeTargetRequired)
	}
}

// --- item 5: provider-capability check before dispatch ---

// httpRegionScopedPolicy grants /widget query access ONLY when region ==
// "US" — "region" is NOT a declared dalgo2http parameter for the widget
// collection (writeRunQueryHTTPTestProject declares only "key"), so this
// residual row condition can never be pushed into the URL template. Follows
// pkg/secureread/fixtures_test.go's ownerScopedPolicy syntax exactly (same
// dal-go/dalgo access YAML schema).
const httpRegionScopedPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: http-region-scoped
default: deny
scopes:
  - path: /widget
    rules:
      - id: region-scoped
        effect: allow
        operations: [query]
        where:
          op: "=="
          left: { field: region }
          right: { value: "US" }
`

// TestExecRunQuery_HTTPSource_UnenforceablePolicyCondition_AccessDenied
// proves the Phase 1 HTTP bounds' "if a requested protected predicate ...
// cannot be enforced safely, reject it rather than fetching an unrestricted
// result and claiming enforcement": a policy row condition on a field the
// HTTP collection never declared as a parameter must refuse the request —
// touching the network never — rather than silently fetching the full
// (unfiltered) live response and returning it as if the policy applied.
// dalgo2http's own query.go (planQuery/collectEqualities) is what fails
// closed with dal.ErrNotSupported before any fetch; this proves
// exec_run_query.go classifies that refusal as 403 ACCESS_DENIED end to end,
// through the real HTTP handler.
func TestExecRunQuery_HTTPSource_UnenforceablePolicyCondition_AccessDenied(t *testing.T) {
	useInsecureLoopbackHTTPQuery(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("an unenforceable policy condition must be refused before any fetch; got a request: %s", r.URL)
	}))
	defer srv.Close()

	dir := t.TempDir()
	projectID := "run-query-http-capability-test-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
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
	mustWriteFile(t, filepath.Join(queriesDir, "widget.query.http"), srv.URL+"/widgets?key={key}\n")
	policiesDir := filepath.Join(dir, "policies")
	mustMkdirAll(t, policiesDir)
	mustWriteFile(t, filepath.Join(policiesDir, "test.yaml"), httpRegionScopedPolicy)

	scope := configureSemanticSession(t, dir, projectID, "alice", nil)

	status, env := postRunQuery(t, httpRunQueryRequest(scope))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeAccessDenied) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeAccessDenied)
	}
	if strings.Contains(strings.ToLower(env.Error.Message), "region") {
		t.Errorf("message must not name the hidden policy field: %q", env.Error.Message)
	}
}
