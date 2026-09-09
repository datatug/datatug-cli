package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/apicontract_local"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"

	_ "modernc.org/sqlite" // sqlite driver, matching pkg/secureread's own fixtures
)

// This file is the Feature core-investigation-loop security matrix at the
// HTTP level (brief item 3/4, plan tasks 6 and 12): restricted-rows-and-
// columns-server, hidden-column-refused, native-sql-labelled and
// dtql-query-runs, run against a synthetic project shaped like
// datatug-demo-projects/demo-project-1's Customer table and
// policies/customers.yaml (row-restricted-by-Country, Email hidden for
// `support`) — the real demo project ships no committed .sqlite file, so
// these tests build their own fixture rather than depending on one.
//
// Task 12 (S64) rewrote exec/run_query to the appendix's ExecutionRequest ->
// Result envelope: every request/response shape below changed from the
// previous api.RunQueryRequest/RunQueryResponse ad-hoc pair to
// apicontract_local's contract types, and every call now first fetches
// agent-info for a current securityContextId (api-contract.md "Scope and
// identity": "An ID is a staleness check ... agent-info obtains the initial
// ID").

const (
	securityMatrixEnv = "local"
	// securityMatrixSource is the catalog's DbModel/DbCatalog ID (both set
	// to the same value by newSecurityMatrixProject below) — the appendix's
	// stable SourceRef.source, resolved by pkg/api's unified resolver
	// (resolver.go), replacing the old catalog-ID-only "database" parameter.
	securityMatrixSource = "chinook"
)

// securityMatrixPolicy mirrors datatug-demo-projects/demo-project-1/policies/customers.yaml
// (admin: unrestricted; support: Customer rows where Country == Canada, Email
// left out of the field allow-list) plus an opaqueQuery scope per role so
// native SQL text execution (REQ:opaque-sql-limitation) is exercisable too.
const securityMatrixPolicy = `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata:
  name: security-matrix
default: deny
ruleSets:
  admin:
    - path: /**
      rules:
        - id: admin-full-access
          effect: allow
          operations: [readwrite]
    - opaqueQuery: true
      rules:
        - id: admin-native-sql
          effect: allow
          operations: [query]
  support:
    - path: /Customer
      rules:
        - id: customers-support
          effect: allow
          operations: [query]
          where:
            op: "=="
            left: { field: Country }
            right: { value: Canada }
          fields: [CustomerId, FirstName, Country]
    - opaqueQuery: true
      rules:
        - id: support-native-sql
          effect: allow
          operations: [query]
bindings:
  roles:
    admin: [admin]
    support: [support]
`

// customerByIDDTQL selects every field of Customer filtered by CustomerId, the
// same "wildcard select, policy hides the rest" shape
// core-investigation-loop's saved DTQL queries use.
const customerByIDDTQL = `from:
  name: Customer
where:
  op: "=="
  left:
    field: CustomerId
  right:
    param: CustomerId
`

// customerEmailExplicitDTQL explicitly selects the Email column — the
// hand-crafted "ask for the hidden column directly" shape AC
// hidden-column-refused exercises: an explicit reference must be refused
// outright, not silently redacted.
const customerEmailExplicitDTQL = `from:
  name: Customer
columns:
  - field: CustomerId
  - field: Email
where:
  op: "=="
  left:
    field: CustomerId
  right:
    param: CustomerId
`

