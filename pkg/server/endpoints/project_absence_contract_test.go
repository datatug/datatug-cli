package endpoints

import (
	"context"
	"encoding/json"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/sneat-co/sneat-go-core/apicore"
	"github.com/sneat-co/sneat-go-core/facade"
	"github.com/sneat-co/sneat-go-core/sneatauth"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisteredMissingProjectMetadataIsTypedProjectAbsence(t *testing.T) {
	for _, missing := range []string{"root", "definition"} {
		t.Run(missing, func(t *testing.T) {
			dir, project := writeSemanticTestProject(t)
			configureSemanticSession(t, dir, project, "alice", []string{"admin"})
			if err := os.Remove(filepath.Join(dir, "datatug-project.json")); err != nil {
				t.Fatal(err)
			}
			if missing == "root" {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
			}
			for _, route := range []struct {
				name, url string
				handler   http.HandlerFunc
			}{
				{"catalog", "/datatug/catalog-tables?project=" + project + "&environment=local&catalog=chinook-local", getCatalogTablesHandler},
				{"full", "/datatug/projects/project_full?id=" + project, getProjectFull},
			} {
				t.Run(route.name, func(t *testing.T) {
					w := httptest.NewRecorder()
					route.handler(w, httptest.NewRequest(http.MethodGet, route.url, nil))
					var body ErrorResponse
					if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if w.Code != 404 || body.Code != "NOT_FOUND" || body.Field != "project" {
						t.Fatalf("%d %s", w.Code, w.Body.String())
					}
				})
			}
		})
	}
}

func TestExistingProjectMissingCatalogStaysTypedSourceAbsence(t *testing.T) {
	dir, project := writeSemanticTestProject(t)
	if err := os.WriteFile(filepath.Join(dir, "datatug-project.json"), []byte(`{"title":"Existing"}`), 0600); err != nil {
		t.Fatal(err)
	}
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	w := httptest.NewRecorder()
	getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=missing", nil))
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 404 || body.Code != "NOT_FOUND" || body.Field != "source" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestBoundWrongProjectDenialPrecedesMetadataResolution(t *testing.T) {
	_, _ = boundEndpointFixture(t)
	oldDir, oldCatalog, oldStore := catalogProjectDir, getCatalogTablesFunc, relatedProjectStoreFor
	t.Cleanup(func() { catalogProjectDir, getCatalogTablesFunc, relatedProjectStoreFor = oldDir, oldCatalog, oldStore })
	catalogProjectDir = func(string) (string, bool) { t.Fatal("project path observed on bound denial"); return "", false }
	getCatalogTablesFunc = func(string, string, string) (*api.CatalogTables, error) {
		t.Fatal("catalog read on bound denial")
		return nil, nil
	}
	relatedProjectStoreFor = func(string) (datatug.ProjectStore, error) { t.Fatal("store opened on bound denial"); return nil, nil }
	for _, route := range []struct {
		url     string
		handler http.HandlerFunc
	}{
		{"/datatug/catalog-tables?project=foreign&environment=local&catalog=missing", getCatalogTablesHandler},
		{"/datatug/projects/project_full?id=foreign", getProjectFull},
		{"/datatug/projects/project_summary?id=foreign", getProjectSummary},
	} {
		w := httptest.NewRecorder()
		route.handler(w, httptest.NewRequest(http.MethodGet, route.url, nil))
		if w.Code != 403 || strings.Contains(w.Body.String(), "NOT_FOUND") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestProjectMetadataIOFailureAndUnregisteredStorageAreNotAbsence(t *testing.T) {
	dir, project := writeSemanticTestProject(t)
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	definition := filepath.Join(dir, "datatug-project.json")
	if err := os.Remove(definition); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(definition, 0700); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct {
		url     string
		handler http.HandlerFunc
	}{
		{"/datatug/catalog-tables?project=" + project + "&environment=local&catalog=missing", getCatalogTablesHandler},
		{"/datatug/projects/project_full?id=" + project, getProjectFull},
	} {
		w := httptest.NewRecorder()
		route.handler(w, httptest.NewRequest(http.MethodGet, route.url, nil))
		if w.Code != 500 || strings.Contains(w.Body.String(), "NOT_FOUND") || strings.Contains(w.Body.String(), dir) {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		route.handler(w, httptest.NewRequest(http.MethodGet, route.url+"&storage=unregistered", nil))
		var body ErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != 400 || body.Code != "INVALID_REQUEST" || body.Field != "storage" {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}

func TestProjectSummaryActualAuthenticatedHandleAbsenceAndAuthPriority(t *testing.T) {
	dir, project := writeSemanticTestProject(t)
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	reads := 0
	storage.NewDatatugStore = func(id string) (storage.Store, error) { reads++; return previousStore(id) }
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		getProjectSummary(w, httptest.NewRequest(http.MethodGet, "/datatug/projects/project_summary?id="+project, nil))
		return w
	}
	validHook := apicore.GetAuthTokenFromHttpRequest
	for _, invalid := range []bool{false, true} {
		apicore.GetAuthTokenFromHttpRequest = func(_ *http.Request, required bool) (*sneatauth.Token, error) {
			if !required {
				t.Fatal("AuthRequired lost")
			}
			if invalid {
				return nil, facade.ErrUnauthorized
			}
			return nil, nil
		}
		before := reads
		w := request()
		// Existing apicore verification returns without writing a status/body.
		// Preserve that response exactly, and prove it never reached metadata.
		if w.Code != 200 || w.Body.Len() != 0 || reads != before {
			t.Fatalf("preworker auth read%d: %d %s", reads-before, w.Code, w.Body.String())
		}
	}
	apicore.GetAuthTokenFromHttpRequest = validHook
	validContext := getContextFromRequest
	getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), facade.ErrForbidden }
	before := reads
	denied := request()
	if denied.Code != 403 || reads != before || strings.Contains(denied.Body.String(), "NOT_FOUND") {
		t.Fatalf("context refusal read%d: %d %s", reads-before, denied.Code, denied.Body.String())
	}
	getContextFromRequest = validContext
	w := request()
	if w.Code != 200 {
		t.Fatalf("success: %d %s", w.Code, w.Body.String())
	}
	if err := os.Remove(filepath.Join(dir, "datatug-project.json")); err != nil {
		t.Fatal(err)
	}
	w = request()
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 404 || body.Code != "NOT_FOUND" || body.Field != "project" {
		t.Fatalf("absence: %d %s", w.Code, w.Body.String())
	}
	if err := os.Mkdir(filepath.Join(dir, "datatug-project.json"), 0700); err != nil {
		t.Fatal(err)
	}
	w = request()
	if w.Code != 500 || strings.Contains(w.Body.String(), "NOT_FOUND") || !strings.Contains(w.Body.String(), `"error":{`) {
		t.Fatalf("IO: %d %s", w.Code, w.Body.String())
	}
}
