package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// This file is Task 13's (S107) own acceptance layer, on top of what
// security_matrix_test.go already proves (restricted-rows-and-columns-
// server, native-sql-labelled, hidden-column-refused): it converges legacy
// and contract routes' failure behaviour for shapes/attacks the existing
// suite did not yet exercise end to end. See
// spec/research/2026-09-09-layered-acl-reconciliation.md for the port
// context this stream also reconciled.

// customerAliasedColumnDTQL is the "unsupported protected shape" case
// pkg/accesspolicies/run.go's own ErrInvalidQuery documents: an aliased
// column under support's field-restricted policy. This looked, before
// writing this test, like it might fall through to writeContractError's
// generic default (500 INTERNAL, leaking the raw error text) — it does
// not: exec_run_query.go's own handler already has a local catch-all
// (`return apicontract.Result{}, newInvalidRequest("", err.Error())`,
// reached once the handler's explicit ErrOpaqueSQLNotGranted/
// ErrAccessDenied/ErrSourceFileMissing/dalgo2http cases are excluded) that
// already maps this, and every other unclassified error, to 400
// INVALID_REQUEST. Verified empirically by reverting to unmodified main and
// re-running this exact test before writing it any other way: it already
// passes there. This test exists to lock that already-correct behavior in
// as regression coverage (Task 13 item (a): "test current and legacy
// routes ... for unsupported protected shapes") — no production code
// change was needed for exec/run_query's own contract-envelope half.
const customerAliasedColumnDTQL = `from:
  name: Customer
columns:
  - field: CustomerId
  - field: FirstName
    as: display_name
where:
  op: "=="
  left:
    field: CustomerId
  right:
    param: CustomerId
`

// customerInvoiceDTQL selects from a collection ("Invoice") that
// securityMatrixPolicy's support ruleSet names no path for at all —
// default: deny admits nothing here, distinct from
// TestRunQuery_HiddenColumnExplicit_Refused's "one hidden field within an
// otherwise-granted source" shape.
const customerInvoiceDTQL = `from:
  name: Invoice
where:
  op: "=="
  left:
    field: CustomerId
  right:
    param: CustomerId
`

