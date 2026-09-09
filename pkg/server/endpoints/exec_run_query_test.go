package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// --- exec/run_query request-validation tests (S91) ---
//
// S91's gap: exec/run_query decoded apicontract.ExecutionRequest (core
// v0.27.0, pkg/apicontract — the schema authority since PR #215) with
// DecodeStrict but never called req.Validate(), unlike its semantic/related
// and semantic/related/rows siblings (semantic_related.go). This file
// proves the fix two ways: the VALID-shape tests decode core's own frozen
// ExecutionRequest fixtures (pkg/apicontract/fixtures, fixtures.Read)
// through the real production path and run them through computeRunQuery
// end to end; the INVALID-shape table asserts every rejection this stream
// wires — validateScope's own STALE_CONTEXT aside — carries the exact
// status AND apicontract.ErrorCode api-contract.md's "Security and errors"
// requires ("Tests assert both status and code, not English wording"),
// through the real HTTP handler (runQueryHandler), not a hand-called
// compute function, so decodeContractBody's own decode path stays
// exercised too.

// runQueryTestCustomerByIDDTQL selects Customer rows by CustomerId — the
// same "from/where" DTQL shape security_matrix_test.go's own
// customerByIDDTQL uses (pkg/server/security_matrix_test.go), reused here
// verbatim rather than reinvented.
const runQueryTestCustomerByIDDTQL = `from:
  name: Customer
where:
  op: "=="
  left:
    field: CustomerId
  right:
    param: CustomerId
`

// writeRunQueryTestProject builds a self-contained project (its own
// per-test project ID, registered via filestore.SetProjectPath — see
// writeSemanticTestProject's identical pattern in semantic_fixture_test.go)
// with exactly one registered source (a copy of dbcopy's checked-in Chinook
// SQLite fixture, under the stable source id semanticTestSource, reusing
// copyChinookDB/registerChinookEnvironment from semantic_fixture_test.go
// unchanged) and one real, executable saved DTQL query,
// "customers/customer-invoices" (CustomerId:integer, required) — written
// with BOTH its "<id>.query.json" metadata and "<id>.query.dtql" text
// sidecar (api.LoadQueryDocument reads the sidecar directly off disk; see
// security_matrix_test.go's own saveQuery helper for why a bare
// datatug.ProjectStore.SaveQuery call would not be enough).
// writeSemanticTestProject's own "customers/customer-invoices" (same
// package, semantic_fixture_test.go) is SQL-typed with no sidecar at all —
// fine for semantic discovery, which never executes it, but unusable
// here — so this is a separate, purpose-built project rather than a reuse
// of that one.
func writeRunQueryTestProject(t *testing.T) (projectDir, projectID string) {
	t.Helper()
	dir := t.TempDir()
	projectID = "run-query-test-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	filestore.SetProjectPath(projectID, dir)

	dbPath := copyChinookDB(t, dir)
	registerChinookEnvironment(t, dir, projectID, dbPath)
	writePolicies(t, dir)

	queriesDir := filepath.Join(dir, "queries", "customers")
	mustMkdirAll(t, queriesDir)
	mustWriteFile(t, filepath.Join(queriesDir, "customer-invoices.query.json"), `{
		"id": "customer-invoices",
		"title": "Customer invoices",
		"type": "DTQL",
		"parameters": [
			{"id": "CustomerId", "type": "integer", "isRequired": true}
		]
	}`)
	mustWriteFile(t, filepath.Join(queriesDir, "customer-invoices.query.dtql"), runQueryTestCustomerByIDDTQL)

	return dir, projectID
}

// --- valid shapes: core's own golden fixtures, through the real path ---

