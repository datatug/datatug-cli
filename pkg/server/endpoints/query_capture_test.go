package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// captureTestDTQL reads Invoice rows for one customer - the ad-hoc lookup a
// user would capture from the Customer page.
const captureTestDTQL = "from:\n  name: Invoice\ncolumns:\n  - field: InvoiceId\n  - field: Total\n" +
	"where:\n  op: ==\n  left:\n    field: CustomerId\n  right:\n    param: Customer.ID\n"

// fakeCaptureStore is an in-memory queryCaptureStore with the revisioned
// store's create/update semantics, recording every call.
type fakeCaptureStore struct {
	records map[string]storedCapture
	puts    []fakeCapturePut
	err     error
	onPut   func()
	next    int
}

type fakeCapturePut struct {
	record    capturedRecord
	condition captureWriteCondition
}

func newFakeCaptureStore() *fakeCaptureStore {
	return &fakeCaptureStore{records: map[string]storedCapture{}}
}

func (f *fakeCaptureStore) PutQuery(_ context.Context, record capturedRecord, condition captureWriteCondition) (storedCapture, error) {
	f.puts = append(f.puts, fakeCapturePut{record: record, condition: condition})
	if f.onPut != nil {
		f.onPut()
	}
	if f.err != nil {
		return storedCapture{}, f.err
	}
	key := captureQueryID(record.Query.FolderPath, record.Query.ID)
	current, exists := f.records[key]
	switch {
	case condition.IfNoneMatch && exists:
		return storedCapture{}, &captureStoreError{Kind: captureErrConflict, Reason: "a query already exists at this location"}
	case !condition.IfNoneMatch && (!exists || current.Revision != condition.IfMatch):
		return storedCapture{}, &captureStoreError{Kind: captureErrConflict, Reason: "stale or missing revision"}
	}
	f.next++
	stored := storedCapture{Record: record, Revision: fmt.Sprintf("rev-%d", f.next)}
	f.records[key] = stored
	return stored, nil
}

// captureTestSetup serves a fresh copy of the semantic test project
// (environment "local", catalog source "chinook", policies granting admin
// read/write on everything and support customer reads only) as principal
// as/roles, with --allow-writes set as given, and installs a fake capture
// store.
func captureTestSetup(t *testing.T, as string, roles []string, allowWrites bool) (apicontract.Scope, *fakeCaptureStore) {
	t.Helper()
	return captureTestSetupWithPolicies(t, as, roles, allowWrites, nil)
}

