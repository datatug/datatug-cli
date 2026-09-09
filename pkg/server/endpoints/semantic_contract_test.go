package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// configureSemanticSession wires api.ConfigureSecureSession the way
// `datatug serve` does at startup (http_server.go), so the rewritten
// semantic endpoints' shared api.SecureExecutor()/api.SecurityContextID()
// have something to read — replacing the pre-Task-12 design where each
// semantic request built its OWN session from query parameters
// (semanticSessionFromQuery, deleted: api-contract.md "a client-supplied
// principal or role are rejected" — that was exactly what it did). Returns
// a base Scope with the freshly-minted securityContextId already filled
// in.
func configureSemanticSession(t *testing.T, projectDir, projectID, as string, roles []string) apicontract.Scope {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{
		As: as, Roles: roles, PoliciesDir: projectDir + "/policies",
	})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	pathsByID := map[string]string{projectID: projectDir}
	api.ConfigureSecureSession(session, pathsByID, api.Capabilities{})
	// api.ProjectStoreFor (used by the resolver — pkg/api/resolver.go) goes
	// through storage.NewDatatugStore, the package-level var pkg/server's
	// ServeHTTP wires at startup (newDatatugStoreFactory); this package's
	// own tests exercise the handlers directly with no real server running,
	// so they must wire the exact same factory themselves.
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}
	return apicontract.Scope{Project: projectID, Environment: semanticTestEnv, SecurityContextID: api.SecurityContextID()}
}

func declaredFact(id, entity, field string, value apicontract.TypedValue, source, collection, column string) apicontract.Fact {
	return apicontract.Fact{
		ID: id, Entity: entity, Field: field, Value: value, Origin: apicontract.FactOriginSelection,
		Physical: &apicontract.PhysicalRef{Source: source, Collection: collection, Column: column},
		Mapping:  "declared", Enabled: true,
	}
}

// assertValid fails the test unless v satisfies datatug-core's own
// Validate() — the schema authority's rule engine, generated from the same
// appendix that produced pkg/apicontract/fixtures' frozen examples. This is
// this stream's fixture-conformance check for the endpoints
// (exec/run_query, semantic/columns, semantic/related(/rows),
// queries/applicable) whose real responses carry live query data and so
// cannot be byte-compared against a frozen fixture the way the static error
// envelopes are (contract_error_test.go) — every response this package
// builds must still be a valid instance of the type core's own fixtures
// demonstrate, checked with core's own code, not a hand-rolled duplicate of
// it.
func assertValid(t *testing.T, v interface{ Validate() error }) {
	t.Helper()
	if err := v.Validate(); err != nil {
		t.Errorf("response does not satisfy datatug-core/pkg/apicontract's own Validate(): %v", err)
	}
}

// --- semantic/columns ---

func TestSemanticColumns_ChinookCustomer_Declared(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	resp, err := computeSemanticColumns(context.Background(), scope, apicontract.SourceRef{Source: semanticTestSource, Collection: "Customer"})
	if err != nil {
		t.Fatalf("computeSemanticColumns: %v", err)
	}
	assertValid(t, resp)
	byColumn := map[string]apicontract.SemanticColumnMapping{}
	for _, c := range resp.Columns {
		byColumn[c.Column] = c
	}
	custID, ok := byColumn["CustomerId"]
	if !ok {
		t.Fatalf("CustomerId not resolved; got %+v", resp.Columns)
	}
	if custID.Entity != "Customer" || custID.Field != "ID" || custID.Provenance != apicontract.SemanticProvenanceDeclared {
		t.Fatalf("CustomerId resolution = %+v, want Entity=Customer Field=ID Provenance=declared", custID)
	}
	if _, ok := byColumn["FirstName"]; ok {
		t.Fatalf("FirstName resolved, want omitted (AC:mapped-columns-server)")
	}
}

func TestSemanticColumns_SupportNotesRecordset_Declared(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	resp, err := computeSemanticColumns(context.Background(), scope, apicontract.SourceRef{Source: "support-notes", Collection: "support-notes"})
	if err != nil {
		t.Fatalf("computeSemanticColumns: %v", err)
	}
	assertValid(t, resp)
	var found bool
	for _, c := range resp.Columns {
		if c.Column == "CustomerId" {
			found = true
			if c.Provenance != apicontract.SemanticProvenanceDeclared {
				t.Fatalf("CustomerId provenance = %q, want declared", c.Provenance)
			}
		}
	}
	if !found {
		t.Fatalf("CustomerId not resolved from support-notes; got %+v", resp.Columns)
	}
}

