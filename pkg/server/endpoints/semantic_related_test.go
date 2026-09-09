package endpoints

import (
	"context"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func adminSession(t *testing.T, projectDir string) secureread.Session {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{
		As: "alice", Roles: []string{"admin"},
		PoliciesDir: projectDir + "/policies",
	})
	if err != nil {
		t.Fatalf("secureread.NewSession(admin): %v", err)
	}
	return session
}

func supportSession(t *testing.T, projectDir string) secureread.Session {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{
		As: "bob", Roles: []string{"support"},
		PoliciesDir: projectDir + "/policies",
	})
	if err != nil {
		t.Fatalf("secureread.NewSession(support): %v", err)
	}
	return session
}

// TestSemanticRelated_CustomerFiveInvoicesAndSupportNotes is
// core-investigation-loop's own example (AC:related-across-sources-server):
// selecting Customer.ID=5 lists Invoices (chinook, count 7) and Support
// notes (support-notes, count 2, the sameField lookup into inGitDB).
func TestSemanticRelated_CustomerFiveInvoicesAndSupportNotes(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	session := adminSession(t, projectDir)

	resp, err := computeSemanticRelated(context.Background(), semanticRelatedRequest{
		ProjectID: projectID, Entity: "Customer", Field: "ID", Value: "5",
		Source: "chinook", Collection: "Customer", Column: "CustomerId", Provenance: "declared",
	}, session)
	if err != nil {
		t.Fatalf("computeSemanticRelated: %v", err)
	}

	byCollection := map[string]RelatedEntry{}
	for _, e := range resp.Related {
		byCollection[e.Source+"/"+e.Collection] = e
	}

	invoices, ok := byCollection["chinook/Invoice"]
	if !ok {
		t.Fatalf("Invoice lookup not found; got %+v", resp.Related)
	}
	if invoices.Kind != "referencedBy" {
		t.Fatalf("Invoice lookup Kind = %q, want referencedBy", invoices.Kind)
	}
	if invoices.Count == nil || *invoices.Count != 7 {
		t.Fatalf("Invoice count = %v, want 7", invoices.Count)
	}

	notes, ok := byCollection["support-notes/support-notes"]
	if !ok {
		t.Fatalf("support-notes lookup not found; got %+v", resp.Related)
	}
	if notes.Kind != "sameField" {
		t.Fatalf("support-notes lookup Kind = %q, want sameField", notes.Kind)
	}
	if notes.Count == nil || *notes.Count != 2 {
		t.Fatalf("support-notes count = %v, want 2", notes.Count)
	}
}

// TestSemanticRelated_RestrictedPrincipal_NoTrueCount is
// AC:related-respects-policy: a principal with no grant at all on Invoice
// sees no true count for it.
func TestSemanticRelated_RestrictedPrincipal_NoTrueCount(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	session := supportSession(t, projectDir)

	resp, err := computeSemanticRelated(context.Background(), semanticRelatedRequest{
		ProjectID: projectID, Entity: "Customer", Field: "ID", Value: "5",
		Source: "chinook", Collection: "Customer", Column: "CustomerId", Provenance: "declared",
	}, session)
	if err != nil {
		t.Fatalf("computeSemanticRelated: %v", err)
	}
	for _, e := range resp.Related {
		if e.Source == "chinook" && e.Collection == "Invoice" {
			if e.Count != nil {
				t.Fatalf("restricted principal's Invoice count = %v, want nil (count unavailable)", *e.Count)
			}
			return
		}
	}
	t.Fatalf("Invoice lookup not found; got %+v", resp.Related)
}

func TestSemanticRelated_MissingRequiredParams(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	req := semanticRelatedRequest{ProjectID: projectID}
	if err := req.validate(); err == nil {
		t.Fatalf("validate() = nil, want error for an entirely empty request")
	}
}

func TestSemanticRelatedRows_CustomerFiveInvoices(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	session := adminSession(t, projectDir)
	lookupID := encodeLookupID("chinook", "Invoice", "CustomerId")

	resp, err := computeSemanticRelatedRows(context.Background(), projectID, lookupID, "5", 0, session)
	if err != nil {
		t.Fatalf("computeSemanticRelatedRows: %v", err)
	}
	if len(resp.Rows) != 7 {
		t.Fatalf("len(Rows) = %d, want 7", len(resp.Rows))
	}
}

// TestSemanticRelatedRows_RestrictedPrincipal_ZeroRows is
// AC:related-respects-policy's row-fetch half: with the network^H^H^H
// policy disabled for Invoice, the restricted principal gets zero rows
// (success, not a hard failure) — the endpoint must not crash or leak rows.
func TestSemanticRelatedRows_RestrictedPrincipal_ZeroRows(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	session := supportSession(t, projectDir)
	lookupID := encodeLookupID("chinook", "Invoice", "CustomerId")

	resp, err := computeSemanticRelatedRows(context.Background(), projectID, lookupID, "5", 0, session)
	if err != nil {
		// Also acceptable: accesspolicies.Run refuses the query outright for
		// a collection nothing grants. Either shape must never crash and
		// must never return a real Invoice row to this principal.
		if !isAccessDeniedError(err) {
			t.Fatalf("computeSemanticRelatedRows: %v (want either 0 rows or an ACCESS_DENIED structured error)", err)
		}
		return
	}
	if len(resp.Rows) != 0 {
		t.Fatalf("len(Rows) = %d, want 0 for a principal with no Invoice grant", len(resp.Rows))
	}
}

func isAccessDeniedError(err error) bool {
	se, ok := err.(*structuredError)
	return ok && se.Code == "ACCESS_DENIED"
}

func TestSemanticRelatedRows_InvalidLookupID(t *testing.T) {
	_, projectID := writeSemanticTestProject(t)
	session := secureread.Session{Unrestricted: true}
	if _, err := computeSemanticRelatedRows(context.Background(), projectID, "not-valid-base64!!", "5", 0, session); err == nil {
		t.Fatalf("want error for an invalid lookupId")
	}
}

func TestEncodeDecodeLookupID_RoundTrip(t *testing.T) {
	id := encodeLookupID("chinook", "Invoice", "CustomerId")
	source, collection, column, err := decodeLookupID(id)
	if err != nil {
		t.Fatalf("decodeLookupID: %v", err)
	}
	if source != "chinook" || collection != "Invoice" || column != "CustomerId" {
		t.Fatalf("decodeLookupID() = (%q, %q, %q)", source, collection, column)
	}
}
