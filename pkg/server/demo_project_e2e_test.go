package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

// demoProjectDefaultDir is the real datatug-demo-projects/demo-project-1
// checkout this stream's brief names explicitly ("the demo lives at
// /Users/alex/projects/datatug/datatug-demo-projects/demo-project-1,
// read-only, copy to a temp dir"). demoProjectDirEnvVar overrides it for a
// machine where that sibling checkout lives elsewhere (or CI, where it may
// not exist at all — this test then skips, matching the pattern this
// package's other real-demo-project tests already used before this
// stream).
const (
	demoProjectDefaultDir = "/Users/alex/projects/datatug/datatug-demo-projects/demo-project-1"
	demoProjectDirEnvVar  = "DATATUG_DEMO_PROJECT_DIR"
	demoProjectID         = "datatug-demo-project"
	demoProjectEnv        = "local"
	// demoProjectSource is the "chinook" DbModel/source id
	// datatug-demo-projects/demo-project-1's own environments/local/
	// catalogs/chinook-local/chinook-local.db.json declares
	// (dbModel:"chinook") and its entities/Customer/Customer.entity.json
	// maps Customer.ID's Mappings to (source:"chinook", ...) — see
	// pkg/api/resolver.go's ResolvedSource doc comment for why the stable
	// DbModel, not the environment-specific catalog id "chinook-local", is
	// the appendix's SourceRef.source.
	demoProjectSource = "chinook"
)

// resolveDemoProjectDir returns the real demo-project-1 checkout to copy
// from, skipping the test when it is not present on this machine.
func resolveDemoProjectDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(demoProjectDirEnvVar)
	if dir == "" {
		dir = demoProjectDefaultDir
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Skipf("real demo-project-1 checkout not found at %s (set %s to override): %v", dir, demoProjectDirEnvVar, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "datatug-project.json")); err != nil {
		t.Skipf("%s has no datatug-project.json; not a real project checkout: %v", dir, err)
	}
	return dir
}

// copyDemoProjectToTempDir copies srcDir's tree into a fresh t.TempDir()
// (the brief's own instruction: "copy to a temp dir", so the real
// datatug-demo-projects checkout is only ever read, never written to).
func copyDemoProjectToTempDir(t *testing.T, srcDir string) string {
	t.Helper()
	dstDir := t.TempDir()
	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dstDir, rel), 0o755)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(filepath.Join(dstDir, rel), data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy demo project from %s: %v", srcDir, err)
	}
	return dstDir
}

