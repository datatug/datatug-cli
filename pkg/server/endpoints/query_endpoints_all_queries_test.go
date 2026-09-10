package endpoints

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
)

// indexQueriesByCanonicalID flattens a QueriesFolder tree back into a
// canonical-id -> type map, mirroring how loadModuleQueries'
// canonicalIDs/buildQueriesFolderTree pair relate — used here only to
// assert the RESULT of that round trip.
func indexQueriesByCanonicalID(folder *datatug.QueriesFolder, prefix string) map[string]string {
	out := map[string]string{}
	for _, item := range folder.Items {
		id := item.ID
		if prefix != "" {
			id = prefix + "/" + id
		}
		out[id] = string(item.Type)
	}
	for _, sub := range folder.Folders {
		subPrefix := sub.ID
		if prefix != "" {
			subPrefix = prefix + "/" + sub.ID
		}
		for k, v := range indexQueriesByCanonicalID(sub, subPrefix) {
			out[k] = v
		}
	}
	return out
}

// wantDemoQueryTypes is writeSemanticTestProject's own query fixture
// (writeApplicableQueries/writeHTTPCountryQuery, semantic_fixture_test.go):
// two SQL queries under customers/, one SQL under invoices/, one HTTP under
// reference/ — every canonical id folder-qualified per S97's convention.
var wantDemoQueryTypes = map[string]string{
	"customers/customer-invoices": "SQL",
	"customers/customer-export":   "SQL",
	"invoices/invoice-lines":      "SQL",
	"reference/country-facts":     "HTTP",
}

// TestGetAllQueries_AdminSeesAllDemoQueriesWithFolders covers Task 17 item
// A.1's first required case (S121): every demo query listed, correctly
// folder-qualified, HTTP-typed queries listed with their type.
func TestGetAllQueries_AdminSeesAllDemoQueriesWithFolders(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	folder, err := getAllQueries(context.Background(), dto.ProjectRef{StoreID: "files", ProjectID: projectID})
	if err != nil {
		t.Fatalf("getAllQueries: %v", err)
	}
	if folder.ID != datatug.RootSharedFolderName {
		t.Errorf("root folder ID = %q, want %q", folder.ID, datatug.RootSharedFolderName)
	}
	got := indexQueriesByCanonicalID(folder, "")
	if len(got) != len(wantDemoQueryTypes) {
		t.Fatalf("admin got %d queries, want %d: %v", len(got), len(wantDemoQueryTypes), got)
	}
	for id, wantType := range wantDemoQueryTypes {
		gotType, ok := got[id]
		if !ok {
			t.Errorf("missing query %q", id)
			continue
		}
		if gotType != wantType {
			t.Errorf("query %q type = %q, want %q", id, gotType, wantType)
		}
	}
	// Folder nesting itself, not just flattened ids: customers/ must carry
	// exactly its two items, as a *sub*folder of root — not items dumped
	// straight onto root.
	var customers *datatug.QueriesFolder
	for _, f := range folder.Folders {
		if f.ID == "customers" {
			customers = f
		}
	}
	if customers == nil {
		t.Fatalf("no customers/ subfolder in %+v", folder.Folders)
	}
	if len(customers.Items) != 2 {
		t.Errorf("customers/ has %d items, want 2: %+v", len(customers.Items), customers.Items)
	}
}

// TestGetAllQueries_SupportSeesSameAuthorizedSet covers Task 17 item A.1's
// second required case: support sees the same set as admin (both are
// authorized under demo-project-1's own policy — see
// getQueriesHandler's doc comment on why no listing-time ACL filter
// applies), never a query outside policy (asserted here as "never MORE
// than admin's own set").
func TestGetAllQueries_SupportSeesSameAuthorizedSet(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "bob", []string{"support"})

	folder, err := getAllQueries(context.Background(), dto.ProjectRef{StoreID: "files", ProjectID: projectID})
	if err != nil {
		t.Fatalf("getAllQueries: %v", err)
	}
	got := indexQueriesByCanonicalID(folder, "")
	if len(got) != len(wantDemoQueryTypes) {
		t.Fatalf("support got %d queries, want %d (must match admin's set exactly): %v", len(got), len(wantDemoQueryTypes), got)
	}
	for id, wantType := range wantDemoQueryTypes {
		gotType, ok := got[id]
		if !ok {
			t.Errorf("support missing query %q — expected authorized (never a query outside policy)", id)
			continue
		}
		if gotType != wantType {
			t.Errorf("query %q type = %q, want %q", id, gotType, wantType)
		}
	}
}

// TestGetQueriesHandler_HTTP covers the full route: registration
// (routes.go's queriesRoutes), request parsing (newProjectRef) and JSON
// encoding (returnJSON) — not just the business-logic function above.
func TestGetQueriesHandler_HTTP(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	req := httptest.NewRequest(http.MethodGet, "/datatug/queries/all_queries?project="+projectID+"&folder=~", nil)
	rr := httptest.NewRecorder()
	getQueriesHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := rr.Body.String()
	for _, want := range []string{"customer-invoices", "customer-export", "invoice-lines", "country-facts", `"type":"HTTP"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response body missing %q: %s", want, body)
		}
	}
}
