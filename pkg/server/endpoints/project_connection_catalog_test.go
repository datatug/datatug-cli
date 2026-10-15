package endpoints

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/julienschmidt/httprouter"
)

func TestLocalConnectionCatalogIsAuthorizedFixedBoundedMetadata(t *testing.T) {
	dir, project := writeRunQueryTestProject(t)
	configureSemanticSessionWithCapabilities(t, dir, project, "alice", []string{"admin"}, api.Capabilities{})
	if err := os.Mkdir(filepath.Join(dir, "connections"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "connections", "demo-db.json")
	valid := `{"format":"datatug-demo-connections/v1","connections":[{"id":"chinook-sqlite","dataset":"chinook","storage":"sqlite","readiness":"public-api","source":"https://demodb.dev/ovdb/v1/databases/chinook","fixtureSha256":"7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15","browserFixture":{"url":"https://chinook.demodb.dev/data/chinook.sqlite","bytes":1007616},"password":"private-value"}],"credentials":{"apiKey":"private-value"}}`
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(valid)
	router := httprouter.New()
	projectsRoutes("/datatug", guardedRouter{router: router, guard: NewRequestGuard("localhost", 0)}, nil, false, Capabilities{})
	request := func(query string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://localhost/datatug/projects/connection_catalog?"+query, nil)
		r.Header.Set("Origin", "http://datatug.localhost:4200")
		router.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Access-Control-Allow-Origin") != "http://datatug.localhost:4200" {
			t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-value") || strings.Contains(w.Body.String(), dir) {
			t.Fatal("catalog disclosed profile/path")
		}
		return w
	}
	query := "storage=local&project=" + project
	if got := request(query, http.StatusOK).Body.String(); !strings.Contains(got, `"fixtureSha256":"7651ba378ac2fcd0dfc3c66fb101f7a7eed3ba39a612ec642b96e20702061f15"`) || !strings.Contains(got, `"bytes":1007616`) {
		t.Fatal("catalog lost actual source pins")
	}
	request(query+"&path=datatug-project.json", http.StatusBadRequest)
	request(query+"&project=foreign", http.StatusBadRequest)
	request("storage=local&project=foreign", http.StatusBadRequest)
	write(strings.Replace(valid, "https://demodb.dev/", "https://private-value@demodb.dev/", 1))
	request(query, http.StatusInternalServerError)
	write(strings.Repeat("x", (256<<10)+1))
	request(query, http.StatusInternalServerError)
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	request(query, http.StatusInternalServerError)
	api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	request(query, http.StatusForbidden)
}
