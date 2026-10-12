package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// An environment a client sends becomes a folder name under the project, in the
// project store's own lookups. Every route that takes one refuses anything but a
// plain name before the first lookup: the contract routes in validateScope, and
// the legacy routes in the Validate of their requests.

func TestValidateScope_RefusesAnEnvironmentThatIsNotAPlainName(t *testing.T) {
	scope := func(environment string) apicontract.Scope {
		return apicontract.Scope{Project: "demo", Environment: environment, SecurityContextID: "stale-context"}
	}
	// A plain name passes this check and reaches the next one, the security context.
	if err := validateScope(scope("local")); err == nil || err.(*contractError).Code != apicontract.ErrCodeStaleContext {
		t.Fatalf("a plain environment: validateScope = %v, want the stale-context answer", err)
	}
	for _, c := range sourcecases.UnsafeIdentifiers() {
		err := validateScope(scope(c.ID))
		var contract *contractError
		if err == nil {
			t.Errorf("%s: validateScope accepted %q", c.Name, c.ID)
			continue
		}
		contract = err.(*contractError)
		wantCode := apicontract.ErrCodeInvalidRequest
		if c.ID == "" {
			wantCode = apicontract.ErrCodeMissingParameter // the answer it gave before
		}
		if contract.Code != wantCode || contract.Field != "environment" {
			t.Errorf("%s: validateScope = %s on field %q, want %s on field environment", c.Name, contract.Code, contract.Field, wantCode)
		}
		if len(c.ID) >= 3 && strings.Contains(contract.Message, c.ID) {
			t.Errorf("%s: the message echoes the value: %s", c.Name, contract.Message)
		}
	}
}

// lookupCounts counts the lookups of an environment or a database that reach the
// project store, whichever way a request got there.
type lookupCounts struct{ n int }

type lookupCountingProjectStore struct {
	datatug.ProjectStore
	counts *lookupCounts
}

func (s lookupCountingProjectStore) LoadEnvironment(ctx context.Context, id string, o ...datatug.StoreOption) (*datatug.Environment, error) {
	s.counts.n++
	return s.ProjectStore.LoadEnvironment(ctx, id, o...)
}

func (s lookupCountingProjectStore) LoadEnvDbCatalog(ctx context.Context, envID, serverID, dbID string, o ...datatug.StoreOption) (datatug.DbCatalog, error) {
	s.counts.n++
	return s.ProjectStore.LoadEnvDbCatalog(ctx, envID, serverID, dbID, o...)
}

func (s lookupCountingProjectStore) LoadEnvDbCatalogs(ctx context.Context, envID string, o ...datatug.StoreOption) (datatug.DbCatalogs, error) {
	s.counts.n++
	return s.ProjectStore.LoadEnvDbCatalogs(ctx, envID, o...)
}

type lookupCountingStore struct {
	storage.Store
	counts *lookupCounts
}

func (s lookupCountingStore) GetProjectStore(id string) datatug.ProjectStore {
	return lookupCountingProjectStore{ProjectStore: s.Store.GetProjectStore(id), counts: s.counts}
}

// countLookups wraps the store factory a test has wired, so every project store it
// hands out counts the lookups of an environment and of a database.
func countLookups(t *testing.T) *lookupCounts {
	t.Helper()
	counts := &lookupCounts{}
	inner := storage.NewDatatugStore
	storage.NewDatatugStore = func(id string) (storage.Store, error) {
		store, err := inner(id)
		if err != nil {
			return nil, err
		}
		return lookupCountingStore{Store: store, counts: counts}, nil
	}
	t.Cleanup(func() { storage.NewDatatugStore = inner })
	return counts
}

func postJSON(t *testing.T, handler http.HandlerFunc, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw)))
	return w
}