// captureTestSetupWithPolicies is captureTestSetup with extra policy
// documents, keyed by file name, loaded next to the project's own: every
// loaded policy must allow a write.
func captureTestSetupWithPolicies(t *testing.T, as string, roles []string, allowWrites bool, extra map[string]string) (apicontract.Scope, *fakeCaptureStore) {
	t.Helper()
	projectDir, projectID := writeSemanticTestProject(t)
	for name, doc := range extra {
		mustWriteFile(t, filepath.Join(projectDir, "policies", name), doc)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: as, Roles: roles, PoliciesDir: projectDir + "/policies"})
	if err != nil {
		t.Fatalf("secureread.NewSession: %v", err)
	}
	pathsByID := map[string]string{projectID: projectDir}
	api.ConfigureSecureSession(session, pathsByID, api.Capabilities{AllowWrites: allowWrites})
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", pathsByID)
	}
	fake := newFakeCaptureStore()
	saved := captureStoreFor
	captureStoreFor = func(string) (queryCaptureStore, error) { return fake, nil }
	t.Cleanup(func() {
		captureStoreFor = saved
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	return apicontract.Scope{Project: projectID, Environment: semanticTestEnv, SecurityContextID: api.SecurityContextID()}, fake
}

func validCaptureRequest(scope apicontract.Scope) captureQueryRequest {
	return captureQueryRequest{
		Project: scope.Project, Environment: scope.Environment, SecurityContextID: scope.SecurityContextID,
		IfNoneMatch: true,
		Query: capturedQuery{
			FolderPath: "customers",
			ID:         "customer-invoices-captured",
			Title:      "Customer invoices",
			Purpose:    "Which invoices does this customer have?",
			Source:     semanticTestSource,
			DTQL:       captureTestDTQL,
			Parameters: []capturedParameter{{
				ID: "Customer.ID", Type: "integer", Title: "Customer", IsRequired: true,
				Meta: &entityFieldRef{Entity: "Customer", Field: "ID"},
			}},
			BindingOrigins: []captureBindingOrigin{{ParameterID: "Customer.ID", Origin: "selection"}},
		},
	}
}

func postCaptureBody(t *testing.T, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/datatug/queries/capture", bytes.NewReader(body))
	w := httptest.NewRecorder()
	captureQueryHandler(w, r)
	return w
}

func marshalCapture(t *testing.T, req captureQueryRequest) []byte {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func postCapture(t *testing.T, req captureQueryRequest) *httptest.ResponseRecorder {
	t.Helper()
	return postCaptureBody(t, marshalCapture(t, req))
}

func decodeCaptureResponse(t *testing.T, w *httptest.ResponseRecorder) captureQueryResponse {
	t.Helper()
	var resp captureQueryResponse
	if err := decodeContractBody(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not a capture response: %v\n%s", err, w.Body.String())
	}
	return resp
}

func decodeCaptureError(t *testing.T, w *httptest.ResponseRecorder) apicontract.ErrorEnvelope {
	t.Helper()
	var env apicontract.ErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an error envelope: %v\n%s", err, w.Body.String())
	}
	if env.Error.Message == "" || env.Error.RequestID == "" {
		t.Errorf("error envelope lacks a message or requestId: %s", w.Body.String())
	}
	return env
}

// assertCaptureError checks status, code and field, and that the store was
// never called.
func assertCaptureError(t *testing.T, w *httptest.ResponseRecorder, fake *fakeCaptureStore, status int, code apicontract.ErrorCode, field string) apicontract.ErrorEnvelope {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d: %s", w.Code, status, w.Body.String())
	}
	env := decodeCaptureError(t, w)
	if env.Error.Code != string(code) {
		t.Errorf("code = %q, want %q: %s", env.Error.Code, code, w.Body.String())
	}
	if env.Error.Field != field {
		t.Errorf("field = %q, want %q: %s", env.Error.Field, field, w.Body.String())
	}
	if fake != nil && len(fake.puts) != 0 {
		t.Errorf("the store was called %d time(s); a refused capture must write nothing", len(fake.puts))
	}
	return env
}

