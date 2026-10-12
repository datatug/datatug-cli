package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

// The environment and the database of a source are looked up in the store of the project,
// whose error quotes the path it built from the project folder and says whether that path is
// missing, a file or a folder. resolveSourceURL's own error is the one of the lookup (see
// LookupError), and the entries that execute through it (ExecuteSelect and ExecuteCommands)
// answer one sentence built from the kind and the IDs, whatever the store said; what it said
// goes to the log of the server.

// lookupFailing serves the project "p1" with a store whose lookups fail with cause, and
// returns what the log of the server holds.
func lookupFailing(t *testing.T, cause error) *strings.Builder {
	t.Helper()
	serveProjects(t, "p1")
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			return mockProjectStore{
				loadEnvironmentFunc: func(_ context.Context, id string, _ ...datatug.StoreOption) (*datatug.Environment, error) {
					if id == "broken-catalogs" {
						return &datatug.Environment{DbServers: []*datatug.EnvDbServer{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}, nil
					}
					return nil, cause
				},
				loadEnvDbCatalogFunc: func(context.Context, string, string, string, ...datatug.StoreOption) (datatug.DbCatalog, error) {
					return datatug.DbCatalog{}, cause
				},
			}
		}}, nil
	}
	return captureLog(t)
}

func TestExecuteEntries_ALookupThatFailsAnswersOneSentenceBuiltFromTheKindAndTheIDs(t *testing.T) {
	cause := errors.New("failed to load environment[local] from /srv/projects/p1/environments/local/local.env.json: open /srv/projects/p1/environments/local/local.env.json: no such file or directory")
	logged := lookupFailing(t, cause)
	ctx := context.Background()

	for _, c := range []struct {
		name string
		run  func() error
		want string
	}{
		{"ExecuteSelect, an environment", func() error {
			_, err := ExecuteSelect(ctx, "files", SelectRequest{Project: "p1", Environment: "local", Database: "chinook", SQL: "SELECT 1"})
			return err
		}, `environment "local" not found`},
		{"ExecuteSelect, a database", func() error {
			_, err := ExecuteSelect(ctx, "files", SelectRequest{Project: "p1", Environment: "broken-catalogs", Database: "chinook", SQL: "SELECT 1"})
			return err
		}, `database "chinook" not found in environment "broken-catalogs"`},
		{"ExecuteCommands, an environment", func() error {
			_, err := ExecuteCommands(ctx, "files", ExecuteCommandsRequest{Project: "p1", Commands: []ExecuteCommandRequest{
				{Type: "SQL", Text: "SELECT 1", Env: "local", DB: "chinook"},
			}})
			return err
		}, `command 0: environment "local" not found`},
		{"ExecuteCommands, a database", func() error {
			_, err := ExecuteCommands(ctx, "files", ExecuteCommandsRequest{Project: "p1", Commands: []ExecuteCommandRequest{
				{Type: "SQL", Text: "SELECT 1", Env: "broken-catalogs", DB: "chinook"},
			}})
			return err
		}, `command 0: database "chinook" not found in environment "broken-catalogs"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			logged.Reset()
			err := c.run()
			if err == nil || err.Error() != c.want {
				t.Fatalf("got %v, want exactly %q", err, c.want)
			}
			if errors.Is(err, cause) {
				t.Errorf("the answer wraps what the store said")
			}
			if !strings.Contains(logged.String(), "/srv/projects/p1/environments/local/local.env.json") {
				t.Errorf("what the store said is not in the log: %q", logged.String())
			}
		})
	}
}

// A lookup whose error is not the failure of a lookup of a store (an environment with no db
// servers, whose message holds only the IDs) is answered as it is.
func TestSourceLookupAnswer_AnotherErrorIsUnchanged(t *testing.T) {
	_, _, err := resolveSourceURL(context.Background(), mockProjectStoreWithoutServers(), "no-servers", "chinook", t.TempDir())
	if err == nil {
		t.Fatal("want an error")
	}
	if got := sourceLookupAnswer(err); got != err {
		t.Errorf("sourceLookupAnswer(%v) = %v, want it unchanged", err, got)
	}
	other := errors.New("something else")
	if got := sourceLookupAnswer(other); got != other {
		t.Errorf("sourceLookupAnswer(%v) = %v, want it unchanged", other, got)
	}
}

func mockProjectStoreWithoutServers() mockProjectStore {
	return mockProjectStore{loadEnvironmentFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Environment, error) {
		return &datatug.Environment{}, nil
	}}
}

// An ID that is not a plain name (a source string with a password, which a client may send
// where an ID belongs) is not shown in the answer, and neither is the cause.
func TestSourceLookupAnswer_AnIDThatIsNotAPlainNameIsNotShown(t *testing.T) {
	cause := errors.New("open /srv/p/environments/x: no such file or directory")
	store := mockProjectStore{
		loadEnvironmentFunc: func(_ context.Context, _ string, _ ...datatug.StoreOption) (*datatug.Environment, error) {
			return nil, cause
		},
	}
	const secret = "http://u:s3cretpw@host/x"

	_, _, err := resolveSourceURL(context.Background(), store, secret, "chinook", t.TempDir())

	if err == nil {
		t.Fatal("want an error")
	}
	logged := captureLog(t)
	answer := sourceLookupAnswer(err)
	if strings.Contains(answer.Error(), "s3cretpw") || strings.Contains(answer.Error(), "/srv/p") {
		t.Errorf("the answer holds the ID or the cause: %v", answer)
	}
	if strings.Contains(logged.String(), "s3cretpw") {
		t.Errorf("the log holds the password: %q", logged.String())
	}
}

// What a lookup wrapped (the store's error) is still found through the failure, for a tool that
// reads it (a plain ID names the cause); the sentence of the answer is another matter.
func TestSourceLookupError_UnwrapsToTheLookupsOwnError(t *testing.T) {
	cause := errors.New("open /srv/p/environments/local/local.env.json: no such file or directory")
	store := mockProjectStore{loadEnvironmentFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Environment, error) {
		return nil, cause
	}}

	_, _, err := resolveSourceURL(context.Background(), store, "local", "chinook", t.TempDir())

	if !errors.Is(err, cause) {
		t.Errorf("the lookup's own error does not wrap what the store said: %v", err)
	}
	if !strings.Contains(err.Error(), "load environment") {
		t.Errorf("the lookup's own error is %q, want the one LookupError builds", err.Error())
	}
}

// The loaders of a project answer one sentence built from the kind of what was asked for, and
// the ID of the project or of the item as it may be shown: what the store said, which quotes the
// path it built from the project folder, is in the log and is not in the answer.
func TestProjectLoaders_AFailureAnswersOneBuiltSentence(t *testing.T) {
	cause := errors.New("failed to load project file: open /srv/projects/p1/datatug-project.json: no such file or directory")
	serveProjects(t, "p1")
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{
			getProjectsFunc: func(context.Context) ([]datatug.ProjectBrief, error) { return nil, cause },
			getProjectStoreFunc: func(string) datatug.ProjectStore {
				return mockProjectStore{
					loadProjectFunc: func(context.Context, ...datatug.StoreOption) (*datatug.Project, error) { return nil, cause },
					loadEntitiesFunc: func(context.Context, ...datatug.StoreOption) (datatug.Entities, error) {
						return nil, cause
					},
					loadEnvironmentSummaryFunc: func(context.Context, string) (*datatug.EnvironmentSummary, error) { return nil, cause },
				}
			},
		}, nil
	}
	ctx := context.Background()
	logged := captureLog(t)

	for _, tc := range []struct {
		name string
		call func() error
		want string
	}{
		{"GetProjects", func() error { _, err := GetProjects(ctx, "local"); return err }, "the projects could not be listed"},
		{"GetProjectFull", func() error { _, err := GetProjectFull(ctx, dto.ProjectRef{ProjectID: "p1"}); return err }, `project "p1" not found`},
		{"GetAllEntities", func() error { _, err := GetAllEntities(ctx, dto.ProjectRef{ProjectID: "p1"}); return err }, `entities of project "p1" could not be loaded`},
		{"GetEnvironmentSummary", func() error {
			_, err := GetEnvironmentSummary(ctx, dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1"}, ID: "local"})
			return err
		}, `environment "local" not found`},
	} {
		logged.Reset()
		err := tc.call()
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: got %v, want exactly %q", tc.name, err, tc.want)
		}
		if errors.Is(err, cause) {
			t.Errorf("%s: the answer wraps what the store said", tc.name)
		}
		if !strings.Contains(logged.String(), "/srv/projects/p1/datatug-project.json") {
			t.Errorf("%s: what the store said is not in the log: %q", tc.name, logged.String())
		}
	}
}
