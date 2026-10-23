package endpoints

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/dto"
)

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failure") }

func serveEnvironmentProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(`{"id":"p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "admin"}, Unrestricted: true}, map[string]string{"p": root}, api.Capabilities{AllowWrites: true, EnvironmentPrivateDir: t.TempDir(), EnvironmentAuthorityID: "test-agent"})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	return root
}

func environmentCall(handler http.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	switch b := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case string:
		reader = bytes.NewReader([]byte(b))
	default:
		encoded, _ := json.Marshal(b)
		reader = bytes.NewReader(encoded)
	}
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(method, target, reader))
	return w
}

func wantEnvironmentHTTP(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || !strings.Contains(w.Body.String(), code) {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body.String(), status, code)
	}
}

func TestEnvironmentStateHandlerSelectors(t *testing.T) {
	root := serveEnvironmentProject(t)
	for _, target := range []string{
		"/environments/state?storage=local&project=p&storage=local", // a duplicate selector
		"/environments/state?storage=local&project=p&env=dev",       // an unknown selector
		"/environments/state?%zz",                                   // an unparsable query
		"/environments/state?storage=local&project=p&spaceID=s",     // a Space is not local
	} {
		wantEnvironmentHTTP(t, environmentCall(environmentStateHandler, http.MethodGet, target, nil), http.StatusBadRequest, "INVALID_REQUEST")
	}
	w := environmentCall(environmentStateHandler, http.MethodGet, "/environments/state?storage=local&project=p&projectApi=agent", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"projectApi":"agent"`) {
		t.Fatalf("projectApi state %d %s", w.Code, w.Body.String())
	}
	wantEnvironmentHTTP(t, environmentCall(environmentStateHandler, http.MethodGet, "/environments/state?storage=local&project=p&branch=main", nil), http.StatusConflict, "ENVIRONMENT_REVISION_CONFLICT")
	// An error that is not an environment error keeps the project mutation mapping.
	if w = environmentCall(environmentStateHandler, http.MethodGet, "/environments/state?storage=local&project=unknown", nil); w.Code < 400 {
		t.Fatalf("unknown project %d", w.Code)
	}
	// A lock marker that is not a plain file needs recovery.
	if err := os.Mkdir(filepath.Join(root, ".datatug-environments.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantEnvironmentHTTP(t, environmentCall(environmentStateHandler, http.MethodGet, "/environments/state?storage=local&project=p", nil), http.StatusConflict, "OUTCOME_UNCERTAIN")
}

func TestEnvironmentMutationHandlers(t *testing.T) {
	serveEnvironmentProject(t)
	read := func() dto.EnvironmentState {
		w := environmentCall(environmentStateHandler, http.MethodGet, "/environments/state?storage=local&project=p", nil)
		var state dto.EnvironmentState
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil {
			t.Fatalf("state %d %s", w.Code, w.Body.String())
		}
		return state
	}
	state := read()
	mutation := func(n string) dto.EnvironmentMutation {
		return dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: state.ScopeBinding + "." + n + "-1234-4123-8123-123456789abc"}
	}
	create := dto.CreateEnvironmentRequest{EnvironmentMutation: mutation("11111111"), Name: "Dev"}
	if w := environmentCall(createEnvironmentHandler, http.MethodPost, "/environment/create_environment", create); w.Code != http.StatusOK {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	stale := dto.CreateEnvironmentRequest{EnvironmentMutation: mutation("22222222"), Name: "QA"}
	wantEnvironmentHTTP(t, environmentCall(createEnvironmentHandler, http.MethodPost, "/environment/create_environment", stale), http.StatusConflict, "ENVIRONMENT_REVISION_CONFLICT")

	status := dto.EnvironmentOperationStatusRequest{Scope: state.Scope, OperationID: create.OperationID}
	if w := environmentCall(environmentOperationStatusHandler, http.MethodPost, "/environments/operation_status", status); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"environmentId":"dev"`) {
		t.Fatalf("status %d %s", w.Code, w.Body.String())
	}
	status.OperationID = "unknown"
	wantEnvironmentHTTP(t, environmentCall(environmentOperationStatusHandler, http.MethodPost, "/environments/operation_status", status), http.StatusConflict, "OPERATION_CONFLICT")

	state = read()
	save := dto.SaveEnvironmentOrderRequest{EnvironmentMutation: mutation("33333333"), EnvironmentOrder: []string{"dev"}}
	if w := environmentCall(saveEnvironmentOrderHandler, http.MethodPost, "/environments/save_order", save); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"environmentOrder":["dev"]`) {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	save.OperationID = state.ScopeBinding + ".44444444-1234-4123-8123-123456789abc"
	wantEnvironmentHTTP(t, environmentCall(saveEnvironmentOrderHandler, http.MethodPost, "/environments/save_order", save), http.StatusConflict, "ENVIRONMENT_REVISION_CONFLICT")

	// Every mutation route refuses a malformed request before touching the project.
	for _, handler := range []http.HandlerFunc{createEnvironmentHandler, saveEnvironmentOrderHandler, environmentOperationStatusHandler} {
		wantEnvironmentHTTP(t, environmentCall(handler, http.MethodPost, "/x?storage=local", `{}`), http.StatusBadRequest, "INVALID_REQUEST")
		wantEnvironmentHTTP(t, environmentCall(handler, http.MethodPost, "/x", strings.Repeat(" ", maxProjectQueryMutationBody+1)), http.StatusBadRequest, "INVALID_REQUEST")
		wantEnvironmentHTTP(t, environmentCall(handler, http.MethodPost, "/x", `{"scope":{},"operationId":"\ud800"}`), http.StatusBadRequest, "INVALID_REQUEST")
		wantEnvironmentHTTP(t, environmentCall(handler, http.MethodPost, "/x", `{"unexpected":true}`), http.StatusBadRequest, "INVALID_REQUEST")
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodPost, "/x", failingBody{}))
		wantEnvironmentHTTP(t, w, http.StatusBadRequest, "INVALID_REQUEST")
	}
}