func TestCaptureQuery_CreateAuthorized(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	req := validCaptureRequest(scope)
	w := postCapture(t, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	resp := decodeCaptureResponse(t, w)
	if resp.QueryID != "customers/customer-invoices-captured" {
		t.Errorf("queryId = %q", resp.QueryID)
	}
	if resp.Revision != "rev-1" {
		t.Errorf("revision = %q, want the store's rev-1", resp.Revision)
	}
	if !reflect.DeepEqual(resp.Query, req.Query) {
		t.Errorf("query = %+v, want %+v", resp.Query, req.Query)
	}
	wantProvenance := captureProvenance{Author: "admin", Environment: semanticTestEnv, Collection: "Invoice"}
	if resp.Provenance != wantProvenance {
		t.Errorf("provenance = %+v, want %+v", resp.Provenance, wantProvenance)
	}
	if len(fake.puts) != 1 {
		t.Fatalf("store calls = %d, want 1", len(fake.puts))
	}
	put := fake.puts[0]
	if put.condition != (captureWriteCondition{IfNoneMatch: true}) {
		t.Errorf("condition = %+v, want create-only", put.condition)
	}
	if put.record.Author != "admin" || put.record.Environment != semanticTestEnv || put.record.Collection != "Invoice" {
		t.Errorf("record provenance = %+v", put.record)
	}
}

func TestCaptureQuery_UpdateWithTheCurrentRevision(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	created := decodeCaptureResponse(t, postCapture(t, validCaptureRequest(scope)))

	update := validCaptureRequest(scope)
	update.IfNoneMatch, update.IfMatch = false, created.Revision
	update.Query.Title = "Customer invoices, reviewed"
	w := postCapture(t, update)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	resp := decodeCaptureResponse(t, w)
	if resp.Revision == created.Revision {
		t.Errorf("an update must return a new revision, got %q again", resp.Revision)
	}
	if resp.Query.Title != update.Query.Title {
		t.Errorf("title = %q", resp.Query.Title)
	}
	if got := fake.puts[1].condition; got != (captureWriteCondition{IfMatch: created.Revision}) {
		t.Errorf("condition = %+v, want ifMatch %q", got, created.Revision)
	}
}

func TestCaptureQuery_StaleRevisionConflicts(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	created := decodeCaptureResponse(t, postCapture(t, validCaptureRequest(scope)))

	first := validCaptureRequest(scope)
	first.IfNoneMatch, first.IfMatch = false, created.Revision
	first.Query.Title = "first edit"
	if w := postCapture(t, first); w.Code != http.StatusOK {
		t.Fatalf("first update: %d %s", w.Code, w.Body.String())
	}

	stale := validCaptureRequest(scope)
	stale.IfNoneMatch, stale.IfMatch = false, created.Revision
	stale.Query.Title = "stale edit"
	w := postCapture(t, stale)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	env := decodeCaptureError(t, w)
	if env.Error.Code != string(codeRevisionConflict) || env.Error.Field != "ifMatch" {
		t.Errorf("error = %+v, want REVISION_CONFLICT on ifMatch", env.Error)
	}
	if got := fake.records["customers/customer-invoices-captured"].Record.Query.Title; got != "first edit" {
		t.Errorf("stored title = %q; a stale update must change nothing", got)
	}
}

func TestCaptureQuery_CreateOverAnExistingIDConflicts(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	if w := postCapture(t, validCaptureRequest(scope)); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	again := validCaptureRequest(scope)
	again.Query.Title = "overwrite attempt"
	w := postCapture(t, again)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	env := decodeCaptureError(t, w)
	if env.Error.Code != string(codeRevisionConflict) || env.Error.Field != "query.id" {
		t.Errorf("error = %+v, want REVISION_CONFLICT on query.id", env.Error)
	}
	if got := fake.records["customers/customer-invoices-captured"]; got.Revision != "rev-1" || got.Record.Query.Title != "Customer invoices" {
		t.Errorf("stored = %+v; a conflicting create must change nothing", got)
	}
}

func TestCaptureQuery_UpdateOfAMissingQueryConflicts(t *testing.T) {
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	update := validCaptureRequest(scope)
	update.IfNoneMatch, update.IfMatch = false, "rev-never-issued"
	w := postCapture(t, update)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if env := decodeCaptureError(t, w); env.Error.Field != "ifMatch" {
		t.Errorf("field = %q, want ifMatch", env.Error.Field)
	}
}

func TestCaptureQuery_RejectsInvalidRequests(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	withDTQL := func(dtql string) func(r *captureQueryRequest) {
		return func(r *captureQueryRequest) { r.Query.DTQL = dtql }
	}
	tests := []struct {
		name   string
		mutate func(r *captureQueryRequest)
		field  string
	}{
		{"parent-directory folder", func(r *captureQueryRequest) { r.Query.FolderPath = "../outside" }, "query.folderPath"},
		{"folder that climbs out", func(r *captureQueryRequest) { r.Query.FolderPath = "customers/../../outside" }, "query.folderPath"},
		{"absolute folder", func(r *captureQueryRequest) { r.Query.FolderPath = "/etc" }, "query.folderPath"},
		{"hidden folder", func(r *captureQueryRequest) { r.Query.FolderPath = ".git/hooks" }, "query.folderPath"},
		{"the shared-root id as a folder", func(r *captureQueryRequest) { r.Query.FolderPath = "~" }, "query.folderPath"},
		{"backslash folder", func(r *captureQueryRequest) { r.Query.FolderPath = `customers\..\..` }, "query.folderPath"},
		{"parent-directory id", func(r *captureQueryRequest) { r.Query.ID = "../escape" }, "query.id"},
		{"id with a separator", func(r *captureQueryRequest) { r.Query.ID = "a/b" }, "query.id"},
		{"hidden id", func(r *captureQueryRequest) { r.Query.ID = ".hidden" }, "query.id"},
		{"empty id", func(r *captureQueryRequest) { r.Query.ID = "" }, "query.id"},
		{"id with NUL", func(r *captureQueryRequest) { r.Query.ID = "a\x00b" }, "query.id"},
		{"id with a colon", func(r *captureQueryRequest) { r.Query.ID = "c:q" }, "query.id"},
		{"over-long id", func(r *captureQueryRequest) { r.Query.ID = strings.Repeat("q", 129) }, "query.id"},

		{"password in a URL in the dtql", withDTQL(strings.Replace(captureTestDTQL, "field: Total",
			"field: Total\n  - field: Note\n    alias: \"postgres://u:secret@db.example/prod\"", 1)), "query.dtql"},
		{"password literal in the dtql", withDTQL("from:\n  name: Invoice\nwhere:\n  op: ==\n  left:\n    field: Dsn\n  right:\n    value: \"Password=secret\"\n"), "query.dtql"},
		{"DSN in the purpose", func(r *captureQueryRequest) { r.Query.Purpose = "copied from app:hunter2@tcp(db)/prod" }, "query.purpose"},
		{"password key in the title", func(r *captureQueryRequest) { r.Query.Title = "uid=app;pwd=hunter2" }, "query.title"},
		{"password in a parameter title", func(r *captureQueryRequest) { r.Query.Parameters[0].Title = "https://a:b@example.com" }, "query.parameters"},
		{"password in a meta field", func(r *captureQueryRequest) { r.Query.Parameters[0].Meta.Field = "x;password=y" }, "query.parameters"},

		{"missing title", func(r *captureQueryRequest) { r.Query.Title = " " }, "query.title"},
		{"over-long title", func(r *captureQueryRequest) { r.Query.Title = strings.Repeat("t", 201) }, "query.title"},
		{"missing purpose", func(r *captureQueryRequest) { r.Query.Purpose = "" }, "query.purpose"},
		{"missing dtql", withDTQL(""), "query.dtql"},
		{"dtql that does not parse", withDTQL("from: [unterminated\n"), "query.dtql"},
		{"dtql with a misplaced parameter", withDTQL("from:\n  name: Invoice\nwhere:\n  op: ==\n  left:\n    param: Customer.ID\n  right:\n    field: CustomerId\n"), "query.dtql"},
		{"dtql parameter that is not declared", func(r *captureQueryRequest) {
			r.Query.Parameters, r.Query.BindingOrigins = nil, nil
		}, "query.parameters"},
		{"declared parameter the dtql never uses", func(r *captureQueryRequest) {
			r.Query.Parameters = append(r.Query.Parameters, capturedParameter{ID: "Since", Type: "date"})
		}, "query.parameters"},
		{"unknown parameter type", func(r *captureQueryRequest) { r.Query.Parameters[0].Type = "text" }, "query.parameters"},
		{"null parameter type", func(r *captureQueryRequest) { r.Query.Parameters[0].Type = "null" }, "query.parameters"},
		{"invalid parameter id", func(r *captureQueryRequest) { r.Query.Parameters[0].ID = "1st" }, "query.parameters"},
		{"duplicate parameter", func(r *captureQueryRequest) {
			r.Query.Parameters = append(r.Query.Parameters, r.Query.Parameters[0])
		}, "query.parameters"},
		{"meta without a field", func(r *captureQueryRequest) { r.Query.Parameters[0].Meta.Field = "" }, "query.parameters"},
		{"default binding origin", func(r *captureQueryRequest) { r.Query.BindingOrigins[0].Origin = "default" }, "query.bindingOrigins"},
		{"binding origin for an undeclared parameter", func(r *captureQueryRequest) {
			r.Query.BindingOrigins[0].ParameterID = "Invoice.ID"
		}, "query.bindingOrigins"},
		{"duplicate binding origin", func(r *captureQueryRequest) {
			r.Query.BindingOrigins = append(r.Query.BindingOrigins, r.Query.BindingOrigins[0])
		}, "query.bindingOrigins"},

		{"a file path as the source", func(r *captureQueryRequest) { r.Query.Source = "/etc/passwd" }, "query.source"},
		{"a URL as the source", func(r *captureQueryRequest) { r.Query.Source = "https://example.com/db" }, "query.source"},
		{"an unregistered source", func(r *captureQueryRequest) { r.Query.Source = "nope" }, "query.source"},
		{"a source a saved query cannot target", func(r *captureQueryRequest) { r.Query.Source = "support-notes" }, "query.source"},
		{"an unknown environment", func(r *captureQueryRequest) { r.Environment = "prod" }, "query.source"},

		{"neither condition", func(r *captureQueryRequest) { r.IfNoneMatch = false }, "ifNoneMatch/ifMatch"},
		{"both conditions", func(r *captureQueryRequest) { r.IfMatch = "rev-1" }, "ifNoneMatch/ifMatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake.puts = nil
			req := validCaptureRequest(scope)
			tt.mutate(&req)
			assertCaptureError(t, postCapture(t, req), fake, http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, tt.field)
		})
	}
}

