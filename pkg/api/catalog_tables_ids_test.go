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

// A catalog file that cannot be read, parsed or used says so. A plain ID is
// named, with the file error that says why; any other ID is not shown and the
// file error, which quotes its path, is left out.
func TestCatalogDbModel_NamesAPlainIDAndNeverAnyOther(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prepare  func(t *testing.T, file string)
		wantText string
		// cause is what a plain ID's message carries of the file error.
		cause string
	}{
		{"unreadable", func(t *testing.T, file string) { mustMkdir(t, file) }, "read the file of catalog", "is a directory"},
		{"unparsable", func(t *testing.T, file string) { mustWrite(t, file, "{malformed") }, "parse the file of catalog", "invalid character"},
		{"no dbModel", func(t *testing.T, file string) { mustWrite(t, file, `{"id":"x"}`) }, "has no dbModel set", ""},
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
				if plain && !strings.Contains(err.Error(), tc.cause) {
					t.Errorf("%q: a plain ID keeps the file error: %v", id, err)
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