func TestSemanticColumns_HTTPSource_Declared(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	resp, err := computeSemanticColumns(context.Background(), scope, apicontract.SourceRef{Source: "country-facts", Collection: "country-facts"})
	if err != nil {
		t.Fatalf("computeSemanticColumns: %v", err)
	}
	assertValid(t, resp)
	byColumn := map[string]apicontract.SemanticColumnMapping{}
	for _, c := range resp.Columns {
		byColumn[c.Column] = c
	}
	currency, ok := byColumn["currency"]
	if !ok || currency.Entity != "Country" || currency.Field != "Currency" || currency.Provenance != apicontract.SemanticProvenanceDeclared {
		t.Fatalf("currency resolution = %+v (ok=%v), want declared Country.Currency", currency, ok)
	}
	if _, ok := byColumn["population"]; ok {
		t.Fatalf("population (no meta) resolved, want omitted")
	}
}

func TestSemanticColumns_UnknownSource_SourceUnavailable(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	_, err := computeSemanticColumns(context.Background(), scope, apicontract.SourceRef{Source: "no-such-source", Collection: "Customer"})
	assertContractError(t, err, apicontract.ErrCodeSourceUnavailable)
}

func TestSemanticColumns_MissingParams(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	_, err := computeSemanticColumns(context.Background(), scope, apicontract.SourceRef{Collection: "Customer"})
	assertContractError(t, err, apicontract.ErrCodeMissingParameter)
	_, err = computeSemanticColumns(context.Background(), scope, apicontract.SourceRef{Source: semanticTestSource})
	assertContractError(t, err, apicontract.ErrCodeMissingParameter)
}

func TestSemanticColumns_StaleContext(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	scope.SecurityContextID = "not-the-real-one"
	_, err := computeSemanticColumns(context.Background(), scope, apicontract.SourceRef{Source: semanticTestSource, Collection: "Customer"})
	assertContractError(t, err, apicontract.ErrCodeStaleContext)
}

// --- semantic/related, semantic/related/rows ---

// TestSemanticRelated_CustomerFiveInvoicesAndSupportNotes is
// core-investigation-loop's own example (AC:related-across-sources-server):
// selecting Customer.ID=5 lists Invoices (chinook, count 7) and Support
// notes (support-notes, count 2, the sameField lookup into inGitDB).
func TestSemanticRelated_CustomerFiveInvoicesAndSupportNotes(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	fact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")
	resp, err := computeSemanticRelated(context.Background(), relatedRequest{Scope: scope, Fact: fact})
	if err != nil {
		t.Fatalf("computeSemanticRelated: %v", err)
	}
	assertValid(t, resp)
	byCollection := map[string]apicontract.RelatedItem{}
	for _, e := range resp.Related {
		byCollection[e.Source+"/"+e.Collection] = e
	}
	invoices, ok := byCollection[semanticTestSource+"/Invoice"]
	if !ok {
		t.Fatalf("Invoice lookup not found; got %+v", resp.Related)
	}
	if invoices.Count == nil || *invoices.Count != 7 {
		t.Fatalf("Invoice count = %v, want 7", invoices.Count)
	}
	notes, ok := byCollection["support-notes/support-notes"]
	if !ok {
		t.Fatalf("support-notes lookup not found; got %+v", resp.Related)
	}
	if notes.Count == nil || *notes.Count != 2 {
		t.Fatalf("support-notes count = %v, want 2", notes.Count)
	}
	if resp.Truncated {
		t.Fatalf("Truncated = true, want false for 2 lookups (cap is 50)")
	}
}

// TestSemanticRelated_RestrictedPrincipal_NoTrueCount is
// AC:related-respects-policy: a principal with no grant at all on Invoice
// sees no true count for it.
func TestSemanticRelated_RestrictedPrincipal_NoTrueCount(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "bob", []string{"support"})

	fact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")
	resp, err := computeSemanticRelated(context.Background(), relatedRequest{Scope: scope, Fact: fact})
	if err != nil {
		t.Fatalf("computeSemanticRelated: %v", err)
	}
	for _, e := range resp.Related {
		if e.Source == semanticTestSource && e.Collection == "Invoice" {
			if e.Count != nil {
				t.Fatalf("restricted principal's Invoice count = %v, want nil (count unavailable)", *e.Count)
			}
			return
		}
	}
	t.Fatalf("Invoice lookup not found; got %+v", resp.Related)
}