// TestDemoProject_RunQuery_RealParameterEffect is AC:real-transport-and-
// parameter-effect's own required proof (S64 brief item 4): "two different
// Customer.ID values give different rows through exec/run_query against
// the demo project, driver-bound, never textual substitution." It runs the
// real customer-invoices DTQL query (queries/customers/customer-invoices.
// query.dtql) through the real, rewritten POST /datatug/exec/run_query
// against a temp copy of the real demo-project-1 and its real
// ~/datatug/dbs/chinook-local.sqlite Chinook data (resolved through the
// project's real environments/local/catalogs/chinook-local record — the
// SAME environment/catalog resolution path pkg/api's unified resolver uses
// for every caller, not a synthetic fixture).
//
// The two chosen customer IDs (1 and 2) have completely disjoint Chinook
// Invoice.InvoiceId sets (verified with the sqlite3 CLI while building this
// test: customer 1 -> {98,121,143,195,316,327,382}, customer 2 ->
// {1,12,67,196,219,241,293}) — a textual-substitution or echo-only bug
// that ignored the parameter would return the SAME rows for both calls, or
// the request's raw parameter values instead of real InvoiceId/Total data;
// this test would catch either.
func TestDemoProject_RunQuery_RealParameterEffect(t *testing.T) {
	srcDir := resolveDemoProjectDir(t)
	projectDir := copyDemoProjectToTempDir(t, srcDir)

	session, err := secureread.NewSession(secureread.SessionOptions{
		As: "admin", Roles: []string{"admin"}, PoliciesDir: filepath.Join(projectDir, "policies"),
	})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	baseURL := startServeHTTPWithSession(t, map[string]string{demoProjectID: projectDir}, session)

	status, raw := getURL(t, baseURL+"/datatug/agent-info")
	if status != http.StatusOK {
		t.Fatalf("GET agent-info: status %d, body %s", status, raw)
	}
	var info apicontract.AgentInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode agent-info: %v (body %s)", err, raw)
	}
	if info.Principal.ID != "admin" {
		t.Fatalf("agent-info principal.id = %q, want admin", info.Principal.ID)
	}

	runForCustomer := func(customerID int64) apicontract.Result {
		t.Helper()
		request := apicontract.ExecutionRequest{
			Project: demoProjectID, Environment: demoProjectEnv, SecurityContextID: info.SecurityContextID,
			Source: demoProjectSource, QueryID: "customers/customer-invoices",
			Parameters: map[string]apicontract.TypedValueOrSet{"CustomerId": apicontract.ScalarValue(apicontract.NewIntegerValue(strconv.FormatInt(customerID, 10)))},
			BindingOrigins: []apicontract.BindingOriginEntry{
				{ParameterID: "CustomerId", Origin: apicontract.BindingOriginManual},
			},
			Mode: apicontract.ProvenanceModeLive,
		}
		status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
		if status != http.StatusOK {
			t.Fatalf("run_query(CustomerId=%d): status %d, body %s", customerID, status, raw)
		}
		var result apicontract.Result
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("decode run_query(CustomerId=%d) response: %v (body %s)", customerID, err, raw)
		}
		return result
	}

	resultOne := runForCustomer(1)
	resultTwo := runForCustomer(2)

	if len(resultOne.Recordset.Rows) == 0 {
		t.Fatalf("customer 1: 0 rows, want at least 1 real invoice row")
	}
	if len(resultTwo.Recordset.Rows) == 0 {
		t.Fatalf("customer 2: 0 rows, want at least 1 real invoice row")
	}

	invoiceIDColumn := -1
	for i, c := range resultOne.Recordset.Columns {
		if c.Name == "InvoiceId" {
			invoiceIDColumn = i
		}
	}
	if invoiceIDColumn < 0 {
		t.Fatalf("customer-invoices recordset has no InvoiceId column; got %+v", resultOne.Recordset.Columns)
	}
	// InvoiceId comes through this real DTQL/structured-query path typed
	// "number" (a float64 driver value), not "integer": dalgo2sqlite's
	// structured-query row scan does not preserve SQLite's declared INTEGER
	// column affinity distinctly from REAL through condeval.ToMap — see
	// fromGoValue (pkg/server/endpoints)'s own doc comment on inferring a
	// TypedValue's type from the observed Go runtime value. That is a
	// faithful, contract-conformant TypedValue (a number IS a valid
	// representation of an integer value); typedValueKey below compares by
	// whichever field the type actually populated, so this test's
	// assertion is not coupled to that driver-shape detail.

	invoiceIDs := func(result apicontract.Result) map[string]bool {
		out := map[string]bool{}
		for _, row := range result.Recordset.Rows {
			out[typedValueKey(row[invoiceIDColumn])] = true
		}
		return out
	}
	idsOne, idsTwo := invoiceIDs(resultOne), invoiceIDs(resultTwo)
	for id := range idsOne {
		if idsTwo[id] {
			t.Fatalf("InvoiceId %s returned for BOTH CustomerId 1 and 2 — the parameter did not actually constrain the query (textual-substitution/echo bug); customer1=%v customer2=%v", id, idsOne, idsTwo)
		}
	}

	// bindingsApplied must be execution-confirmed, never an echo of the raw
	// request before execution (api-contract.md): the returned value must
	// be the SAME typed value that was actually applied, and its origin
	// evidence must be honestly reported as client-reported (this test drove
	// the binding by hand, standing in for the browser's own auto-binding).
	if len(resultOne.BindingsApplied) != 1 || resultOne.BindingsApplied[0].ParameterID != "CustomerId" || resultOne.BindingsApplied[0].Value.Scalar == nil || resultOne.BindingsApplied[0].Value.Scalar.Str != "1" {
		t.Fatalf("customer 1 BindingsApplied = %+v, want [{CustomerId, integer 1}]", resultOne.BindingsApplied)
	}
	if resultOne.BindingsApplied[0].OriginEvidence != apicontract.BindingOriginEvidenceClientReported {
		t.Fatalf("customer 1 BindingsApplied[0].OriginEvidence = %q, want client-reported", resultOne.BindingsApplied[0].OriginEvidence)
	}
	if resultTwo.BindingsApplied[0].Value.Scalar == nil || resultTwo.BindingsApplied[0].Value.Scalar.Str != "2" {
		t.Fatalf("customer 2 BindingsApplied = %+v, want CustomerId=2", resultTwo.BindingsApplied)
	}

	// The source resolved through the real environment/catalog record, not
	// a fabricated database (AC:real-transport-and-parameter-effect: "HTTP
	// targets resolve without a fake database" — the same resolver rule
	// applies to every source kind).
	if resultOne.Provenance.Source != demoProjectSource {
		t.Fatalf("Provenance.Source = %q, want %q", resultOne.Provenance.Source, demoProjectSource)
	}
	if resultOne.Provenance.ExecutionProfile != apicontract.ExecutionProfileProtected {
		t.Fatalf("Provenance.ExecutionProfile = %q, want protected (DTQL through the policy path)", resultOne.Provenance.ExecutionProfile)
	}
}

