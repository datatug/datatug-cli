package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/storage"
	"github.com/datatug/datatug-core/pkg/storage/filestore"
)

// The routes that read a project through its store answer with a sentence built from the
// kind of what was asked for and its ID, and never with the text of the store, which quotes
// the path it built from the project folder and says whether the path is missing, a file or a
// folder. Whatever is at the path (nothing, a file where a folder is expected, a folder where
// a file is expected, a file that is not JSON), the answer is the same sentence; the store's
// text is for the log of the server.

var projectSequence atomic.Int64

// serveBlankProject serves a project that is an empty folder, with the real file store, and
// returns its folder and its ID (a plain name).
func serveBlankProject(t *testing.T) (dir, projectID string) {
	t.Helper()
	dir = t.TempDir()
	projectID = fmt.Sprintf("blank-project-%d", projectSequence.Add(1))
	filestore.SetProjectPath(projectID, dir)
	session, err := secureread.NewSession(secureread.SessionOptions{NoPolicies: true})
	if err != nil {
		t.Fatal(err)
	}
	pathsByID := map[string]string{projectID: dir}
	api.ConfigureSecureSession(session, pathsByID, api.Capabilities{AllowWrites: true})
	previous := storage.NewDatatugStore
	storage.NewDatatugStore = func(string) (storage.Store, error) { return filestore.NewStore("files", pathsByID) }
	t.Cleanup(func() {
		storage.NewDatatugStore = previous
		api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{})
	})
	return dir, projectID
}

// answerMessage is the message of an error answer: the legacy envelope holds it as the error,
// and the one of apicore as the message of the error.
func answerMessage(body string) string {
	var legacy struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(body), &legacy) == nil && legacy.Error != "" {
		return legacy.Error
	}
	var wrapped struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &wrapped) == nil {
		return wrapped.Error.Message
	}
	return body
}

// descriptorSentence is the answer for a PostgreSQL catalog whose connection descriptor cannot be read.
const descriptorSentence = `catalog "shop": the PostgreSQL connection descriptor cannot be read`

// httpQueriesNotListed is the answer for a project whose HTTP query definitions cannot be listed.
const httpQueriesNotListed = "the HTTP query definitions of the project could not be listed"

// brokenQueries is what is at the queries of a project whose definitions cannot be listed, with
// an environment that lists no catalog, for the routes that resolve a source.
func brokenQueries(file func(path, content string) func(dir string)) map[string]func(string) {
	environment := file("environments/local/local.env.json", "{}")
	return map[string]func(string){
		"a file that is not JSON": func(dir string) {
			environment(dir)
			file("queries/broken.query.json", "{not json")(dir)
		},
	}
}

// postgresCatalog records, in the project at dir, the environment "local" with one server and
// the PostgreSQL catalog "shop", whose connection descriptor is the file
// connections/prod/shop.json of the project (not written: what is at that path is the
// breakage of the test).
func postgresCatalog(t *testing.T, dir string) {
	t.Helper()
	store := filestore.NewProjectStore("setup", dir)
	ctx := context.Background()
	server := datatug.ServerRef{Driver: "postgres", Host: "db.example.com"}
	env := &datatug.Environment{DbServers: datatug.EnvDbServers{{ServerRef: server}}}
	env.ID = "local"
	if err := store.SaveEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}
	catalog := &datatug.DbCatalog{DbCatalogBase: datatug.DbCatalogBase{Driver: "postgres", Path: "connections/prod/shop.json"}}
	catalog.ID = "shop"
	if err := store.SaveEnvDbCatalog(ctx, "local", (&datatug.EnvDbServer{ServerRef: server}).GetID(), "shop", catalog); err != nil {
		t.Fatal(err)
	}
}

