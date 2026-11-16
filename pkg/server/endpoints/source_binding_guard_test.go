package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func TestBoundSourceDirectSavedTypesRefuseBeforeDispatch(t *testing.T) {
	root, project := boundEndpointFixture(t)
	// Resolve and validate the exact registered source before reading any of
	// another catalog's stored table/view metadata.
	oldCatalog := getCatalogTablesFunc
	metadataReads := 0
	getCatalogTablesFunc = func(a, b, c string) (*api.CatalogTables, error) { metadataReads++; return oldCatalog(a, b, c) }
	t.Cleanup(func() { getCatalogTablesFunc = oldCatalog })
	other := httptest.NewRecorder()
	getCatalogTablesHandler(other, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?project="+project+"&environment=local&catalog=other", nil))
	if other.Code == http.StatusOK || metadataReads != 0 {
		t.Fatalf("other source metadata read before binding: %d reads=%d", other.Code, metadataReads)
	}
	if _, err := resolveSQLSourceURL(context.Background(), "other", "sqlite://"+filepath.Join(root, "other.sqlite"), "Customer"); err == nil {
		t.Fatal("direct metadata bypassed exact target")
	}
	oldHTTP, oldSQL := runStructuredHTTPQuery, runNativeSQLHook
	t.Cleanup(func() { runStructuredHTTPQuery = oldHTTP; runNativeSQLHook = oldSQL })
	httpCalls, sqlCalls := 0, 0
	runStructuredHTTPQuery = func(ctx context.Context, e *secureread.Executor, source string, q dal.Query, vars map[string]any) (secureread.Result, error) {
		httpCalls++
		return oldHTTP(ctx, e, source, q, vars)
	}
	runNativeSQLHook = func(e *secureread.Executor, ctx context.Context, source, text string, args ...dal.QueryArg) (secureread.Result, error) {
		sqlCalls++
		return oldSQL(e, ctx, source, text, args...)
	}
	for _, kind := range []string{"HTTP", "SQL", "JavaScript"} {
		id := "bound-kind"
		metadata, _ := json.Marshal(map[string]any{"id": id, "title": "Executable", "createdAt": "2026-01-01T00:00:00Z", "type": kind})
		mustWriteFile(t, filepath.Join(root, "queries", id+".query.json"), string(metadata))
		req := apicontract.ExecutionRequest{Project: project, Environment: "local", Source: "chinook-local", SecurityContextID: api.SecurityContextID(), QueryID: id, Mode: apicontract.ProvenanceModeLive, Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{}}
		body, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		runQueryHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body)))
		if w.Code != http.StatusForbidden || httpCalls != 0 || sqlCalls != 0 {
			t.Fatalf("saved %s reached dispatch: status=%d http=%d sql=%d %s", kind, w.Code, httpCalls, sqlCalls, w.Body.String())
		}
	}
	// A prepared response cannot survive mandatory configuration invalidation.
	mustWriteFile(t, filepath.Join(root, "policies", "revoked.yaml"), "changed")
	held := httptest.NewRecorder()
	writeContractResponse(held, httptest.NewRequest(http.MethodGet, "/datatug/records/schema", nil), nil, map[string]any{"secret": "held-value"})
	if held.Code != http.StatusForbidden || bytes.Contains(held.Body.Bytes(), []byte("held-value")) {
		t.Fatalf("held metadata released after invalidation: %d %s", held.Code, held.Body.String())
	}
}

func boundEndpointFixture(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are unsupported")
	}
	root, project := writeSemanticTestProject(t)
	configureSemanticSession(t, root, project, "bob", []string{"support"})
	data, _ := json.Marshal(map[string]any{"profile": secureread.ExactTableReadProfile, "project": project, "environment": "local", "catalog": "chinook-local", "sqliteFile": filepath.Join(root, "chinook.sqlite"), "requiredPolicies": []string{"policies"}, "principal": map[string]any{"id": "bob", "roles": []string{"support"}}})
	mustMkdirAll(t, filepath.Join(root, ".datatug"))
	mustWriteFile(t, filepath.Join(root, ".datatug", "security.json"), string(data))
	s, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
	if err != nil || !declared {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(s, map[string]string{project: root}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })

	return root, project
}

