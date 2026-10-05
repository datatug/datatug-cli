package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/internal/sourcecases"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
)

// The ID of a board, an entity, a recordset definition, a db server and a folder, the
// host and the driver of a db server and the project of any of them are joined into file
// paths by the project store. Every route that takes one refuses what is not a plain name
// (an ID with folders: not plain names between slashes; a host: not a host) with a 400
// that names the field and echoes nothing, before the project store is asked for, over the
// real request lifecycle, a real project folder and a counter of the project stores asked.

type askCountingStore struct {
	storage.Store
	asked *int
}

func (s askCountingStore) GetProjectStore(id string) datatug.ProjectStore {
	*s.asked++
	return s.Store.GetProjectStore(id)
}

// countProjectStoreAsks wraps the store factory a test has wired, so every project store
// it hands out is counted.
func countProjectStoreAsks(t *testing.T) *int {
	t.Helper()
	asked := new(int)
	inner := storage.NewDatatugStore
	storage.NewDatatugStore = func(id string) (storage.Store, error) {
		store, err := inner(id)
		if err != nil {
			return nil, err
		}
		return askCountingStore{Store: store, asked: asked}, nil
	}
	t.Cleanup(func() { storage.NewDatatugStore = inner })
	return asked
}

// sendBody serves one request with a JSON body through handler.
func sendBody(t *testing.T, handler http.HandlerFunc, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(method, target, bytes.NewReader(raw)))
	return w
}

func sendQuery(handler http.HandlerFunc, method, path string, query url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(method, path+"?"+query.Encode(), nil))
	return w
}

type itemRoute struct {
	name     string
	position string
	unsafe   []sourcecases.UnsafeIdentifier
	// emptyOK is true when an empty value is valid in this position.
	emptyOK bool
	// send serves the request with value in the position under test, and plain values in
	// the others; project is the plain project's ID.
	send func(value string) *httptest.ResponseRecorder
}