// TestRunQuery_UnsupportedShape_ColumnAlias_InvalidRequest is Task 13 item
// (a)'s contract-route half: exec/run_query fails closed, with the
// appendix's own closed error-code set, for a structured shape
// accesspolicies.Run refuses before execution — a 400 INVALID_REQUEST, not
// a bare 500 with leaked internal text.
func TestRunQuery_UnsupportedShape_ColumnAlias_InvalidRequest(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	info := fetchAgentInfo(t, baseURL)

	value, origin := intParam("CustomerId", 2)
	request := apicontract.ExecutionRequest{
		Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
		Source: securityMatrixSource, DTQL: customerAliasedColumnDTQL,
		Parameters:     map[string]apicontract.TypedValueOrSet{"CustomerId": apicontract.ScalarValue(value)},
		BindingOrigins: []apicontract.BindingOriginEntry{origin},
		Mode:           apicontract.ProvenanceModeLive,
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
	if status != http.StatusBadRequest {
		t.Fatalf("run_query(aliased column) status = %d, want 400; body %s", status, raw)
	}
	var errResponse apicontract.ErrorEnvelope
	if err := json.Unmarshal(raw, &errResponse); err != nil {
		t.Fatalf("decode error response: %v (body %s)", err, raw)
	}
	if errResponse.Error.Code != string(apicontract.ErrCodeInvalidRequest) {
		t.Errorf("error code = %q, want INVALID_REQUEST", errResponse.Error.Code)
	}
	if errResponse.Error.RequestID == "" {
		t.Errorf("error.requestId is empty")
	}
	if bytes.Contains(raw, []byte(`"recordset"`)) {
		t.Errorf("refusal body carries recordset data: %s", raw)
	}
}

// TestRunQuery_ForbiddenSource_AccessDenied is Task 13 item (c)'s "forbidden
// source" half (distinct from TestRunQuery_HiddenColumnExplicit_Refused's
// "hidden field within an otherwise-granted source"): a source support's
// policy names no path for at all is denied outright, with no metadata
// leak (no row data, no table/column names beyond what the request itself
// already named).
func TestRunQuery_ForbiddenSource_AccessDenied(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	info := fetchAgentInfo(t, baseURL)

	value, origin := intParam("CustomerId", 2)
	request := apicontract.ExecutionRequest{
		Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
		Source: securityMatrixSource, DTQL: customerInvoiceDTQL,
		Parameters:     map[string]apicontract.TypedValueOrSet{"CustomerId": apicontract.ScalarValue(value)},
		BindingOrigins: []apicontract.BindingOriginEntry{origin},
		Mode:           apicontract.ProvenanceModeLive,
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
	if status != http.StatusForbidden {
		t.Fatalf("run_query(forbidden source Invoice) status = %d, want 403; body %s", status, raw)
	}
	var errResponse apicontract.ErrorEnvelope
	if err := json.Unmarshal(raw, &errResponse); err != nil {
		t.Fatalf("decode error response: %v (body %s)", err, raw)
	}
	if errResponse.Error.Code != string(apicontract.ErrCodeAccessDenied) {
		t.Errorf("error code = %q, want ACCESS_DENIED", errResponse.Error.Code)
	}
	if bytes.Contains(raw, []byte(`"recordset"`)) {
		t.Errorf("refusal body carries recordset data: %s", raw)
	}
}

// TestRunQuery_ForgedSecurityContextID_StaleContext is Task 13 item (b)'s
// "forged securityContextId -> STALE_CONTEXT" half. validateScope
// (contract_scope.go) already implements this for every contract route;
// this is the missing direct proof on exec/run_query specifically.
func TestRunQuery_ForgedSecurityContextID_StaleContext(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	// Deliberately never call agent-info: any securityContextId this
	// process did not itself mint is indistinguishable from a forged one.
	value, origin := intParam("CustomerId", 2)
	request := apicontract.ExecutionRequest{
		Project: projectID, Environment: securityMatrixEnv, SecurityContextID: "forged-does-not-exist",
		Source: securityMatrixSource, DTQL: customerByIDDTQL,
		Parameters:     map[string]apicontract.TypedValueOrSet{"CustomerId": apicontract.ScalarValue(value)},
		BindingOrigins: []apicontract.BindingOriginEntry{origin},
		Mode:           apicontract.ProvenanceModeLive,
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
	if status != http.StatusConflict {
		t.Fatalf("run_query(forged securityContextId) status = %d, want 409; body %s", status, raw)
	}
	var errResponse apicontract.ErrorEnvelope
	if err := json.Unmarshal(raw, &errResponse); err != nil {
		t.Fatalf("decode error response: %v (body %s)", err, raw)
	}
	if errResponse.Error.Code != string(apicontract.ErrCodeStaleContext) {
		t.Errorf("error code = %q, want STALE_CONTEXT", errResponse.Error.Code)
	}
}

// TestRunQuery_PrincipalFieldsInBody_RequestRejectedNotElevated is Task 13
// item (b)'s "no as=/role= anywhere" half for the request BODY: a request
// smuggling a principal-shaped field inside the contract JSON body is
// rejected outright (decodeContractBody's DisallowUnknownFields,
// api-contract.md: "a client-supplied principal or role are rejected,
// never reconciled by precedence") rather than silently accepted as an
// override, and rejection alone proves it never reached the executor with
// an elevated identity.
func TestRunQuery_PrincipalFieldsInBody_RequestRejectedNotElevated(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	info := fetchAgentInfo(t, baseURL)

	// A hand-built body: apicontract.ExecutionRequest carries no "as"/
	// "role"/"principal" field to marshal, so this is deliberately raw
	// JSON standing in for a hand-crafted attack request, not something
	// Go's own struct could accidentally produce.
	bodyMap := map[string]any{
		"project": projectID, "environment": securityMatrixEnv, "securityContextId": info.SecurityContextID,
		"source": securityMatrixSource, "dtql": customerByIDDTQL,
		"parameters":     map[string]any{"CustomerId": map[string]any{"type": "integer", "value": "2"}},
		"bindingOrigins": []map[string]any{{"parameterId": "CustomerId", "origin": "manual"}},
		"mode":           "live",
		"as":             "admin",
		"role":           "admin",
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", bodyMap)
	if status != http.StatusBadRequest {
		t.Fatalf("run_query(as/role smuggled in body) status = %d, want 400 (rejected, never silently elevated); body %s", status, raw)
	}
	var errResponse apicontract.ErrorEnvelope
	if err := json.Unmarshal(raw, &errResponse); err != nil {
		t.Fatalf("decode error response: %v (body %s)", err, raw)
	}
	if errResponse.Error.Code != string(apicontract.ErrCodeInvalidRequest) {
		t.Errorf("error code = %q, want INVALID_REQUEST", errResponse.Error.Code)
	}
}

// TestRunQuery_PrincipalFieldsInQueryStringAndHeader_Ignored is Task 13 item
// (b)'s "no as=/role= anywhere" half for the URL query string and headers:
// exec/run_query reads Scope/identity from the JSON body alone
// (api-contract.md: "GET parameters go in the URL query; POST parameters
// go only in the JSON body" plus the Scope/session model), so a ?as=
// /?role= query string or a spoofed principal header riding alongside a
// genuinely support-scoped body changes nothing — the response is
// byte-for-byte the restricted (support) outcome, not the admin one.
func TestRunQuery_PrincipalFieldsInQueryStringAndHeader_Ignored(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	info := fetchAgentInfo(t, baseURL)

	value, origin := intParam("CustomerId", 2)
	request := apicontract.ExecutionRequest{
		Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
		Source: securityMatrixSource, DTQL: customerByIDDTQL,
		Parameters:     map[string]apicontract.TypedValueOrSet{"CustomerId": apicontract.ScalarValue(value)},
		BindingOrigins: []apicontract.BindingOriginEntry{origin},
		Mode:           apicontract.ProvenanceModeLive,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/datatug/exec/run_query?as=admin&role=admin&principal=admin", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Datatug-As", "admin")
	httpReq.Header.Set("X-Datatug-Role", "admin")
	resp, err := testHTTPClient.Do(httpReq)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run_query(as/role in query+header) status = %d, want 200", resp.StatusCode)
	}
	var result apicontract.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Cathy (Canadian) is visible to support, but Email is still hidden —
	// exactly TestRunQuery_RestrictedRowsAndColumns's own support-scoped
	// outcome, proving the query string/header never elevated this request
	// to admin (which would additionally show Email).
	for _, col := range result.Recordset.Columns {
		if col.Name == "Email" {
			t.Fatalf("Email column present: the query string/header ?as=/?role=/X-Datatug-* elevated this request to admin; body columns=%+v", result.Recordset.Columns)
		}
	}
	if len(result.Limitations) == 0 {
		t.Errorf("Limitations = %+v, want a policy-attributed entry (support scope, not admin)", result.Limitations)
	}
}

// TestExecuteSelect_PrincipalQueryParamsIgnored is Task 13 item (b)'s
// legacy-route half: exec/select (GET, query-param driven) reads only the
// specific parameters SelectRequest names (pkg/api/execute_select_api.go) —
// an extra ?as=/?role= alongside a genuine request changes nothing; the
// session's own configured principal (support) still applies.
func TestExecuteSelect_PrincipalQueryParamsIgnored(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)

	requestURL := fmt.Sprintf("%s/datatug/exec/select?proj=%s&env=%s&db=%s&from=Customer&as=admin&role=admin&principal=admin",
		baseURL, projectID, securityMatrixEnv, securityMatrixSource)
	status, raw := getURL(t, requestURL)
	if status != http.StatusOK {
		t.Fatalf("exec/select(as/role in query) status = %d, want 200; body %s", status, raw)
	}
	var response api.QueryResultResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, raw)
	}
	// support's row policy still applies (only the Canadian customer, id 2)
	// and Email is still absent from the column list — proving ?as=admin
	// never elevated this request.
	if len(response.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (support's row policy still applied); body %s", len(response.Rows), raw)
	}
	for _, col := range response.Columns {
		if col == "Email" {
			t.Fatalf("Email column present: ?as=admin/?role=admin elevated this request; columns=%v", response.Columns)
		}
	}
}

// TestExecuteCommands_NativeSQL_RefusedWithoutGrant is Task 13 item (e) for
// the one legacy route that had no native-sql-labelled coverage at all:
// exec/execute_commands's SQL command type shares
// secureread.Executor.RunNativeSQL's opaque-grant gate with exec/select and
// exec/run_query (api-contract.md: "All legacy routes obey the same
// rule"), but nothing exercised it directly before this stream.
func TestExecuteCommands_NativeSQL_RefusedWithoutGrant(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session) // Capabilities{} — no --allow-opaque-sql.

	request := api.ExecuteCommandsRequest{
		Commands: []api.ExecuteCommandRequest{
			{ID: "c1", Type: "SQL", Text: "select CustomerId, FirstName, Country from Customer order by CustomerId", Env: securityMatrixEnv, DB: securityMatrixSource},
		},
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/execute_commands?project="+projectID, request)
	if status != http.StatusForbidden {
		t.Fatalf("execute_commands(sql, no grant) status = %d, want 403; body %s", status, raw)
	}
	var errResponse struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(raw, &errResponse); err != nil {
		t.Fatalf("decode error response: %v (body %s)", err, raw)
	}
	if errResponse.Code != "UNSUPPORTED_PROTECTED_EXECUTION" {
		t.Errorf("error code = %q, want UNSUPPORTED_PROTECTED_EXECUTION", errResponse.Code)
	}
}

// TestExecuteCommands_NativeSQL_LabelledWithGrant is
// TestExecuteCommands_NativeSQL_RefusedWithoutGrant's "with the grant"
// counterpart (AC native-sql-labelled's second half): the same command
// executes and reports opaque-privileged provenance, bypassing row policy
// (both customers return).
func TestExecuteCommands_NativeSQL_LabelledWithGrant(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSessionAndCapabilities(t, pathsByID, session, api.Capabilities{AllowOpaqueSQL: true})

	request := api.ExecuteCommandsRequest{
		Commands: []api.ExecuteCommandRequest{
			{ID: "c1", Type: "SQL", Text: "select CustomerId, FirstName, Country from Customer order by CustomerId", Env: securityMatrixEnv, DB: securityMatrixSource},
		},
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/execute_commands?project="+projectID, request)
	if status != http.StatusOK {
		t.Fatalf("execute_commands(sql, with grant) status = %d, want 200; body %s", status, raw)
	}
	var response api.ExecuteCommandsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, raw)
	}
	if len(response.Commands) != 1 || len(response.Commands[0].Items) != 1 {
		t.Fatalf("commands = %+v", response.Commands)
	}
	itemEncoded, err := json.Marshal(response.Commands[0].Items[0].Value)
	if err != nil {
		t.Fatalf("marshal item value: %v", err)
	}
	var itemResult api.QueryResultResponse
	if err := json.Unmarshal(itemEncoded, &itemResult); err != nil {
		t.Fatalf("decode item value: %v (raw %s)", err, itemEncoded)
	}
	if len(itemResult.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (native SQL is not row-restricted); body %s", len(itemResult.Rows), itemEncoded)
	}
	if itemResult.Provenance.ExecutionProfile != apicontract.ExecutionProfileOpaquePrivileged {
		t.Errorf("Provenance.ExecutionProfile = %q, want opaque-privileged", itemResult.Provenance.ExecutionProfile)
	}
}
