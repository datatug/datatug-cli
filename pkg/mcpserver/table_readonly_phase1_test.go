package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/agentaccess"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-cli/pkg/server/endpoints"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
	"github.com/julienschmidt/httprouter"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sneat-co/sneat-go-core/apicore"
)

// Uses the normal registered handlers and native SQLite, with an HTTP transport
// directly driving Recorder. No listener, service, fake query executor or DSN
// chosen by a tool request is involved.
func phase1MCPFixture(t *testing.T) (*Client, *httprouter.Router, string, datatug.ProjectStore) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("exact-table-readonly-v1 drive-letter paths are not supported in this release")
	}
	t.Setenv(agentaccess.DirEnv, t.TempDir())
	dir := t.TempDir()
	var canonicalErr error
	dir, canonicalErr = filepath.EvalSymlinks(dir)
	if canonicalErr != nil {
		t.Fatal(canonicalErr)
	}
	project := "phase1-" + strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())
	path := filepath.Join(dir, "tables.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"A", "B", "C"} {
		if _, err := db.Exec(`CREATE TABLE ` + table + ` (id INTEGER PRIMARY KEY, label TEXT); INSERT INTO ` + table + ` VALUES (1,'value-` + table + `')`); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A second real physical DB with the same table name exists locally, but
	// is outside this server's one registered connection.
	other, err := sql.Open("sqlite", filepath.Join(dir, "other.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`CREATE TABLE A (id INTEGER PRIMARY KEY, label TEXT); INSERT INTO A VALUES (1,'unregistered-connection-a')`); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	store := filestore.NewProjectStore(project, dir)
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}}}
	env.ID = "local"
	if err := store.SaveEnvironment(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	serverID := (&datatug.EnvDbServer{ServerRef: datatug.ServerRef{Driver: "sqlite3"}}).GetID()
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: path, DbModel: "fixture"}}
	catalog.ID = "fixture"
	if err := store.SaveEnvDbCatalog(context.Background(), "local", serverID, catalog.ID, catalog); err != nil {
		t.Fatal(err)
	}
	otherCatalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "sqlite3", Path: filepath.Join(dir, "other.sqlite"), DbModel: "other"}}
	otherCatalog.ID = "other"
	if err := store.SaveEnvDbCatalog(context.Background(), "local", serverID, otherCatalog.ID, otherCatalog); err != nil {
		t.Fatal(err)
	}
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "policies", "tables.yaml"), `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: exact-tables}
default: deny
scopes:
  - path: /A
    rules: [{id: a, effect: allow, operations: [query]}]
  - path: /C
    rules: [{id: c, effect: allow, operations: [query]}]
`)
	join := func(table string) string {
		return "from:\n  name: A\n  alias: a\n  joins:\n    - from: {name: " + table + ", alias: j}\n      on: [{left: {field: id, source: a}, op: '==', right: {field: id, source: j}}]\ncolumns: [{field: label, source: j, as: label}]\n"
	}
	for id, doc := range map[string]string{"a": "from: {name: A}\ncolumns: [{field: label}]\nwhere: {op: '==', left: {field: id}, right: {param: id}}\n", "b": "from: {name: B}\n", "ac": join("C"), "ab": join("B"), "mutation": "delete: {from: A}\n"} {
		parameters := []map[string]any{}
		if id == "a" {
			parameters = append(parameters, map[string]any{"id": "id", "type": "integer", "isRequired": true})
		}
		data, err := json.Marshal(map[string]any{"createdAt": "2026-01-01T00:00:00Z", "id": id, "title": id, "type": "TugQL", "parameters": parameters})
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(dir, "queries", id+".query.json"), string(data))
		write(filepath.Join(dir, "queries", id+".query.tugql.yaml"), doc)
	}
	write(filepath.Join(dir, "queries", "raw.query.json"), `{"createdAt":"2026-01-01T00:00:00Z","id":"raw","title":"Raw query","type":"SQL","text":"DELETE FROM A"}`)
	write(filepath.Join(dir, "queries", "raw.query.sql"), "DELETE FROM A")
	for _, table := range []string{"A", "B"} {
		if err := os.MkdirAll(filepath.Join(dir, "dbmodels", "fixture", "main", "tables", table), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	filestore.SetProjectPath(project, dir)
	previousAuth := apicore.GetAuthTokenFromHttpRequest
	apicore.GetAuthTokenFromHttpRequest = api.AuthTokenFromHTTPRequest
	t.Cleanup(func() { apicore.GetAuthTokenFromHttpRequest = previousAuth })
	previousStore := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) {
		return filestore.NewStore("files", map[string]string{project: dir})
	}
	t.Cleanup(func() {
		storage.NewDatatugStore = previousStore
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	config, err := json.Marshal(map[string]any{"profile": secureread.ExactTableReadProfile, "project": project, "environment": "local", "catalog": "fixture", "sqliteFile": path, "requiredPolicies": []string{"policies/tables.yaml"}, "principal": map[string]any{"id": "operator"}})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, ".datatug", "security.json"), string(config))
	session, declared, err := secureread.LoadBoundSession(context.Background(), dir, secureread.BoundSessionOptions{Required: true})
	if !declared {
		t.Fatal("profile not loaded")
	}
	if err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{project: dir}, api.Capabilities{})
	router := httprouter.New()
	endpoints.RegisterDatatugHandlers("", router, endpoints.RegisterAllHandlers, func(h http.HandlerFunc) http.HandlerFunc { return h }, func(r *http.Request) (context.Context, error) { return r.Context(), nil }, apicore.Execute)
	client, err := NewClient("http://127.0.0.1:18989")
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code >= http.StatusBadRequest {
			t.Logf("owned fixture route %s status=%d body=%s", r.URL.Path, w.Code, w.Body.String())
		}
		return w.Result(), nil
	})
	t.Cleanup(client.Close)
	return client, router, project, store
}

