package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindCatalogRelations(t *testing.T) {
	dir := t.TempDir()
	writeCatalogFile(t, dir, "local", "shop", "shop")
	writeColumnsFile(t, dir, "shop", "main", "tables", "Customer", `{"columns":[{"name":"id","pkPosition":1}]}`)
	writeColumnsFile(t, dir, "shop", "other", "views", "Customer", `{}`)
	writeColumnsFile(t, dir, "shop", "main", "tables", "Broken", `not json`) // never read: another name
	_ = os.WriteFile(filepath.Join(dir, "dbmodels", "shop", "stray.txt"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "dbmodels", "shop", "main", "tables", "stray.txt"), nil, 0o644)

	got, scanned, err := FindCatalogRelations(dir, "local", "shop", "Customer")
	if err != nil || !scanned || len(got) != 2 || got[0].Schema != "main" || got[0].DbType != "BASE TABLE" ||
		got[0].Columns[0].PrimaryKeyPosition != 1 || got[1].Schema != "other" || got[1].DbType != "VIEW" {
		t.Fatalf("%+v %v %v", got, scanned, err)
	}
	got, scanned, err = FindCatalogRelations(dir, "local", "shop", "ghost")
	if err != nil || !scanned || len(got) != 0 {
		t.Fatalf("%+v %v %v", got, scanned, err)
	}
	if _, _, err = FindCatalogRelations(dir, "local", "shop", "Broken"); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatal(err)
	}
}

func TestFindCatalogRelationsNotScanned(t *testing.T) {
	dir := t.TempDir()
	writeCatalogFile(t, dir, "local", "shop", "shop")
	if _, scanned, err := FindCatalogRelations(dir, "local", "shop", "t"); scanned || err != nil {
		t.Fatalf("no model folder: %v %v", scanned, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dbmodels", "shop", "main", "tables"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, scanned, err := FindCatalogRelations(dir, "local", "shop", "t"); scanned || err != nil {
		t.Fatalf("zero relations: %v %v", scanned, err)
	}
	if _, _, err := FindCatalogRelations(dir, "local", "ghost", "t"); err == nil {
		t.Fatal("unknown catalog")
	}
}

func TestFindCatalogRelationsUnreadable(t *testing.T) {
	cols := func(dir string) string { return filepath.Join(dir, "dbmodels", "shop", "main", "tables", "t") }
	setup := func(t *testing.T) string {
		dir := t.TempDir()
		writeCatalogFile(t, dir, "local", "shop", "shop")
		writeColumnsFile(t, dir, "shop", "main", "tables", "t", `{"columns":[]}`)
		return dir
	}
	file := func(dir string) string { return filepath.Join(cols(dir), "main.t.columns.json") }
	cases := map[string]func(t *testing.T, dir string){
		"model is a file": func(t *testing.T, dir string) {
			_ = os.RemoveAll(filepath.Join(dir, "dbmodels", "shop"))
			_ = os.WriteFile(filepath.Join(dir, "dbmodels", "shop"), nil, 0o644)
		},
		"kind is a file": func(t *testing.T, dir string) {
			_ = os.RemoveAll(filepath.Join(dir, "dbmodels", "shop", "main", "tables"))
			_ = os.WriteFile(filepath.Join(dir, "dbmodels", "shop", "main", "tables"), nil, 0o644)
		},
		"two columns files": func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(cols(dir), "x.columns.json"), []byte(`{}`), 0o644)
		},
		"no columns file": func(t *testing.T, dir string) { _ = os.Remove(file(dir)) },
		"link to a file": func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "elsewhere.json")
			_ = os.WriteFile(target, []byte(`{}`), 0o644)
			_ = os.Remove(file(dir))
			if err := os.Symlink(target, file(dir)); err != nil {
				t.Skip(err)
			}
		},
		"too large": func(t *testing.T, dir string) {
			big := `{"columns":[],"x":"` + strings.Repeat("a", MaxColumnsFileSize) + `"}`
			_ = os.WriteFile(file(dir), []byte(big), 0o644)
		},
		"unopenable": func(t *testing.T, dir string) { _ = os.Chmod(file(dir), 0) },
		"folder closed": func(t *testing.T, dir string) {
			_ = os.Chmod(cols(dir), 0)
			t.Cleanup(func() { _ = os.Chmod(cols(dir), 0o755) })
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := setup(t)
			mutate(t, dir)
			if _, _, err := FindCatalogRelations(dir, "local", "shop", "t"); err == nil || err.Error() != errRelationUnreadable.Error() {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestFindCatalogRelationsSkipsLinkedFolders(t *testing.T) {
	dir := t.TempDir()
	writeCatalogFile(t, dir, "local", "shop", "shop")
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "tables", "t"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dbmodels", "shop"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "dbmodels", "shop", "linked")); err != nil {
		t.Skip(err)
	}
	if _, scanned, err := FindCatalogRelations(dir, "local", "shop", "t"); scanned || err != nil {
		t.Fatalf("a linked schema folder was followed: %v %v", scanned, err)
	}
}
