package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestCatalogTablesTypedAbsentProjectPreservesLegacyError(t *testing.T) {
	dir, _ := writeSemanticTestProject(t)
	configureSemanticSession(t, dir, "missing", "alice", []string{"admin"})
	old := catalogProjectDir
	t.Cleanup(func() { catalogProjectDir = old })
	catalogProjectDir = func(string) (string, bool) { return "", false }
	w := httptest.NewRecorder()
	getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project=missing&environment=local&catalog=selected", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 404 || len(body) != 3 || body["error"] != `catalog not found: unknown project "missing"` || body["code"] != "NOT_FOUND" || body["field"] != "project" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestCatalogTablesTypedAbsentSourcePreservesLegacyError(t *testing.T) {
	dir, project := writeSemanticTestProject(t)
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	w := httptest.NewRecorder()
	getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=missing", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 404 || len(body) != 3 || body["error"] != `catalog not found: catalog "missing" in environment "local"` || body["code"] != "NOT_FOUND" || body["field"] != "source" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestCatalogTablesTypedAbsenceDoesNotClassifyOtherLoadFailures(t *testing.T) {
	dir, project := writeSemanticTestProject(t)
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	old := getCatalogTablesFunc
	t.Cleanup(func() { getCatalogTablesFunc = old })
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{{"internal", errors.New("catalog not found text only"), 500, ""}, {"denied", secureread.ErrAccessDenied, 403, "ACCESS_DENIED"}, {"denial-before-absence", errors.Join(api.ErrCatalogNotFound, secureread.ErrAccessDenied), 403, "ACCESS_DENIED"}} {
		t.Run(tc.name, func(t *testing.T) {
			getCatalogTablesFunc = func(string, string, string) (*api.CatalogTables, error) { return nil, tc.err }
			w := httptest.NewRecorder()
			getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=chinook-local", nil))
			var body ErrorResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || body.Field != "" || body.Code != tc.code {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCatalogTablesBoundDenialDoesNotExposeTypedSourceAbsence(t *testing.T) {
	_, project := boundEndpointFixture(t)
	oldResolve, oldLoad := apiResolveSource, getCatalogTablesFunc
	t.Cleanup(func() { apiResolveSource = oldResolve; getCatalogTablesFunc = oldLoad })
	apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		return api.ResolvedSource{}, fmt.Errorf("private source: %w", api.ErrCatalogNotFound)
	}
	reads := 0
	getCatalogTablesFunc = func(string, string, string) (*api.CatalogTables, error) { reads++; return nil, api.ErrCatalogNotFound }
	w := httptest.NewRecorder()
	getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=chinook-local", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 503 || reads != 0 || body["field"] != nil || body["code"] == "NOT_FOUND" {
		t.Fatalf("reads%d %d %s", reads, w.Code, w.Body.String())
	}
}
