package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

func TestDeclaredDefaultBindingAndCodec(t *testing.T) {
	const definition = `{"id":"q","title":"Exact defaults","type":"SQL","createdAt":"2026-10-01T00:00:00Z","parameters":[{"id":"p","type":"integer","defaultValue":9007199254740993},{"id":"d","type":"decimal","defaultValue":9007199254740992.10}]}`
	projectDir := t.TempDir()
	queryDir := filepath.Join(projectDir, storage.QueriesFolder)
	if err := os.MkdirAll(queryDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queryDir, "q.query.json"), []byte(definition), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queryDir, "q.query.sql"), []byte("SELECT :p, :d"), 0600); err != nil {
		t.Fatal(err)
	}
	stored, err := filestore.NewProjectStore("fixture", projectDir).LoadQuery(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(datatug.QueryDefWithFolderPath{QueryDef: *stored}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var q datatug.QueryDefWithFolderPath
	if err = json.NewDecoder(response.Body).Decode(&q); err != nil {
		t.Fatal(err)
	}
	bound, origins, err := bindParameters(q.Parameters, nil)
	if err != nil || bound["p"].Scalar.Str != "9007199254740993" || bound["d"].Scalar.Str != "9007199254740992.10" {
		t.Fatalf("lossy defaults: %#v %v", bound, err)
	}
	for _, origin := range origins {
		if origin.Origin != apicontract.BindingOriginDefault {
			t.Fatal(origin)
		}
	}
	bound, origins, err = bindParameters(q.Parameters, map[string]string{"p": "7"})
	if err != nil || bound["p"].Scalar.Str != "7" || origins[1].Origin != apicontract.BindingOriginManual {
		t.Fatalf("manual override: %#v %#v %v", bound, origins, err)
	}
	q.Parameters[0].DefaultValue = 1.5
	if _, _, err = bindParameters(q.Parameters, nil); err == nil {
		t.Fatal("invalid default accepted")
	}
}
