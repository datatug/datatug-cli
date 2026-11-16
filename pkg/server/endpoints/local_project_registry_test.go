package endpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/localprojects"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestLocalProjectRegistryHTTPCreateArchiveRestore(t *testing.T) {
	registry, err := localprojects.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := secureread.NewSession(secureread.SessionOptions{As: "owner", NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{}, api.Capabilities{AllowWrites: true})
	api.ConfigureLocalProjectRegistry(registry)
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })
	call := func(handler http.HandlerFunc, method, url, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, url, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "https://datatug.app")
		w := httptest.NewRecorder()
		handler(w, request)
		return w
	}
	create := `{"store":"local","id":"http-registry-project","title":"HTTP local project","operationId":"create"}`
	w := call(ProjectAgentEndpoints{}.createProject, "POST", "/", create)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store, no-transform" {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var project localprojects.Project
	if err = json.Unmarshal(w.Body.Bytes(), &project); err != nil || project.ID != "http-registry-project" || project.Archived {
		t.Fatalf("invalid created result: %+v %v", project, err)
	}
	for _, bad := range []string{strings.Replace(create, `"store":"local"`, `"store":"local","store":"elsewhere"`, 1), strings.Replace(create, `"store":"local"`, `"path":"/tmp/arbitrary","store":"local"`, 1), strings.Repeat("x", 8193)} {
		w = call(ProjectAgentEndpoints{}.createProject, "POST", "/", bad)
		if w.Code != 400 {
			t.Fatalf("bad create %d %s", w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"storage":"local","project":"http-registry-project","archived":true,"operationId":"archive"}`, `{"storage":"local","project":"http-registry-project","archived":false,"operationId":"restore"}`} {
		w = call(archiveLocalProject, "POST", "/", body)
		if w.Code != 200 {
			t.Fatalf("archive/restore %d %s", w.Code, w.Body.String())
		}
	}
	w = call(getLocalProjectRegistry, "GET", "/", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"canCreate":true`) || !strings.Contains(w.Body.String(), `"archived":false`) {
		t.Fatalf("registry %d %s", w.Code, w.Body.String())
	}
	w = call(getLocalProjectRegistry, "GET", "/?path=/private", "")
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