func itemRoutes(t *testing.T, project string) []itemRoute {
	identifiers := sourcecases.UnsafeIdentifiers()
	paths := sourcecases.UnsafePathIdentifiers()
	hosts := sourcecases.UnsafeHosts()
	var routes []itemRoute
	add := func(r itemRoute) { routes = append(routes, r) }

	query := func(handler http.HandlerFunc, method, path string, values func(v string) url.Values) func(string) *httptest.ResponseRecorder {
		return func(v string) *httptest.ResponseRecorder { return sendQuery(handler, method, path, values(v)) }
	}
	item := func(v string) url.Values { return url.Values{"project": {project}, "id": {v}} }
	inProject := func(extra url.Values) func(v string) url.Values {
		return func(v string) url.Values {
			values := url.Values{"project": {v}}
			for k, vs := range extra {
				values[k] = vs
			}
			return values
		}
	}

	// The ID of an item that is a plain name.
	for _, r := range []struct {
		name, position, path, method string
		handler                      http.HandlerFunc
		unsafe                       []sourcecases.UnsafeIdentifier
	}{
		{"boards/board", "boardID", "/datatug/boards/board", http.MethodGet, getBoard, identifiers},
		{"boards/delete_board", "boardID", "/datatug/boards/delete_board", http.MethodDelete, deleteBoard, identifiers},
		{"entities/entity", "entityID", "/datatug/entities/entity", http.MethodGet, getEntity, identifiers},
		{"entities/delete_entity", "entityID", "/datatug/entities/delete_entity", http.MethodDelete, deleteEntity, identifiers},
		{"recordsets/recordset_definition", "recordsetID", "/datatug/recordsets/recordset_definition", http.MethodGet, getRecordsetDefinition, paths},
		{"folders/delete_folder", "folderID", "/datatug/folders/delete_folder", http.MethodDelete, deleteFolder, paths},
	} {
		add(itemRoute{name: r.name, position: r.position, unsafe: r.unsafe, send: query(r.handler, r.method, r.path, item)})
	}

	// Routes that carry the ID in the body.
	// An empty ID in a body is the answer the request's own validation gave before (a
	// board's ID is required), and an entity with no ID is saved under its title.
	add(itemRoute{name: "boards/save_board", position: "boardID", unsafe: identifiers, emptyOK: true, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, saveBoard, http.MethodPut, "/datatug/boards/save_board?project="+url.QueryEscape(project), map[string]any{"id": v, "title": "T"})
	}})
	add(itemRoute{name: "entities/save_entity", position: "entityID", unsafe: identifiers, emptyOK: true, send: func(v string) *httptest.ResponseRecorder {
		// saveEntity takes the ID of the entity from the query, and the title from the body.
		return sendBody(t, saveEntity, http.MethodPut, "/datatug/entities/save_entity?project="+url.QueryEscape(project)+"&id="+url.QueryEscape(v), map[string]any{"id": "x", "title": "T"})
	}})
	add(itemRoute{name: "folders/create_folder (path)", position: "path", unsafe: paths, emptyOK: true, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, createFolder, http.MethodPut, "/datatug/folders/create_folder", map[string]any{"storage": "files", "project": project, "path": v, "name": "n"})
	}})
	add(itemRoute{name: "folders/create_folder (name)", position: "name", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, createFolder, http.MethodPut, "/datatug/folders/create_folder", map[string]any{"storage": "files", "project": project, "path": "f", "name": v})
	}})

	// The driver and the host of a db server.
	for _, r := range []struct {
		name, path, method string
		handler            http.HandlerFunc
	}{
		{"dbserver-summary", "/datatug/dbserver-summary", http.MethodGet, getDbServerSummary},
		{"dbserver-delete", "/datatug/dbserver-delete", http.MethodDelete, deleteDbServer},
	} {
		r := r
		add(itemRoute{name: r.name + " (driver)", position: "driver", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
			return sendQuery(r.handler, r.method, r.path, url.Values{"project": {project}, "driver": {v}, "host": {"localhost"}})
		}})
		add(itemRoute{name: r.name + " (host)", position: "host", unsafe: hosts, send: func(v string) *httptest.ResponseRecorder {
			return sendQuery(r.handler, r.method, r.path, url.Values{"project": {project}, "driver": {"sqlserver"}, "host": {v}})
		}})
	}
	addServer := func(driver, host string) map[string]any {
		id := driver + ":" + host
		return map[string]any{"id": id, "title": "S", "server": map[string]any{"driver": driver, "host": host}, "catalogs": []any{}}
	}
	add(itemRoute{name: "dbserver-add (driver)", position: "driver", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, addDbServer, http.MethodPost, "/datatug/dbserver-add?project="+url.QueryEscape(project), addServer(v, "localhost"))
	}})
	add(itemRoute{name: "dbserver-add (host)", position: "host", unsafe: hosts, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, addDbServer, http.MethodPost, "/datatug/dbserver-add?project="+url.QueryEscape(project), addServer("sqlserver", v))
	}})
	// Values that pass the rules of a name and of a host, and that the validation of the
	// server reference refuses with a message that quotes them.
	for _, c := range []struct {
		name, position, driver, host string
	}{
		{"a plain driver that no store records", "driver", "mongodb", "localhost"},
		{"a user and a password typed as the host of a sqlite3 server", "host", "sqlite3", "alice:s3cretpw"},
		{"a host name given to a sqlite3 server", "host", "sqlite3", "db.example.com"},
	} {
		c := c
		value := c.driver
		if c.position == "host" {
			value = c.host
		}
		unsafe := []sourcecases.UnsafeIdentifier{{Name: c.name, ID: value}}
		for _, r := range []struct {
			name, path, method string
			handler            http.HandlerFunc
		}{
			{"dbserver-summary", "/datatug/dbserver-summary", http.MethodGet, getDbServerSummary},
			{"dbserver-delete", "/datatug/dbserver-delete", http.MethodDelete, deleteDbServer},
		} {
			r := r
			add(itemRoute{name: r.name + " (" + c.name + ")", position: c.position, unsafe: unsafe, send: func(string) *httptest.ResponseRecorder {
				return sendQuery(r.handler, r.method, r.path, url.Values{"project": {project}, "driver": {c.driver}, "host": {c.host}})
			}})
		}
		add(itemRoute{name: "dbserver-add (" + c.name + ")", position: c.position, unsafe: unsafe, send: func(string) *httptest.ResponseRecorder {
			return sendBody(t, addDbServer, http.MethodPost, "/datatug/dbserver-add?project="+url.QueryEscape(project), addServer(c.driver, c.host))
		}})
	}
	add(itemRoute{name: "dbserver-add (id)", position: "id", unsafe: hosts, emptyOK: true, send: func(v string) *httptest.ResponseRecorder {
		body := addServer("sqlserver", "localhost")
		body["id"] = v
		return sendBody(t, addDbServer, http.MethodPost, "/datatug/dbserver-add?project="+url.QueryEscape(project), body)
	}})

	// The project of every route that takes one.
	for _, r := range []struct {
		name, path, method string
		handler            http.HandlerFunc
		extra              url.Values
	}{
		{"boards/board", "/datatug/boards/board", http.MethodGet, getBoard, url.Values{"id": {"b1"}}},
		{"boards/delete_board", "/datatug/boards/delete_board", http.MethodDelete, deleteBoard, url.Values{"id": {"b1"}}},
		{"entities/entity", "/datatug/entities/entity", http.MethodGet, getEntity, url.Values{"id": {"e1"}}},
		{"entities/all_entities", "/datatug/entities/all_entities", http.MethodGet, getEntities, nil},
		{"entities/delete_entity", "/datatug/entities/delete_entity", http.MethodDelete, deleteEntity, url.Values{"id": {"e1"}}},
		{"recordsets/recordset_definition", "/datatug/recordsets/recordset_definition", http.MethodGet, getRecordsetDefinition, url.Values{"id": {"d1"}}},
		{"recordsets/recordsets_summary", "/datatug/recordsets/recordsets_summary", http.MethodGet, getRecordsetsSummary, nil},
		{"folders/delete_folder", "/datatug/folders/delete_folder", http.MethodDelete, deleteFolder, url.Values{"id": {"f1"}}},
		{"dbserver-summary", "/datatug/dbserver-summary", http.MethodGet, getDbServerSummary, url.Values{"driver": {"sqlserver"}, "host": {"localhost"}}},
		{"dbserver-delete", "/datatug/dbserver-delete", http.MethodDelete, deleteDbServer, url.Values{"driver": {"sqlserver"}, "host": {"localhost"}}},
		{"environment-summary", "/datatug/environment-summary", http.MethodGet, getEnvironmentSummary, url.Values{"id": {"local"}}},
		{"projects/project_summary", "/datatug/projects/project_summary", http.MethodGet, getProjectSummary, nil},
		{"projects/project_full", "/datatug/projects/project_full", http.MethodGet, getProjectFull, nil},
	} {
		r := r
		extra := r.extra
		send := func(v string) *httptest.ResponseRecorder {
			values := inProject(extra)(v)
			if strings.HasPrefix(r.name, "projects/") {
				// These two take the project as ?id=.
				values = url.Values{"id": {v}}
			}
			return sendQuery(r.handler, r.method, r.path, values)
		}
		add(itemRoute{name: r.name + " (project)", position: "project", unsafe: identifiers, send: send})
	}
	add(itemRoute{name: "boards/save_board (project)", position: "project", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, saveBoard, http.MethodPut, "/datatug/boards/save_board?project="+url.QueryEscape(v), map[string]any{"id": "b1", "title": "T"})
	}})
	add(itemRoute{name: "boards/create_board (project)", position: "project", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, createBoard, http.MethodPost, "/datatug/boards/create_board?project="+url.QueryEscape(v), map[string]any{"id": "x", "title": "T"})
	}})
	add(itemRoute{name: "entities/save_entity (project)", position: "project", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, saveEntity, http.MethodPut, "/datatug/entities/save_entity?id=e1&project="+url.QueryEscape(v), map[string]any{"id": "e1", "title": "T"})
	}})
	add(itemRoute{name: "folders/create_folder (project)", position: "project", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, createFolder, http.MethodPut, "/datatug/folders/create_folder", map[string]any{"storage": "files", "project": v, "name": "n"})
	}})
	add(itemRoute{name: "exec/select (project)", position: "project", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		return sendQuery(executeSelectHandler, http.MethodGet, "/datatug/exec/select", url.Values{"project": {v}, "environment": {"local"}, "db": {"chinook"}, "sql": {"SELECT 1"}})
	}})
	add(itemRoute{name: "exec/execute_commands (project)", position: "project", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		body := map[string]any{"commands": []map[string]any{{"type": "SQL", "text": "SELECT 1", "env": "local", "db": "chinook"}}}
		return sendBody(t, executeCommandsHandler, http.MethodPost, "/datatug/exec/execute_commands?project="+url.QueryEscape(v), body)
	}})
	add(itemRoute{name: "dbserver-add (project)", position: "project", unsafe: identifiers, send: func(v string) *httptest.ResponseRecorder {
		return sendBody(t, addDbServer, http.MethodPost, "/datatug/dbserver-add?project="+url.QueryEscape(v), addServer("sqlserver", "localhost"))
	}})
	return routes
}