// TestCaptureQuery_RejectsResultRowsAndForeignFields proves the strict body
// decode: result rows, a captured default value, a session-local fact id, a
// client-claimed author or principal, a duplicate key or a malformed or
// oversized body never reach the store.
func TestCaptureQuery_RejectsResultRowsAndForeignFields(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	base := string(marshalCapture(t, validCaptureRequest(scope)))
	tests := map[string]string{
		"result rows at the top level":  strings.Replace(base, `"query":`, `"rows":[[1,"Leonie"]],"query":`, 1),
		"a recordset inside the query":  strings.Replace(base, `"dtql":`, `"recordset":{"rows":[[1]]},"dtql":`, 1),
		"a captured default value":      strings.Replace(base, `"isRequired":`, `"defaultValue":5,"isRequired":`, 1),
		"a session-local fact id":       strings.Replace(base, `"origin":"selection"`, `"origin":"selection","factId":"f1"`, 1),
		"a client-claimed author":       strings.Replace(base, `"query":`, `"author":"root","query":`, 1),
		"a client-supplied principal":   strings.Replace(base, `"query":`, `"principal":{"id":"root"},"query":`, 1),
		"a duplicate key":               strings.Replace(base, `"query":`, `"project":"other","query":`, 1),
		"trailing data":                 base + `{}`,
		"not JSON":                      "project=p",
		"a body over the 1 MiB ceiling": strings.Replace(base, `"purpose":"`, `"purpose":"`+strings.Repeat("x", maxCaptureBodyBytes), 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			fake.puts = nil
			assertCaptureError(t, postCaptureBody(t, []byte(body)), fake, http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "")
		})
	}
}