func TestMCPSavedTugQLExactTableReadonlyJourney(t *testing.T) {
	client, _, project, _ := phase1MCPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := New(client, "phase1").Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "phase1-test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	call := func(tool string, args map[string]any, allowed bool) string {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if !allowed {
			if err == nil && result != nil && !result.IsError {
				t.Fatalf("forbidden MCP operation succeeded: %s", tool)
			}
			raw, _ := json.Marshal(result)
			for _, forbidden := range []string{"value-B", "unregistered-connection-a"} {
				if strings.Contains(string(raw), forbidden) {
					t.Fatalf("denied MCP operation leaked a row: %s", raw)
				}
			}
			return string(raw)
		}
		if err != nil || result == nil || result.IsError {
			raw, _ := json.Marshal(result)
			t.Fatalf("MCP %s failed: %s %v", tool, raw, err)
		}
		raw, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	description := call("datatug_describe_project", map[string]any{"project": project}, true)
	if !strings.Contains(description, "fixture") || strings.Contains(description, `"other"`) || strings.Contains(description, ".sqlite") {
		t.Fatalf("source discovery escaped binding: %s", description)
	}
	listed := call("datatug_list_queries", map[string]any{"project": project}, true)
	if !strings.Contains(listed, `"type":"TugQL"`) || !strings.Contains(listed, `"id":"a"`) {
		t.Fatalf("saved TugQL undiscoverable: %s", listed)
	}
	tables := call("datatug_list_tables", map[string]any{"project": project, "environment": "local", "source": "fixture"}, true)
	if !strings.Contains(tables, `"name":"A"`) || strings.Contains(tables, `"name":"B"`) {
		t.Fatalf("metadata restriction: %s", tables)
	}
	args := func(id string) map[string]any {
		return map[string]any{"project": project, "environment": "local", "source": "fixture", "queryId": id, "parameters": map[string]string{}}
	}
	a := args("a")
	a["parameters"] = map[string]string{"id": "1"}
	out := call("datatug_run_query", a, true)
	if !strings.Contains(out, "value-A") || !strings.Contains(out, `"executionProfile":"protected"`) {
		t.Fatalf("typed saved TugQL result: %s", out)
	}
	var typed struct {
		BindingsApplied []binding `json:"bindingsApplied"`
	}
	if err := json.Unmarshal([]byte(out), &typed); err != nil || len(typed.BindingsApplied) != 1 {
		t.Fatalf("declared integer binding changed: %s %v", out, err)
	}
	value := typed.BindingsApplied[0].Value
	if value.Scalar == nil || value.Set != nil || *value.Scalar != apicontract.NewIntegerValue("1") {
		t.Fatalf("declared integer binding changed: %s", out)
	}
	if out := call("datatug_run_query", args("ac"), true); !strings.Contains(out, "value-C") {
		t.Fatalf("actual permitted join: %s", out)
	}
	for _, id := range []string{"b", "ab"} {
		if failure := call("datatug_run_query", args(id), false); !strings.Contains(failure, "ACCESS_DENIED") {
			t.Fatalf("table denial did not reach the policy path: %s", failure)
		}
	}
	if failure := call("datatug_run_query", args("mutation"), false); !strings.Contains(failure, "INVALID_REQUEST") {
		t.Fatalf("mutation category: %s", failure)
	}
	if failure := call("datatug_run_query", args("raw"), false); !strings.Contains(failure, "UNSUPPORTED_PROTECTED_EXECUTION") {
		t.Fatalf("raw grant refusal changed: %s", failure)
	}
	for field, value := range map[string]any{"principal": "admin", "policy": "allow-all", "dsn": "sqlite://" + filepath.Join(filestore.GetProjectPath(project), "other.sqlite"), "allowWrites": true, "connection": "other"} {
		forged := args("ac")
		forged[field] = value
		call("datatug_run_query", forged, false)
	}
	if detail := call("datatug_describe_table", map[string]any{"project": project, "environment": "local", "source": "fixture", "schema": "main", "table": "A"}, true); !strings.Contains(detail, "label") {
		t.Fatalf("allowed schema: %s", detail)
	}
	for _, table := range []string{"B", "Missing"} {
		if failure := call("datatug_describe_table", map[string]any{"project": project, "environment": "local", "source": "fixture", "schema": "main", "table": table}, false); !strings.Contains(failure, "ACCESS_DENIED") {
			t.Fatalf("schema denial: %s", failure)
		}
	}
	if doc := call("datatug_get_query", map[string]any{"project": project, "queryId": "a"}, true); !strings.Contains(doc, "TugQL") || !strings.Contains(doc, "from:") {
		t.Fatalf("TugQL definition: %s", doc)
	}
	zero := args("a")
	zero["parameters"] = map[string]string{"id": "999"}
	if result := call("datatug_run_query", zero, true); strings.Contains(result, "value-A") || !strings.Contains(result, `"rows":[]`) {
		t.Fatalf("zero row result: %s", result)
	}
	for _, tool := range []string{"datatug_list_tables", "datatug_describe_table"} {
		input := map[string]any{"project": project, "environment": "local", "source": "other"}
		if tool == "datatug_describe_table" {
			input["schema"] = "main"
			input["table"] = "A"
		}
		call(tool, input, false)
	}

	other := args("a")
	other["source"] = "other"
	other["parameters"] = map[string]string{"id": "1"}
	if failure := call("datatug_run_query", other, false); !strings.Contains(failure, "TARGET_REQUIRED") {
		t.Fatalf("other source category: %s", failure)
	}
	call("delete_table", map[string]any{}, false)
	// A real successful read after all rejected writes still returns the owned row.
	if out := call("datatug_run_query", a, true); !strings.Contains(out, "value-A") {
		t.Fatalf("data changed: %s", out)
	}
}