func TestExecRunQuery_AdhocFixture_DecodesAndExecutes(t *testing.T) {
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_adhoc.json", &req)
	if req.DTQL == "" {
		t.Fatalf("fixture execution_request_adhoc.json has no dtql")
	}

	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	// The fixture's own source ("chinook-local") is a placeholder id, not a
	// source this test process registers — this project's real chinook
	// catalog is semanticTestSource ("chinook"). Only the source id is
	// substituted for execution.
	req.Source = semanticTestSource
	// The fixture's own dtql text used to be "select:\n  from: Customer\n" —
	// YAML-shaped prose dal-go/dalgo's own dtql.Deserialize
	// (pkg/secureread/executor.go's RunDTQL) rejected outright ("field select
	// not found in type dtql.document"), a real wire-compatibility gap this
	// stream's report surfaced against datatug-core v0.27.0. datatug-core
	// v0.27.3 (datatug-core#316) replaced it with a real DTQL document — a
	// Customer selection (CustomerId/FirstName/LastName/Email) filtered by a
	// Customer.ID-named param bound "from selection", parameters/bindingOrigins
	// populated to match — so it now parses and executes unmodified: no
	// substitution is needed here any more, and the fixture's own dtql,
	// parameters and bindingOrigins all flow into computeRunQuery untouched.

	result, err := computeRunQuery(context.Background(), req)
	if err != nil {
		t.Fatalf("computeRunQuery(fixture request): %v", err)
	}
	assertValid(t, result)
	if len(result.Recordset.Rows) != 1 {
		t.Fatalf("len(Rows) = %d, want 1 (the fixture's where clause filters to Customer.ID=5)", len(result.Recordset.Rows))
	}

	colIndex := func(name string) int {
		t.Helper()
		for i, col := range result.Recordset.Columns {
			if col.Name == name {
				return i
			}
		}
		t.Fatalf("column %q not found in recordset columns %v", name, result.Recordset.Columns)
		return -1
	}
	row := result.Recordset.Rows[0]
	// The Chinook SQLite driver reports CustomerId as a numeric column
	// (fromGoValue's float64 branch, typed_value_convert.go), unlike the
	// fixture's own declared parameter type ("integer") — the recordset
	// column carries the driver's own observed type, not the request
	// parameter's declared one, so this checks .Num rather than .Str.
	if got := row[colIndex("CustomerId")].Num; got != 5 {
		t.Errorf("CustomerId = %v, want %v", got, 5)
	}
	if got := row[colIndex("FirstName")].Str; got != "František" {
		t.Errorf("FirstName = %q, want %q", got, "František")
	}
	if got := row[colIndex("LastName")].Str; got != "Wichterlová" {
		t.Errorf("LastName = %q, want %q", got, "Wichterlová")
	}
	if got := row[colIndex("Email")].Str; got != "frantisekw@jetbrains.com" {
		t.Errorf("Email = %q, want %q", got, "frantisekw@jetbrains.com")
	}

	// bindingsApplied must echo the fixture's own "from selection" origin
	// (bindingOrigins: [{parameterId: "Customer.ID", origin: "selection",
	// factId: "f1"}]) for the one parameter it actually applied — ad-hoc DTQL
	// has no queryDef to narrow against, so every supplied parameter is
	// reported (bindingsApplied's own doc comment, exec_run_query.go).
	if len(result.BindingsApplied) != 1 {
		t.Fatalf("len(BindingsApplied) = %d, want 1: %+v", len(result.BindingsApplied), result.BindingsApplied)
	}
	binding := result.BindingsApplied[0]
	if binding.ParameterID != "Customer.ID" {
		t.Errorf("BindingsApplied[0].ParameterID = %q, want %q", binding.ParameterID, "Customer.ID")
	}
	if binding.Origin != apicontract.BindingOriginSelection {
		t.Errorf("BindingsApplied[0].Origin = %q, want %q (the fixture's own bindingOrigins entry)", binding.Origin, apicontract.BindingOriginSelection)
	}
	if binding.OriginEvidence != apicontract.BindingOriginEvidenceClientReported {
		t.Errorf("BindingsApplied[0].OriginEvidence = %q, want %q", binding.OriginEvidence, apicontract.BindingOriginEvidenceClientReported)
	}
	if binding.FactID != "f1" {
		t.Errorf("BindingsApplied[0].FactID = %q, want %q (the fixture's own bindingOrigins factId)", binding.FactID, "f1")
	}
}

func TestExecRunQuery_SavedFixture_DecodesAndExecutes(t *testing.T) {
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_saved.json", &req)
	if req.QueryID == "" {
		t.Fatalf("fixture execution_request_saved.json has no queryId")
	}

	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	// The fixture's own queryId ("customers/customer-purchases-by-genre")
	// is a placeholder this test project does not define; substituted with
	// its own real saved DTQL query — the same CustomerId:integer parameter
	// shape the fixture's own parameters/bindingOrigins already supply
	// (CustomerId=5, origin selection), left untouched.
	req.QueryID = "customers/customer-invoices"

	result, err := computeRunQuery(context.Background(), req)
	if err != nil {
		t.Fatalf("computeRunQuery(fixture request): %v", err)
	}
	assertValid(t, result)
	if len(result.Recordset.Rows) != 1 {
		t.Fatalf("len(Rows) = %d, want 1 (Customer 5)", len(result.Recordset.Rows))
	}
}