func TestCaptureQuery_OnlyPOST(t *testing.T) {
	_, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	r := httptest.NewRequest(http.MethodGet, "/datatug/queries/capture", nil)
	w := httptest.NewRecorder()
	captureQueryHandler(w, r)
	assertCaptureError(t, w, fake, http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "")
}

func TestCaptureQuery_DeniedPrincipalsWriteNothing(t *testing.T) {
	// A denial a policy decided carries the fixed message and nothing else:
	// which policy, rule and role decided it, and the resource path it
	// protects, go to the agent log (captureAccessDenied). A denial the
	// agent's own mode decided - no --allow-writes - still says so: that
	// names nothing but the agent's configuration, which agent-info
	// publishes anyway.
	tests := []struct {
		name        string
		as          string
		roles       []string
		allowWrites bool
		message     string
	}{
		{name: "a read-only principal", as: "sam", roles: []string{"support"}, allowWrites: true,
			message: captureAccessDeniedMessage},
		{name: "a principal no grant binds", as: "mallory", allowWrites: true,
			message: captureAccessDeniedMessage},
		{name: "admin on an agent started without --allow-writes", as: "admin", roles: []string{"admin"},
			message: captureAccessDeniedMessage + ": this agent was started without --allow-writes; project writes are refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope, fake := captureTestSetup(t, tt.as, tt.roles, tt.allowWrites)
			env := assertCaptureError(t, postCapture(t, validCaptureRequest(scope)), fake, http.StatusForbidden, apicontract.ErrCodeAccessDenied, "")
			if env.Error.Message != tt.message {
				t.Errorf("message %q, want %q", env.Error.Message, tt.message)
			}
			for _, leak := range []string{"/policies", "semantic-test", "datatug_projects", tt.as} {
				if strings.Contains(env.Error.Message, leak) {
					t.Errorf("message %q reveals %q", env.Error.Message, leak)
				}
			}
		})
	}
}

