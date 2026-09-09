package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage/filestore"

	_ "modernc.org/sqlite" // sqlite driver, matching pkg/secureread's own fixtures
)

// executeCommandsFixtureEnv/DB name the environment/database the fixture
// project below registers.
const (
	executeCommandsFixtureEnv = "local"
	executeCommandsFixtureDB  = "widgets"
)

// newExecuteCommandsProject builds a synthetic project with a SQLite
// fixture DB (a Widget table, three rows) wired up through an environment +
// DB catalog resolveSourceURL can walk.
//
// It deliberately does NOT use datatug.ServerRef{Driver: "sqlite3"} for the
// environment's EnvDbServer (unlike a "real" sqlite3-backed environment
// would): datatug-core v0.21.0's ServerRef.Validate() has a confirmed bug
// for that exact case (its "sqlite3" switch case is missing a trailing
// `return nil`, so validation always fails — see
// skipServerRefValidateSqlite3Bug's doc comment in security_matrix_test.go,
// the same file this bug already blocks every test in). Using an allowed
// placeholder driver ("sqlserver") for the EnvDbServer's own ServerRef
// sidesteps that bug entirely without touching datatug-core: the ServerRef
// on EnvDbServer is only ever used to derive the server ID
// (EnvDbServer.GetID(), "<host>:<port>", driver-independent) that indexes
// where the DB catalog is filed; resolveSourceURL then resolves the actual
// connection scheme from the DbCatalog's own Driver field (sqlite3, set
// correctly below), which is completely independent of the EnvDbServer's
// ServerRef and is never validated against it.
func newExecuteCommandsProject(t *testing.T) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	const projectID = "execute-commands-project"

	dbPath := filepath.Join(dir, "widgets.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()
	statements := []string{
		`CREATE TABLE Widget (WidgetId INTEGER PRIMARY KEY, Name TEXT)`,
		`INSERT INTO Widget (WidgetId, Name) VALUES (1, 'Sprocket')`,
		`INSERT INTO Widget (WidgetId, Name) VALUES (2, 'Cog')`,
		`INSERT INTO Widget (WidgetId, Name) VALUES (3, 'Gear')`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	projStore := filestore.NewProjectStore(projectID, dir)
	ctx := context.Background()

	placeholderServer := datatug.ServerRef{Driver: "sqlserver", Host: "placeholder"}
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: placeholderServer}}}
	env.ID = executeCommandsFixtureEnv
	if err := projStore.SaveEnvironment(ctx, env); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}

	serverID := (&datatug.EnvDbServer{ServerRef: placeholderServer}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: dbPath, DbModel: "widgets"}}
	catalog.ID = executeCommandsFixtureDB
	if err := projStore.SaveEnvDbCatalog(ctx, executeCommandsFixtureEnv, serverID, executeCommandsFixtureDB, catalog); err != nil {
		t.Fatalf("SaveEnvDbCatalog: %v", err)
	}

	return map[string]string{projectID: dir}, projectID
}

// executeCommandsSession is an Unrestricted (--no-policies-equivalent)
// session: ExecuteCommands' access-control behaviour (limitations[]/403 for
// a policy refusal) is exactly secureread.Executor.RunNativeSQL's, already
// covered for exec/select by TestExecuteSelect_NativeSQL_Labelled and for a
// policy-refused case by TestRunQuery_HiddenColumnExplicit_Refused in
// security_matrix_test.go — this file's tests are about ExecuteCommands'
// own request/response wiring (multi-command dispatch, the
// datatug-apps-shaped request/response, the panic removal) rather than
// re-proving policy enforcement secureread already owns.
func executeCommandsSession(t *testing.T) secureread.Session {
	t.Helper()
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

// TestExecuteCommands_SingleCommand_ReturnsRows is the brief's item 2
// regression test: POST /datatug/exec/execute_commands with the exact
// request shape datatug-apps' agent.service.ts sends (a single SQL command,
// no namedParams) must return 200 with the recordset, not
// `panic("not implemented yet")`.
func TestExecuteCommands_SingleCommand_ReturnsRows(t *testing.T) {
	pathsByID, projectID := newExecuteCommandsProject(t)
	baseURL := startServeHTTPWithSession(t, pathsByID, executeCommandsSession(t))

	request := api.ExecuteCommandsRequest{
		ID: "exec-1",
		Commands: []api.ExecuteCommandRequest{
			{Type: "SQL", Text: "select WidgetId, Name from Widget order by WidgetId", Env: executeCommandsFixtureEnv, DB: executeCommandsFixtureDB},
		},
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/execute_commands?project="+projectID, request)
	if status != http.StatusOK {
		t.Fatalf("execute_commands: status %d, body %s", status, raw)
	}
	var response api.ExecuteCommandsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, raw)
	}
	if len(response.Commands) != 1 {
		t.Fatalf("Commands = %d, want 1; body %s", len(response.Commands), raw)
	}
	command := response.Commands[0]
	if len(command.Items) != 1 || command.Items[0].Type != "recordset" {
		t.Fatalf("Items = %+v, want one recordset item", command.Items)
	}
	recordset, ok := command.Items[0].Value.(map[string]any)
	if !ok {
		t.Fatalf("Items[0].Value = %T, want a QueryResultResponse-shaped object", command.Items[0].Value)
	}
	rows, ok := recordset["rows"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("rows = %+v, want 3 Widget rows; body %s", recordset["rows"], raw)
	}
}

