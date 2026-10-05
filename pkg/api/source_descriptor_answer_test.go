package api

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// The source of a PostgreSQL catalog is named by a connection descriptor, a file of the served
// project. When that file is not there, is a folder or cannot be read, the error of the
// resolution is the one of the lookup: its own text names the cause, which quotes the path of
// the file, and the sentence for a client is built from the ID of the catalog alone. The
// entries that execute through the resolution (ExecuteSelect and ExecuteCommands) answer that
// sentence, and the cause is in the log of the server.

// descriptorStore serves the project "p1" from dir with a store whose environment "local" has
// one server and one PostgreSQL catalog, "shop", described by the file connections/prod/shop.json
// of the project.
func descriptorStore(t *testing.T, dir string) {
	t.Helper()
	serveProjectDirs(t, map[string]string{"p1": dir})
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			return mockProjectStore{
				loadEnvironmentFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Environment, error) {
					return &datatug.Environment{DbServers: []*datatug.EnvDbServer{{ServerRef: datatug.ServerRef{Driver: "postgres", Host: "db.example.com"}}}}, nil
				},
				loadEnvDbCatalogFunc: func(_ context.Context, _, _, id string, _ ...datatug.StoreOption) (datatug.DbCatalog, error) {
					return datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{
						ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: id}},
						Driver:      DriverPostgres, Path: "connections/prod/shop.json",
					}}, nil
				},
			}
		}}, nil
	}
}

func TestExecuteEntries_ADescriptorThatCannotBeReadAnswersOneSentenceBuiltFromTheCatalogID(t *testing.T) {
	ctx := context.Background()
	for _, breakage := range []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"missing", func(*testing.T, string) {}},
		{"a folder", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "connections", "prod", "shop.json", "in-it"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		for _, entry := range []struct {
			name string
			run  func() error
			want string
		}{
			{"ExecuteSelect", func() error {
				_, err := ExecuteSelect(ctx, "files", SelectRequest{Project: "p1", Environment: "local", Database: "shop", SQL: "SELECT 1"})
				return err
			}, `catalog "shop": the PostgreSQL connection descriptor cannot be read`},
			{"ExecuteCommands", func() error {
				_, err := ExecuteCommands(ctx, "files", ExecuteCommandsRequest{Project: "p1", Commands: []ExecuteCommandRequest{
					{Type: "SQL", Text: "SELECT 1", Env: "local", DB: "shop"},
				}})
				return err
			}, `command 0: catalog "shop": the PostgreSQL connection descriptor cannot be read`},
		} {
			t.Run(entry.name+" "+breakage.name, func(t *testing.T) {
				dir := t.TempDir()
				breakage.setup(t, dir)
				descriptorStore(t, dir)
				logged := captureLog(t)

				err := entry.run()

				if err == nil || err.Error() != entry.want {
					t.Fatalf("got %v, want exactly %q", err, entry.want)
				}
				var pathErr *fs.PathError
				if errors.As(err, &pathErr) {
					t.Errorf("the answer wraps the error of the operating system: %v", err)
				}
				for _, leak := range []string{dir, filepath.Base(dir), "open ", "read ", "no such file", "is a directory"} {
					if strings.Contains(err.Error(), leak) {
						t.Errorf("the answer holds %q: %v", leak, err)
					}
				}
				if !strings.Contains(logged.String(), filepath.Join(dir, "connections", "prod", "shop.json")) {
					t.Errorf("what the operating system said is not in the log: %q", logged.String())
				}
			})
		}
	}
}

// The error of the resolution keeps its own text, with the cause of the read, for a tool that
// reads it (the catalog ID is a plain name); the sentence for a client is another matter.
func TestSourceURLFromCatalog_ADescriptorThatCannotBeReadKeepsItsOwnTextAndAnswersTheCatalogID(t *testing.T) {
	dir := t.TempDir()
	catalog := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "shop"}}, Driver: DriverPostgres, Path: "connections/prod/shop.json"}}

	_, err := sourceURLFromCatalog(catalog, dir)

	if err == nil || !strings.Contains(err.Error(), `catalog "shop": open PostgreSQL connection descriptor`) {
		t.Fatalf("got %v, want the text of the open", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("the error does not wrap the error of the operating system: %v", err)
	}
	logged := captureLog(t)
	if got := sourceLookupAnswer(err); got.Error() != `catalog "shop": the PostgreSQL connection descriptor cannot be read` {
		t.Errorf("sourceLookupAnswer = %q", got)
	}
	if !strings.Contains(logged.String(), dir) {
		t.Errorf("the cause is not in the log: %q", logged.String())
	}
}

// A catalog whose ID is not a plain name is not shown, and neither is the cause.
func TestSourceURLFromCatalog_ADescriptorThatCannotBeReadDoesNotShowACatalogIDThatIsNotAPlainName(t *testing.T) {
	dir := t.TempDir()
	const secret = "http://u:s3cretpw@host/x"
	catalog := datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: secret}}, Driver: DriverPostgres, Path: "connections/prod/shop.json"}}

	_, err := sourceURLFromCatalog(catalog, dir)

	if err == nil {
		t.Fatal("want an error")
	}
	logged := captureLog(t)
	answer := sourceLookupAnswer(err)
	for _, text := range []string{answer.Error(), err.Error(), logged.String()} {
		if strings.Contains(text, "s3cretpw") || strings.Contains(text, dir) {
			t.Errorf("a text holds the ID or the path: %q", text)
		}
	}
}