// An environment or a database that is not a plain name is a 400 that names the
// field and echoes nothing, and no lookup happens, on every route that takes one. A
// request with plain names runs the same code and reaches the store, which is what
// makes the count mean something.
func TestRoutes_RefuseAnUnsafeEnvironmentOrDatabaseBeforeAnyStoreLookup(t *testing.T) {
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	counts := countLookups(t)
	const plain = semanticTestEnv

	type route struct {
		name string
		// send builds and serves the request; database is "" for a route that has none.
		send          func(environment, database string) *httptest.ResponseRecorder
		hasDatabase   bool
		environField  string
		databaseField string
	}
	factFor := func(environment string) apicontract.RelatedRequest {
		fact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")
		return newRelatedRequest(apicontract.Scope{Project: scope.Project, Environment: environment, SecurityContextID: scope.SecurityContextID}, fact, nil)
	}
	scopeFor := func(environment string) apicontract.Scope {
		return apicontract.Scope{Project: scope.Project, Environment: environment, SecurityContextID: scope.SecurityContextID}
	}
	routes := []route{
		{name: "exec/select", hasDatabase: true, environField: "environment", databaseField: "database",
			send: func(environment, database string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				query := url.Values{"project": {scope.Project}, "environment": {environment}, "db": {database}, "sql": {"SELECT 1"}}
				executeSelectHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select?"+query.Encode(), nil))
				return w
			}},
		{name: "exec/select aliases", hasDatabase: true, environField: "environment", databaseField: "database",
			send: func(environment, database string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				query := url.Values{"proj": {scope.Project}, "env": {environment}, "db": {database}, "sql": {"SELECT 1"}}
				executeSelectHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select?"+query.Encode(), nil))
				return w
			}},
		{name: "exec/execute_commands", hasDatabase: true, environField: "env", databaseField: "db",
			send: func(environment, database string) *httptest.ResponseRecorder {
				body := api.ExecuteCommandsRequest{Commands: []api.ExecuteCommandRequest{{Type: "SQL", Text: "SELECT 1", Env: environment, DB: database}}}
				return postJSON(t, executeCommandsHandler, "/datatug/exec/execute_commands?project="+url.QueryEscape(scope.Project), body)
			}},
		{name: "exec/run_query", environField: "environment",
			send: func(environment, _ string) *httptest.ResponseRecorder {
				return postJSON(t, runQueryHandler, "/datatug/exec/run_query", apicontract.ExecutionRequest{
					Project: scope.Project, Environment: environment, SecurityContextID: scope.SecurityContextID,
					Source: semanticTestSource, DTQL: runQueryTestCustomerByIDDTQL,
					Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{},
					Mode: apicontract.ProvenanceModeLive,
				})
			}},
		{name: "semantic/columns", environField: "environment",
			send: func(environment, _ string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				query := url.Values{"project": {scope.Project}, "environment": {environment}, "securityContextId": {scope.SecurityContextID}, "source": {semanticTestSource}, "collection": {"Customer"}}
				semanticColumnsHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns?"+query.Encode(), nil))
				return w
			}},
		{name: "semantic/columns aliases", environField: "environment",
			send: func(environment, _ string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				query := url.Values{"proj": {scope.Project}, "env": {environment}, "securityContextId": {scope.SecurityContextID}, "source": {semanticTestSource}, "collection": {"Customer"}}
				semanticColumnsHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/semantic/columns?"+query.Encode(), nil))
				return w
			}},
		{name: "queries/applicable", environField: "environment",
			send: func(environment, _ string) *httptest.ResponseRecorder {
				fact := declaredFact("f1", "Customer", "ID", apicontract.NewIntegerValue("5"), semanticTestSource, "Customer", "CustomerId")
				return postJSON(t, semanticApplicableHandler, "/datatug/queries/applicable", newApplicableRequest(scopeFor(environment), []apicontract.Fact{fact}))
			}},
		{name: "semantic/related", environField: "environment",
			send: func(environment, _ string) *httptest.ResponseRecorder {
				return postJSON(t, semanticRelatedHandler, "/datatug/semantic/related", factFor(environment))
			}},
		{name: "semantic/related/rows", environField: "environment",
			send: func(environment, _ string) *httptest.ResponseRecorder {
				return postJSON(t, semanticRelatedRowsHandler, "/datatug/semantic/related/rows",
					newRelatedRowsRequest(scopeFor(environment), encodeLookupID(semanticTestSource, "Customer", "CustomerId"), apicontract.NewIntegerValue("5"), nil))
			}},
		{name: "queries/capture", environField: "environment",
			send: func(environment, _ string) *httptest.ResponseRecorder {
				return postCaptureBody(t, marshalCapture(t, validCaptureRequest(scopeFor(environment))))
			}},
	}

	for _, r := range routes {
		// The counter is live: a plain request runs the same route and reaches the store.
		counts.n = 0
		r.send(plain, "chinook-local")
		if counts.n == 0 {
			t.Errorf("%s: a request with plain names made no lookup, so the count below proves nothing", r.name)
		}
		counts.n = 0

		for _, c := range sourcecases.UnsafeIdentifiers() {
			positions := map[string]func() *httptest.ResponseRecorder{
				r.environField: func() *httptest.ResponseRecorder { return r.send(c.ID, "chinook-local") },
			}
			if r.hasDatabase {
				positions[r.databaseField] = func() *httptest.ResponseRecorder { return r.send(plain, c.ID) }
			}
			for field, send := range positions {
				w := send()
				body := w.Body.String()
				switch {
				case w.Code != http.StatusBadRequest:
					t.Errorf("%s: %s as the %s: status = %d, want 400: %s", r.name, c.Name, field, w.Code, body)
				case !strings.Contains(body, field):
					t.Errorf("%s: %s as the %s: the body does not name the field: %s", r.name, c.Name, field, body)
				case len(c.ID) >= 3 && strings.Contains(body, c.ID):
					t.Errorf("%s: %s as the %s: the body echoes the value: %s", r.name, c.Name, field, body)
				}
			}
		}
		if counts.n != 0 {
			t.Errorf("%s: an unsafe ID reached %d store lookups, want 0", r.name, counts.n)
		}
	}
}