// TestExecuteCommands_MultipleCommands_ReturnsRowsForEach proves the
// multi-command dispatch loop: each entry in Commands runs independently
// and gets its own CommandExecutionResult in order.
func TestExecuteCommands_MultipleCommands_ReturnsRowsForEach(t *testing.T) {
	pathsByID, projectID := newExecuteCommandsProject(t)
	baseURL := startServeHTTPWithSession(t, pathsByID, executeCommandsSession(t))

	request := api.ExecuteCommandsRequest{
		ID: "exec-multi",
		Commands: []api.ExecuteCommandRequest{
			{ID: "count", Type: "SQL", Text: "select count(*) as total from Widget", Env: executeCommandsFixtureEnv, DB: executeCommandsFixtureDB},
			{ID: "rows", Type: "SQL", Text: "select WidgetId from Widget where WidgetId = 2", Env: executeCommandsFixtureEnv, DB: executeCommandsFixtureDB},
		},
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/execute_commands?project="+projectID, request)
	if status != http.StatusOK {
		t.Fatalf("execute_commands: status %d, body %s", status, raw)
	}
	var response api.ExecuteCommandsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, raw)
	}
	if len(response.Commands) != 2 {
		t.Fatalf("Commands = %d, want 2; body %s", len(response.Commands), raw)
	}
	if response.Commands[0].CommandID != "count" || response.Commands[1].CommandID != "rows" {
		t.Fatalf("CommandIDs = [%q, %q], want [count, rows] in request order", response.Commands[0].CommandID, response.Commands[1].CommandID)
	}
}

// TestExecuteCommands_NamedParams_Refused is a 400, not a silently-ignored
// parameter: RunNativeSQL has no bind-parameter surface
// (REQ:opaque-sql-limitation), so a command that carries namedParams must
// be refused rather than have them dropped on the floor.
func TestExecuteCommands_NamedParams_Refused(t *testing.T) {
	pathsByID, projectID := newExecuteCommandsProject(t)
	baseURL := startServeHTTPWithSession(t, pathsByID, executeCommandsSession(t))

	request := api.ExecuteCommandsRequest{
		ID: "exec-params",
		Commands: []api.ExecuteCommandRequest{
			{
				Type: "SQL", Text: "select * from Widget where WidgetId = :id",
				Env: executeCommandsFixtureEnv, DB: executeCommandsFixtureDB,
				NamedParams: map[string]any{"id": 1},
			},
		},
	}
	status, raw := postJSON(t, baseURL, "/datatug/exec/execute_commands?project="+projectID, request)
	if status != http.StatusBadRequest {
		t.Fatalf("execute_commands(namedParams): status %d, want 400; body %s", status, raw)
	}
}

// TestExecuteCommands_EmptyRequest_Refused proves ExecuteCommandsRequest's
// Validate() path (no project query param, no commands): a 400, matching
// ExecuteSelect/RunQuery's own Validate() convention, not a panic or a
// silent no-op.
func TestExecuteCommands_EmptyRequest_Refused(t *testing.T) {
	pathsByID, _ := newExecuteCommandsProject(t)
	baseURL := startServeHTTPWithSession(t, pathsByID, executeCommandsSession(t))

	request := api.ExecuteCommandsRequest{ID: "exec-empty", Commands: nil}
	status, raw := postJSON(t, baseURL, "/datatug/exec/execute_commands", request)
	if status != http.StatusBadRequest {
		t.Fatalf("execute_commands(empty request): status %d, want 400; body %s", status, raw)
	}
}