// securityMatrixSession builds the secureread.Session `datatug serve --as
// <as> --role <role>` would build against the fixture's policies/ directory
// (resolveServeSession's own logic lives in apps/datatugapp/commands and is
// unit-tested there; this helper only needs a valid Session to hand
// ServeHTTP, not that resolution logic itself).
func securityMatrixSession(t *testing.T, as, role string) secureread.Session {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(securityMatrixPolicy), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{
		As: as, Roles: []string{role}, PoliciesDir: dir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

// newSecurityMatrixProject builds a synthetic project directory: a SQLite
// fixture DB with a Customer table (two rows: a Brazilian and a Canadian
// customer), an environment + DB catalog pointing at it (DbModel AND
// catalog ID both "chinook" — see securityMatrixSource), and a saved DTQL
// query ("customer-by-id") and a saved SQL query ("customers-sql"). It
// returns the pathsByID map ServeHTTP expects and the project ID.
func newSecurityMatrixProject(t *testing.T) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	const projectID = "security-matrix-project"

	dbPath := filepath.Join(dir, "chinook.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()
	statements := []string{
		`CREATE TABLE Customer (CustomerId INTEGER PRIMARY KEY, FirstName TEXT, Email TEXT, Country TEXT)`,
		`INSERT INTO Customer (CustomerId, FirstName, Email, Country) VALUES (1, 'Ana', 'ana@example.com', 'Brazil')`,
		`INSERT INTO Customer (CustomerId, FirstName, Email, Country) VALUES (2, 'Cathy', 'cathy@example.com', 'Canada')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	projStore := filestore.NewProjectStore(projectID, dir)
	ctx := context.Background()

	env := &datatug.Environment{
		DbServers: datatug.EnvDbServers{
			{ServerRef: datatug.ServerRef{Driver: "sqlite3"}},
		},
	}
	env.ID = securityMatrixEnv
	if err := projStore.SaveEnvironment(ctx, env); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}

	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: dbPath, DbModel: securityMatrixSource}}
	catalog.ID = securityMatrixSource
	if err := projStore.SaveEnvDbCatalog(ctx, securityMatrixEnv, serverID, securityMatrixSource, catalog); err != nil {
		t.Fatalf("SaveEnvDbCatalog: %v", err)
	}

	// Write the saved-query fixtures directly (JSON metadata + the
	// "<id>.query.<type>" text sidecar) rather than through
	// datatug.ProjectStore.SaveQuery: that interface method delegates to the
	// generic fsProjectItemsStore.saveProjectItem, which writes only the
	// JSON file — it does not write the Text sidecar at all (only the
	// separate, non-interface fsQueriesStore.CreateQuery does, via its own
	// private saveQuery). api.LoadQueryDocument (pkg/api/source_resolver.go)
	// reads that sidecar directly, matching CreateQuery's write shape, so
	// the fixture must match it too. This is a pre-existing gap in
	// pkg/datatug-core, not something this stream's wiring scope covers.
	queriesDir := filepath.Join(dir, "queries")
	if err := os.MkdirAll(queriesDir, 0o755); err != nil {
		t.Fatalf("mkdir queries dir: %v", err)
	}
	saveQuery := func(id, title string, queryType datatug.QueryType, text string, parameters datatug.Parameters) {
		t.Helper()
		def := datatug.QueryDef{
			ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id, Title: title}},
			Type:        queryType,
			Parameters:  parameters,
		}
		encoded, err := json.Marshal(def)
		if err != nil {
			t.Fatalf("marshal query %s: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(queriesDir, id+".query.json"), encoded, 0o600); err != nil {
			t.Fatalf("write query json %s: %v", id, err)
		}
		sidecar := fmt.Sprintf("%s.query.%s", id, strings.ToLower(string(queryType)))
		if err := os.WriteFile(filepath.Join(queriesDir, sidecar), []byte(text), 0o600); err != nil {
			t.Fatalf("write query sidecar %s: %v", id, err)
		}
	}
	saveQuery("customer-by-id", "Customer by ID", datatug.QueryTypeDTQL, customerByIDDTQL,
		datatug.Parameters{{ID: "CustomerId", Type: "integer", IsRequired: true}})
	// customers-sql declares NO parameters: its text lists every customer
	// unconditionally, matching requiresSQLParameterBinding's "unparameterized
	// SQL query ... runs as-is" case (exec_run_query.go) — see that file's
	// doc comment for why exec/run_query refuses a PARAMETERIZED native SQL
	// query rather than reintroducing textual substitution.
	saveQuery("customers-sql", "Customers (SQL)", datatug.QueryTypeSQL,
		"select CustomerId, FirstName, Country from Customer order by CustomerId", nil)

	return map[string]string{projectID: dir}, projectID
}

// postJSON POSTs body (JSON-encoded) to baseURL+path and decodes the
// response into out; it always returns the raw status code and response
// bytes too, so a test can assert on both the shape and the exact JSON
// (e.g. that no hidden value leaked into an error body).
func postJSON(t *testing.T, baseURL, path string, body any) (status int, raw []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := testHTTPClient.Post(baseURL+path, "application/json", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, raw
}

func getURL(t *testing.T, requestURL string) (status int, raw []byte) {
	t.Helper()
	resp, err := testHTTPClient.Get(requestURL)
	if err != nil {
		t.Fatalf("GET %s: %v", requestURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, raw
}

// fetchAgentInfo calls GET agent-info and decodes its exact contract
// envelope — every scoped request below needs its securityContextId first
// (api-contract.md: "agent-info obtains the initial ID").
func fetchAgentInfo(t *testing.T, baseURL string) apicontract_local.AgentInfoResponse {
	t.Helper()
	status, raw := getURL(t, baseURL+"/datatug/agent-info")
	if status != http.StatusOK {
		t.Fatalf("GET agent-info: status %d, body %s", status, raw)
	}
	var info apicontract_local.AgentInfoResponse
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode agent-info: %v (body %s)", err, raw)
	}
	return info
}

// intParam builds one manual-origin integer ExecutionRequest parameter/
// bindingOrigins pair for CustomerId — every test below drives run_query by
// hand, standing in for the browser's own auto-binding (task 15's scope).
func intParam(id string, value int64) (apicontract_local.TypedValue, apicontract_local.BindingOriginInput) {
	return apicontract_local.NewIntegerValue(value), apicontract_local.BindingOriginInput{ParameterID: id, Origin: apicontract_local.BindingOriginManual}
}

// TestRunQuery_RestrictedRowsAndColumns is AC restricted-rows-and-columns-server:
// `datatug serve --as <support>`; POST /datatug/exec/run_query runs a saved
// DTQL query for a Brazilian customer (zero rows, rowsFiltered) and then a
// Canadian customer (rows without Email, hiddenColumns=[Email]); agent-info
// reports principal support with its explicit role.
func TestRunQuery_RestrictedRowsAndColumns(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)

	info := fetchAgentInfo(t, baseURL)
	t.Run("agent-info reports the principal with explicit role", func(t *testing.T) {
		if info.Principal.ID != "agent1" {
			t.Fatalf("agent-info principal.id = %q, want agent1", info.Principal.ID)
		}
		if len(info.Principal.Roles) != 1 || info.Principal.Roles[0] != "support" {
			t.Fatalf("agent-info principal.roles = %v, want [support]", info.Principal.Roles)
		}
		if info.SecurityContextID == "" {
			t.Fatal("agent-info securityContextId is empty")
		}
	})

	t.Run("Brazilian customer: zero rows, rowsFiltered", func(t *testing.T) {
		value, origin := intParam("CustomerId", 1)
		request := apicontract_local.ExecutionRequest{
			Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
			Source: securityMatrixSource, QueryID: "customer-by-id",
			Parameters:     map[string]apicontract_local.TypedValue{"CustomerId": value},
			BindingOrigins: []apicontract_local.BindingOriginInput{origin},
			Mode:           apicontract_local.ModeLive,
		}
		status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
		if status != http.StatusOK {
			t.Fatalf("run_query: status %d, body %s", status, raw)
		}
		var response apicontract_local.Result
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatalf("decode response: %v (body %s)", err, raw)
		}
		if len(response.Recordset.Rows) != 0 {
			t.Fatalf("rows = %d, want 0 (Brazil is not support's Country); body %s", len(response.Recordset.Rows), raw)
		}
		if len(response.Limitations) == 0 || !response.Limitations[0].RowsFiltered {
			t.Errorf("Limitations = %+v, want a rowsFiltered entry", response.Limitations)
		}
		if response.Provenance.ExecutionProfile != apicontract_local.ProfileProtected {
			t.Errorf("Provenance.ExecutionProfile = %q, want protected", response.Provenance.ExecutionProfile)
		}
	})

	t.Run("Canadian customer: rows without Email, hiddenColumns", func(t *testing.T) {
		value, origin := intParam("CustomerId", 2)
		request := apicontract_local.ExecutionRequest{
			Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
			Source: securityMatrixSource, QueryID: "customer-by-id",
			Parameters:     map[string]apicontract_local.TypedValue{"CustomerId": value},
			BindingOrigins: []apicontract_local.BindingOriginInput{origin},
			Mode:           apicontract_local.ModeLive,
		}
		status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
		if status != http.StatusOK {
			t.Fatalf("run_query: status %d, body %s", status, raw)
		}
		var response apicontract_local.Result
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Fatalf("decode response: %v (body %s)", err, raw)
		}
		if len(response.Recordset.Rows) != 1 {
			t.Fatalf("rows = %d, want 1 (Cathy, Canada); body %s", len(response.Recordset.Rows), raw)
		}
		row := response.Recordset.Rows[0]
		colIndex := func(name string) int {
			for i, c := range response.Recordset.Columns {
				if c.Name == name {
					return i
				}
			}
			return -1
		}
		if idx := colIndex("Email"); idx >= 0 {
			t.Errorf("row still carries an Email column: %+v", response.Recordset.Columns)
		}
		if idx := colIndex("FirstName"); idx < 0 || row[idx].Text != "Cathy" {
			t.Errorf("row FirstName = %+v, want Cathy", row)
		}
		if len(response.Limitations) == 0 {
			t.Fatalf("Limitations = %+v, want a hiddenColumns entry", response.Limitations)
		}
		hidden := response.Limitations[0].HiddenColumns
		if len(hidden) != 1 || hidden[0] != "Email" {
			t.Errorf("hiddenColumns = %v, want [Email]", hidden)
		}
		// The raw response body must never carry the hidden email value either.
		if bytes.Contains(raw, []byte("cathy@example.com")) {
			t.Errorf("response body leaks the hidden Email value: %s", raw)
		}
	})
}

// TestRunQuery_DTQL_RunsForAdmin is (the admin half of) AC dtql-query-runs:
// a saved DTQL query runs through the policy path and returns rows, with
// execution-confirmed bindingsApplied.
func TestRunQuery_DTQL_RunsForAdmin(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "boss", "admin")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	info := fetchAgentInfo(t, baseURL)

	value, origin := intParam("CustomerId", 1)
	request := apicontract_local.ExecutionRequest{
		Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
		Source: securityMatrixSource, QueryID: "customer-by-id",
		Parameters:     map[string]apicontract_local.TypedValue{"CustomerId": value},
		BindingOrigins: []apicontract_local.BindingOriginInput{origin},
		Mode:           apicontract_local.ModeLive,
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
	if status != http.StatusOK {
		t.Fatalf("run_query: status %d, body %s", status, raw)
	}
	var response apicontract_local.Result
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, raw)
	}
	if len(response.Recordset.Rows) != 1 {
		t.Fatalf("rows = %d, want 1 (admin sees every row); body %s", len(response.Recordset.Rows), raw)
	}
	emailFound := false
	for i, c := range response.Recordset.Columns {
		if c.Name == "Email" && response.Recordset.Rows[0][i].Text == "ana@example.com" {
			emailFound = true
		}
	}
	if !emailFound {
		t.Errorf("admin row missing Email: columns=%+v row=%+v", response.Recordset.Columns, response.Recordset.Rows[0])
	}
	if len(response.BindingsApplied) != 1 || response.BindingsApplied[0].ParameterID != "CustomerId" || response.BindingsApplied[0].Value.Text != "1" {
		t.Errorf("BindingsApplied = %+v, want [{CustomerId, integer 1}]", response.BindingsApplied)
	}
	if response.BindingsApplied[0].OriginEvidence != apicontract_local.EvidenceClientReported {
		t.Errorf("BindingsApplied[0].OriginEvidence = %q, want client-reported", response.BindingsApplied[0].OriginEvidence)
	}
}

// TestRunQuery_HiddenColumnExplicit_Refused is AC hidden-column-refused: a
// hand-crafted ad-hoc DTQL document that explicitly selects Email must be
// refused with ACCESS_DENIED and no row data, naming the field but never
// leaking the hidden value.
func TestRunQuery_HiddenColumnExplicit_Refused(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)
	info := fetchAgentInfo(t, baseURL)

	value, origin := intParam("CustomerId", 2)
	request := apicontract_local.ExecutionRequest{
		Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
		Source: securityMatrixSource, DTQL: customerEmailExplicitDTQL,
		Parameters:     map[string]apicontract_local.TypedValue{"CustomerId": value},
		BindingOrigins: []apicontract_local.BindingOriginInput{origin},
		Mode:           apicontract_local.ModeLive,
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
	if status != http.StatusForbidden {
		t.Fatalf("run_query(explicit Email select) status = %d, want 403; body %s", status, raw)
	}
	var errResponse apicontract_local.ErrorEnvelope
	if err := json.Unmarshal(raw, &errResponse); err != nil {
		t.Fatalf("decode error response: %v (body %s)", err, raw)
	}
	if errResponse.Error.Code != apicontract_local.CodeAccessDenied {
		t.Errorf("error code = %q, want ACCESS_DENIED", errResponse.Error.Code)
	}
	if errResponse.Error.RequestID == "" {
		t.Errorf("error.requestId is empty")
	}
	if bytes.Contains(raw, []byte("cathy@example.com")) {
		t.Errorf("refusal body leaks the hidden Email value: %s", raw)
	}
	// No row data at all, hidden or otherwise, comes back on a refusal.
	if bytes.Contains(raw, []byte(`"recordset"`)) {
		t.Errorf("refusal body carries recordset data: %s", raw)
	}
}

// TestRunQuery_NativeSQL_RefusedWithoutGrant is the corrected half of AC
// native-sql-labelled (REQ:opaque-sql-limitation): a SQL-typed saved query
// is refused before dispatch with UNSUPPORTED_PROTECTED_EXECUTION when this
// serve process carries no explicit --allow-opaque-sql grant — replacing
// the previous behaviour (native SQL always ran, merely labelled
// "nativeSql"), which the 9 September independent review found let a
// warning substitute for row/column protection.
func TestRunQuery_NativeSQL_RefusedWithoutGrant(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "boss", "admin")
	baseURL := startServeHTTPWithSession(t, pathsByID, session) // Capabilities{} — no --allow-opaque-sql.
	info := fetchAgentInfo(t, baseURL)

	if info.Capabilities.OpaqueReadOnly {
		t.Fatalf("agent-info capabilities.opaqueReadOnly = true, want false (no grant configured)")
	}

	request := apicontract_local.ExecutionRequest{
		Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
		Source: securityMatrixSource, QueryID: "customers-sql",
		Parameters:     map[string]apicontract_local.TypedValue{},
		BindingOrigins: []apicontract_local.BindingOriginInput{},
		Mode:           apicontract_local.ModeLive,
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
	if status != http.StatusForbidden {
		t.Fatalf("run_query(SQL, no grant) status = %d, want 403; body %s", status, raw)
	}
	var errResponse apicontract_local.ErrorEnvelope
	if err := json.Unmarshal(raw, &errResponse); err != nil {
		t.Fatalf("decode error response: %v (body %s)", err, raw)
	}
	if errResponse.Error.Code != apicontract_local.CodeUnsupportedProtectedExec {
		t.Errorf("error code = %q, want UNSUPPORTED_PROTECTED_EXECUTION", errResponse.Error.Code)
	}
}

// TestRunQuery_NativeSQL_OpaquePrivilegedWithGrant is the "independently
// tested explicit privileged grant" half of AC native-sql-labelled: with
// --allow-opaque-sql, the same SQL-typed query executes and reports
// opaque-privileged provenance — bypassing row policy entirely (both
// customers return even though support's row policy would otherwise hide
// the Brazilian one; here run as admin, who is unrestricted anyway, so the
// row-visibility contrast itself is proven by TestRunQuery_
// RestrictedRowsAndColumns instead, and this test's own job is only the
// grant/profile-labelling behaviour).
func TestRunQuery_NativeSQL_OpaquePrivilegedWithGrant(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "boss", "admin")
	baseURL := startServeHTTPWithSessionAndCapabilities(t, pathsByID, session, api.Capabilities{AllowOpaqueSQL: true})
	info := fetchAgentInfo(t, baseURL)

	if !info.Capabilities.OpaqueReadOnly {
		t.Fatalf("agent-info capabilities.opaqueReadOnly = false, want true (--allow-opaque-sql set)")
	}

	request := apicontract_local.ExecutionRequest{
		Project: projectID, Environment: securityMatrixEnv, SecurityContextID: info.SecurityContextID,
		Source: securityMatrixSource, QueryID: "customers-sql",
		Parameters:     map[string]apicontract_local.TypedValue{},
		BindingOrigins: []apicontract_local.BindingOriginInput{},
		Mode:           apicontract_local.ModeLive,
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
	if status != http.StatusOK {
		t.Fatalf("run_query(SQL, with grant): status %d, body %s", status, raw)
	}
	var response apicontract_local.Result
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, raw)
	}
	if len(response.Recordset.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (native SQL is not row-restricted); body %s", len(response.Recordset.Rows), raw)
	}
	if response.Provenance.ExecutionProfile != apicontract_local.ProfileOpaquePrivileged {
		t.Errorf("Provenance.ExecutionProfile = %q, want opaque-privileged", response.Provenance.ExecutionProfile)
	}
}

// TestExecuteSelect_NativeSQL_RefusedWithoutGrant proves the legacy GET
// exec/select route is gated by the SAME secureread.Executor.RunNativeSQL
// opaque-grant check exec/run_query uses (api-contract.md: "All legacy
// routes obey the same rule") — replacing the previous
// TestExecuteSelect_NativeSQL_Labelled, which asserted the pre-correction
// behaviour (native SQL always ran).
func TestExecuteSelect_NativeSQL_RefusedWithoutGrant(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSession(t, pathsByID, session)

	sqlText := "select CustomerId, FirstName, Country from Customer order by CustomerId"
	requestURL := fmt.Sprintf("%s/datatug/exec/select?proj=%s&env=%s&db=%s&sql=%s",
		baseURL, projectID, securityMatrixEnv, securityMatrixSource, url.QueryEscape(sqlText))
	status, raw := getURL(t, requestURL)
	if status != http.StatusForbidden {
		t.Fatalf("exec/select(sql, no grant): status %d, want 403; body %s", status, raw)
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

// TestExecuteSelect_NativeSQL_LabelledWithGrant is the legacy route's
// "with an explicit grant" counterpart: native SQL executes and still
// carries the nativeSql limitation label exec/select's own (pre-existing,
// unchanged-shape) QueryResultResponse uses.
func TestExecuteSelect_NativeSQL_LabelledWithGrant(t *testing.T) {
	pathsByID, projectID := newSecurityMatrixProject(t)
	session := securityMatrixSession(t, "agent1", "support")
	baseURL := startServeHTTPWithSessionAndCapabilities(t, pathsByID, session, api.Capabilities{AllowOpaqueSQL: true})

	sqlText := "select CustomerId, FirstName, Country from Customer order by CustomerId"
	requestURL := fmt.Sprintf("%s/datatug/exec/select?proj=%s&env=%s&db=%s&sql=%s",
		baseURL, projectID, securityMatrixEnv, securityMatrixSource, url.QueryEscape(sqlText))
	status, raw := getURL(t, requestURL)
	if status != http.StatusOK {
		t.Fatalf("exec/select(sql, with grant): status %d, body %s", status, raw)
	}
	var response api.QueryResultResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, raw)
	}
	// Opaque SQL bypasses row policy entirely (REQ:opaque-sql-limitation):
	// both customers come back even though support's row policy would
	// otherwise hide the Brazilian one.
	if len(response.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (native SQL is not row-restricted); body %s", len(response.Rows), raw)
	}
	native := findLimitation(response.Limitations, "nativeSql")
	if native == nil {
		t.Fatalf("Limitations = %+v, want a nativeSql entry", response.Limitations)
	}
	if native.Note == "" {
		t.Errorf("nativeSql limitation has no Note")
	}
}

func findLimitation(limitations []api.LimitationDTO, kind string) *api.LimitationDTO {
	for i := range limitations {
		if limitations[i].Kind == kind {
			return &limitations[i]
		}
	}
	return nil
}