// TestCaptureQuery_RouteIsBehindTheWriteCapability drives the real route
// table: with the process's write capability off, POST queries/capture is
// refused before the handler runs, in both registration modes.
func TestCaptureQuery_RouteIsBehindTheWriteCapability(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	for _, writeOnly := range []bool{false, true} {
		rr := &recordingRouter{handlers: map[string]http.HandlerFunc{}}
		registerRoutes("", rr, func(f http.HandlerFunc) http.HandlerFunc { return f }, writeOnly, Capabilities{})
		handler, ok := rr.handlers[http.MethodPost+" /datatug/queries/capture"]
		if !ok {
			t.Fatalf("writeOnly=%v: POST /datatug/queries/capture is not registered", writeOnly)
		}
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/datatug/queries/capture", bytes.NewReader(marshalCapture(t, validCaptureRequest(scope)))))
		assertCaptureError(t, w, fake, http.StatusForbidden, apicontract.ErrCodeAccessDenied, "")
	}
}

type recordingRouter struct {
	handlers map[string]http.HandlerFunc
}

func (r *recordingRouter) HandlerFunc(method, path string, handler http.HandlerFunc) {
	r.handlers[method+" "+path] = handler
}

func TestCaptureQuery_StaleSecurityContext(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	req := validCaptureRequest(scope)
	req.SecurityContextID = "an-old-session"
	assertCaptureError(t, postCapture(t, req), fake, http.StatusConflict, apicontract.ErrCodeStaleContext, "securityContextId")
}

func TestCaptureQuery_UnknownProject(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	req := validCaptureRequest(scope)
	req.Project = "not-served-here"
	assertCaptureError(t, postCapture(t, req), fake, http.StatusNotFound, apicontract.ErrCodeNotFound, "")
}

func TestCaptureQuery_StoreErrorsMapToTheEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   apicontract.ErrorCode
		field  string
	}{
		{"an unsafe location the store found", &captureStoreError{Kind: captureErrLocation, Field: "query.folderPath", Reason: "resolves through a symlink"},
			http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "query.folderPath"},
		{"an incomplete stored pair", &captureStoreError{Kind: captureErrIncomplete, Reason: "metadata without its body"},
			http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "query.id"},
		{"content the store refused", &captureStoreError{Kind: captureErrContent, Field: "query", Reason: "targets[0]: must not store credentials"},
			http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "query"},
		{"a timeout", fmt.Errorf("saving: %w", context.DeadlineExceeded), http.StatusGatewayTimeout, apicontract.ErrCodeTimeout, ""},
		{"an unexpected failure", errors.New("open /srv/secret/project/queries: permission denied"),
			http.StatusInternalServerError, codeInternal, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
			fake.err = tt.err
			w := postCapture(t, validCaptureRequest(scope))
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.status, w.Body.String())
			}
			env := decodeCaptureError(t, w)
			if env.Error.Code != string(tt.code) || env.Error.Field != tt.field {
				t.Errorf("error = %+v, want code %s field %q", env.Error, tt.code, tt.field)
			}
			if strings.Contains(env.Error.Message, "/srv/secret") {
				t.Errorf("message %q leaks a server path", env.Error.Message)
			}
		})
	}
}

