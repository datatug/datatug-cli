package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/strongo/validation"
)

// An environment or a catalog ID that a client sends and that reaches a file path
// must be a plain name. The tests below send every text of sourcecases.UnsafeIdentifiers
// to the places of this package that turn a client's ID into a path, and count
// the reads: a refusal comes before the first one.

func TestValidateIdentifier(t *testing.T) {
	for _, id := range []string{"local", "chinook-local", "UAT", "a.b_c-d", "Chinook1", "données", strings.Repeat("a", 128)} {
		if err := ValidateIdentifier("environment", id); err != nil {
			t.Errorf("ValidateIdentifier(%q) = %v, want a plain name accepted", id, err)
		}
	}
	for _, c := range sourcecases.UnsafeIdentifiers() {
		err := ValidateIdentifier("catalog", c.ID)
		if err == nil || !validation.IsBadRequestError(err) || !validation.IsBadFieldValueError(err) {
			t.Errorf("%s: ValidateIdentifier(%q) = %v, want a bad-request field error", c.Name, c.ID, err)
			continue
		}
		if !strings.Contains(err.Error(), "[catalog]") {
			t.Errorf("%s: the error does not name the field: %v", c.Name, err)
		}
		if len(c.ID) >= 3 && strings.Contains(err.Error(), c.ID) {
			t.Errorf("%s: the error echoes the ID: %v", c.Name, err)
		}
	}
	// "<source id not shown>" is what SourceIDDisplay returns for an ID that is not
	// a plain name, so it must not pass for one.
	if err := ValidateIdentifier("environment", "<source id not shown>"); err == nil {
		t.Error("the text SourceIDDisplay puts in place of an ID is not a plain name")
	}
}

// countReads replaces the catalog file read with a counter for one test.
func countReads(t *testing.T) *int {
	t.Helper()
	reads := 0
	previous := readCatalogFile
	readCatalogFile = func(name string) ([]byte, error) {
		reads++
		return nil, os.ErrNotExist
	}
	t.Cleanup(func() { readCatalogFile = previous })
	return &reads
}

func TestGetCatalogTables_RefusesAnUnsafeIDWithoutReadingAnyFile(t *testing.T) {
	reads := countReads(t)
	dir := t.TempDir()

	// The counter is live: a plain pair reaches the read.
	if _, err := GetCatalogTables(dir, "local", "chinook-local"); !errors.Is(err, ErrCatalogNotFound) || *reads != 1 {
		t.Fatalf("plain IDs: err = %v, reads = %d, want ErrCatalogNotFound after 1 read", err, *reads)
	}
	*reads = 0

	for _, c := range sourcecases.UnsafeIdentifiers() {
		for position, ids := range map[string][2]string{
			"environment": {c.ID, "chinook-local"},
			"catalog":     {"local", c.ID},
			"both":        {c.ID, c.ID},
		} {
			_, err := GetCatalogTables(dir, ids[0], ids[1])
			if err == nil || !validation.IsBadRequestError(err) || errors.Is(err, ErrCatalogNotFound) {
				t.Errorf("%s in %s: GetCatalogTables = %v, want a bad-request refusal", c.Name, position, err)
			}
			if len(c.ID) >= 3 && err != nil && strings.Contains(err.Error(), c.ID) {
				t.Errorf("%s in %s: the refusal echoes the ID: %v", c.Name, position, err)
			}
		}
	}
	if *reads != 0 {
		t.Fatalf("an unsafe ID reached %d file reads, want 0", *reads)
	}
}

// A project store joins an environment ID into a path of its own, so the ID is
// checked before the store is asked.
func TestGetEnvironmentSummary_RefusesAnUnsafeEnvironmentBeforeTheStore(t *testing.T) {
	serveProjects(t, "p1") // the project is one this process serves: a store is handed out for it
	origNewStore := storage.NewDatatugStore
	defer func() { storage.NewDatatugStore = origNewStore }()
	asked := 0
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore {
			asked++
			return mockProjectStore{loadEnvironmentSummaryFunc: func(context.Context, string) (*datatug.EnvironmentSummary, error) {
				return &datatug.EnvironmentSummary{}, nil
			}}
		}}, nil
	}
	ref := func(id string) dto.ProjectItemRef {
		return dto.ProjectItemRef{ProjectRef: dto.ProjectRef{ProjectID: "p1"}, ID: id}
	}
	if _, err := GetEnvironmentSummary(context.Background(), ref("local")); err != nil || asked != 1 {
		t.Fatalf("plain ID: err = %v, store asked %d times, want 1", err, asked)
	}
	asked = 0
	for _, c := range sourcecases.UnsafeIdentifiers() {
		if c.ID == "" {
			continue // an empty ID is the missing-field error the function already gave
		}
		if _, err := GetEnvironmentSummary(context.Background(), ref(c.ID)); err == nil || !validation.IsBadRequestError(err) {
			t.Errorf("%s: GetEnvironmentSummary = %v, want a bad-request refusal", c.Name, err)
		}
	}
	if asked != 0 {
		t.Fatalf("the store was asked %d times for an unsafe ID, want 0", asked)
	}
}

// The legacy routes (exec/select and exec/execute_commands) look an environment and
// a database up in the project store, which joins each into a path of its own. They
// are checked in the request's own Validate, before any lookup, so the same check
// holds whatever calls the route's function.