// typedValueKey renders a TypedValue as a comparable string regardless of
// which variant populated it (Text for the text-shaped variants, a decimal
// rendering of Number for TypeNumber, "true"/"false" for TypeBoolean) — see
// this test's own note above on why a real driver-scanned integer column
// can legitimately arrive as TypeNumber rather than TypeInteger.
func typedValueKey(v apicontract.TypedValue) string {
	switch v.Type {
	case apicontract.ValueTypeNumber:
		return strconv.FormatFloat(v.Num, 'f', -1, 64)
	case apicontract.ValueTypeBoolean:
		return strconv.FormatBool(v.Bool)
	default:
		return v.Str
	}
}

// TestDemoProject_RunQuery_SQL_RealParameterEffect_WithGrant is the
// native-SQL counterpart of TestDemoProject_RunQuery_RealParameterEffect:
// the real customer-purchases-by-genre SQL query (queries/customers/
// customer-purchases-by-genre.query.sql, "WHERE i.CustomerId = @CustomerId")
// runs through the SAME rewritten exec/run_query, with an explicit
// --allow-opaque-sql grant (REQ:opaque-sql-limitation), and its
// @CustomerId placeholder binds as a real driver argument
// (dal.QueryArg{Name:"CustomerId"} -> dalgo2sql's sql.Named — see
// native_sql.go and exec_run_query.go's sqlQueryArgs) rather than the
// textual @ParamName substitution
// apps/datatugapp/commands/cmd_query_run_saved.go used to do (removed
// upstream once dal-go/dalgo2sql v0.11.7 fixed real TextQuery arg
// binding). Two different customers' top genre ("Rock" for both) has a
// different TracksPurchased/TotalSpent (verified with the sqlite3 CLI:
// customer 1 -> 14 tracks/13.86; customer 2 -> 17 tracks/16.83) — a
// textual-substitution bug that mishandled the placeholder, or one that
// silently ignored it and ran the same aggregate for both, would not
// reproduce these exact, distinct numbers.
func TestDemoProject_RunQuery_SQL_RealParameterEffect_WithGrant(t *testing.T) {
	srcDir := resolveDemoProjectDir(t)
	projectDir := copyDemoProjectToTempDir(t, srcDir)

	session, err := secureread.NewSession(secureread.SessionOptions{
		As: "admin", Roles: []string{"admin"}, PoliciesDir: filepath.Join(projectDir, "policies"),
	})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	baseURL := startServeHTTPWithSessionAndCapabilities(t, map[string]string{demoProjectID: projectDir}, session, api.Capabilities{AllowOpaqueSQL: true})

	status, raw := getURL(t, baseURL+"/datatug/agent-info")
	if status != http.StatusOK {
		t.Fatalf("GET agent-info: status %d, body %s", status, raw)
	}
	var info apicontract.AgentInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode agent-info: %v (body %s)", err, raw)
	}
	if !info.Capabilities.OpaqueReadOnly {
		t.Fatalf("agent-info capabilities.opaqueReadOnly = false, want true (--allow-opaque-sql set)")
	}

	runForCustomer := func(customerID int64) apicontract.Result {
		t.Helper()
		request := apicontract.ExecutionRequest{
			Project: demoProjectID, Environment: demoProjectEnv, SecurityContextID: info.SecurityContextID,
			Source: demoProjectSource, QueryID: "customers/customer-purchases-by-genre",
			Parameters: map[string]apicontract.TypedValueOrSet{"CustomerId": apicontract.ScalarValue(apicontract.NewIntegerValue(strconv.FormatInt(customerID, 10)))},
			BindingOrigins: []apicontract.BindingOriginEntry{
				{ParameterID: "CustomerId", Origin: apicontract.BindingOriginManual},
			},
			Mode: apicontract.ProvenanceModeLive,
		}
		status, raw := postJSON(t, baseURL, "/datatug/exec/run_query", request)
		if status != http.StatusOK {
			t.Fatalf("run_query(CustomerId=%d): status %d, body %s", customerID, status, raw)
		}
		var result apicontract.Result
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("decode run_query(CustomerId=%d) response: %v (body %s)", customerID, err, raw)
		}
		return result
	}

	resultOne := runForCustomer(1)
	resultTwo := runForCustomer(2)

	if resultOne.Provenance.ExecutionProfile != apicontract.ExecutionProfileOpaquePrivileged {
		t.Fatalf("Provenance.ExecutionProfile = %q, want opaque-privileged", resultOne.Provenance.ExecutionProfile)
	}

	rockRow := func(result apicontract.Result) []apicontract.TypedValue {
		nameCol, tracksCol := -1, -1
		for i, c := range result.Recordset.Columns {
			switch c.Name {
			case "GenreName":
				nameCol = i
			case "TracksPurchased":
				tracksCol = i
			}
		}
		if nameCol < 0 || tracksCol < 0 {
			t.Fatalf("customer-purchases-by-genre recordset missing GenreName/TracksPurchased; got %+v", result.Recordset.Columns)
		}
		for _, row := range result.Recordset.Rows {
			if typedValueKey(row[nameCol]) == "Rock" {
				return row
			}
		}
		t.Fatalf("no Rock row in result; got %+v", result.Recordset.Rows)
		return nil
	}
	rowOne, rowTwo := rockRow(resultOne), rockRow(resultTwo)
	tracksCol := -1
	for i, c := range resultOne.Recordset.Columns {
		if c.Name == "TracksPurchased" {
			tracksCol = i
		}
	}
	if typedValueKey(rowOne[tracksCol]) == typedValueKey(rowTwo[tracksCol]) {
		t.Fatalf("Rock TracksPurchased is the SAME for CustomerId 1 and 2 (%s) — the @CustomerId placeholder did not actually bind", typedValueKey(rowOne[tracksCol]))
	}
	if typedValueKey(rowOne[tracksCol]) != "14" {
		t.Errorf("customer 1 Rock TracksPurchased = %s, want 14", typedValueKey(rowOne[tracksCol]))
	}
	if typedValueKey(rowTwo[tracksCol]) != "17" {
		t.Errorf("customer 2 Rock TracksPurchased = %s, want 17", typedValueKey(rowTwo[tracksCol]))
	}
}