// TestExecRunQuery_SnapshotFixture_DecodesAndValidates proves core's
// mode:snapshot ExecutionRequest shape (mode + snapshotId together) decodes
// and satisfies Validate() through the real production path (decodeRequestFixture
// itself asserts this) — the same wire-compatibility proof the adhoc/saved
// cases give through a live computeRunQuery execution. It stops at
// decode+Validate(): the fixture's own queryId ("reference/country-facts")
// is an HTTP-typed query, and bounded HTTP execution against a fake source
// is plan task 14's scope (runHTTPQuery's own doc comment in
// exec_run_query.go) — out of reach for a hermetic unit test without
// inventing a real HTTP fixture server, which this stream's gap (a missing
// Validate() call) does not require.
func TestExecRunQuery_SnapshotFixture_DecodesAndValidates(t *testing.T) {
	var req apicontract.ExecutionRequest
	decodeRequestFixture(t, "execution_request_snapshot.json", &req)
	if req.Mode != apicontract.ProvenanceModeSnapshot {
		t.Fatalf("Mode = %q, want %q", req.Mode, apicontract.ProvenanceModeSnapshot)
	}
	if req.SnapshotID == "" {
		t.Fatalf("fixture execution_request_snapshot.json has no snapshotId")
	}
}

// --- invalid shapes: req.Validate() rejections, status AND code ---

// postRunQuery marshals req, POSTs it to the real runQueryHandler through
// httptest (exercising decodeContractBody + validateScope + req.Validate()
// + requestValidationError exactly as a live server would), and decodes the
// response as an apicontract.ErrorEnvelope.
func postRunQuery(t *testing.T, req apicontract.ExecutionRequest) (status int, env apicontract.ErrorEnvelope) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body))
	runQueryHandler(w, r)
	if w.Code >= 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal error envelope: %v (body: %s)", err, w.Body.String())
		}
	}
	return w.Code, env
}

// TestExecRunQuery_InvalidRequestBodies_StatusAndCode is the brief's
// "hand-built invalid bodies for each error code" table: every case starts
// from a minimal valid ad-hoc-DTQL request (base()) and perturbs exactly
// one field, so each failure is attributable to the one field named in
// wantField. Several cases exist specifically to document a genuine
// classification change from adopting req.Validate() + requestValidationError
// (contract_error.go, PR #215's classifier, reused verbatim here per the
// brief's "mirror that exactly") in place of the hand-written checks this
// stream deleted — see each case's own comment and this stream's final
// report for the full list.
func TestExecRunQuery_InvalidRequestBodies_StatusAndCode(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	base := func() apicontract.ExecutionRequest {
		return apicontract.ExecutionRequest{
			Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
			Source: semanticTestSource, DTQL: runQueryTestCustomerByIDDTQL,
			Parameters: map[string]apicontract.TypedValue{}, BindingOrigins: []apicontract.BindingOriginEntry{},
			Mode: apicontract.ProvenanceModeLive,
		}
	}
	limit0 := 0
	limit501 := 501

	cases := []struct {
		name       string
		build      func() apicontract.ExecutionRequest
		wantStatus int
		wantCode   apicontract.ErrorCode
		wantField  string
	}{
		{
			name: "mode absent is now rejected (used to default to live)",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.Mode = ""
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeInvalidRequest, wantField: "mode",
		},
		{
			name: "mode unknown value",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.Mode = "bogus"
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeInvalidRequest, wantField: "mode",
		},
		{
			name: "explicit limit 0 is INVALID_REQUEST (lead session's semantics assumption; absent limit still defaults via boundLimit)",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.Limit = &limit0
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeInvalidRequest, wantField: "limit",
		},
		{
			name: "limit over the 500 cap is now rejected (used to silently clamp via boundLimit)",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.Limit = &limit501
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeInvalidRequest, wantField: "limit",
		},
		{
			name: "mode snapshot without snapshotId",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.Mode = apicontract.ProvenanceModeSnapshot
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeMissingParameter, wantField: "snapshotId",
		},
		{
			name: "neither queryId nor dtql (exclusivity violation now classifies MISSING_PARAMETER, not the deleted check's INVALID_REQUEST)",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.DTQL = ""
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeMissingParameter, wantField: "queryId/dtql",
		},
		{
			name: "both queryId and dtql set (same exclusivity message, same classification)",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.QueryID = "customers/customer-invoices"
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeMissingParameter, wantField: "queryId/dtql",
		},
		{
			name: "dtql set with an empty source (now INVALID_REQUEST, was MISSING_PARAMETER from the deleted resolveExecutionSource check)",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.Source = ""
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeInvalidRequest, wantField: "source",
		},
		{
			name: "bindingOrigins names an unrecognized origin",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.Parameters = map[string]apicontract.TypedValue{"CustomerId": apicontract.NewIntegerValue("5")}
				req.BindingOrigins = []apicontract.BindingOriginEntry{{ParameterID: "CustomerId", Origin: "bogus"}}
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeInvalidRequest, wantField: "bindingOrigins",
		},
		{
			name: "bindingOrigins missing an entry for a supplied parameter",
			build: func() apicontract.ExecutionRequest {
				req := base()
				req.Parameters = map[string]apicontract.TypedValue{"CustomerId": apicontract.NewIntegerValue("5")}
				return req
			},
			wantStatus: http.StatusBadRequest, wantCode: apicontract.ErrCodeInvalidRequest, wantField: "bindingOrigins",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, env := postRunQuery(t, tc.build())
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d (code=%q message=%q)", status, tc.wantStatus, env.Error.Code, env.Error.Message)
			}
			if env.Error.Code != string(tc.wantCode) {
				t.Errorf("code = %q, want %q", env.Error.Code, tc.wantCode)
			}
			if tc.wantField != "" && env.Error.Field != tc.wantField {
				t.Errorf("field = %q, want %q", env.Error.Field, tc.wantField)
			}
		})
	}
}