func TestMCPSavedQueryExecutionProfilesStayDistinct(t *testing.T) {
	t.Setenv(agentaccess.DirEnv, t.TempDir())
	client, err := NewClient("http://127.0.0.1:18989")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, test := range []struct {
		kind    datatug.QueryType
		profile string
		allowed bool
	}{
		{datatug.QueryTypeSQL, apicontract.ExecutionProfileOpaquePrivileged, true},
		{datatug.QueryTypeSQL, apicontract.ExecutionProfileProtected, false},
		{datatug.QueryTypeTugQL, apicontract.ExecutionProfileProtected, true},
		{datatug.QueryTypeTugQL, apicontract.ExecutionProfileOpaquePrivileged, false},
	} {
		client.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
			result := apicontract.Result{Recordset: apicontract.Recordset{Columns: []apicontract.Column{{Name: "label", Type: "string"}}, Rows: [][]apicontract.TypedValue{{apicontract.NewStringValue("allowed")}}}, Limitations: []apicontract.Limitation{}, BindingsApplied: []apicontract.Binding{}, Provenance: apicontract.Provenance{Source: "fixture", QueryID: "a", Mode: "live", ObservedAt: "2026-11-02T00:00:00Z", ExecutionProfile: test.profile}}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})
		q := &datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{Type: test.kind}}
		_, err := client.executeQuery(context.Background(), runInput{Project: "p", Environment: "local", Source: "fixture", QueryID: "a"}, q, apicontract.AgentInfo{SecurityContextID: "fixed"})
		if (err == nil) != test.allowed {
			t.Fatalf("type=%s profile=%s err=%v", test.kind, test.profile, err)
		}
	}
	if _, err := client.executeQuery(context.Background(), runInput{}, nil, apicontract.AgentInfo{}); err == nil {
		t.Fatal("nil saved definition admitted")
	}
}

