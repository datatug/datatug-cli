package endpoints

import (
	"bytes"
	"encoding/json"
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
	"github.com/julienschmidt/httprouter"
)

func TestPublicOVDBConnectionRoutesExactCreateAndStrictBodies(t *testing.T) {
	root, private := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "datatug-project.json"), []byte(`{"id":"p","databases":[{"id":"music","connectionId":"music-ovdb","ovdb":{"baseUrl":"https://public.example","databaseId":"music-db"}}],"token":"private-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "environments", "dev"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "environments", "dev", "dev.env.json"), []byte(`{"id":"dev","dbServers":[],"password":"private-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	configure := func(writes, unrestricted bool) {
		api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "operator"}, Unrestricted: unrestricted}, map[string]string{"p": root}, api.Capabilities{AllowWrites: writes, EnvironmentPrivateDir: private, EnvironmentAuthorityID: "test-agent"})
	}
	configure(true, true)
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	router := httprouter.New()
	environmentsRoutes("/datatug", guardedRouter{router: router, guard: NewRequestGuard("localhost", 0)}, nil, false, Capabilities{AllowWrites: true})
	call := func(method, url string, body []byte, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		request := httptest.NewRequest(method, "http://localhost/datatug"+url, bytes.NewReader(body))
		request.Header.Set("Origin", "http://datatug.localhost:4200")
		router.ServeHTTP(w, request)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, url, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable connection response")
		}
		for _, private := range []string{root, private, "private-value", "https://public.example"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatalf("response leaked %s", private)
			}
		}
		return w
	}
	url := "/connections/public_ovdb_state?storage=local&project=p&environment=dev&database=music"
	w := call(http.MethodGet, url, nil, 200)
	var state dto.PublicOVDBConnectionState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || !state.CanAdd {
		t.Fatalf("bad capability %+v %v", state, err)
	}
	for _, suffix := range []string{"&path=private", "&environment=prod", "&branch=x&branch=y"} {
		call(http.MethodGet, url+suffix, nil, 400)
	}
	for _, body := range []string{`{"scope":{},"scope":{}}`, `{"scope":{},"databaseId":"\ud800"}`, `{"scope":{},"descriptor":{"password":"private-value"}}`} {
		call(http.MethodPost, "/connections/create_public_ovdb", []byte(body), 400)
	}
	request := dto.CreatePublicOVDBConnectionRequest{EnvironmentMutation: dto.EnvironmentMutation{Scope: state.Scope, IfRevision: state.Revision, OperationID: state.ScopeBinding + ".12345678-1234-4123-8123-123456789abc"}, EnvironmentID: "dev", DatabaseID: "music"}
	body, _ := json.Marshal(request)
	call(http.MethodPost, "/connections/create_public_ovdb?environment=prod", body, 400)
	configure(true, false)
	call(http.MethodPost, "/connections/create_public_ovdb", body, 403)
	configure(true, true)
	w = call(http.MethodGet, url, nil, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &state)
	request.Scope = state.Scope
	request.IfRevision = state.Revision
	request.OperationID = state.ScopeBinding + ".12345678-1234-4123-8123-123456789abc"
	body, _ = json.Marshal(request)
	created := call(http.MethodPost, "/connections/create_public_ovdb", body, 200)
	recovery, _ := json.Marshal(dto.EnvironmentOperationStatusRequest{Scope: request.Scope, OperationID: request.OperationID})
	status := call(http.MethodPost, "/environments/operation_status", recovery, 200)
	if created.Body.String() != status.Body.String() {
		t.Fatal("operation status did not recover exact connection receipt")
	}
	w = call(http.MethodGet, url, nil, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &state)
	if state.CanAdd {
		t.Fatal("duplicate still advertised")
	}
	configure(false, true)
	call(http.MethodPost, "/connections/create_public_ovdb", body, 403)
}