// TestExecRunQuery_DeclaredParameterTypeMismatch_StillTypeMismatch proves
// TYPE_MISMATCH is still reachable for exec/run_query after this stream's
// change — but NOT through req.Validate(). ExecutionRequest.Validate()
// wraps every Parameters entry's own TypedValue.Validate() failure under
// Field "parameters" (core's execution_request.go), never "value" (unlike
// RelatedRowsRequest's direct r.Value.Validate() — see
// requestValidationError's own doc comment in contract_error.go), so
// requestValidationError can only ever classify an ExecutionRequest
// Validate() failure as MISSING_PARAMETER or INVALID_REQUEST, never
// TYPE_MISMATCH — and in practice a malformed TypedValue never even reaches
// Validate() at all, since TypedValue.UnmarshalJSON already calls its own
// Validate() at JSON-decode time, before computeRunQuery ever runs (a
// malformed TypedValue instead surfaces as a decode-time INVALID_REQUEST
// from decodeContractBody, before req.Validate() is reached). TYPE_MISMATCH
// is produced instead by the pre-existing, KEPT typedParametersToVariables
// check: a syntactically valid TypedValue (e.g. a string) whose Type
// disagrees with the SAVED QUERY's own declared parameter type (e.g.
// CustomerId:integer) — business logic req.Validate() has no way to know
// about, since it never sees queryDef.
func TestExecRunQuery_DeclaredParameterTypeMismatch_StillTypeMismatch(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	req := apicontract.ExecutionRequest{
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		QueryID: "customers/customer-invoices",
		Parameters: map[string]apicontract.TypedValue{
			"CustomerId": apicontract.NewStringValue("5"), // declared "integer" on the saved query
		},
		BindingOrigins: []apicontract.BindingOriginEntry{{ParameterID: "CustomerId", Origin: apicontract.BindingOriginSelection}},
		Mode:           apicontract.ProvenanceModeLive,
	}
	status, env := postRunQuery(t, req)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeTypeMismatch) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeTypeMismatch)
	}
}

// TestExecRunQuery_MissingDeclaredRequiredParameter_StillMissingParameter
// proves MISSING_PARAMETER for a saved query's own declared required
// parameter (queryDef.Parameters[].IsRequired) is ALSO still produced by
// the pre-existing, KEPT typedParametersToVariables check, not by
// req.Validate() — Validate() has no visibility into a specific saved
// query's own parameter declarations, only into the wire envelope itself.
func TestExecRunQuery_MissingDeclaredRequiredParameter_StillMissingParameter(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	req := apicontract.ExecutionRequest{
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		QueryID:        "customers/customer-invoices",
		Parameters:     map[string]apicontract.TypedValue{},
		BindingOrigins: []apicontract.BindingOriginEntry{},
		Mode:           apicontract.ProvenanceModeLive,
	}
	status, env := postRunQuery(t, req)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (code=%q message=%q)", status, env.Error.Code, env.Error.Message)
	}
	if env.Error.Code != string(apicontract.ErrCodeMissingParameter) {
		t.Errorf("code = %q, want %q", env.Error.Code, apicontract.ErrCodeMissingParameter)
	}
	if env.Error.Field != "CustomerId" {
		t.Errorf("field = %q, want %q", env.Error.Field, "CustomerId")
	}
}