// jsonTexts returns every string of the JSON document body (its keys and its values, as
// they read once decoded: JSON writes a backslash, a NUL, "<", ">" and "&" escaped), or
// body itself when it is not JSON.
func jsonTexts(body string) []string {
	var document any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		return []string{body}
	}
	var texts []string
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			texts = append(texts, v)
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for key, item := range v {
				texts = append(texts, key)
				walk(item)
			}
		}
	}
	walk(document)
	return texts
}

// echoes reports whether value is in body, as it is written and as it reads once decoded.
func echoes(body, value string) bool {
	if strings.Contains(body, value) {
		return true
	}
	for _, text := range jsonTexts(body) {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func TestEchoes_SeesAValueThatJSONWritesEscaped(t *testing.T) {
	for _, value := range []string{`a\b`, "a\x00b", "a<b>&c"} {
		raw, err := json.Marshal(map[string]any{"error": map[string]string{"message": "bad value: " + value}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), value) {
			t.Fatalf("%q is written as it is: the test proves nothing about escaping", value)
		}
		if !echoes(string(raw), value) {
			t.Errorf("an echo of %q in %s is not seen", value, raw)
		}
		if echoes(string(raw), value+"-not-there") {
			t.Errorf("a value that is not in %s is seen", raw)
		}
	}
	if !echoes("plain text, not JSON: a<b", "a<b") || echoes("plain text", "a<b") {
		t.Error("a body that is not JSON is read as text")
	}
}

func TestRoutes_RefuseAnUnsafeProjectItemIDBeforeAnyProjectStoreIsAsked(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	asked := countProjectStoreAsks(t)

	for _, r := range itemRoutes(t, scope.Project) {
		for _, c := range r.unsafe {
			if c.ID == "" && r.emptyOK {
				continue
			}
			*asked = 0
			w := r.send(c.ID)
			body := w.Body.String()
			switch {
			case w.Code != http.StatusBadRequest:
				t.Errorf("%s: %s as the %s: status = %d, want 400: %s", r.name, c.Name, r.position, w.Code, body)
			case c.ID != "" && !strings.Contains(body, r.position):
				t.Errorf("%s: %s as the %s: the body does not name the field: %s", r.name, c.Name, r.position, body)
			case len(c.ID) >= 3 && echoes(body, c.ID):
				t.Errorf("%s: %s as the %s: the body echoes the value: %s", r.name, c.Name, r.position, body)
			}
			if *asked != 0 {
				t.Errorf("%s: %s as the %s reached %d project stores, want 0", r.name, c.Name, r.position, *asked)
			}
		}
	}
}

// The count above means something only if requests with plain values do reach the store.
func TestRoutes_APlainProjectItemRequestReachesTheStore(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	asked := countProjectStoreAsks(t)
	project := url.QueryEscape(scope.Project)

	for name, send := range map[string]func() *httptest.ResponseRecorder{
		"boards/board": func() *httptest.ResponseRecorder {
			return sendQuery(getBoard, http.MethodGet, "/datatug/boards/board", url.Values{"project": {scope.Project}, "id": {"b1"}})
		},
		"entities/entity": func() *httptest.ResponseRecorder {
			return sendQuery(getEntity, http.MethodGet, "/datatug/entities/entity", url.Values{"project": {scope.Project}, "id": {"Customer"}})
		},
		"recordsets/recordset_definition": func() *httptest.ResponseRecorder {
			return sendQuery(getRecordsetDefinition, http.MethodGet, "/datatug/recordsets/recordset_definition", url.Values{"project": {scope.Project}, "id": {"support-notes"}})
		},
		"dbserver-summary": func() *httptest.ResponseRecorder {
			return sendQuery(getDbServerSummary, http.MethodGet, "/datatug/dbserver-summary", url.Values{"project": {scope.Project}, "driver": {"sqlserver"}, "host": {"localhost"}})
		},
		"dbserver-summary on an IPv6 host": func() *httptest.ResponseRecorder {
			return sendQuery(getDbServerSummary, http.MethodGet, "/datatug/dbserver-summary", url.Values{"project": {scope.Project}, "driver": {"mysql"}, "host": {"::1"}})
		},
		"folders/delete_folder": func() *httptest.ResponseRecorder {
			return sendQuery(deleteFolder, http.MethodDelete, "/datatug/folders/delete_folder", url.Values{"project": {scope.Project}, "id": {"a/b"}})
		},
		"boards/save_board": func() *httptest.ResponseRecorder {
			return sendBody(t, saveBoard, http.MethodPut, "/datatug/boards/save_board?project="+project, map[string]any{"id": "b2", "title": "T"})
		},
		"dbserver-add": func() *httptest.ResponseRecorder {
			return sendBody(t, addDbServer, http.MethodPost, "/datatug/dbserver-add?project="+project,
				map[string]any{"id": "sqlserver:db.example.com", "title": "S", "server": map[string]any{"driver": "sqlserver", "host": "db.example.com"}, "catalogs": []any{}})
		},
		"folders/create_folder": func() *httptest.ResponseRecorder {
			return sendBody(t, createFolder, http.MethodPut, "/datatug/folders/create_folder", map[string]any{"storage": "files", "project": scope.Project, "path": "a", "name": "b"})
		},
	} {
		*asked = 0
		w := send()
		if w.Code == http.StatusBadRequest {
			t.Errorf("%s: a request with plain values was refused as a bad request: %s", name, w.Body.String())
		}
		if *asked == 0 {
			t.Errorf("%s: a request with plain values never reached the project store, so the counts prove nothing: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// Whatever the store found at the path of an item it cannot load (nothing, a file, a
// folder with nothing in it, a file that is not JSON), the answer is the same sentence,
// and no answer quotes a path of the server.
func TestRoutes_AnItemTheStoreCannotLoadIsOneAnswerThatQuotesNoPath(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	projectDir, ok := api.ProjectDir(scope.Project)
	if !ok {
		t.Fatal("the project is not served")
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// For each kind: an ID that is missing, one that is a file, one that is a folder with
	// nothing in it, and one whose file is not JSON.
	for _, kind := range []struct {
		name   string
		folder string
		route  http.HandlerFunc
		path   string
		// layout makes the four IDs in the project.
		layout func(dir string)
	}{
		{"board", "boards", getBoard, "/datatug/boards/board", func(dir string) {
			write(filepath.Join(dir, "isfile"), "x")
			if err := os.MkdirAll(filepath.Join(dir, "isfolder"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(dir, "notjson", "board.json"), "{not json")
		}},
		{"entity", "entities", getEntity, "/datatug/entities/entity", func(dir string) {
			write(filepath.Join(dir, "isfile"), "x")
			if err := os.MkdirAll(filepath.Join(dir, "isfolder"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(dir, "notjson", "notjson.entity.json"), "{not json")
		}},
		{"recordset", "recordsets", getRecordsetDefinition, "/datatug/recordsets/recordset_definition", func(dir string) {
			write(filepath.Join(dir, "isfile"), "x")
			if err := os.MkdirAll(filepath.Join(dir, "isfolder"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(dir, "notjson.recordset.json"), "{not json")
		}},
	} {
		t.Run(kind.name, func(t *testing.T) {
			folder := filepath.Join(projectDir, kind.folder)
			kind.layout(folder)
			for _, id := range []string{"missing", "isfile", "isfolder", "notjson"} {
				w := sendQuery(kind.route, http.MethodGet, kind.path, url.Values{"project": {scope.Project}, "id": {id}})
				body := w.Body.String()
				if w.Code != http.StatusInternalServerError || !strings.Contains(body, kind.name+` \"`+id+`\" not found`) {
					t.Errorf("%s %q: %d %s, want the answer that names the %s and the ID and says it is not found", kind.name, id, w.Code, body, kind.name)
				}
				for _, leak := range []string{projectDir, "no such file", "not a directory", "is a directory", "invalid character", "failed to load", filepath.Base(projectDir)} {
					if strings.Contains(body, leak) {
						t.Errorf("%s %q: the answer holds %q: %s", kind.name, id, leak, body)
					}
				}
			}
		})
	}
}

// A route that takes its project from the request itself and opens its project store
// refuses a project this process does not serve, as a route that resolves the store of its
// project does (see api.ResolveStoreID), before any project store is asked for.
func TestRoutes_AProjectThatIsNotServedIsRefusedBeforeAnyProjectStoreIsAsked(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	asked := countProjectStoreAsks(t)

	routes := map[string]func(project string) *httptest.ResponseRecorder{
		"exec/select": func(project string) *httptest.ResponseRecorder {
			return sendQuery(executeSelectHandler, http.MethodGet, "/datatug/exec/select", url.Values{"project": {project}, "environment": {scope.Environment}, "db": {"chinook"}, "sql": {"SELECT 1"}})
		},
		"exec/select (from)": func(project string) *httptest.ResponseRecorder {
			return sendQuery(executeSelectHandler, http.MethodGet, "/datatug/exec/select", url.Values{"proj": {project}, "env": {scope.Environment}, "db": {"chinook"}, "from": {"Customer"}})
		},
		"exec/execute_commands": func(project string) *httptest.ResponseRecorder {
			body := map[string]any{"commands": []map[string]any{{"type": "SQL", "text": "SELECT 1", "env": scope.Environment, "db": "chinook"}}}
			return sendBody(t, executeCommandsHandler, http.MethodPost, "/datatug/exec/execute_commands?project="+url.QueryEscape(project), body)
		},
		"folders/create_folder": func(project string) *httptest.ResponseRecorder {
			return sendBody(t, createFolder, http.MethodPut, "/datatug/folders/create_folder", map[string]any{"storage": "files", "project": project, "path": "a", "name": "b"})
		},
	}
	const notServed = "not-served"
	for name, send := range routes {
		*asked = 0
		w := send(notServed)
		body := w.Body.String()
		if w.Code != http.StatusBadRequest || !strings.Contains(body, "unknown storage id") || !strings.Contains(body, `\"`+notServed+`\"`) {
			t.Errorf("%s: a project that is not served: %d %s, want a 400 that says no store is configured for the project", name, w.Code, body)
		}
		if *asked != 0 {
			t.Errorf("%s: a project that is not served reached %d project stores, want 0", name, *asked)
		}

		// The count means something only if the same request for the served project does
		// reach the project store, and is not refused for its project.
		*asked = 0
		w = send(scope.Project)
		if strings.Contains(w.Body.String(), "unknown storage id") {
			t.Errorf("%s: the served project was refused: %d %s", name, w.Code, w.Body.String())
		}
		if *asked == 0 {
			t.Errorf("%s: the served project never reached the project store, so the counts prove nothing: %d %s", name, w.Code, w.Body.String())
		}
	}
}

// listTree returns every file and folder under dir, with the size of each file, so that two
// listings differ when something was made or written.
func listTree(t *testing.T, dir string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, rel+" "+strconv.FormatInt(info.Size(), 10))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// The file store of a project this process does not hold is rooted at the working
// directory of the process. A request for such a project, made while the working directory
// is a project folder, reads nothing from it and writes nothing to it.
func TestRoutes_AProjectThatIsNotServedIsNotLookedForInTheWorkingDirectory(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	captureTestSetup(t, "alice", []string{"admin"}, true) // serves one project, with the real file store

	t.Run("a folder that holds a project", func(t *testing.T) {
		// A project with an environment, a catalog and a database, which is not served.
		otherDir, _ := writeSemanticTestProject(t)
		t.Chdir(otherDir)
		before := listTree(t, otherDir)

		for name, send := range map[string]func() *httptest.ResponseRecorder{
			"exec/select": func() *httptest.ResponseRecorder {
				return sendQuery(executeSelectHandler, http.MethodGet, "/datatug/exec/select", url.Values{"project": {"not-served"}, "environment": {semanticTestEnv}, "db": {semanticTestSource}, "sql": {"SELECT 1 AS one"}})
			},
			"exec/execute_commands": func() *httptest.ResponseRecorder {
				body := map[string]any{"commands": []map[string]any{{"type": "SQL", "text": "SELECT 1 AS one", "env": semanticTestEnv, "db": semanticTestSource}}}
				return sendBody(t, executeCommandsHandler, http.MethodPost, "/datatug/exec/execute_commands?project=not-served", body)
			},
			"folders/create_folder": func() *httptest.ResponseRecorder {
				return sendBody(t, createFolder, http.MethodPut, "/datatug/folders/create_folder", map[string]any{"storage": "files", "project": "not-served", "path": "reports", "name": "new-folder"})
			},
		} {
			w := send()
			body := w.Body.String()
			if w.Code != http.StatusBadRequest || !strings.Contains(body, "unknown storage id") {
				t.Errorf("%s: %d %s, want a 400 that says no store is configured for the project", name, w.Code, body)
			}
			if strings.Contains(body, `"one"`) || strings.Contains(body, "rows") {
				t.Errorf("%s: the answer holds a result read from the working directory: %s", name, body)
			}
		}
		if after := listTree(t, otherDir); !reflect.DeepEqual(before, after) {
			t.Errorf("the working directory changed:\n before %v\n after  %v", before, after)
		}
		if _, err := os.Stat(filepath.Join(otherDir, "folders")); err == nil {
			t.Error("a folder was made in the working directory")
		}
	})
}

// The web client sends the project in the query of an entity save, and the whole entity,
// its ID included, in the body. The entity is saved under that ID, and the ID is what is
// checked: the title is free text.
func TestRoutes_SaveEntityKeepsTheIDOfTheBody(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	projectDir, ok := api.ProjectDir(scope.Project)
	if !ok {
		t.Fatal("the project is not served")
	}
	target := "/datatug/entities/save_entity?project=" + url.QueryEscape(scope.Project)

	w := sendBody(t, saveEntity, http.MethodPut, target, map[string]any{"id": "Order", "title": "Customer order (draft)"})

	if w.Code != http.StatusCreated {
		t.Fatalf("an entity with a plain ID and a title that is free text: %d %s, want 201", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, "entities", "Order", "Order.entity.json")); err != nil {
		t.Errorf("the entity is not saved under the ID of its body: %v", err)
	}

	// The ID of the body is the one that is checked: an ID that is not a plain name is
	// refused for what it is, whatever the title says.
	w = sendBody(t, saveEntity, http.MethodPut, target, map[string]any{"id": "bad id", "title": "Order"})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "entityID") || echoes(w.Body.String(), "bad id") {
		t.Errorf("an entity with an ID that is not a plain name: %d %s, want a 400 that names entityID and not the value", w.Code, w.Body.String())
	}

	// The id of the query, when there is one, is the ID of the entity (as it was).
	w = sendBody(t, saveEntity, http.MethodPut, target+"&id=Shipment", map[string]any{"id": "ignored", "title": "Shipment"})
	if w.Code != http.StatusCreated {
		t.Errorf("an entity saved with an id in the query: %d %s, want 201", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, "entities", "Shipment", "Shipment.entity.json")); err != nil {
		t.Errorf("the entity is not saved under the id of the query: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "entities", "ignored")); err == nil {
		t.Error("the entity was saved under the ID of its body although the query had one")
	}
}

// A request to exec/select that names no project is not given one: it gets the missing-field
// answer of its own validation, whatever store is configured.
func TestRoutes_ExecSelectWithNoProjectIsAskedForOne(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	captureTestSetup(t, "alice", []string{"admin"}, true)
	asked := countProjectStoreAsks(t)

	w := sendQuery(executeSelectHandler, http.MethodGet, "/datatug/exec/select", url.Values{"environment": {semanticTestEnv}, "db": {"chinook"}, "sql": {"SELECT 1"}})

	body := w.Body.String()
	if w.Code != http.StatusBadRequest || !strings.Contains(body, "project") || !strings.Contains(body, "missing required field") || strings.Contains(body, "plain name") {
		t.Errorf("no project: %d %s, want a 400 that says the project is missing", w.Code, body)
	}
	if *asked != 0 {
		t.Errorf("no project reached %d project stores, want 0", *asked)
	}
}
