package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
)

func TestSessionContextHTTPPrivacyReplayAndRevocation(t *testing.T) {
	project, private := t.TempDir(), t.TempDir()
	envDir := filepath.Join(project, "environments", "uat")
	if err := os.MkdirAll(envDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "uat.env.json"), []byte(`{"id":"uat","dbServers":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"transport-test": project}
	original := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() {
		storage.NewDatatugStore = original
		_ = api.CloseSessionContext()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	setActor := func(id string) {
		api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: id}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, EvidencePrivateDir: private})
	}
	setActor("alice")
	router := httprouter.New()
	sessionContextRoutes("/datatug", guardedRouter{router: router, guard: NewRequestGuard("localhost", 0)}, nil, false, Capabilities{AllowWrites: true})
	request := func(method, suffix string, body []byte, want int) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://localhost/datatug/session-context"+suffix, bytes.NewReader(body))
		r.Header.Set("Origin", "http://localhost:4200")
		router.ServeHTTP(w, r)
		if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:4200" || w.Header().Get("Vary") != "Origin" {
			t.Fatalf("browser response lacks admitted CORS on status %d: %v", w.Code, w.Header())
		}
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, suffix, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private response is cacheable")
		}
		return w
	}
	w := request(http.MethodGet, "/capabilities?storage=local&project=transport-test", nil, http.StatusOK)
	var caps api.SessionContextCapabilities
	if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if len(caps.Environments) != 1 || caps.ActorID != "alice" {
		t.Fatalf("capabilities %+v", caps)
	}
	query := url.Values{"storage": {"local"}, "project": {"transport-test"}, "databaseId": {caps.DatabaseID}, "projectId": {caps.ProjectID}}
	request(http.MethodGet, "/operations/unknown?"+query.Encode(), nil, http.StatusNotFound)
	request(http.MethodGet, "/operations/unknown?"+query.Encode()+"&unsupported=1", nil, http.StatusBadRequest)
	request(http.MethodPost, "/query?"+query.Encode(), []byte(`{}`), http.StatusBadRequest)
	for _, origin := range []string{"https://foreign.example", "http://localhost:4200/path", "http://localhost:4200@foreign.example"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://localhost/datatug/session-context/operations/unknown?"+query.Encode(), nil)
		r.Header.Set("Origin", origin)
		router.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("foreign origin reflected: %q status=%d headers=%v", origin, w.Code, w.Header())
		}
	}

	command := sessioncontext.Command{OpID: "create", Action: "createSession", SessionID: "s1", BagID: "b1", DatabaseID: caps.DatabaseID, ProjectID: caps.ProjectID, Title: "private-sensitive-title", Scope: &sessioncontext.Scope{EnvironmentID: "uat", IdentityDomain: caps.Environments[0].IdentityDomain}}
	body, _ := json.Marshal(command)
	created := request(http.MethodPost, "/commands?"+query.Encode(), body, http.StatusOK)
	if strings.Contains(created.Body.String(), command.Title) {
		t.Fatal("receipt contains private title")
	}
	replayed := request(http.MethodPost, "/commands?"+query.Encode(), body, http.StatusOK)
	if created.Body.String() != replayed.Body.String() {
		t.Fatal("replay changed immutable receipt")
	}
	request(http.MethodGet, "/sessions/s1?"+query.Encode(), nil, http.StatusOK)
	request(http.MethodPost, "/commands?"+query.Encode(), []byte(`{"opId":"a","opId":"b"}`), http.StatusBadRequest)
	request(http.MethodPost, "/commands?"+query.Encode(), []byte(`{"actor":"admin"}`), http.StatusBadRequest)
	command.OpID = "forged-env"
	command.SessionID = "s2"
	command.BagID = "b2"
	command.Scope.IdentityDomain = "prod"
	body, _ = json.Marshal(command)
	request(http.MethodPost, "/commands?"+query.Encode(), body, http.StatusForbidden)
	setActor("bob")
	changed := request(http.MethodGet, "/capabilities?storage=local&project=transport-test", nil, http.StatusOK)
	if json.Unmarshal(changed.Body.Bytes(), &caps) != nil || caps.ActorID != "bob" {
		t.Fatal("capabilities retained former actor")
	}
	request(http.MethodGet, "/sessions/s1?"+query.Encode(), nil, http.StatusForbidden)
	setActor("alice")
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, nil, api.Capabilities{EvidencePrivateDir: private})
	request(http.MethodGet, "/operations/create?"+query.Encode(), nil, http.StatusForbidden)
}

func TestSessionContextQueryPinsExecutesAndNeverReplaysSource(t *testing.T) {
	projectDir, projectID := writeRunQueryTestProject(t)
	configureSemanticSessionWithCapabilities(t, projectDir, projectID, "alice", []string{"admin"}, api.Capabilities{AllowWrites: true, EvidencePrivateDir: t.TempDir()})
	configureExecutionEvidence(t, projectID, projectDir)
	t.Cleanup(func() { _ = api.CloseSessionContext() })
	ctx := context.Background()
	service, p, caps, err := api.ResolveSessionContext(ctx, "local", projectID)
	if err != nil {
		t.Fatal(err)
	}
	scope := sessioncontext.Scope{EnvironmentID: caps.Environments[0].EnvironmentID, IdentityDomain: caps.Environments[0].IdentityDomain}
	commands := []sessioncontext.Command{
		{OpID: "create", Action: "createSession", SessionID: "s", BagID: "b", DatabaseID: p.DatabaseID, ProjectID: p.ProjectID, Title: "test", Scope: &scope},
		{OpID: "put", Action: "putEntry", SessionID: "s", BagID: "b", ExpectedVersion: 1, ExpectedBagVersion: 1, EntryID: "e", RevisionID: "r", Subject: &sessioncontext.Subject{ConceptID: "customer", FieldID: "CustomerId", FieldMappingRef: "test-mapping"}, Value: &sessioncontext.ValueEnvelope{SchemaVersion: 1, Kind: "scalar", Scalar: typedIntegerPointer("5")}},
	}
	for _, command := range commands {
		if _, err = service.Execute(ctx, p, command); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := service.GetSnapshot(ctx, p, "s")
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err = service.Execute(ctx, p, sessioncontext.Command{OpID: "apply", Action: "applyContext", SessionID: "s", ExpectedVersion: snapshot.Session.Version, BagID: "b", ExpectedBagVersion: snapshot.Bags[0].Version, ExpectedContextSetID: snapshot.ContextSet.ID, Selection: []sessioncontext.EntryRef{{EntryID: "e", RevisionID: "r"}}, Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = service.GetSnapshot(ctx, p, "s")
	if err != nil {
		t.Fatal(err)
	}
	request := sessionQueryRequest{Command: sessioncontext.Command{OpID: "query", Action: "pinInput", Kind: "query", SessionID: "s", ExpectedVersion: snapshot.Session.Version, InputSnapshotID: "input", RunID: "run", Scope: &scope}, QueryID: "customers/customer-invoices", Bindings: []sessionQueryBinding{{EntryID: "e", ParameterID: "CustomerId"}}}
	body, _ := json.Marshal(request)
	query := url.Values{"storage": {"local"}, "project": {projectID}, "databaseId": {p.DatabaseID}, "projectId": {p.ProjectID}}
	perform := func(body []byte, want int) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		sessionContextQueryHandler(w, httptest.NewRequest(http.MethodPost, "http://localhost/datatug/session-context/query?"+query.Encode(), bytes.NewReader(body)))
		if w.Code != want {
			t.Fatalf("query status=%d %s", w.Code, w.Body.String())
		}
		return w
	}
	response := perform(body, http.StatusOK)
	var result struct {
		Run    sessioncontext.Run `json:"run"`
		Result apicontract.Result `json:"result"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Run.State != "completed" || result.Run.OutcomeOrigin != "server-executor" || len(result.Result.Recordset.Rows) != 1 {
		t.Fatalf("actual executor result %+v", result)
	}
	if result.Result.Execution == nil {
		t.Fatal("query lacks native execution receipt")
	}
	pinnedInput, _, inputErr := service.ReadInput(ctx, p, "input")
	if inputErr != nil || pinnedInput.ParametersOrigin != "server-executor" || pinnedInput.ResolvedParameters["CustomerId"].Str != "5" || !strings.Contains(string(pinnedInput.DefinitionDocument), "CustomerId") {
		t.Fatalf("native execution input snapshot %+v %v", pinnedInput, inputErr)
	}

	// Removing the source proves an accepted retry and reopen cannot dispatch it again.
	if err = os.RemoveAll(filepath.Join(projectDir, "queries")); err != nil {
		t.Fatal(err)
	}
	replay := perform(body, http.StatusOK)
	if !strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatal("retry did not resolve the accepted operation")
	}
	if _, err = service.GetSnapshot(ctx, p, "s"); err != nil {
		t.Fatal(err)
	}
	request.Bindings[0].ParameterID = "forged"
	changed, _ := json.Marshal(request)
	perform(changed, http.StatusConflict)
}

func typedIntegerPointer(value string) *apicontract.TypedValue {
	typed := apicontract.NewIntegerValue(value)
	return &typed
}
