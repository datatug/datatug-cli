package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeCatalogFile reproduces datatug-demo-projects/demo-project-1's own
// environments/<env>/catalogs/<catalog>/<catalog>.db.json shape
// ({"driver":"sqlite3","path":"...","dbModel":"..."}) — the file
// catalogDbModel reads to resolve environmentID+catalogID to a dbModel id.
func writeCatalogFile(t *testing.T, projectDir, env, catalog, dbModel string) {
	t.Helper()
	dir := filepath.Join(projectDir, "environments", env, "catalogs", catalog)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
	content := fmt.Sprintf(`{"driver":"sqlite3","path":"~/datatug/dbs/%s.sqlite","dbModel":%q}`, catalog, dbModel)
	path := filepath.Join(dir, catalog+".db.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// writeTableDir reproduces datatug-demo-projects/demo-project-1's own
// dbmodels/<dbModel>/<schema>/{tables,views}/<Name>/ directory-per-object
// layout (the columns.json file inside it is never read by
// listCatalogTables — only the directory name is the identity).
func writeTableDir(t *testing.T, projectDir, dbModel, schema, kind, name string) {
	t.Helper()
	dir := filepath.Join(projectDir, "dbmodels", dbModel, schema, kind, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", dir, err)
	}
}

// TestGetCatalogTables covers Task 17 item A.2 (S121): the catalog
// table/view list datatug-apps' EnvDbPageComponent needs, read from the
// project's already-scanned dbmodel files (no live DB connection).
func TestGetCatalogTables(t *testing.T) {
	dir := t.TempDir()
	writeCatalogFile(t, dir, "local", "chinook-local", "chinook")
	writeTableDir(t, dir, "chinook", "main", "tables", "Album")
	writeTableDir(t, dir, "chinook", "main", "tables", "Customer")
	writeTableDir(t, dir, "chinook", "main", "views", "ActiveCustomers")

	got, err := GetCatalogTables(dir, "local", "chinook-local")
	if err != nil {
		t.Fatalf("GetCatalogTables: %v", err)
	}
	wantTables := []CatalogTable{
		{Schema: "main", Name: "Album", DbType: "BASE TABLE"},
		{Schema: "main", Name: "Customer", DbType: "BASE TABLE"},
	}
	if !reflect.DeepEqual(got.Tables, wantTables) {
		t.Errorf("Tables = %+v, want %+v", got.Tables, wantTables)
	}
	wantViews := []CatalogTable{
		{Schema: "main", Name: "ActiveCustomers", DbType: "VIEW"},
	}
	if !reflect.DeepEqual(got.Views, wantViews) {
		t.Errorf("Views = %+v, want %+v", got.Views, wantViews)
	}
}

// TestGetCatalogTables_NoViews covers a catalog whose dbModel has no
// views/ subdirectory at all (demo-project-1's own chinook dbModel, in
// fact) — must return an empty slice, not an error.
func TestGetCatalogTables_NoViews(t *testing.T) {
	dir := t.TempDir()
	writeCatalogFile(t, dir, "local", "chinook-local", "chinook")
	writeTableDir(t, dir, "chinook", "main", "tables", "Album")

	got, err := GetCatalogTables(dir, "local", "chinook-local")
	if err != nil {
		t.Fatalf("GetCatalogTables: %v", err)
	}
	if len(got.Tables) != 1 || got.Tables[0].Name != "Album" {
		t.Errorf("Tables = %+v, want [Album]", got.Tables)
	}
	if len(got.Views) != 0 {
		t.Errorf("Views = %+v, want empty", got.Views)
	}
}

// TestGetCatalogTables_UnknownCatalog covers ErrCatalogNotFound (mapped to
// a clean 404, never a raw filesystem error — see
// pkg/server/endpoints/util_error_handling.go's handleError) for a catalog
// with no <id>.db.json under the requested environment.
func TestGetCatalogTables_UnknownCatalog(t *testing.T) {
	dir := t.TempDir()
	writeCatalogFile(t, dir, "local", "chinook-local", "chinook")

	_, err := GetCatalogTables(dir, "local", "no-such-catalog")
	if !errors.Is(err, ErrCatalogNotFound) {
		t.Fatalf("err = %v, want ErrCatalogNotFound", err)
	}
}

// TestGetCatalogTables_UnknownEnvironment covers the same not-found
// treatment when the environment itself doesn't exist.
func TestGetCatalogTables_UnknownEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeCatalogFile(t, dir, "local", "chinook-local", "chinook")

	_, err := GetCatalogTables(dir, "no-such-env", "chinook-local")
	if !errors.Is(err, ErrCatalogNotFound) {
		t.Fatalf("err = %v, want ErrCatalogNotFound", err)
	}
}