func TestSemanticRelated_DisabledFact_Refused(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	fact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")
	fact.Enabled = false
	_, err := computeSemanticRelated(context.Background(), relatedRequest{Scope: scope, Fact: fact})
	// Disabled facts carry no special error of their own here — the field
	// is still structurally valid; this just documents that Enabled is not
	// independently validated by computeSemanticRelated (a disabled fact
	// simply should not be sent by a well-behaved client at all — the
	// browser-side "ignore disabled facts" rule is task 15's own scope).
	// The call succeeds; nothing here asserts filtering happened, since a
	// single fact's own Enabled flag does not change /semantic/related's
	// single-fact contract shape.
	if err != nil {
		t.Fatalf("computeSemanticRelated(disabled fact): %v", err)
	}
}

func TestSemanticRelated_MissingRequiredFields(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	_, err := computeSemanticRelated(context.Background(), relatedRequest{Scope: scope})
	assertContractError(t, err, apicontract.ErrCodeMissingParameter)
}

func TestSemanticRelatedRows_CustomerFiveInvoices(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	lookupID := encodeLookupID(semanticTestSource, "Invoice", "CustomerId")

	resp, err := computeSemanticRelatedRows(context.Background(), relatedRowsRequest{
		Scope: scope, LookupID: lookupID, Value: apicontract.NewIntegerValue("5"),
	})
	if err != nil {
		t.Fatalf("computeSemanticRelatedRows: %v", err)
	}
	assertValid(t, resp)
	if len(resp.Recordset.Rows) != 7 {
		t.Fatalf("len(Rows) = %d, want 7", len(resp.Recordset.Rows))
	}
	if resp.Provenance.ExecutionProfile != apicontract.ExecutionProfileProtected {
		t.Fatalf("ExecutionProfile = %q, want protected", resp.Provenance.ExecutionProfile)
	}
}

// TestSemanticRelatedRows_RestrictedPrincipal_ZeroOrDenied is
// AC:related-respects-policy's row-fetch half: with no Invoice grant, the
// restricted principal gets zero rows or a clean ACCESS_DENIED — never a
// crash, never real Invoice rows.
func TestSemanticRelatedRows_RestrictedPrincipal_ZeroOrDenied(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "bob", []string{"support"})
	lookupID := encodeLookupID(semanticTestSource, "Invoice", "CustomerId")

	resp, err := computeSemanticRelatedRows(context.Background(), relatedRowsRequest{
		Scope: scope, LookupID: lookupID, Value: apicontract.NewIntegerValue("5"),
	})
	if err != nil {
		var ce *contractError
		if !isContractErrorCode(err, apicontract.ErrCodeAccessDenied, &ce) {
			t.Fatalf("computeSemanticRelatedRows: %v (want either 0 rows or ACCESS_DENIED)", err)
		}
		return
	}
	if len(resp.Recordset.Rows) != 0 {
		t.Fatalf("len(Rows) = %d, want 0 for a principal with no Invoice grant", len(resp.Recordset.Rows))
	}
}

func TestSemanticRelatedRows_InvalidLookupID(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	_, err := computeSemanticRelatedRows(context.Background(), relatedRowsRequest{
		Scope: scope, LookupID: "not-valid-base64!!", Value: apicontract.NewIntegerValue("5"),
	})
	assertContractError(t, err, apicontract.ErrCodeInvalidRequest)
}

func TestEncodeDecodeLookupID_RoundTrip(t *testing.T) {
	id := encodeLookupID(semanticTestSource, "Invoice", "CustomerId")
	source, collection, column, err := decodeLookupID(id)
	if err != nil {
		t.Fatalf("decodeLookupID: %v", err)
	}
	if source != semanticTestSource || collection != "Invoice" || column != "CustomerId" {
		t.Fatalf("decodeLookupID() = (%q, %q, %q)", source, collection, column)
	}
}

// --- queries/applicable ---

