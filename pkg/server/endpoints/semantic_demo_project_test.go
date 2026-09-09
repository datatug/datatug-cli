package endpoints

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/datatug-core/storage/filestore"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// demoProjectDirEnvVar names the environment variable this test reads the
// real datatug-demo-projects/demo-project-1 checkout's path from. Unset in
// CI (that sibling repo is not checked out there), so this test skips
// rather than fails — matching the same pattern dal-go/dalgo2http's and
// datatug-cli's httpsource streams already established for the same reason.
const demoProjectDirEnvVar = "DATATUG_DEMO_PROJECT_DIR"

// TestApplicable_RealDemoProject is AC:applicable-with-chain-server run
// against the actual demo-project-1 files (not this package's own synthetic
// fixture): customer-invoices is applicable, invoice-lines is not yet
// (missing Invoice.ID).
//
// This is the one of the four endpoints' scenarios the real project's
// CURRENT content can demonstrate end to end. /semantic/columns and
// /semantic/related cannot be, today, against this project — see the PR
// body's Assumptions for the two verified gaps: entities/Customer/
// Customer.entity.json declares no EntityField.Mappings at all (only
// NamePatterns, so every resolution comes back "inferred", never
// "declared", and the support-notes sameField lookup — which is driven
// entirely by Mappings — cannot fire), and no dbs/chinook.sqlite (or any
// other registered SQL catalog path) exists for the "chinook" source, so it
// cannot be opened for live schema scanning or row execution at all. Both
// are content/infra gaps in datatug-demo-projects, a separate repository
// outside this stream's file scope; semantic_related_test.go's synthetic
// fixture proves the CODE handles both cases correctly once a project
// declares them.
func TestApplicable_RealDemoProject(t *testing.T) {
	dir := os.Getenv(demoProjectDirEnvVar)
	if dir == "" {
		t.Skipf("%s not set; skipping the real demo-project-1 integration test (see the synthetic-fixture tests for CI coverage of the same behaviour)", demoProjectDirEnvVar)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("%s=%s: %v", demoProjectDirEnvVar, dir, err)
	}
	projectID := "real-demo-project-1"
	filestore.SetProjectPath(projectID, dir)

	resp, err := computeSemanticApplicable(projectID, ApplicableRequest{
		Values: []ApplicableValue{
			{Entity: "Customer", Field: "ID", Value: float64(5), Source: "chinook", Collection: "Customer", Column: "CustomerId", Provenance: "inferred"},
		},
	})
	if err != nil {
		t.Fatalf("computeSemanticApplicable: %v", err)
	}

	var foundInvoices, foundLines bool
	for _, a := range resp.Applicable {
		if a.Query == "customer-invoices" {
			foundInvoices = true
			if len(a.Chain) == 0 {
				t.Fatalf("customer-invoices has no chain text")
			}
		}
	}
	for _, n := range resp.NotYet {
		if n.Query == "invoice-lines" {
			foundLines = true
			if len(n.Missing) != 1 || n.Missing[0] != "Invoice.ID" {
				t.Fatalf("invoice-lines missing = %+v, want [Invoice.ID]", n.Missing)
			}
		}
	}
	if !foundInvoices {
		t.Fatalf("customer-invoices not applicable; got %+v", resp.Applicable)
	}
	if !foundLines {
		t.Fatalf("invoice-lines not in notYet; got %+v", resp.NotYet)
	}
}

// TestRelated_RealDemoProject_SupportNotesRowsWork proves the ONE part of
// /semantic/related/rows the real project's data can exercise today:
// fetching support-notes rows for customer 5 through the real inGitDB store
// at data/ingitdb, bypassing RelatedLookups/Resolve entirely (which, per
// TestApplicable_RealDemoProject's doc, cannot succeed against this
// project's current entity declarations) by building the lookupId directly.
func TestRelated_RealDemoProject_SupportNotesRowsWork(t *testing.T) {
	dir := os.Getenv(demoProjectDirEnvVar)
	if dir == "" {
		t.Skipf("%s not set; skipping", demoProjectDirEnvVar)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "ingitdb")); err != nil {
		t.Skipf("no data/ingitdb under %s: %v", dir, err)
	}
	projectID := "real-demo-project-1-rows"
	filestore.SetProjectPath(projectID, dir)

	session := secureread.Session{Unrestricted: true}
	lookupID := encodeLookupID("support-notes", "support-notes", "CustomerId")
	resp, err := computeSemanticRelatedRows(context.Background(), projectID, lookupID, "5", 0, session)
	if err != nil {
		t.Fatalf("computeSemanticRelatedRows: %v", err)
	}
	if len(resp.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2 (sn-005, sn-006 — verified against the real fixture files while building this stream)", len(resp.Rows))
	}
}