func TestRoutes_AStoreFailureAnswersOneBuiltSentenceWhateverIsAtThePath(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })

	file := func(path, content string) func(dir string) {
		return func(dir string) {
			full := filepath.Join(dir, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	folder := func(path string) func(dir string) {
		return func(dir string) {
			if err := os.MkdirAll(filepath.Join(dir, path, "in-it"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	nothing := func(string) {}

	for _, route := range []struct {
		name    string
		handler http.HandlerFunc
		path    string
		query   func(project string) url.Values
		// method is GET unless the route is another.
		method string
		// body is the JSON body of a route that reads one.
		body func(project string) any
		// status is the status of the answer: 500 unless the route is another.
		status int
		// sentence is the answer, for the project.
		sentence func(project string) string
		// breakages are what is at the path of what the route reads.
		breakages map[string]func(dir string)
	}{
		{
			name: "environment-summary", handler: getEnvironmentSummary, path: "/datatug/environment-summary",
			query:    func(p string) url.Values { return url.Values{"project": {p}, "env": {"local"}} },
			sentence: func(string) string { return `environment "local" not found` },
			breakages: map[string]func(string){
				"missing":                           nothing,
				"a file where a folder is expected": file("environments", "x"),
				"a folder where a file is expected": folder("environments/local/local.env.json"),
				"a file that is not JSON":           file("environments/local/local.env.json", "{not json"),
			},
		},
		{
			name: "projects/project_summary", handler: getProjectSummary, path: "/datatug/projects/project_summary",
			query:    func(p string) url.Values { return url.Values{"id": {p}} },
			sentence: func(p string) string { return fmt.Sprintf("project %q not found", p) },
			breakages: map[string]func(string){
				"missing":                           nothing,
				"a folder where a file is expected": folder("datatug-project.json"),
				"a file that is not JSON":           file("datatug-project.json", "{not json"),
			},
		},
		{
			name: "projects/project_full", handler: getProjectFull, path: "/datatug/projects/project_full",
			query:    func(p string) url.Values { return url.Values{"id": {p}} },
			sentence: func(p string) string { return fmt.Sprintf("project %q not found", p) },
			breakages: map[string]func(string){
				"missing":                           nothing,
				"a folder where a file is expected": folder("datatug-project.json"),
				"a file that is not JSON":           file("datatug-project.json", "{not json"),
			},
		},
		{
			name: "entities/all_entities", handler: getEntities, path: "/datatug/entities/all_entities",
			query:    func(p string) url.Values { return url.Values{"project": {p}} },
			sentence: func(p string) string { return fmt.Sprintf("entities of project %q could not be loaded", p) },
			breakages: map[string]func(string){
				"a file where a folder is expected": file("entities", "x"),
				"a folder where a file is expected": folder("entities/Customer/Customer.entity.json"),
				"a file that is not JSON":           file("entities/Customer/Customer.entity.json", "{not json"),
			},
		},
		{
			name: "projects/projects_summary", handler: getProjects, path: "/datatug/projects/projects_summary",
			query:    func(string) url.Values { return url.Values{} },
			sentence: func(string) string { return "the projects could not be listed" },
			breakages: map[string]func(string){
				"missing":                           nothing,
				"a folder where a file is expected": folder("datatug-project.json"),
				"a file that is not JSON":           file("datatug-project.json", "{not json"),
			},
		},
		{
			name: "queries/all_queries", handler: getQueriesHandler, path: "/datatug/queries/all_queries",
			query:    func(p string) url.Values { return url.Values{"project": {p}} },
			sentence: func(p string) string { return fmt.Sprintf("queries of project %q could not be loaded", p) },
			breakages: map[string]func(string){
				"a file that is not JSON": file("queries/broken.query.json", "{not json"),
			},
		},
		{
			name: "semantic/columns", handler: semanticColumnsHandler, path: "/datatug/semantic/columns", status: http.StatusServiceUnavailable,
			query: func(p string) url.Values {
				return url.Values{"project": {p}, "environment": {"local"}, "securityContextId": {api.SecurityContextID()}, "source": {"s"}, "collection": {"c"}}
			},
			sentence:  func(string) string { return httpQueriesNotListed },
			breakages: brokenQueries(file),
		},
		{
			name: "exec/run_query", handler: runQueryHandler, path: "/datatug/exec/run_query", method: http.MethodPost, status: http.StatusServiceUnavailable,
			query: func(string) url.Values { return url.Values{} },
			body: func(p string) any {
				return apicontract.ExecutionRequest{Project: p, Environment: "local", SecurityContextID: api.SecurityContextID(), Source: "s", DTQL: "from: {name: t}\n", Mode: apicontract.ProvenanceModeLive}
			},
			sentence:  func(string) string { return httpQueriesNotListed },
			breakages: brokenQueries(file),
		},
		{
			name: "exec/select", handler: executeSelectHandler, path: "/datatug/exec/select",
			query: func(p string) url.Values {
				return url.Values{"proj": {p}, "env": {"local"}, "db": {"chinook"}, "sql": {"select 1"}}
			},
			sentence: func(string) string { return `environment "local" not found` },
			breakages: map[string]func(string){
				"missing":                           nothing,
				"a folder where a file is expected": folder("environments/local/local.env.json"),
				"a file that is not JSON":           file("environments/local/local.env.json", "{not json"),
			},
		},
		{
			name: "exec/execute_commands", handler: executeCommandsHandler, path: "/datatug/exec/execute_commands", method: http.MethodPost,
			query: func(p string) url.Values { return url.Values{"project": {p}} },
			body: func(string) any {
				return map[string]any{"commands": []map[string]any{{"type": "SQL", "env": "local", "db": "chinook", "text": "select 1"}}}
			},
			sentence: func(string) string { return `command 0: environment "local" not found` },
			breakages: map[string]func(string){
				"missing":                           nothing,
				"a folder where a file is expected": folder("environments/local/local.env.json"),
				"a file that is not JSON":           file("environments/local/local.env.json", "{not json"),
			},
		},
		{
			name: "exec/select, a PostgreSQL catalog", handler: executeSelectHandler, path: "/datatug/exec/select",
			query: func(p string) url.Values {
				return url.Values{"proj": {p}, "env": {"local"}, "db": {"shop"}, "sql": {"select 1"}}
			},
			sentence: func(string) string { return descriptorSentence },
			breakages: map[string]func(string){
				"a descriptor that is not there": func(dir string) { postgresCatalog(t, dir) },
				"a descriptor that is a folder": func(dir string) {
					postgresCatalog(t, dir)
					folder("connections/prod/shop.json")(dir)
				},
			},
		},
		{
			name: "exec/execute_commands, a PostgreSQL catalog", handler: executeCommandsHandler, path: "/datatug/exec/execute_commands", method: http.MethodPost,
			query: func(p string) url.Values { return url.Values{"project": {p}} },
			body: func(string) any {
				return map[string]any{"commands": []map[string]any{{"type": "SQL", "env": "local", "db": "shop", "text": "select 1"}}}
			},
			sentence: func(string) string { return "command 0: " + descriptorSentence },
			breakages: map[string]func(string){
				"a descriptor that is not there": func(dir string) { postgresCatalog(t, dir) },
				"a descriptor that is a folder": func(dir string) {
					postgresCatalog(t, dir)
					folder("connections/prod/shop.json")(dir)
				},
			},
		},
		{
			name: "recordsets/recordsets_summary", handler: getRecordsetsSummary, path: "/datatug/recordsets/recordsets_summary",
			query:    func(p string) url.Values { return url.Values{"project": {p}} },
			sentence: func(p string) string { return fmt.Sprintf("recordsets of project %q could not be loaded", p) },
			breakages: map[string]func(string){
				"a file where a folder is expected": file("recordsets", "x"),
				"a file that is not JSON":           file("recordsets/notes.recordset.json", "{not json"),
			},
		},
	} {
		for name, breakage := range route.breakages {
			t.Run(route.name+" "+name, func(t *testing.T) {
				dir, project := serveBlankProject(t)
				breakage(dir)

				method, status := route.method, route.status
				if method == "" {
					method = http.MethodGet
				}
				if status == 0 {
					status = http.StatusInternalServerError
				}
				var w *httptest.ResponseRecorder
				if route.body != nil {
					w = sendBody(t, route.handler, method, route.path+"?"+route.query(project).Encode(), route.body(project))
				} else {
					w = sendQuery(route.handler, method, route.path, route.query(project))
				}
				body := w.Body.String()
				if want := route.sentence(project); w.Code != status || answerMessage(body) != want {
					t.Errorf("%d %s, want a %d whose message is exactly %q", w.Code, body, status, want)
				}
				for _, leak := range []string{dir, filepath.Base(dir), "no such file", "not a directory", "is a directory", "invalid character", "failed to load", "does not exist", "open ", "read "} {
					if strings.Contains(body, leak) {
						t.Errorf("the answer holds %q: %s", leak, body)
					}
				}
			})
		}
	}
}

// The personal queries of the serving principal are kept outside the project, in a folder of
// their own: the answer for one that cannot be loaded is the same sentence, which names
// neither the folder of the project nor that one.
func TestRoutes_ThePersonalQueriesThatCannotBeLoadedAreOneBuiltSentence(t *testing.T) {
	personalRoot := t.TempDir()
	t.Setenv("DATATUG_PERSONAL_DIR", personalRoot)
	projectDir := t.TempDir()
	const project = "personal-sentence-project"
	queries := filepath.Join(personalRoot, project, "queries")
	if err := os.MkdirAll(queries, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queries, "broken.query.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	api.ConfigureSecureSession(secureread.Session{Principal: &access.Principal{ID: "alice"}}, map[string]string{project: projectDir}, api.Capabilities{})
	t.Cleanup(func() { api.ConfigureSecureSession(secureread.Session{}, nil, api.Capabilities{}) })

	w := sendQuery(getQueriesHandler, http.MethodGet, "/datatug/queries/all_queries", url.Values{"project": {project}, "root": {"personal"}})

	body := w.Body.String()
	if want := fmt.Sprintf("queries of project %q could not be loaded", project); w.Code != http.StatusInternalServerError || answerMessage(body) != want {
		t.Errorf("%d %s, want a 500 whose message is exactly %q", w.Code, body, want)
	}
	for _, leak := range []string{personalRoot, filepath.Base(personalRoot), projectDir, "invalid character", "load queries from", "parse "} {
		if strings.Contains(body, leak) {
			t.Errorf("the answer holds %q: %s", leak, body)
		}
	}
}
