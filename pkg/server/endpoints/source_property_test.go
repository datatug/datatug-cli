package endpoints

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// TestProperty_ServeNeverEchoesASourceSecret is the DT-0C acceptance property at
// the serve layer. A source string reaches a response in three ways: a source URL
// the server opens (resolveSQLSourceURL builds the error), a source a client sent
// where an ID belongs (the unknown-source and unauthorized-target messages), and
// an environment or a database a legacy route takes (exec/select and
// exec/execute_commands, which TestProperty_LegacyRoutesNeverEchoASourceSecret
// holds). For every generated source string no secret of four or more characters
// is in the error text, in the HTTP body, in stderr or in the log.
//
// The client-sent source is read through every message that echoes one: the
// unknown-source message (ad-hoc DTQL), the not-an-authorized-target message (a
// saved query, with the eligible targets stubbed so that branch is reached) and
// the not-a-catalog-source message of a query capture (checkCaptureSource).
//
// The text is read before it reaches any sink (the error's own Error() and the
// contract error's Message field), and again in the response, so a redactor in
// the sink cannot be what keeps it out.
func TestProperty_ServeNeverEchoesASourceSecret(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	logged := captureAgentLog(t)

	// A saved query with two eligible targets: a source that is neither is "not an
	// authorized target". A capture store with no catalogs: nothing is a catalog source.
	origEligible, origStore := eligibleTargetsHook, captureProjectStoreFor
	t.Cleanup(func() { eligibleTargetsHook, captureProjectStoreFor = origEligible, origStore })
	eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
		return []api.ResolvedSource{{ID: "chinook", Label: "Chinook"}, {ID: "orders", Label: "Orders"}}, nil
	}
	captureProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return mockStoreWithCatalogs{err: errors.New("no catalogs")}, nil
	}
	cases := sourcecases.CommandCases()
	failed := 0
	for _, c := range cases {
		var texts []string
		note := func(err error) {
			if err != nil {
				texts = append(texts, err.Error())
			}
		}
		respond := func(err error) {
			w := httptest.NewRecorder()
			stderr := captureStderr(t, func() { handleError(err, w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select", nil)) })
			texts = append(texts, w.Body.String(), stderr)
		}

		// A source URL the server opens.
		_, err := resolveSQLSourceURL(ctx, "src", c.Source, "customers")
		note(err)
		respond(err)

		// A source a client sent as an ID, through the resolver and the contract.
		_, err = apiResolveSource(ctx, nil, projectDir, "", c.Source)
		note(err)
		// Each request reaches a different message: the ad-hoc DTQL one says the
		// source is unknown, the saved-query one that it is not an authorized target.
		for _, request := range []struct {
			name       string
			req        apicontract.ExecutionRequest
			wantPhrase string
		}{
			{"ad-hoc DTQL", apicontract.ExecutionRequest{DTQL: "from: {name: t}", Source: c.Source}, "unknown source"},
			{"saved query", apicontract.ExecutionRequest{QueryID: "q", Source: c.Source}, "is not an authorized target"},
		} {
			_, err = resolveExecutionSource(ctx, nil, projectDir, request.req, &datatug.QueryDef{ID: "q"})
			var contract *contractError
			if !errors.As(err, &contract) {
				t.Fatalf("%s: resolveExecutionSource (%s) returned %v, want a contract error", c.Name, request.name, err)
			}
			if !strings.Contains(contract.Message, request.wantPhrase) {
				t.Fatalf("%s: the %s message %q does not say %q: the property does not reach the message it is meant to read", c.Name, request.name, contract.Message, request.wantPhrase)
			}
			texts = append(texts, contract.Message) // before the sink that redacts it
			w := httptest.NewRecorder()
			writeContractResponse(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", nil), contract, nil)
			texts = append(texts, w.Body.String())
		}

		// A source a client sent to capture a query.
		err = checkCaptureSource(ctx, "demo", projectDir, "local", c.Source)
		var captureErr *contractError
		if !errors.As(err, &captureErr) || !strings.Contains(captureErr.Message, "is not a catalog source") {
			t.Fatalf("%s: checkCaptureSource returned %v, want the not-a-catalog-source contract error", c.Name, err)
		}
		texts = append(texts, captureErr.Message)

		// The environment is a field a client fills with whatever it likes, too: the
		// same requests with a source string as the environment and a plain source.
		// The capture message names the environment, whatever the failure.
		err = checkCaptureSource(ctx, "demo", projectDir, c.Source, "chinook")
		if !errors.As(err, &captureErr) || !strings.Contains(captureErr.Message, "is not a catalog source of environment") {
			t.Fatalf("%s: checkCaptureSource with a bad environment returned %v, want the not-a-catalog-source contract error", c.Name, err)
		}
		texts = append(texts, captureErr.Message)
		// A saved query that has no eligible source in the environment, and the
		// resolver's own failure to list the environment's catalogs.
		eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
			return nil, nil
		}
		catalogStore := mockStoreWithCatalogs{err: errors.New("no catalogs")}
		for _, request := range []struct {
			name       string
			req        apicontract.ExecutionRequest
			wantPhrase string
		}{
			{"saved query with no eligible source", apicontract.ExecutionRequest{QueryID: "q", Environment: c.Source}, "has no eligible source in environment"},
			{"ad-hoc DTQL", apicontract.ExecutionRequest{DTQL: "from: {name: t}", Source: "chinook", Environment: c.Source}, "list catalogs for environment"},
		} {
			_, err = resolveExecutionSource(ctx, catalogStore, projectDir, request.req, &datatug.QueryDef{ID: "q"})
			var contract *contractError
			if !errors.As(err, &contract) || !strings.Contains(contract.Message, request.wantPhrase) {
				t.Fatalf("%s: resolveExecutionSource (%s, bad environment) returned %v, want a contract error that says %q: the property does not reach the message it is meant to read", c.Name, request.name, err, request.wantPhrase)
			}
			texts = append(texts, contract.Message) // before the sink that redacts it
			w := httptest.NewRecorder()
			writeContractResponse(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", nil), contract, nil)
			texts = append(texts, w.Body.String())
		}
		eligibleTargetsHook = func(context.Context, datatug.ProjectStore, string, string, *datatug.QueryDef) ([]api.ResolvedSource, error) {
			return []api.ResolvedSource{{ID: "chinook", Label: "Chinook"}, {ID: "orders", Label: "Orders"}}, nil
		}
		texts = append(texts, logged.String())
		logged.Reset()

		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d generated sources leaked a secret through a serve response", failed, len(cases))
	}
}

// legacyRouteStore is the project store behind exec/select and
// exec/execute_commands: one environment with one DB server, and a catalog
// loader that fails the way the file store's does, naming what it could not
// load and the ID it was given. It counts its lookups: a request that is refused
// at the route's entry makes none.
type legacyRouteStore struct {
	datatug.ProjectStore
	lookups *int
}

func (s legacyRouteStore) LoadEnvironment(_ context.Context, id string, _ ...datatug.StoreOption) (*datatug.Environment, error) {
	*s.lookups++
	if id != "local" {
		return nil, fmt.Errorf("failed to load environment[%s] from project: %w", id, os.ErrNotExist)
	}
	return &datatug.Environment{DbServers: []*datatug.EnvDbServer{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}, nil
}

func (s legacyRouteStore) LoadEnvDbCatalog(_ context.Context, env, _, id string, _ ...datatug.StoreOption) (datatug.DbCatalog, error) {
	*s.lookups++
	return datatug.DbCatalog{}, fmt.Errorf("failed to load env db catalog[%s/%s] from project: %w", env, id, os.ErrNotExist)
}

type legacyRouteStoreFactory struct {
	storage.Store
	lookups *int
}

func (f legacyRouteStoreFactory) GetProjectStore(string) datatug.ProjectStore {
	return legacyRouteStore{lookups: f.lookups}
}

// The legacy routes (exec/select, exec/execute_commands) take an environment and
// a database in the request, and a client can send a source string for either. A
// source string is not a plain name, so each route refuses it at its entry, before
// any lookup, with a message that names the field and the rule. For every generated
// source string no secret of four or more characters is in that error (read before
// any sink), in what handleError writes to the response body, or on stderr. The
// messages of the lookups themselves, which quote the path the value was turned
// into, are held where they are built (pkg/api's
// TestProperty_ResolveSourceURLNeverEchoesASourceString).
func TestProperty_LegacyRoutesNeverEchoASourceSecret(t *testing.T) {
	ctx := context.Background()
	origStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = origStore })
	lookups := 0
	storage.NewDatatugStore = func(string) (storage.Store, error) { return legacyRouteStoreFactory{lookups: &lookups}, nil }
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{"demo": t.TempDir()}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	logged := captureAgentLog(t)

	// The counter is live: plain names reach the store.
	if _, err := api.ExecuteSelect(ctx, "files", api.SelectRequest{Project: "demo", Environment: "local", Database: "chinook", SQL: "SELECT 1"}); err == nil || lookups == 0 {
		t.Fatalf("plain names: err = %v, lookups = %d, want a lookup that finds nothing", err, lookups)
	}
	lookups = 0

	cases := sourcecases.CommandCases()
	failed := 0
	for _, c := range cases {
		if dbcopy.IsPlainSourceID(c.Source) {
			t.Fatalf("%s: %q is a plain name, and the route would look it up: the property does not reach the refusal it is meant to read", c.Name, c.Source)
		}
		var texts []string
		for _, request := range []struct {
			name              string
			environment, base string
			selectField       string
			commandField      string
		}{
			{"database", "local", c.Source, "database", "db"},
			{"environment", c.Source, "chinook", "environment", "env"},
		} {
			_, errSelect := api.ExecuteSelect(ctx, "files", api.SelectRequest{Project: "demo", Environment: request.environment, Database: request.base, SQL: "SELECT 1"})
			_, errCommands := api.ExecuteCommands(ctx, "files", api.ExecuteCommandsRequest{Project: "demo", Commands: []api.ExecuteCommandRequest{
				{Type: "SQL", Text: "SELECT 1", Env: request.environment, DB: request.base},
			}})
			for route, got := range map[string]struct {
				err   error
				field string
			}{"exec/select": {errSelect, request.selectField}, "exec/execute_commands": {errCommands, request.commandField}} {
				if got.err == nil || !validation.IsBadRequestError(got.err) || !strings.Contains(got.err.Error(), "["+got.field+"]") || !strings.Contains(got.err.Error(), "must be a plain name") {
					t.Fatalf("%s: %s with a bad %s returned %v, want a bad-request refusal that names [%s] and the plain-name rule", c.Name, route, request.name, got.err, got.field)
				}
				texts = append(texts, got.err.Error()) // before the sink that redacts it
				w := httptest.NewRecorder()
				stderr := captureStderr(t, func() { handleError(got.err, w, httptest.NewRequest(http.MethodGet, "/datatug/"+route, nil)) })
				if w.Code != http.StatusBadRequest {
					t.Fatalf("%s: %s with a bad %s answered %d, want 400", c.Name, route, request.name, w.Code)
				}
				texts = append(texts, w.Body.String(), stderr)
			}
		}
		texts = append(texts, logged.String())
		logged.Reset()
		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
			}
		}
	}
	if lookups != 0 {
		t.Errorf("a source string reached %d store lookups through a legacy route, want 0", lookups)
	}
	if failed > 0 {
		t.Errorf("%d of %d generated sources leaked through a legacy route", failed, len(cases))
	}
}