func TestMCPSavedUnsupportedExecutableTypesRefuse(t *testing.T) {
	// Saved HTTP and mutation definitions must never acquire the newly enabled
	// structured profile merely because they are valid project metadata.
	for _, kind := range []datatug.QueryType{datatug.QueryTypeHTTP, "DELETE"} {
		if _, err := publicQuery(&datatug.QueryDef{Type: kind}, "q", true); err == nil {
			t.Fatalf("unsupported saved executable type exposed: %s", kind)
		}
	}
}

func TestBoundSourceDirectHandlersRefuseUnqualifiedRoutesAndChangedPolicy(t *testing.T) {
	_, router, project, _ := phase1MCPFixture(t)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = strings.NewReader(string(data))
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://127.0.0.1:18989"+path, reader)
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		return w
	}
	for _, route := range []struct{ method, path string }{{http.MethodGet, "/datatug/results/owned-result"}, {http.MethodGet, "/datatug/dbserver-databases"}, {http.MethodPost, "/datatug/session-context/commands"}, {http.MethodPost, "/datatug/queries/save_query"}} {
		w := request(route.method, route.path, map[string]any{})
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "value-A") {
			t.Fatalf("unqualified route %s: %d %s", route.path, w.Code, w.Body.String())
		}
	}
	root := filestore.GetProjectPath(project)
	// Same saved-query endpoint, not MCP schema filtering: executable types are
	// refused by the server before HTTP/native dispatch. The URL is never dialed.
	for _, kind := range []string{"HTTP", "JavaScript", "SQL"} {
		id := strings.ToLower(kind) + "-direct"
		doc := map[string]any{"createdAt": "2026-01-01T00:00:00Z", "id": id, "title": id, "type": kind, "parameters": []any{}}
		data, _ := json.Marshal(doc)
		if err := os.WriteFile(filepath.Join(root, "queries", id+".query.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		body := apicontract.ExecutionRequest{Project: project, Environment: "local", Source: "fixture", SecurityContextID: api.SecurityContextID(), QueryID: id, Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{}, Mode: apicontract.ProvenanceModeLive}
		w := request(http.MethodPost, "/datatug/exec/run_query", body)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "UNSUPPORTED_PROTECTED_EXECUTION") {
			t.Fatalf("direct executable type %s: %d %s", kind, w.Code, w.Body.String())
		}
	}
	body := apicontract.ExecutionRequest{Project: project, Environment: "local", Source: "fixture", SecurityContextID: api.SecurityContextID(), TugQL: "from: {name: A}\n", Parameters: map[string]apicontract.TypedValueOrSet{}, BindingOrigins: []apicontract.BindingOriginEntry{}, Mode: apicontract.ProvenanceModeLive}
	w := request(http.MethodPost, "/datatug/exec/run_query", body)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "value-A") {
		t.Fatalf("direct read: %d %s", w.Code, w.Body.String())
	}
	for _, table := range []string{"A", "B"} {
		w = request(http.MethodGet, "/datatug/exec/select?project="+project+"&env=local&db=fixture&from="+table, nil)
		if table == "A" {
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "value-A") {
				t.Fatalf("table route: %d %s", w.Code, w.Body.String())
			}
		} else if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "value-B") {
			t.Fatalf("denied table route: %d %s", w.Code, w.Body.String())
		}
	}
	personal := request(http.MethodGet, "/datatug/queries/all_queries?project="+project+"&root=personal", nil)
	if personal.Code != http.StatusForbidden {
		t.Fatalf("personal root escaped configured project: %d %s", personal.Code, personal.Body.String())
	}
	shared := request(http.MethodGet, "/datatug/queries/all_queries?project="+project+"&root=shared", nil)
	if shared.Code != http.StatusOK {
		t.Fatalf("default shared root refused: %d %s", shared.Code, shared.Body.String())
	}
	wrongProject := request(http.MethodGet, "/datatug/projects/project_full?storage=files&id=other-project", nil)
	if wrongProject.Code != http.StatusForbidden {
		t.Fatalf("project substitution: %d %s", wrongProject.Code, wrongProject.Body.String())
	}
	originalID := api.SecurityContextID()
	if err := os.WriteFile(filepath.Join(root, "policies", "tables.yaml"), []byte("policy removed"), 0600); err != nil {
		t.Fatal(err)
	}
	w = request(http.MethodPost, "/datatug/exec/run_query", body)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "value-A") || api.ValidateSecurityContext(originalID) {
		t.Fatalf("changed policy released output: %d %s", w.Code, w.Body.String())
	}
}

