package endpoints

import (
	"context"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/apicontract/fixtures"
)

// --- request-envelope fixture contract tests (S83) ---
//
// These decode datatug-core's own frozen request fixtures
// (pkg/apicontract/fixtures, PR datatug-core#313 / v0.27.0 of datatug-core)
// through the exact apicontract.DecodeStrict call this package's real HTTP
// handlers use (semantic_applicable.go/semantic_related.go), proving
// byte-for-byte wire compatibility with the schema authority's own golden
// examples — then run the decoded request through the real compute function
// (computeSemanticApplicable/computeSemanticRelated/
// computeSemanticRelatedRows, the same functions the HTTP handlers call) and
// assert the response satisfies core's own Validate(), the same
// fixture-conformance check assertValid already applies elsewhere in this
// file (contract_error_test.go does the equivalent for the static error
// envelopes).
//
// Each fixture's project/environment/securityContextId/fact.physical.source/
// lookupId are placeholder identity values with no server this test process
// registers to satisfy them (e.g. project "demo-project-1", source
// "chinook-local", a human-readable lookupId "l-invoices-by-customer") — the
// fixtures document request SHAPE, not a runnable scenario against any one
// server's live state. Each test below overrides exactly those identity
// fields to the live test session/project (configureSemanticSession,
// writeSemanticTestProject), while leaving every appendix-defined VALUE the
// fixture decoded — facts, typed values, limits — untouched.

// decodeRequestFixture reads name from datatug-core's frozen fixture set via
// the real production decode path (apicontract.DecodeStrict) into v,
// failing the test on any decode or structural-validation error — a
// fixture datatug-cli's own DecodeStrict-based handlers cannot accept, or
// that does not even satisfy its own Validate(), is itself a wire-
// compatibility finding this stream's report would need to surface, not a
// silently skipped case.
func decodeRequestFixture(t *testing.T, name string, v interface{ Validate() error }) {
	t.Helper()
	data, err := fixtures.Read(name)
	if err != nil {
		t.Fatalf("fixtures.Read(%s): %v", name, err)
	}
	if err := apicontract.DecodeStrict(data, v); err != nil {
		t.Fatalf("apicontract.DecodeStrict(%s): %v", name, err)
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("fixture %s does not satisfy its own Validate(): %v", name, err)
	}
}

func TestApplicableRequestFixture_DecodesAndValidatesThroughRealHandler(t *testing.T) {
	var req apicontract.ApplicableRequest
	decodeRequestFixture(t, "applicable_request.json", &req)
	if len(req.Values) != 2 {
		t.Fatalf("len(Values) = %d, want 2 (applicable_request.json's own f1/f2)", len(req.Values))
	}

	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID

	resp, err := computeSemanticApplicable(context.Background(), req)
	if err != nil {
		t.Fatalf("computeSemanticApplicable(fixture request): %v", err)
	}
	assertValid(t, resp)

	// The fixture's own f1 (Customer.ID=5, selection, declared, enabled) is
	// exactly this project's AC:applicable-with-chain-server example — see
	// TestSemanticApplicable_CustomerInvoicesApplicable_InvoiceLinesNotYet's
	// hand-built equivalent — so customer-invoices must come back Applicable
	// through the real handler too.
	for _, c := range resp.Applicable {
		if c.QueryID == "customer-invoices" {
			return
		}
	}
	t.Fatalf("customer-invoices not applicable from the fixture's own facts; got applicable=%+v notYet=%+v", resp.Applicable, resp.NotYet)
}

func TestRelatedRequestFixture_DecodesAndValidatesThroughRealHandler(t *testing.T) {
	var req apicontract.RelatedRequest
	decodeRequestFixture(t, "related_request.json", &req)
	if req.Limit == nil || *req.Limit != 25 {
		t.Fatalf("Limit = %v, want 25 (related_request.json's own value)", req.Limit)
	}

	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	// The fixture's fact.physical.source ("chinook-local") is a placeholder
	// id, not a source this test process registers — writeSemanticTestProject
	// registers its Chinook copy under semanticTestSource ("chinook"). Only
	// the source id is substituted; collection/column (Customer/CustomerId)
	// are exactly the fixture's own and exactly what this project's schema
	// declares.
	req.Fact.Physical.Source = semanticTestSource

	resp, err := computeSemanticRelated(context.Background(), req)
	if err != nil {
		t.Fatalf("computeSemanticRelated(fixture request): %v", err)
	}
	assertValid(t, resp)
	if len(resp.Related) == 0 {
		t.Fatalf("Related is empty for the fixture's own Customer.ID=5 fact")
	}
}

func TestRelatedRowsRequestFixture_DecodesAndValidatesThroughRealHandler(t *testing.T) {
	var req apicontract.RelatedRowsRequest
	decodeRequestFixture(t, "related_rows_request.json", &req)
	if req.Limit == nil || *req.Limit != 100 {
		t.Fatalf("Limit = %v, want 100 (related_rows_request.json's own value)", req.Limit)
	}

	projectDir, projectID := writeSemanticTestProject(t)
	scope := configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	req.Project, req.Environment, req.SecurityContextID = scope.Project, scope.Environment, scope.SecurityContextID
	// The fixture's lookupId ("l-invoices-by-customer") is a human-readable
	// placeholder, not this package's own opaque encodeLookupID encoding —
	// substituted with a real one naming the same Invoice/CustomerId lookup
	// TestSemanticRelatedRows_CustomerFiveInvoices exercises.
	req.LookupID = encodeLookupID(semanticTestSource, "Invoice", "CustomerId")

	resp, err := computeSemanticRelatedRows(context.Background(), req)
	if err != nil {
		t.Fatalf("computeSemanticRelatedRows(fixture request): %v", err)
	}
	assertValid(t, resp)
	if len(resp.Recordset.Rows) != 7 {
		t.Fatalf("len(Rows) = %d, want 7 (the fixture's own value=5 against Customer 5's known Invoice count)", len(resp.Recordset.Rows))
	}
}
