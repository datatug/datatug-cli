package endpoints

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestCatalogTablesFiltersTablesAndViewsWithFixedPolicy(t *testing.T) {
	dir, project := writeSemanticTestProject(t)
	configureSemanticSession(t, dir, project, "bob", []string{"support"})
	for _, kind := range []string{"tables", "views"} {
		for _, name := range []string{"Customer", "Invoice", "Missing"} {
			if err := os.MkdirAll(filepath.Join(dir, "dbmodels", semanticTestSource, "main", kind, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	get := func(ctx context.Context) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := url.Values{"project": {project}, "environment": {semanticTestEnv}, "catalog": {"chinook-local"}}
		getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?"+q.Encode(), nil).WithContext(ctx))
		return w
	}
	answer := get(context.Background())
	if answer.Code != http.StatusOK || strings.Count(answer.Body.String(), `"name":"Customer"`) != 2 || strings.Contains(answer.Body.String(), "Invoice") || strings.Contains(answer.Body.String(), "Missing") {
		t.Fatalf("table/view filtering: %d %s", answer.Code, answer.Body.String())
	}
	denied := access.WithPolicy(context.Background(), access.MustPolicy("mandatory-deny", access.Root(access.Deny(access.Query))))
	answer = get(denied)
	if answer.Code != http.StatusOK || strings.Contains(answer.Body.String(), "Customer") {
		t.Fatalf("context deny did not filter all names: %d %s", answer.Code, answer.Body.String())
	}
	// Missing mandatory evaluation is a failed request, never an empty/partial catalog.
	failed := access.WithPolicy(context.Background(), catalogUnavailablePolicy{})
	answer = get(failed)
	if answer.Code == http.StatusOK || strings.Contains(answer.Body.String(), "Customer") || strings.Contains(answer.Body.String(), "private-provider-error") {
		t.Fatalf("policy outage exposed partial success: %d %s", answer.Code, answer.Body.String())
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	answer = get(cancelled)
	if answer.Code == http.StatusOK || strings.Contains(answer.Body.String(), "Customer") {
		t.Fatalf("cancelled catalog: %d %s", answer.Code, answer.Body.String())
	}
	between, cancelBetween := context.WithCancel(context.Background())
	defer cancelBetween()
	calls := 0
	cancelPolicy := catalogCancelPolicy{Policy: access.MustPolicy("cancel-after-admission", access.Root(access.Allow(access.Query))), cancel: cancelBetween, calls: &calls}
	answer = get(access.WithPolicy(between, cancelPolicy))
	if answer.Code == http.StatusOK || strings.Contains(answer.Body.String(), "Customer") || calls != 1 {
		t.Fatalf("cancel between table decisions: %d calls=%d %s", answer.Code, calls, answer.Body.String())
	}
	// An unsupported canonical spelling refuses the request after an allowed item too.
	if err := os.MkdirAll(filepath.Join(dir, "dbmodels", semanticTestSource, "unresolved", "tables", "Unknown"), 0o700); err != nil {
		t.Fatal(err)
	}
	answer = get(context.Background())
	if answer.Code == http.StatusOK || strings.Contains(answer.Body.String(), "Customer") {
		t.Fatalf("unsupported catalog identity returned partial success: %d %s", answer.Code, answer.Body.String())
	}
}

type catalogUnavailablePolicy struct{}

func (catalogUnavailablePolicy) Name() string { return "unavailable-provider" }
func (catalogUnavailablePolicy) Decide(context.Context, access.Request) access.Decision {
	return access.Decision{Code: access.CodeSourceUnavailable, Explanation: "private-provider-error"}
}
func (catalogUnavailablePolicy) Authorize(context.Context, access.Request) error {
	return errors.New("private-provider-error")
}

func TestCatalogTablesRefusesRegistryFailure(t *testing.T) {
	_, project := boundEndpointFixture(t)
	previous := apiResolveSource
	t.Cleanup(func() { apiResolveSource = previous })
	apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		return api.ResolvedSource{}, errors.New("private registry path")
	}
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=chinook-local", nil))
		return w
	}
	for _, malformed := range []bool{false, true} {
		if malformed {
			apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
				return api.ResolvedSource{URL: "invalid-source"}, nil
			}
		}
		w := request()
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "private registry path") {
			t.Fatalf("registry failure: %d %s", w.Code, w.Body.String())
		}
	}
	apiResolveSource = previous
	oldStore := relatedProjectStoreFor
	t.Cleanup(func() { relatedProjectStoreFor = oldStore })
	relatedProjectStoreFor = func(string) (datatug.ProjectStore, error) { return nil, errors.New("private store error") }
	w := request()
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "private store error") {
		t.Fatalf("store failure: %d %s", w.Code, w.Body.String())
	}
	relatedProjectStoreFor = oldStore
	oldCatalog := getCatalogTablesFunc
	t.Cleanup(func() { getCatalogTablesFunc = oldCatalog })
	getCatalogTablesFunc = func(string, string, string) (*api.CatalogTables, error) { return nil, nil }
	w = request()
	if w.Code == http.StatusOK {
		t.Fatalf("nil catalog loader result accepted: %d %s", w.Code, w.Body.String())
	}
	oldExecutor := secureExecutorHook
	t.Cleanup(func() { secureExecutorHook = oldExecutor })
	secureExecutorHook = func() (*secureread.Executor, bool) { return nil, false }
	w = request()
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing policy session: %d %s", w.Code, w.Body.String())
	}
}

type catalogCancelPolicy struct {
	access.Policy
	cancel context.CancelFunc
	calls  *int
}

