package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// The catalog and environment IDs of GET .../tables are request path segments,
// and a source string can be sent where an ID belongs. GetCatalogTables refuses
// anything but a plain name before it reads a file, and catalogDbModel, which
// builds the message of a catalog that is not there, names an ID only when it is
// a plain name: neither message shows a source string.
func TestProperty_GetCatalogTablesNeverEchoesASourceString(t *testing.T) {
	dir := t.TempDir()
	failed := 0
	for _, c := range sourcecases.All() {
		var texts []string
		for _, ids := range [][2]string{{"local", c.Source}, {c.Source, "chinook-local"}, {c.Source, c.Source}} {
			_, err := GetCatalogTables(dir, ids[0], ids[1])
			if err == nil || !validation.IsBadRequestError(err) {
				t.Fatalf("%s: GetCatalogTables(%q, %q) = %v, want a bad-request refusal", c.Name, ids[0], ids[1], err)
			}
			texts = append(texts, err.Error())
			_, err = catalogDbModel(dir, ids[0], ids[1])
			if !errors.Is(err, ErrCatalogNotFound) {
				t.Fatalf("%s: catalogDbModel(%q, %q) = %v, want ErrCatalogNotFound", c.Name, ids[0], ids[1], err)
			}
			texts = append(texts, err.Error())
		}
		if leaked := sourcecases.Leaks(c, texts...); len(leaked) > 0 {
			failed++
			if failed <= 20 {
				t.Errorf("%s\n  leaked %q in:\n    %s", c.Name, leaked, strings.Join(texts, "\n    "))
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d generated sources leaked through a catalog lookup error", failed)
	}
	if _, err := GetCatalogTables(dir, "local", "chinook-local"); err == nil || !strings.Contains(err.Error(), `catalog "chinook-local" in environment "local"`) {
		t.Fatalf("a plain ID should be named: %v", err)
	}
}

// A catalog file that cannot be read, parsed or used says so, in one sentence built from the
// catalog and the environment: a plain ID is named, any other ID is not shown, and the file
// error, which quotes the path of the file, is never in the answer (it is logged). A file that
// cannot be read and a file that cannot be parsed are the same answer.
func TestCatalogDbModel_NamesAPlainIDAndNeverAnyOther(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prepare  func(t *testing.T, file string)
		wantText string
		// causes are what the file error says, which no answer holds.
		causes []string
	}{
		{"unreadable", func(t *testing.T, file string) { mustMkdir(t, file) }, "could not be read", []string{"is a directory", "read "}},
		{"unparsable", func(t *testing.T, file string) { mustWrite(t, file, "{malformed") }, "could not be read", []string{"invalid character", "parse "}},
		{"no dbModel", func(t *testing.T, file string) { mustWrite(t, file, `{"id":"x"}`) }, "has no dbModel set", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, id := range []string{"broken", "my broken catalog", "ingitdb://alice:s3cretpw@host/x"} {
				dir := t.TempDir()
				file := filepath.Join(dir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, id, storage.JsonFileName(id, storage.DbCatalogFileSuffix))
				mustMkdir(t, filepath.Dir(file))
				tc.prepare(t, file)
				_, err := catalogDbModel(dir, "dev", id)
				if err == nil || !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("%q: error = %v, want %q", id, err, tc.wantText)
				}
				plain := dbcopy.SourceIDDisplay(id) == id
				if plain != strings.Contains(err.Error(), `"`+id+`"`) {
					t.Errorf("%q: the ID is named = %v, want %v: %v", id, !plain, plain, err)
				}
				if !plain && (strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "s3cretpw") || strings.Contains(err.Error(), "my broken")) {
					t.Errorf("%q: the error shows what the ID was turned into: %v", id, err)
				}
				// Whatever the ID is, the answer holds nothing of the file error or of the path.
				for _, cause := range append(tc.causes, dir, filepath.Base(dir)) {
					if strings.Contains(err.Error(), cause) {
						t.Errorf("%q: the answer holds %q: %v", id, cause, err)
					}
				}
			}
		})
	}
}

// What is listed under the model of a catalog is read from folders of the project: a folder
// that cannot be listed is the same answer as a catalog file that cannot be read, from every
// entry that lists it, and the answer holds no path.
func TestCatalogRelations_AFolderThatCannotBeListedIsOneBuiltSentence(t *testing.T) {
	for name, layout := range map[string]func(t *testing.T, models string){
		"a file where the model's folder is expected": func(t *testing.T, models string) {
			mustMkdir(t, models)
			mustWrite(t, filepath.Join(models, "shop-model"), "x")
		},
		"a file where a table's folder is expected": func(t *testing.T, models string) {
			mustMkdir(t, filepath.Join(models, "shop-model", "main"))
			mustWrite(t, filepath.Join(models, "shop-model", "main", "tables"), "x")
		},
		"a table with two files of columns": func(t *testing.T, models string) {
			table := filepath.Join(models, "shop-model", "main", "tables", "Customer")
			mustMkdir(t, table)
			mustWrite(t, filepath.Join(table, "a."+storage.ColumnsFileSuffix+".json"), `{}`)
			mustWrite(t, filepath.Join(table, "b."+storage.ColumnsFileSuffix+".json"), `{}`)
		},
		"a table whose columns file is not JSON": func(t *testing.T, models string) {
			table := filepath.Join(models, "shop-model", "main", "tables", "Customer")
			mustMkdir(t, table)
			mustWrite(t, filepath.Join(table, "Customer."+storage.ColumnsFileSuffix+".json"), `{not json`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			catalog := filepath.Join(dir, storage.EnvironmentsFolder, "dev", storage.EnvDbCatalogsFolder, "shop")
			mustMkdir(t, catalog)
			mustWrite(t, filepath.Join(catalog, storage.JsonFileName("shop", storage.DbCatalogFileSuffix)), `{"dbModel":"shop-model"}`)
			layout(t, filepath.Join(dir, storage.DbModelsFolder))

			const want = `catalog "shop" in environment "dev" could not be read`
			for entry, call := range map[string]func() error{
				"GetCatalogSchema":        func() error { _, err := GetCatalogSchema(dir, "dev", "shop"); return err },
				"GetCatalogSchemaPartial": func() error { _, err := GetCatalogSchemaPartial(dir, "dev", "shop"); return err },
				"GetCatalogTables":        func() error { _, err := GetCatalogTables(dir, "dev", "shop"); return err },
			} {
				err := call()
				if name == "a table whose columns file is not JSON" && entry == "GetCatalogTables" {
					// The tables are listed by their folders: the columns file is not read.
					if err != nil {
						t.Errorf("%s: %v, want the tables listed", entry, err)
					}
					continue
				}
				if name == "a table with two files of columns" && entry == "GetCatalogTables" {
					if err != nil {
						t.Errorf("%s: %v, want the tables listed", entry, err)
					}
					continue
				}
				if entry == "GetCatalogSchemaPartial" && strings.HasPrefix(name, "a table ") {
					// A partial load keeps the relation and says nothing of the file.
					if err != nil {
						t.Errorf("%s: %v, want the relation kept with an issue", entry, err)
					}
					continue
				}
				if err == nil || err.Error() != want {
					t.Errorf("%s: %v, want exactly %q", entry, err, want)
				}
			}
		})
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