func TestCaptureQuery_UnavailableStoreFailsClosed(t *testing.T) {
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	captureStoreFor = func(string) (queryCaptureStore, error) { return nil, errCaptureStoreUnavailable }
	w := postCapture(t, validCaptureRequest(scope))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body.String())
	}
	if env := decodeCaptureError(t, w); !strings.Contains(env.Error.Message, "revisioned") {
		t.Errorf("message %q should say the build has no revisioned query store", env.Error.Message)
	}
}

// TestCaptureQuery_RespondsOnlyAfterTheStoreReturns checks nothing reaches
// the client while the store is still writing, and that a failed write
// never looks like a success.
func TestCaptureQuery_RespondsOnlyAfterTheStoreReturns(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	r := httptest.NewRequest(http.MethodPost, "/datatug/queries/capture", bytes.NewReader(marshalCapture(t, validCaptureRequest(scope))))
	w := httptest.NewRecorder()
	fake.onPut = func() {
		if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
			t.Errorf("the handler wrote a response before the store returned: %q", w.Body.String())
		}
	}
	captureQueryHandler(w, r)
	if w.Code != http.StatusCreated || len(fake.puts) != 1 {
		t.Fatalf("status = %d, store calls = %d", w.Code, len(fake.puts))
	}

	fake.onPut = nil
	fake.err = errors.New("disk full")
	w = postCapture(t, validCaptureRequest(scope))
	if w.Code < 400 {
		t.Errorf("a failed write answered %d", w.Code)
	}
}

// TestCaptureQuery_ErrorEnvelopesMatchCoreFixtures compares the endpoint's
// real error responses with datatug-core's frozen fixtures for the same
// codes. REVISION_CONFLICT is not in any tagged datatug-core yet, so only
// its status and code are checked here; the build-tagged fixture test
// checks it against error_revision_conflict.json.
func TestCaptureQuery_ErrorEnvelopesMatchCoreFixtures(t *testing.T) {
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	invalid := validCaptureRequest(scope)
	invalid.Query.FolderPath = "../outside"
	stale := validCaptureRequest(scope)
	stale.SecurityContextID = "an-old-session"
	unknown := validCaptureRequest(scope)
	unknown.Project = "not-served-here"
	cases := map[string]*httptest.ResponseRecorder{
		"error_invalid_request.json": postCapture(t, invalid),
		"error_stale_context.json":   postCapture(t, stale),
		"error_not_found.json":       postCapture(t, unknown),
	}
	for fixture, w := range cases {
		t.Run(fixture, func(t *testing.T) {
			want := decodeErrorFixture(t, fixture)
			got := decodeCaptureError(t, w)
			if err := got.Validate(); err != nil {
				t.Fatalf("response envelope fails core's Validate(): %v\n%s", err, w.Body.String())
			}
			if got.Error.Code != want.Error.Code {
				t.Errorf("code = %q, want %q", got.Error.Code, want.Error.Code)
			}
			if w.Code != apicontract.ErrorCode(want.Error.Code).HTTPStatus() {
				t.Errorf("status = %d, want %d", w.Code, apicontract.ErrorCode(want.Error.Code).HTTPStatus())
			}
		})
	}
	t.Run("error_access_denied.json", func(t *testing.T) {
		denyScope, _ := captureTestSetup(t, "sam", []string{"support"}, true)
		w := postCapture(t, validCaptureRequest(denyScope))
		got := decodeCaptureError(t, w)
		if err := got.Validate(); err != nil {
			t.Fatalf("response envelope fails core's Validate(): %v", err)
		}
		if want := decodeErrorFixture(t, "error_access_denied.json"); got.Error.Code != want.Error.Code || w.Code != http.StatusForbidden {
			t.Errorf("got %d %q, want 403 %q", w.Code, got.Error.Code, want.Error.Code)
		}
	})
	t.Run("REVISION_CONFLICT", func(t *testing.T) {
		scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
		postCapture(t, validCaptureRequest(scope))
		w := postCapture(t, validCaptureRequest(scope))
		got := decodeCaptureError(t, w)
		if w.Code != http.StatusConflict || got.Error.Code != "REVISION_CONFLICT" {
			t.Errorf("got %d %q, want 409 REVISION_CONFLICT", w.Code, got.Error.Code)
		}
	})
}

