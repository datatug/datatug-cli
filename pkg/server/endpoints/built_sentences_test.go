package endpoints

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
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

				w := sendQuery(route.handler, http.MethodGet, route.path, route.query(project))
				body := w.Body.String()
				if want := route.sentence(project); w.Code != http.StatusInternalServerError || answerMessage(body) != want {
					t.Errorf("%d %s, want a 500 whose message is exactly %q", w.Code, body, want)
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
