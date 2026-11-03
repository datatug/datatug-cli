package endpoints

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogTablesHostedMarkerAndFinalGeneration(t *testing.T) {
	root, project := boundEndpointFixture(t)
	configPath := filepath.Join(root, ".datatug", "security.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "chinook.sqlite")
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	config["hostedTableRead"] = map[string]any{"version": 1, "spaceID": "S1", "projectId": "P1", "connectionId": "C1", "catalogSHA256": strings.Repeat("a", 64), "fileSHA256": hex.EncodeToString(sum[:]), "database": "controlled"}
	data, _ = json.Marshal(config)
	mustWriteFile(t, configPath, string(data))
	session, _, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{project: root}, api.Capabilities{})
	mustMkdirAll(t, filepath.Join(root, "dbmodels", semanticTestSource, "main", "tables", "Customer"))
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		getCatalogTablesHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/catalog-tables?"+url.Values{"project": {project}, "environment": {"local"}, "catalog": {"chinook-local"}}.Encode(), nil))
		return w
	}
	w := get()
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"available"`) || !strings.Contains(w.Body.String(), `"schema":"main"`) || strings.Contains(w.Body.String(), "sqliteFile") {
		t.Fatalf("available %d %s", w.Code, w.Body.String())
	}
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	w = get()
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"hostedTableRead":{"version":1,"status":"unavailable","reasonCode":"source-binding-unavailable"}`) || strings.Contains(w.Body.String(), `"spaceID":"S1"`) {
		t.Fatalf("unavailable %d %s", w.Code, w.Body.String())
	}
	old := getCatalogTablesFunc
	t.Cleanup(func() { getCatalogTablesFunc = old })
	getCatalogTablesFunc = func(p, e, c string) (*api.CatalogTables, error) {
		tables, err := old(p, e, c)
		api.ConfigureSecureSession(session, map[string]string{project: root}, api.Capabilities{})
		return tables, err
	}
	w = get()
	if w.Code == 200 || strings.Contains(w.Body.String(), "hostedTableRead") || strings.Contains(w.Body.String(), "Customer") {
		t.Fatalf("generation leak %d %s", w.Code, w.Body.String())
	}
}