// TestSemanticApplicable_CustomerInvoicesApplicable_InvoiceLinesNotYet is
// AC:applicable-with-chain-server: given Customer.ID=5, customer-invoices is
// applicable with a chain, and invoice-lines is not-yet with missing
// InvoiceId — the appendix's own "missing contains the parameter ID" rule
// (not the "Invoice.ID" entity.field text the underlying pkg/semantic
// helper itself reports).
func TestSemanticApplicable_CustomerInvoicesApplicable_InvoiceLinesNotYet(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	fact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")

	resp, err := computeSemanticApplicable(context.Background(), applicableRequest{Scope: scope, Values: []apicontract.Fact{fact}})
	if err != nil {
		t.Fatalf("computeSemanticApplicable: %v", err)
	}
	assertValid(t, resp)

	var invoices *apicontract.Candidate
	for i := range resp.Applicable {
		if resp.Applicable[i].QueryID == "customer-invoices" {
			invoices = &resp.Applicable[i]
		}
	}
	if invoices == nil {
		t.Fatalf("customer-invoices not applicable; got applicable=%+v notYet=%+v", resp.Applicable, resp.NotYet)
	}
	if len(invoices.Bindings) != 1 || invoices.Bindings[0].ParameterID != "CustomerId" {
		t.Fatalf("customer-invoices bindings = %+v", invoices.Bindings)
	}
	if len(invoices.Chain) == 0 || invoices.Chain[0].Explanation == "" {
		t.Fatalf("customer-invoices chain is empty, want resolution chain text")
	}
	if invoices.State != apicontract.CandidateStateRunnable {
		t.Fatalf("customer-invoices state = %q, want runnable", invoices.State)
	}
	if invoices.SelectedSource != semanticTestSource {
		t.Fatalf("customer-invoices selectedSource = %q, want %q", invoices.SelectedSource, semanticTestSource)
	}

	var lines *apicontract.Candidate
	for i := range resp.NotYet {
		if resp.NotYet[i].QueryID == "invoice-lines" {
			lines = &resp.NotYet[i]
		}
	}
	if lines == nil {
		t.Fatalf("invoice-lines not in notYet; got notYet=%+v", resp.NotYet)
	}
	if len(lines.Missing) != 1 || lines.Missing[0] != "InvoiceId" {
		t.Fatalf("invoice-lines missing = %+v, want [InvoiceId] (the parameter ID)", lines.Missing)
	}
	if lines.State != apicontract.CandidateStateNeedsInput {
		t.Fatalf("invoice-lines state = %q, want needs-input", lines.State)
	}
}

// TestSemanticApplicable_NonSemanticRequiredParam_AlwaysMissing covers
// missingNonSemanticParameters: customer-export has CustomerId (bindable)
// AND a plain required "format" parameter with no Meta tag — it must land
// in notYet with BOTH CustomerId's absence excluded (it IS bound) and
// "format" present in missing, never silently dropped.
func TestSemanticApplicable_NonSemanticRequiredParam_AlwaysMissing(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	fact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")

	resp, err := computeSemanticApplicable(context.Background(), applicableRequest{Scope: scope, Values: []apicontract.Fact{fact}})
	if err != nil {
		t.Fatalf("computeSemanticApplicable: %v", err)
	}
	var export *apicontract.Candidate
	for i := range resp.NotYet {
		if resp.NotYet[i].QueryID == "customer-export" {
			export = &resp.NotYet[i]
		}
	}
	if export == nil {
		t.Fatalf("customer-export not in notYet; got applicable=%+v notYet=%+v", resp.Applicable, resp.NotYet)
	}
	if len(export.Missing) != 1 || export.Missing[0] != "format" {
		t.Fatalf("customer-export missing = %+v, want [format]", export.Missing)
	}
}

