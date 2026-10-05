package endpoints

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
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

// The environment and the catalog of GET /datatug/catalog-tables become folder
// names under the project, so each must be a plain name: anything else is a 400
// with a fixed message that names the parameter and echoes nothing, before either
// reaches a file path. getCatalogTablesFunc is the only step that reads, so a count
// of its calls is a count of requests that went on to read.
func TestGetCatalogTablesHandler_RefusesAnUnsafeEnvironmentOrCatalogBeforeAnyRead(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	reads := 0
	previous := getCatalogTablesFunc
	getCatalogTablesFunc = func(projectDir, environment, catalog string) (*api.CatalogTables, error) {
		reads++
		return previous(projectDir, environment, catalog)
	}
	t.Cleanup(func() { getCatalogTablesFunc = previous })

	get := func(rawQuery string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		getCatalogTablesHandler(rr, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?"+rawQuery, nil))
		return rr
	}

	// The counter is live: a plain pair is looked up.
	if rr := get("project=" + projectID + "&environment=" + semanticTestEnv + "&catalog=no-such-catalog"); rr.Code != http.StatusNotFound || reads != 1 {
		t.Fatalf("plain IDs: status = %d, reads = %d, want 404 after 1 read: %s", rr.Code, reads, rr.Body.String())
	}
	reads = 0

	for _, c := range sourcecases.UnsafeIdentifiers() {
		for _, names := range [][2]string{{"environment", "catalog"}, {"env", "db"}} {
			for _, position := range []string{names[0], names[1]} {
				query := url.Values{"project": {projectID}, names[0]: {semanticTestEnv}, names[1]: {"chinook-local"}}
				query.Set(position, c.ID)
				rr := get(query.Encode())
				if rr.Code != http.StatusBadRequest {
					t.Errorf("%s as %s: status = %d, want 400: %s", c.Name, position, rr.Code, rr.Body.String())
					continue
				}
				body := rr.Body.String()
				field := "environment"
				if position == names[1] {
					field = "catalog"
				}
				if !strings.Contains(body, "["+field+"]") || !strings.Contains(body, "must be a plain name") {
					t.Errorf("%s as %s: the body does not name the parameter and the rule: %s", c.Name, position, body)
				}
				if len(c.ID) >= 3 && strings.Contains(body, c.ID) {
					t.Errorf("%s as %s: the body echoes the value: %s", c.Name, position, body)
				}
			}
		}
	}

	// The value as a client writes it on the wire: one level of percent-encoding is
	// undone by the server, a second level is still text with a "%" in it.
	for name, raw := range map[string]string{
		"encoded parent and slash":       "environment=%2e%2e%2fsecret&catalog=chinook-local",
		"encoded twice parent and slash": "environment=%252e%252e%252f&catalog=chinook-local",
		"encoded slash in the catalog":   "environment=local&catalog=a%2Fb",
		"encoded twice slash in catalog": "environment=local&catalog=a%252Fb",
		"encoded NUL in the environment": "environment=a%00b&catalog=chinook-local",
		"encoded absolute path":          "environment=local&catalog=%2Fetc%2Fpasswd",
		"encoded backslash and drive":    "environment=C%3A%5Cdata&catalog=chinook-local",
		"aliases with a traversal":       "env=..%2F..%2Fx&db=chinook-local",
		"aliases with a traversal in db": "env=local&db=..%2F..%2Fx",
	} {
		if rr := get("project=" + projectID + "&" + raw); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rr.Code, rr.Body.String())
		}
	}
	if reads != 0 {
		t.Fatalf("%d refused requests went on to read, want 0", reads)
	}
}

// The project of the request is quoted in the 404 only when it is a plain name: a
// source string can be sent where a project ID belongs. The project is configured
// here, so that the request gets past the store lookup, and the directory lookup
// (catalogProjectDir) finds nothing.
func TestGetCatalogTablesHandler_UnknownProjectNamesOnlyAPlainName(t *testing.T) {
	const typed = "postgres://alice:s3cretpw@db.example.com/shop"
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{typed: t.TempDir(), "no-such-project": t.TempDir()}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	previous := catalogProjectDir
	catalogProjectDir = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { catalogProjectDir = previous })

	get := func(project string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		query := url.Values{"project": {project}, "environment": {"local"}, "catalog": {"chinook-local"}}
		getCatalogTablesHandler(rr, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?"+query.Encode(), nil))
		return rr
	}
	rr := get(typed)
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "unknown project") {
		t.Fatalf("status = %d, want the 404 for an unknown project, body = %s", rr.Code, rr.Body.String())
	}
	for _, leaked := range []string{"s3cretpw", "alice", "db.example.com"} {
		if strings.Contains(rr.Body.String(), leaked) {
			t.Errorf("the 404 shows %q: %s", leaked, rr.Body.String())
		}
	}
	if rr := get("no-such-project"); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), `unknown project \"no-such-project\"`) {
		t.Errorf("a plain project should be named in the 404: %d %s", rr.Code, rr.Body.String())
	}
}