func TestSavedTugQLMetadataCredentialGuard(t *testing.T) {
	q := &datatug.QueryDef{Type: datatug.QueryTypeTugQL, Text: "from: {name: A}\n# postgres://reader:embedded-password@host.invalid/db\n"}
	if _, err := publicQuery(q, "owned", true); err == nil {
		t.Fatal("TugQL metadata released embedded credential")
	}
}

func TestBoundSourceSavedParameterCannotOverrideFixedPrincipal(t *testing.T) {
	client, _, project, _ := phase1MCPFixture(t)
	root := filestore.GetProjectPath(project)
	db, err := sql.Open("sqlite", filepath.Join(root, "tables.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE A ADD COLUMN ownerID TEXT; UPDATE A SET ownerID='alice',label='alice-owned-row'; INSERT INTO A(id,label,ownerID) VALUES(2,'bob-forbidden-row','bob')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, ".datatug", "security.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["principal"] = map[string]any{"id": "alice"}
	data, _ = json.Marshal(cfg)
	if err = os.WriteFile(cfgPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	policy := `apiVersion: dalgo.io/access/v1
kind: AccessPolicy
metadata: {name: mandatory-owner}
default: deny
scopes:
  - path: /A
    rules:
      - id: owner
        effect: allow
        operations: [query]
        where: {op: '==', left: {field: ownerID}, right: {param: currentUser}}
`
	if err = os.WriteFile(filepath.Join(root, "policies", "tables.yaml"), []byte(policy), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(map[string]any{"id": "owner", "title": "Owned rows", "type": "TugQL", "createdAt": "2026-01-01T00:00:00Z", "parameters": []any{map[string]any{"id": "currentUser", "type": "string"}}})
	if err = os.WriteFile(filepath.Join(root, "queries", "owner.query.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "queries", "owner.query.tugql.yaml"), []byte("from: {name: A}\ncolumns: [{field: label}]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
	if err != nil || !declared {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(session, map[string]string{project: root}, api.Capabilities{})
	request := runInput{Project: project, Environment: "local", Source: "fixture", QueryID: "owner", Parameters: map[string]string{}}
	first, err := client.runQuery(context.Background(), request)
	raw, _ := json.Marshal(first)
	if err != nil || !strings.Contains(string(raw), "alice-owned-row") || strings.Contains(string(raw), "bob-forbidden-row") {
		t.Fatalf("trusted principal baseline: %v %s", err, raw)
	}
	request.Parameters = map[string]string{"currentUser": "bob"}
	out, err := client.runQuery(context.Background(), request)
	raw, _ = json.Marshal(out)
	if err == nil && (!strings.Contains(string(raw), "alice-owned-row") || strings.Contains(string(raw), "bob-forbidden-row")) {
		t.Fatalf("saved parameter overrode fixed alice principal: %s", raw)
	}
}
