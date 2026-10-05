package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// Every route that reads its project from the query resolves the store of that project
// first (see ResolveStoreID), and so refuses a project this process does not serve. Three
// entries take the project from the request itself, and open its project store: they refuse
// a project that is not served too, with the same answer, before the factory of the stores
// is asked for anything.

// storeAsks counts what the store factory is asked for.
type storeAsks struct {
	factories int // stores asked for
	projects  int // project stores asked for
}

func (a *storeAsks) total() int { return a.factories + a.projects }

// serveProjectsWithCounter serves the projects (see serveProjects) and replaces the store
// factory with one that counts the stores and the project stores it hands out; a project store answers a request for a source with a file that is not
// there, so a request that passes its checks is looked up and fails. Both are undone when the
// test ends.
func serveProjectsWithCounter(t *testing.T, ids ...string) *storeAsks {
	t.Helper()
	asks := &storeAsks{}
	projectStore := mockProjectStore{
		loadEnvironmentFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Environment, error) {
			return &datatug.Environment{DbServers: []*datatug.EnvDbServer{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}, nil
		},
		loadEnvDbCatalogFunc: func(context.Context, string, string, string, ...datatug.StoreOption) (datatug.DbCatalog, error) {
			return datatug.DbCatalog{}, os.ErrNotExist
		},
		loadEnvDbCatalogsFunc: func(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			return nil, os.ErrNotExist
		},
	}
	previous := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		asks.factories++
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			asks.projects++
			return projectStore
		}}, nil
	}
	serveProjects(t, ids...)
	return asks
}

// serveProjects makes this process serve the projects, each in a folder of its own, with an
// executor that applies no policy, until the test ends.
func serveProjects(t *testing.T, ids ...string) {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	for _, id := range ids {
		paths[id] = t.TempDir()
	}
	ConfigureSecureSession(session, paths, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
}

// servedProjectEntries are the entries that take the project from the request itself.
func servedProjectEntries() map[string]func(project string) error {
	ctx := context.Background()
	return map[string]func(project string) error{
		"ExecuteSelect": func(project string) error {
			_, err := ExecuteSelect(ctx, "files", SelectRequest{Project: project, Environment: "local", Database: "chinook", SQL: "SELECT 1"})
			return err
		},
		"ExecuteSelect (structured)": func(project string) error {
			_, err := ExecuteSelect(ctx, "files", SelectRequest{Project: project, Environment: "local", Database: "chinook", From: "Customer"})
			return err
		},
		"ExecuteCommands": func(project string) error {
			_, err := ExecuteCommands(ctx, "files", ExecuteCommandsRequest{Project: project, Commands: []ExecuteCommandRequest{
				{Type: "SQL", Text: "SELECT 1", Env: "local", DB: "chinook"},
			}})
			return err
		},
		"CreateFolder": func(project string) error {
			_, err := CreateFolder(ctx, dto.CreateFolder{ProjectRef: dto.ProjectRef{ProjectID: project, StoreID: "files"}, Path: "a", Name: "b"})
			return err
		},
	}
}

func TestEntries_AProjectThatIsNotServedIsRefusedBeforeAnyStoreIsAsked(t *testing.T) {
	asks := serveProjectsWithCounter(t, "p1")
	wantText := func() string {
		_, err := ResolveStoreID("", "elsewhere")
		return err.Error()
	}()
	for name, call := range servedProjectEntries() {
		*asks = storeAsks{}
		err := call("elsewhere")
		switch {
		case err == nil:
			t.Errorf("%s: a project that is not served was not refused", name)
		case !errors.Is(err, ErrUnknownStoreID) && !validation.IsBadRequestError(err):
			// CreateFolder is answered by apicore, which gives 400 to a bad request only.
			t.Errorf("%s: a project that is not served gave %v, want %v or a bad request", name, err, ErrUnknownStoreID)
		case !strings.HasSuffix(err.Error(), wantText):
			t.Errorf("%s: the answer is %q, want the text of the routes that resolve a store: %q", name, err.Error(), wantText)
		}
		if asks.total() != 0 {
			t.Errorf("%s: a project that is not served reached the store factory (%d stores, %d project stores), want nothing", name, asks.factories, asks.projects)
		}
	}
}

// The count above means something only if the same requests for a project that is
// served do reach the project store, and are not refused.
func TestEntries_AServedProjectReachesTheProjectStore(t *testing.T) {
	asks := serveProjectsWithCounter(t, "p1")
	for name, call := range servedProjectEntries() {
		*asks = storeAsks{}
		err := call("p1")
		if err != nil && strings.Contains(err.Error(), ErrUnknownStoreID.Error()) {
			t.Errorf("%s: a served project was refused: %v", name, err)
		}
		if asks.factories == 0 || asks.projects == 0 {
			t.Errorf("%s: a served project reached %d stores and %d project stores, want both asked", name, asks.factories, asks.projects)
		}
	}
}

// A project that is neither served nor a plain name is still refused first, by the check of
// the name, and the answer shows nothing of it.
func TestEntries_AProjectThatIsNeitherServedNorAPlainNameIsRefusedAsBefore(t *testing.T) {
	asks := serveProjectsWithCounter(t, "p1")
	const secret = "../../s3cret-project"
	for name, call := range servedProjectEntries() {
		err := call(secret)
		if errors.Is(err, ErrUnknownStoreID) || err == nil || !strings.Contains(err.Error(), "[project]") || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: an unsafe project gave %v, want the refusal of the project's name", name, err)
		}
	}
	if asks.total() != 0 {
		t.Errorf("an unsafe project reached the store factory %d times, want 0", asks.total())
	}
}

func TestServedProjectDir(t *testing.T) {
	serveProjectsWithCounter(t, "p1")
	want, _ := projectDir("p1")
	if dir, err := servedProjectDir("p1"); err != nil || dir != want || dir == "" {
		t.Errorf("servedProjectDir(p1) = %q, %v, want %q", dir, err, want)
	}
	if dir, err := servedProjectDir("elsewhere"); !errors.Is(err, ErrUnknownStoreID) || dir != "" {
		t.Errorf("servedProjectDir(elsewhere) = %q, %v, want %v", dir, err, ErrUnknownStoreID)
	}
}