// A project or a saved-query ID a client sent to exec/run_query can be a whole source
// string. The not-found messages quote either only when it is a plain name (a query
// ID is folders and a name, each a plain name), read before any sink, and again in the
// response.
func TestProperty_RunQueryNeverEchoesAProjectOrQueryIDThatIsASourceString(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "queries"), 0o755); err != nil {
		t.Fatal(err)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{"demo": projectDir}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	logged := captureAgentLog(t)
	origStoreFor := execRunProjectStoreFor
	t.Cleanup(func() { execRunProjectStoreFor = origStoreFor })
	execRunProjectStoreFor = func(string) (datatug.ProjectStore, error) {
		return mockStoreWithCatalogs{err: errors.New("no catalogs")}, nil
	}
	request := func(project, queryID string) apicontract.ExecutionRequest {
		req := apicontract.ExecutionRequest{
			Project: project, Environment: "local", SecurityContextID: api.SecurityContextID(),
			Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{},
			Mode: apicontract.ProvenanceModeLive,
		}
		if queryID != "" {
			req.QueryID = queryID
		} else {
			req.Source, req.DTQL = "chinook", runQueryTestCustomerByIDDTQL
		}
		return req
	}

	cases := sourcecases.CommandCases()
	failed := 0
	for _, c := range cases {
		var texts []string
		for _, call := range []struct {
			name       string
			req        apicontract.ExecutionRequest
			wantPhrase string
		}{
			{"project", request(c.Source, ""), "unknown project"},
			{"saved query", request("demo", c.Source), "not found"},
		} {
			_, err := computeRunQuery(ctx, call.req)
			var contract *contractError
			if !errors.As(err, &contract) || !strings.Contains(contract.Message, call.wantPhrase) {
				t.Fatalf("%s: a bad %s returned %v, want a contract error that says %q: the property does not reach the message it is meant to read", c.Name, call.name, err, call.wantPhrase)
			}
			texts = append(texts, contract.Message) // before the sink that redacts it
			w := httptest.NewRecorder()
			writeContractResponse(w, httptest.NewRequest(http.MethodPost, "/datatug/exec/run_query", nil), contract, nil)
			texts = append(texts, w.Body.String())
		}
		texts = append(texts, logged.String())
		logged.Reset()
		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d generated sources leaked through a project or query ID", failed, len(cases))
	}

	// A plain name is still named, so the message stays useful.
	_, err = computeRunQuery(ctx, request("demo", "no-such-query"))
	if contract := new(contractError); !errors.As(err, &contract) || !strings.Contains(contract.Message, `query "no-such-query" not found`) {
		t.Fatalf("a plain query ID should be named: %v", err)
	}
	_, err = computeRunQuery(ctx, request("no-such-project", ""))
	if contract := new(contractError); !errors.As(err, &contract) || !strings.Contains(contract.Message, `unknown project "no-such-project"`) {
		t.Fatalf("a plain project ID should be named: %v", err)
	}
	_, err = computeRunQuery(ctx, request("demo", "folder/no-such-query"))
	if contract := new(contractError); !errors.As(err, &contract) || !strings.Contains(contract.Message, `query "folder/no-such-query" not found`) {
		t.Fatalf("a folder-qualified query ID of plain names should be named: %v", err)
	}
}