func TestBoundSourceSchemaChangeAfterRealOpenReleasesNoFacts(t *testing.T) {
	root, _ := boundEndpointFixture(t)
	oldOpen, oldClose, oldSchema := openBoundSchemaDB, closeSchemaDB, dalAsSchemaReader
	t.Cleanup(func() { openBoundSchemaDB, closeSchemaDB, dalAsSchemaReader = oldOpen, oldClose, oldSchema })
	opens, closes, facts := 0, 0, 0
	openBoundSchemaDB = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
		opens++
		db, err := oldOpen(ctx, ref)
		if err == nil {
			mustWriteFile(t, filepath.Join(root, "policies", "after-open.yaml"), "changed")
		}
		return db, err
	}
	closeSchemaDB = func(db dal.DB) { closes++; oldClose(db) }
	dalAsSchemaReader = func(db dal.DB) (dbschema.SchemaReader, bool) { facts++; return oldSchema(db) }
	_, err := resolveSQLSourceURL(context.Background(), "chinook-local", dbcopy.LocalSourceURL("sqlite", filepath.Join(root, "chinook.sqlite")), "Customer")
	if !errors.Is(err, secureread.ErrSourceBinding) || opens != 1 || closes != 1 || facts != 0 {
		t.Fatalf("changed source metadata escaped: %v opens=%d closes=%d facts=%d", err, opens, closes, facts)
	}
}

type bindingChangeQueryStore struct {
	datatug.ProjectStore
	change func()
}

func (s bindingChangeQueryStore) LoadQuery(ctx context.Context, id string, options ...datatug.StoreOption) (*datatug.QueryDef, error) {
	q, err := s.ProjectStore.LoadQuery(ctx, id, options...)
	if err == nil {
		s.change()
	}
	return q, err
}
func TestBoundSourcePolicyChangeAfterSavedLoadRefusesBeforeDispatch(t *testing.T) {
	root, project := boundEndpointFixture(t)
	id := "after-load"
	metadata, _ := json.Marshal(map[string]any{"id": id, "title": "Before change", "createdAt": "2026-01-01T00:00:00Z", "type": "TugQL"})
	mustWriteFile(t, filepath.Join(root, "queries", id+".query.json"), string(metadata))
	mustWriteFile(t, filepath.Join(root, "queries", id+".query.tugql.yaml"), "from: {name: Customer}\n")
	oldStore, oldTugQL := execRunProjectStoreFor, runTugQLHook
	t.Cleanup(func() { execRunProjectStoreFor, runTugQLHook = oldStore, oldTugQL })
	calls := 0
	execRunProjectStoreFor = func(id string) (datatug.ProjectStore, error) {
		store, err := oldStore(id)
		return bindingChangeQueryStore{ProjectStore: store, change: func() { mustWriteFile(t, filepath.Join(root, "policies", "changed.yaml"), "changed") }}, err
	}
	runTugQLHook = func(e *secureread.Executor, ctx context.Context, source string, doc []byte, vars map[string]any) (secureread.Result, error) {
		calls++
		return oldTugQL(e, ctx, source, doc, vars)
	}
	req := apicontract.ExecutionRequest{Project: project, Environment: "local", Source: "chinook-local", SecurityContextID: api.SecurityContextID(), QueryID: id, Mode: apicontract.ProvenanceModeLive, Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{}}
	body, _ := json.Marshal(req)
	w := httptest.NewRecorder()
	runQueryHandler(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", bytes.NewReader(body)))
	if w.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("changed policy reached dispatch: status=%d calls=%d %s", w.Code, calls, w.Body.String())
	}
}