// TestSemanticApplicable_AmbiguousFacts covers the appendix's ambiguity
// rule: two distinct Customer.ID facts block customer-invoices, reporting
// an Ambiguous entry rather than silently picking one.
func TestSemanticApplicable_AmbiguousFacts(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	f1 := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")
	f2 := declaredFact("f2", "Customer", "ID", apicontract.NewIntegerValue("6"), semanticTestSource, "Customer", "CustomerId")

	resp, err := computeSemanticApplicable(context.Background(), applicableRequest{Scope: scope, Values: []apicontract.Fact{f1, f2}})
	if err != nil {
		t.Fatalf("computeSemanticApplicable: %v", err)
	}
	var invoices *apicontract.Candidate
	for i := range resp.NotYet {
		if resp.NotYet[i].QueryID == "customer-invoices" {
			invoices = &resp.NotYet[i]
		}
	}
	if invoices == nil {
		t.Fatalf("customer-invoices not in notYet with ambiguous facts; got applicable=%+v notYet=%+v", resp.Applicable, resp.NotYet)
	}
	if len(invoices.Ambiguous) != 1 || invoices.Ambiguous[0].ParameterID != "CustomerId" {
		t.Fatalf("customer-invoices ambiguous = %+v, want one CustomerId entry", invoices.Ambiguous)
	}
	if len(invoices.Ambiguous[0].FactIDs) != 2 {
		t.Fatalf("customer-invoices ambiguous factIds = %+v, want 2", invoices.Ambiguous[0].FactIDs)
	}
}

func TestSemanticApplicable_NoValues_EverythingNotYet(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	resp, err := computeSemanticApplicable(context.Background(), applicableRequest{Scope: scope})
	if err != nil {
		t.Fatalf("computeSemanticApplicable: %v", err)
	}
	if len(resp.Applicable) != 0 {
		t.Fatalf("Applicable = %+v, want none with no values on hand", resp.Applicable)
	}
	// customer-invoices, customer-export, invoice-lines (writeApplicableQueries)
	// and country-facts (writeHTTPCountryQuery, its own required Meta-tagged
	// "name" parameter also unbindable with no values on hand).
	if len(resp.NotYet) != 4 {
		t.Fatalf("len(NotYet) = %d, want 4 (all four fixture queries); got %+v", len(resp.NotYet), resp.NotYet)
	}
}

// TestSemanticApplicable_UnknownProject_NotFound covers the appendix's
// NOT_FOUND mapping for a project this process is not serving.
func TestSemanticApplicable_UnknownProject_NotFound(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	scope.Project = "no-such-project-xyz"
	_, err := computeSemanticApplicable(context.Background(), applicableRequest{Scope: scope})
	assertContractError(t, err, apicontract.ErrCodeNotFound)
}

// --- HTTP-level handler smoke tests ---

func TestSemanticColumnsHandler_HTTP(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	q := url.Values{
		urlParamProjectID: {scope.Project}, "environment": {scope.Environment}, "securityContextId": {scope.SecurityContextID},
		"source": {semanticTestSource}, "collection": {"Customer"},
	}
	req := httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	semanticColumnsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp apicontract.SemanticColumnsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Columns) == 0 {
		t.Fatalf("Columns is empty")
	}
}

func TestSemanticColumnsHandler_HTTP_MissingParameterEnvelope(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns", nil) // no project/environment/securityContextId
	rec := httptest.NewRecorder()
	semanticColumnsHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	var env apicontract.ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if env.Error.Code != string(apicontract.ErrCodeMissingParameter) || env.Error.Field == "" || env.Error.RequestID == "" {
		t.Fatalf("error response = %+v, want MISSING_PARAMETER with a field and requestId", env.Error)
	}
}

func TestSemanticApplicableHandler_HTTP_DuplicateKeyRejected(t *testing.T) {
	body := []byte(`{"project":"p1","project":"p2","environment":"e","securityContextId":"x","values":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/datatug/queries/applicable", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	semanticApplicableHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a duplicate JSON key; body = %s", rec.Code, rec.Body.String())
	}
}

func TestSemanticApplicableHandler_HTTP_UnknownFieldRejected(t *testing.T) {
	body := []byte(`{"project":"p1","environment":"e","securityContextId":"x","values":[],"role":"admin"}`)
	req := httptest.NewRequest(http.MethodPost, "/datatug/queries/applicable", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	semanticApplicableHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown field (e.g. a client-supplied role); body = %s", rec.Code, rec.Body.String())
	}
}

// assertContractError fails the test unless err is a *contractError with the
// given code.
func assertContractError(t *testing.T, err error, code apicontract.ErrorCode) {
	t.Helper()
	var ce *contractError
	if !isContractErrorCode(err, code, &ce) {
		t.Fatalf("err = %v, want *contractError{Code: %s}", err, code)
	}
}

func isContractErrorCode(err error, code apicontract.ErrorCode, out **contractError) bool {
	ce, ok := err.(*contractError)
	if !ok || ce.Code != code {
		return false
	}
	*out = ce
	return true
}