// The facts side of a compare resolves a source in the side's environment before
// computeRunQuery's own scope check runs, so the side's environment is checked first:
// an environment that is not a plain name reaches neither the project directory, the
// store nor the source resolution.
func TestProveNativeFactsBinding_RefusesAnEnvironmentThatIsNotAPlainNameBeforeAnyLookup(t *testing.T) {
	projectDir, projectID := writeSemanticTestProject(t)
	configureSemanticSession(t, projectDir, projectID, "alice", []string{"admin"})
	counts := countLookups(t)
	resolved := 0
	previousResolve, previousStoreFor := resolveExecutionSourceHookCompare, compareFactsProjectStoreFor
	t.Cleanup(func() {
		resolveExecutionSourceHookCompare, compareFactsProjectStoreFor = previousResolve, previousStoreFor
	})
	resolveExecutionSourceHookCompare = func(ctx context.Context, store datatug.ProjectStore, dir string, req apicontract.ExecutionRequest, def *datatug.QueryDef) (api.ResolvedSource, error) {
		resolved++
		return previousResolve(ctx, store, dir, req, def)
	}
	storeAsked := 0
	compareFactsProjectStoreFor = func(project string) (datatug.ProjectStore, error) {
		storeAsked++
		return previousStoreFor(project)
	}
	prove := func(environment string) error {
		_, err := proveNativeFactsBinding(context.Background(), "customers/customer-invoices", apicontract.CompareSideSpec{
			Kind: apicontract.CompareSideFacts, StoreID: api.LocalStoreID, Project: projectID,
			Environment: environment, CohortRole: apicontract.CompareCohortAffected,
		}, "Customer", "CustomerId")
		return err
	}

	// The counters are live: a plain environment gets as far as the store.
	_ = prove(semanticTestEnv)
	if storeAsked == 0 {
		t.Fatal("a plain environment never reached the project store, so the counts below prove nothing")
	}
	storeAsked, counts.n = 0, 0

	for _, c := range sourcecases.UnsafeIdentifiers() {
		err := prove(c.ID)
		var contract *contractError
		if !errors.As(err, &contract) || contract.Code != apicontract.ErrCodeInvalidRequest || contract.Field != "environment" {
			t.Errorf("%s: proveNativeFactsBinding = %v, want INVALID_REQUEST on field environment", c.Name, err)
			continue
		}
		if len(c.ID) >= 3 && strings.Contains(contract.Message, c.ID) {
			t.Errorf("%s: the message echoes the value: %s", c.Name, contract.Message)
		}
	}
	if storeAsked != 0 || resolved != 0 || counts.n != 0 {
		t.Fatalf("an unsafe environment reached the store %d times, the source resolution %d times and %d lookups, want none", storeAsked, resolved, counts.n)
	}
}
