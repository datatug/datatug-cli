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
	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/detective"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	core "github.com/datatug/datatug-core/pkg/detective"
	"github.com/datatug/datatug-core/pkg/sessioncontext"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
)

func TestSessionDetectiveHTTPDurablePinRetryAndAcceptedPrivacy(t *testing.T) {
	project, private := t.TempDir(), t.TempDir()
	envDir := filepath.Join(project, "environments", "uat")
	if err := os.MkdirAll(envDir, 0700); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(envDir, "uat.env.json")
	if err := os.WriteFile(envPath, []byte(`{"id":"uat","dbServers":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "queries"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "queries", "q.query.json"), []byte(`{"id":"q","title":"Query","type":"SQL","text":"select old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "queries", "q.query.sql"), []byte("select old"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"bridge-test": project}
	oldFactory := storage.NewDatatugStore
	oldRunner := detectiveComputeRunQueryWithOptions
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", paths) }
	t.Cleanup(func() {
		storage.NewDatatugStore = oldFactory
		detectiveComputeRunQueryWithOptions = oldRunner
		_ = api.CloseSessionContext()
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	setActor := func(actor string) {
		api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: actor}, Unrestricted: true}, paths, api.Capabilities{AllowWrites: true, NativeSessionDetective: true, EvidencePrivateDir: private})
	}
	setActor("alice")
	ctx := context.Background()
	service, p, caps, err := api.ResolveSessionContext(ctx, "local", "bridge-test")
	if err != nil {
		t.Fatal(err)
	}
	scope := sessioncontext.Scope{EnvironmentID: caps.Environments[0].EnvironmentID, IdentityDomain: caps.Environments[0].IdentityDomain}
	created, err := service.Execute(ctx, p, sessioncontext.Command{OpID: "create", Action: "createSession", SessionID: "session", BagID: "bag", DatabaseID: p.DatabaseID, ProjectID: p.ProjectID, Title: "Private session label", Scope: &scope})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.GetSnapshot(ctx, p, "session")
	if err != nil {
		t.Fatal(err)
	}
	local, err := detective.NewLocalStore("bridge-test", project, private)
	if err != nil {
		t.Fatal(err)
	}
	check, err := local.SaveCheck(ctx, detective.Check{ID: "check", Title: "Private Check label", Steps: []detective.Step{{ID: "load", Kind: "query", QueryID: "q"}}}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = local.SaveCase(ctx, detective.Case{ID: "case", Title: "Private Case label"}, "alice", "create-case"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	detectiveComputeRunQueryWithOptions = func(_ context.Context, _ apicontract.ExecutionRequest, options runQueryOptions) (apicontract.Result, error) {
		calls++
		if options.acceptedQuery == nil || options.acceptedQuery.Text != "select old" || options.expectedQueryRevision == "" {
			t.Fatal("source did not receive exact accepted query")
		}
		return apicontract.Result{}, nil
	}
	router := httprouter.New()
	sessionContextRoutes("/datatug", guardedRouter{router: router, guard: NewRequestGuard("localhost", 0)}, nil, false, Capabilities{AllowWrites: true})
	q := url.Values{"storage": {"local"}, "project": {"bridge-test"}, "databaseId": {p.DatabaseID}, "projectId": {p.ProjectID}, "namespaceId": {caps.OperationScope.NamespaceID}}
	request := api.SessionDetectiveRequest{OperationID: "operation", SessionID: "session", ExpectedVersion: created.Receipt.SessionVersion, ExpectedContextSetID: snapshot.ContextSet.ID, CheckID: "check", CaseID: "case", Scope: scope}
	send := func(method, path, origin string, body any, want int) *httptest.ResponseRecorder {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "http://localhost/datatug/session-context"+path+"?"+q.Encode(), bytes.NewReader(data))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}

	origin := "http://datatug.localhost:4200"
	for _, fault := range []struct {
		name   string
		change func(*api.SessionDetectiveRequest)
		status int
	}{
		{"missing session", func(r *api.SessionDetectiveRequest) { r.SessionID = "missing" }, http.StatusNotFound},
		{"stale version", func(r *api.SessionDetectiveRequest) { r.ExpectedVersion++ }, http.StatusConflict},
		{"stale context", func(r *api.SessionDetectiveRequest) { r.ExpectedContextSetID = "other" }, http.StatusConflict},
		{"missing check", func(r *api.SessionDetectiveRequest) { r.CheckID = "missing" }, http.StatusNotFound},
		{"missing revision", func(r *api.SessionDetectiveRequest) {
			r.Bindings = []api.SessionDetectiveInputSelection{{InputName: "id", EntryID: "missing", RevisionID: "missing"}}
		}, http.StatusNotFound},
		{"manual binding collision", func(r *api.SessionDetectiveRequest) {
			r.Bindings = []api.SessionDetectiveInputSelection{{InputName: "id", EntryID: "missing", RevisionID: "missing"}}
			r.Inputs = map[string]core.InputValue{"id": {Scalar: typedIntegerPointer("5")}}
		}, http.StatusBadRequest},
		{"capacity", func(r *api.SessionDetectiveRequest) {
			r.Bindings = make([]api.SessionDetectiveInputSelection, apicontract.MaxSessionInputBindings+1)
		}, http.StatusBadRequest},
	} {
		t.Run(fault.name, func(t *testing.T) {
			bad := request
			bad.OperationID = "invalid-" + strings.ReplaceAll(fault.name, " ", "-")
			fault.change(&bad)
			send(http.MethodPost, "/detective", origin, bad, fault.status)
			if calls != 0 {
				t.Fatal("invalid request dispatched")
			}
		})
	}
	send(http.MethodPost, "/detective", origin, map[string]any{"operationId": "unknown-fields", "grant": true}, http.StatusBadRequest)
	send(http.MethodPost, "/detective/reconcile", origin, map[string]any{"operationId": "unknown-fields", "grant": true}, http.StatusBadRequest)
	send(http.MethodGet, "/detective/operations/missing", origin, nil, http.StatusNotFound)
	p.Executor = true
	if _, prepareErr := prepareSessionDetectivePin(ctx, service, p, local, sessioncontext.NativeNamespace{StorageID: "local", DatabaseID: p.DatabaseID, ProjectID: p.ProjectID, NamespaceID: caps.OperationScope.NamespaceID}, request, strings.Repeat("a", 64)); prepareErr != nil {
		t.Fatalf("prepare failed: %v", prepareErr)
	}
	for _, fixture := range []struct {
		path, prefix string
		limit        int
	}{{"/detective", `{}`, 128 << 10}, {"/detective/reconcile", `{"operationId":"operation"}`, 4 << 10}} {
		raw := fixture.prefix + strings.Repeat(" ", fixture.limit) + `{"hidden":true}`
		r := httptest.NewRequest(http.MethodPost, "http://localhost/datatug/session-context"+fixture.path+"?"+q.Encode(), strings.NewReader(raw))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusRequestEntityTooLarge || calls != 0 {
			t.Fatal("oversized valid prefix reached dispatch", w.Code, calls)
		}
	}
	response := send(http.MethodPost, "/detective", origin, request, http.StatusOK)
	if calls != 1 || strings.Contains(response.Body.String(), "Private") || strings.Contains(response.Body.String(), "select old") {
		t.Fatal("execution/status privacy", calls, response.Body.String())
	}
	var accepted api.SessionDetectiveResponse
	if json.Unmarshal(response.Body.Bytes(), &accepted) != nil || accepted.OperationID != request.OperationID {
		t.Fatal("invalid accepted response")
	}
	check.Title = "Edited Check"
	if _, err = local.SaveCheck(ctx, check, "alice"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(project, "queries", "q.query.json"), []byte(`{"id":"q","title":"Edited","type":"SQL","text":"select changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	send(http.MethodPost, "/detective", origin, request, http.StatusOK)
	if calls != 1 {
		t.Fatal("exact retry reevaluated changed definitions")
	}
	changed := request
	changed.CheckID = "other"
	send(http.MethodPost, "/detective", origin, changed, http.StatusConflict)

	// A second operation loses source content after its source result is produced.
	// Reconcile and retry return accepted metadata without dispatch or Case writes.
	pending := request
	pending.OperationID = "pending-operation"
	pending.ExpectedVersion = 0
	current, snapshotErr := service.GetSnapshot(ctx, p, "session")
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	pending.ExpectedVersion = current.Session.Version
	pending.ExpectedContextSetID = current.ContextSet.ID
	checkCurrent, checkErr := local.GetCheck(ctx, "check")
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	checkCurrent.Title = "Accepted new Check"
	if _, err = local.SaveCheck(ctx, checkCurrent, "alice"); err != nil {
		t.Fatal(err)
	}
	detectiveComputeRunQueryWithOptions = func(context.Context, apicontract.ExecutionRequest, runQueryOptions) (apicontract.Result, error) {
		calls++
		if err := os.Remove(envPath); err != nil {
			t.Fatal(err)
		}
		return apicontract.Result{}, nil
	}
	send(http.MethodPost, "/detective", origin, pending, http.StatusOK)
	send(http.MethodPost, "/detective/reconcile", origin, map[string]string{"operationId": pending.OperationID}, http.StatusOK)
	send(http.MethodPost, "/detective", origin, pending, http.StatusOK)
	if calls != 2 {
		t.Fatal("revoked accepted recovery dispatched", calls)
	}
	if err = os.WriteFile(envPath, []byte(`{"id":"uat","dbServers":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	current, snapshotErr = service.GetSnapshot(ctx, p, "session")
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if _, err = service.Execute(ctx, p, sessioncontext.Command{OpID: "stop", Action: "transitionSession", Transition: "stop", SessionID: "session", ExpectedVersion: current.Session.Version}); err != nil {
		t.Fatal(err)
	}
	send(http.MethodPost, "/detective/reconcile", origin, map[string]string{"operationId": pending.OperationID}, http.StatusOK)
	if calls != 2 {
		t.Fatal("stopped reconcile dispatched", calls)
	}
	if err = os.Remove(envPath); err != nil {
		t.Fatal(err)
	}
	send(http.MethodGet, "/detective/operations/operation", origin, nil, http.StatusOK)
	send(http.MethodGet, "/detective/operations/operation", "https://foreign.example", nil, http.StatusForbidden)
	setActor("bob")
	send(http.MethodGet, "/detective/operations/operation", origin, nil, http.StatusNotFound)
	if calls != 2 {
		t.Fatal("accepted recovery dispatched source")
	}
	setActor("alice")
	originalNamespace := q.Get("namespaceId")
	q.Set("namespaceId", "foreign")
	send(http.MethodGet, "/detective/operations/operation", origin, nil, http.StatusForbidden)
	q.Set("namespaceId", originalNamespace)
	protected := httprouter.New()
	secret := bytes.Repeat([]byte{17}, 32)
	guardedCaps := Capabilities{AllowWrites: true, ServedHost: "localhost", Access: agentaccess.New(secret)}
	sessionContextRoutes("/datatug", guardedRouter{router: protected, guard: guardedCaps.RequestGuard()}, nil, false, guardedCaps)
	for _, token := range []string{"", agentaccess.SessionToken(secret)} {
		r := httptest.NewRequest(http.MethodGet, "http://localhost/datatug/session-context/detective/operations/operation?"+q.Encode(), nil)
		r.Header.Set("Origin", origin)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if token != "" {
			want = http.StatusOK
		}
		if w.Code != want {
			t.Fatal("accepted status bypassed caller token boundary", w.Code)
		}
	}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}, Unrestricted: true}, paths, api.Capabilities{NativeSessionDetective: true, EvidencePrivateDir: private})
	send(http.MethodGet, "/detective/operations/operation", origin, nil, http.StatusForbidden)

}
