package endpoints

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetCatalogTablesHandler_HTTP covers Task 17 item A.2 end to end
// through the real route (routes.go's environmentsRoutes), against
// writeSemanticTestProject's own registered environment/catalog
// (registerChinookEnvironment: env "local", catalog "chinook-local",
// dbModel "chinook" — semanticTestEnv/semanticTestSource) plus a manually
// written dbmodels/ tree (that fixture builds entities/queries/policies
// but no dbmodel scan output, since no other existing test needed one).
func TestGetCatalogTablesHandler_HTTP(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	for _, name := range []string{"Album", "Customer"} {
		dir := filepath.Join(projectDir, "dbmodels", semanticTestSource, "main", "tables", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}

	url := "/datatug/catalog-tables?project=" + projectID + "&environment=" + semanticTestEnv + "&catalog=chinook-local"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	getCatalogTablesHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{`"schema":"main"`, `"name":"Album"`, `"name":"Customer"`, `"dbType":"BASE TABLE"`} {
		if !strings.Contains(body, want) {
			t.Errorf("response body missing %q: %s", want, body)
		}
	}
}

// TestGetCatalogTablesHandler_UnknownCatalog_Is404 covers the not-found
// mapping (util_error_handling.go's handleError, api.ErrCatalogNotFound)
// over the real HTTP handler, not just the business-logic function.
func TestGetCatalogTablesHandler_UnknownCatalog_Is404(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})

	url := "/datatug/catalog-tables?project=" + projectID + "&environment=" + semanticTestEnv + "&catalog=no-such-catalog"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rr := httptest.NewRecorder()
	getCatalogTablesHandler(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rr.Code, rr.Body.String())
	}
}
