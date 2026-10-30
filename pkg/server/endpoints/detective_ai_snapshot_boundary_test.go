package endpoints

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestDetectiveAISnapshotUsesAuthorizedSavedQueryRevisionsAndContext(t *testing.T) {
	scope, projectDir := realCaptureSetup(t, "admin", []string{"admin"})
	store, err := detective.NewLocalStore(scope.Project, projectDir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := detectiveAISnapshot(t.Context(), scope.Project, store); !errors.Is(err, detective.ErrInvalid) {
		t.Fatalf("incomplete legacy query admitted to AI review basis: %v", err)
	}
	for path, body := range map[string]string{
		"customers/customer-invoices.query.sql": "SELECT CustomerId FROM Customer",
		"customers/customer-export.query.sql":   "SELECT CustomerId FROM Customer",
		"invoices/invoice-lines.query.sql":      "SELECT InvoiceId FROM InvoiceLine",
		"reference/country-facts.query.http":    "GET https://example.invalid/country",
	} {
		if err := os.WriteFile(filepath.Join(projectDir, "queries", path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	base, err := detectiveAISnapshot(t.Context(), scope.Project, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Environments) != 1 || base.Environments[0] != semanticTestEnv || len(base.Queries) < 3 || len(base.CatalogRevision) != 64 {
		t.Fatalf("authorized snapshot lacks environment, query catalog or digest: %+v", base)
	}
	projectStore, err := api.ProjectStoreFor(scope.Project)
	if err != nil {
		t.Fatal(err)
	}
	revisioned := projectStore.(datatug.RevisionedQueriesStore)
	var found bool
	for _, resource := range base.Queries {
		if resource.ID != "customers/customer-invoices" {
			continue
		}
		found = true
		stored, err := revisioned.LoadQueryRevision(t.Context(), resource.ID)
		if err != nil || stored == nil || resource.Revision != string(stored.Revision) || resource.Title != "Customer invoices" || len(resource.Inputs) != 1 || resource.Inputs[0].Name != "CustomerId" || !resource.Inputs[0].Required {
			t.Fatalf("saved-query revision/schema not bound: %+v stored=%+v err=%v", resource, stored, err)
		}
	}
	if !found {
		t.Fatalf("canonical nested query missing: %+v", base.Queries)
	}
	state, err := store.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.Checks = []detective.Check{{ID: "check-1", Title: "Review invoices", Version: 2}}
	state.Cases = []detective.Case{{ID: "case-1", Title: "Invoice case", Version: 3}}
	changed, err := detectiveAISnapshotFromState(t.Context(), scope.Project, state)
	if err != nil {
		t.Fatal(err)
	}
	if changed.CatalogRevision == base.CatalogRevision || len(changed.Checks) != 1 || changed.Checks[0].Revision != "2" || len(changed.Cases) != 1 || changed.Cases[0].Revision != "3" {
		t.Fatalf("review basis ignored materialized catalog change: base=%+v changed=%+v", base, changed)
	}
}

func TestDetectiveAISnapshotRejectsUnboundedOrUnknownProject(t *testing.T) {
	scope, _ := realCaptureSetup(t, "admin", []string{"admin"})
	state := detective.State{Cases: make([]detective.Case, 201)}
	if _, err := detectiveAISnapshotFromState(context.Background(), scope.Project, state); !errors.Is(err, detective.ErrCapacity) {
		t.Fatalf("unbounded Case context entered AI prompt: %v", err)
	}
	if _, err := detectiveAISnapshotFromState(context.Background(), "unknown-"+strings.Repeat("x", 8), detective.State{}); err == nil {
		t.Fatal("unknown project obtained AI snapshot")
	}
}