func (p catalogCancelPolicy) Decide(ctx context.Context, request access.Request) access.Decision {
	*p.calls++
	decision := p.Policy.Decide(ctx, request)
	p.cancel()
	return decision
}

// Metadata uses only stored fixtures here; no network source is opened.
func TestCatalogTablesLegacyEnvironmentDriverAndQualifiedMandatoryPolicies(t *testing.T) {
	dir, project := writeSemanticTestProject(t)
	configureSemanticSession(t, dir, project, "alice", []string{"admin"})
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{project: dir}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	oldResolve := apiResolveSource
	t.Cleanup(func() { apiResolveSource = oldResolve })
	resolveCalls := 0
	apiResolveSource = func(context.Context, datatug.ProjectStore, string, string, string) (api.ResolvedSource, error) {
		resolveCalls++
		return api.ResolvedSource{}, errors.New("unrelated live/discovery source unavailable")
	}
	for _, entry := range []struct{ schema, name string }{{"public", "Customer"}, {"private", "Invoice"}} {
		if err := os.MkdirAll(filepath.Join(dir, "dbmodels", semanticTestSource, entry.schema, "tables", entry.name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	request := func(ctx context.Context) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=chinook-local", nil).WithContext(ctx))
		return w
	}
	t.Setenv("DATATUG_OWNED_METADATA_SOURCE", "")
	if err = os.Unsetenv("DATATUG_OWNED_METADATA_SOURCE"); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dir, "recordsets", "unrelated.recordset.json"), "{bad-json")
	mustWriteFile(t, filepath.Join(dir, "queries", "unrelated-http.query.json"), "{bad-json")
	store := filestore.NewProjectStore(project, dir)
	server := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	for _, driver := range []string{"postgres", "libsql+https", "mysql", "unsupported-stored-driver"} {
		catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: driver, Path: "env:DATATUG_OWNED_METADATA_SOURCE", DbModel: semanticTestSource}}
		catalog.ID = "chinook-local"
		if err = store.SaveEnvDbCatalog(context.Background(), "local", server, catalog.ID, catalog); err != nil {
			t.Fatal(err)
		}
		w := request(context.Background())
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Customer") || !strings.Contains(w.Body.String(), "Invoice") || resolveCalls != 0 {
			t.Fatalf("stored %s metadata depended on live discovery: %d resolves=%d %s", driver, w.Code, resolveCalls, w.Body.String())
		}
		policy := access.MustPolicy("qualified-mandatory", access.TableScope(access.TableName{Schema: "private", Name: "Invoice"}, access.Allow(access.Query)))
		w = request(access.WithPolicy(context.Background(), policy))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Customer") || !strings.Contains(w.Body.String(), "Invoice") {
			t.Fatalf("qualified mandatory identity lost: %d %s", w.Code, w.Body.String())
		}
		w = request(access.WithPolicy(context.Background(), access.MustPolicy("context-deny", access.Root(access.Deny(access.Query)))))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Customer") || strings.Contains(w.Body.String(), "Invoice") {
			t.Fatalf("mandatory deny bypass: %d %s", w.Code, w.Body.String())
		}
	}
	// SQLite's supported main.T spelling must match the same bare T resource
	// used by actual reads even in the explicit policy-free baseline.
	if err := os.MkdirAll(filepath.Join(dir, "dbmodels", semanticTestSource, "main", "tables", "Customer"), 0700); err != nil {
		t.Fatal(err)
	}
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: filepath.Join(dir, "chinook.sqlite"), DbModel: semanticTestSource}}
	catalog.ID = "chinook-local"
	if err = store.SaveEnvDbCatalog(context.Background(), "local", server, catalog.ID, catalog); err != nil {
		t.Fatal(err)
	}
	absent := httptest.NewRecorder()
	getCatalogTablesHandler(absent, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=not-registered", nil))
	if absent.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing stored catalog granted metadata: %d %s", absent.Code, absent.Body.String())
	}
	w := request(access.WithPolicy(context.Background(), access.MustPolicy("bare-customer", access.Collection("Customer", access.Allow(access.Query)))))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Customer") {
		t.Fatalf("default-schema policy parity lost: %d %s", w.Code, w.Body.String())
	}
}

func TestCatalogTablesLoaderAndInvalidIdentityNeverReturnPartialSuccess(t *testing.T) {
	dir, project := writeSemanticTestProject(t)
	configureSemanticSession(t, dir, project, "bob", []string{"support"})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	old := getCatalogTablesFunc
	t.Cleanup(func() { getCatalogTablesFunc = old })
	for _, kind := range []string{"error", "nil", "invalid-name", "invalid-schema"} {
		getCatalogTablesFunc = func(string, string, string) (*api.CatalogTables, error) {
			switch kind {
			case "error":
				return nil, errors.New("owned loader unavailable")
			case "nil":
				return nil, nil
			case "invalid-name":
				return &api.CatalogTables{Tables: []api.CatalogTable{{Schema: "main", Name: "Customer"}, {Schema: "main", Name: ""}}}, nil
			default:
				return &api.CatalogTables{Tables: []api.CatalogTable{{Schema: "main", Name: "Customer"}, {Schema: string([]byte{0xff}), Name: "Hidden"}}}, nil
			}
		}
		w := httptest.NewRecorder()
		getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=chinook-local", nil))
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "Customer") || strings.Contains(w.Body.String(), "Hidden") {
			t.Fatalf("partial %s metadata result: %d %s", kind, w.Code, w.Body.String())
		}
	}
}