// countLookups replaces the project store with one that counts the lookups of an
// environment and of a database (all three kinds a store joins into a path) and
// configures an executor, so a request that passes its checks reaches the store.
func countLookups(t *testing.T) *int {
	t.Helper()
	lookups := new(int)
	projectStore := mockProjectStore{
		loadEnvironmentFunc: func(context.Context, string, ...datatug.StoreOption) (*datatug.Environment, error) {
			*lookups++
			return &datatug.Environment{DbServers: []*datatug.EnvDbServer{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}, nil
		},
		loadEnvDbCatalogFunc: func(context.Context, string, string, string, ...datatug.StoreOption) (datatug.DbCatalog, error) {
			*lookups++
			return datatug.DbCatalog{}, os.ErrNotExist
		},
		loadEnvDbCatalogsFunc: func(context.Context, string, ...datatug.StoreOption) (datatug.DbCatalogs, error) {
			*lookups++
			return nil, os.ErrNotExist
		},
	}
	previous := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return mockStore{getProjectStoreFunc: func(string) datatug.ProjectStore { return projectStore }}, nil
	}
	t.Cleanup(func() { storage.NewDatatugStore = previous })
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	ConfigureSecureSession(session, map[string]string{"p1": t.TempDir()}, Capabilities{})
	t.Cleanup(func() { ConfigureSecureSession(secureread.Session{}, nil, Capabilities{}) })
	return lookups
}

// unsafeRefusal reports what is wrong with err, the answer to a request that holds
// the unsafe ID c: a bad-request error that names field and echoes nothing of c.
func unsafeRefusal(err error, field string, c sourcecases.UnsafeIdentifier) string {
	switch {
	case err == nil:
		return "no error"
	case !validation.IsBadRequestError(err):
		return "not a bad-request error: " + err.Error()
	case c.ID != "" && !strings.Contains(err.Error(), "["+field+"]"):
		return "does not name the field " + field + ": " + err.Error()
	case len(c.ID) >= 3 && strings.Contains(err.Error(), c.ID):
		return "echoes the ID: " + err.Error()
	}
	return ""
}

func TestExecuteSelect_RefusesAnUnsafeEnvironmentOrDatabaseBeforeAnyLookup(t *testing.T) {
	lookups := countLookups(t)
	ctx := context.Background()
	request := func(environment, database string) SelectRequest {
		return SelectRequest{Project: "p1", Environment: environment, Database: database, SQL: "SELECT 1"}
	}

	// The counter is live: a plain pair reaches the store.
	if _, err := ExecuteSelect(ctx, "files", request("local", "chinook")); err == nil || *lookups == 0 {
		t.Fatalf("plain IDs: err = %v, lookups = %d, want a lookup that finds nothing", err, *lookups)
	}
	*lookups = 0

	for _, c := range sourcecases.UnsafeIdentifiers() {
		for field, pair := range map[string][2]string{"environment": {c.ID, "chinook"}, "database": {"local", c.ID}} {
			_, err := ExecuteSelect(ctx, "files", request(pair[0], pair[1]))
			if problem := unsafeRefusal(err, field, c); problem != "" {
				t.Errorf("%s as the %s: ExecuteSelect: %s", c.Name, field, problem)
			}
			// The request's own check gives the same answer.
			if problem := unsafeRefusal(request(pair[0], pair[1]).Validate(), field, c); problem != "" {
				t.Errorf("%s as the %s: Validate: %s", c.Name, field, problem)
			}
		}
	}
	if *lookups != 0 {
		t.Fatalf("an unsafe ID reached %d store lookups, want 0", *lookups)
	}
}

func TestExecuteCommands_RefusesAnUnsafeEnvironmentOrDatabaseBeforeAnyLookup(t *testing.T) {
	lookups := countLookups(t)
	ctx := context.Background()
	request := func(environment, database string) ExecuteCommandsRequest {
		return ExecuteCommandsRequest{Project: "p1", Commands: []ExecuteCommandRequest{
			{Type: "SQL", Text: "SELECT 1", Env: "local", DB: "chinook"},
			{Type: "SQL", Text: "SELECT 1", Env: environment, DB: database},
		}}
	}

	// The counter is live: a plain pair reaches the store.
	if _, err := ExecuteCommands(ctx, "files", request("local", "chinook")); err == nil || *lookups == 0 {
		t.Fatalf("plain IDs: err = %v, lookups = %d, want a lookup that finds nothing", err, *lookups)
	}
	*lookups = 0

	for _, c := range sourcecases.UnsafeIdentifiers() {
		for field, pair := range map[string][2]string{"env": {c.ID, "chinook"}, "db": {"local", c.ID}} {
			// The first command is plain and the second is not: no command of the
			// request is looked up before the whole request has been checked.
			_, err := ExecuteCommands(ctx, "files", request(pair[0], pair[1]))
			if problem := unsafeRefusal(err, field, c); problem != "" {
				t.Errorf("%s as the %s: ExecuteCommands: %s", c.Name, field, problem)
			}
			if problem := unsafeRefusal(request(pair[0], pair[1]).Validate(), field, c); problem != "" {
				t.Errorf("%s as the %s: Validate: %s", c.Name, field, problem)
			}
		}
	}
	if *lookups != 0 {
		t.Fatalf("an unsafe ID reached %d store lookups, want 0", *lookups)
	}
}