func TestCaptureQuery_RootFolderWithoutParameters(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	req := validCaptureRequest(scope)
	req.Query.FolderPath = ""
	req.Query.ID = "all-invoices"
	req.Query.Purpose = "Every invoice.\nA starting point for anything billing-related."
	req.Query.DTQL = "from:\n  name: Invoice\n"
	req.Query.Parameters, req.Query.BindingOrigins = nil, nil
	w := postCapture(t, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{`"parameters":[]`, `"bindingOrigins":[]`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("empty lists must be arrays on the wire, want %s in %s", want, w.Body.String())
		}
	}
	if resp := decodeCaptureResponse(t, w); resp.QueryID != "all-invoices" {
		t.Errorf("queryId = %q, want the bare id for a root query", resp.QueryID)
	}
	if len(fake.puts) != 1 {
		t.Errorf("store calls = %d, want 1", len(fake.puts))
	}
}

func TestCaptureQuery_ControlCharacters(t *testing.T) {
	scope, fake := captureTestSetup(t, "admin", []string{"admin"}, true)
	req := validCaptureRequest(scope)
	req.Query.Title = "Customer\ninvoices"
	assertCaptureError(t, postCapture(t, req), fake, http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "query.title")
	req = validCaptureRequest(scope)
	req.Query.Purpose = "ring the bell\a"
	assertCaptureError(t, postCapture(t, req), fake, http.StatusBadRequest, apicontract.ErrCodeInvalidRequest, "query.purpose")
}

func TestCaptureStoreFailure_FieldFallbacks(t *testing.T) {
	create := captureWriteCondition{IfNoneMatch: true}
	for kind, want := range map[captureStoreErrorKind]string{captureErrLocation: "query.folderPath", captureErrContent: "query"} {
		var ce *contractError
		if err := captureStoreFailure(&captureStoreError{Kind: kind, Reason: "refused"}, create); !errors.As(err, &ce) ||
			ce.Code != apicontract.ErrCodeInvalidRequest || ce.Field != want {
			t.Errorf("kind %d without a field: got %v, want INVALID_REQUEST on %q", kind, err, want)
		}
	}
	if msg := (&captureStoreError{Kind: captureErrConflict, Reason: "stale revision"}).Error(); !strings.Contains(msg, "stale revision") {
		t.Errorf("Error() = %q does not carry the reason", msg)
	}
}

func TestRejectTrailingJSON(t *testing.T) {
	if err := rejectTrailingJSON([]byte("{")); err == nil {
		t.Error("expected an error for an unterminated JSON value")
	}
	if err := rejectTrailingJSON([]byte("{}\n  ")); err != nil {
		t.Errorf("trailing whitespace is not trailing data: %v", err)
	}
	if err := rejectTrailingJSON([]byte(`{} {}`)); err == nil {
		t.Error("expected an error for a second JSON value")
	}
}
