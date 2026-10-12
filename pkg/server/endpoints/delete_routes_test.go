package endpoints

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/storage"
)

// A delete of an item that the project does not have is answered as that, a 404 with the
// sentence that says the item is not found, where a delete that fails is a 500 with the
// sentence that says it could not be deleted: the file store answers nothing for the first,
// so the route looks the item up first. Neither answer quotes a path of the server.

func TestRoutes_ADeleteOfAMissingItemIsNotFoundAndAFailedDeleteIsNot(t *testing.T) {
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
	// pathThatCannotBeRemoved makes path a folder with a file in it, which the store finds
	// (a file is expected there) and cannot remove.
	pathThatCannotBeRemoved := func(path string) {
		t.Helper()
		write(filepath.Join(path, "in-it"), "x")
	}
	// leaks are texts of the store and of the server that no answer holds.
	leaks := []string{projectDir, filepath.Base(projectDir), "no such file", "directory not empty", "not a directory", "is a directory", "failed to load", "remove "}

	for _, kind := range []struct {
		name  string
		route http.HandlerFunc
		path  string
		// query makes the request for the item with this ID.
		query func(id string) url.Values
		// layout puts an item that is there under id, and one that cannot be removed under
		// the other.
		layout func(there, stuck string)
		// shown is how an answer names the item with this ID.
		shown func(id string) string
		// there is the file of the item that is there.
		there func(id string) string
		// skip says why a kind is not tried on this system.
		skip func() string
		// noFailure is set for a kind whose store deletes nothing that it cannot find.
		noFailure bool
	}{
		{name: "board", route: deleteBoard, path: "/datatug/boards/delete_board",
			query: func(id string) url.Values { return url.Values{"project": {scope.Project}, "id": {id}} },
			layout: func(there, stuck string) {
				write(filepath.Join(projectDir, "boards", there, "board.json"), `{"title":"T"}`)
				pathThatCannotBeRemoved(filepath.Join(projectDir, "boards", stuck, "board.json"))
			},
			shown: func(id string) string { return id },
			there: func(id string) string { return filepath.Join(projectDir, "boards", id, "board.json") }},
		{name: "entity", route: deleteEntity, path: "/datatug/entities/delete_entity",
			query: func(id string) url.Values { return url.Values{"project": {scope.Project}, "id": {id}} },
			layout: func(there, stuck string) {
				write(filepath.Join(projectDir, "entities", there, there+".entity.json"), `{"title":"E"}`)
				pathThatCannotBeRemoved(filepath.Join(projectDir, "entities", stuck, stuck+".entity.json"))
			},
			shown: func(id string) string { return id },
			there: func(id string) string { return filepath.Join(projectDir, "entities", id, id+".entity.json") }},
		{name: "folder", route: deleteFolder, path: "/datatug/folders/delete_folder", noFailure: true,
			query: func(id string) url.Values { return url.Values{"project": {scope.Project}, "id": {id}} },
			layout: func(there, _ string) {
				write(filepath.Join(projectDir, "folders", filepath.FromSlash(there), ".datatug-folder.json"), `{"name":"f"}`)
			},
			shown: func(id string) string { return id },
			there: func(id string) string {
				return filepath.Join(projectDir, "folders", filepath.FromSlash(id), ".datatug-folder.json")
			}},
	} {
		t.Run(kind.name, func(t *testing.T) {
			if kind.skip != nil {
				if why := kind.skip(); why != "" {
					t.Skip(why)
				}
			}
			there, stuck, missing := "there", "stuck", "missing"
			if kind.name == "folder" {
				there, stuck, missing = "a/there", "a/stuck", "a/missing"
			}
			kind.layout(there, stuck)

			// An item that is not there: a 404 that says so.
			w := sendQuery(kind.route, http.MethodDelete, kind.path, kind.query(missing))
			body := w.Body.String()
			if want := kind.name + ` \"` + kind.shown(missing) + `\" not found`; w.Code != http.StatusNotFound || !strings.Contains(body, want) {
				t.Errorf("missing %s: %d %s, want a 404 that says %q", kind.name, w.Code, body, want)
			}
			for _, leak := range leaks {
				if strings.Contains(body, leak) {
					t.Errorf("missing %s: the answer holds %q: %s", kind.name, leak, body)
				}
			}

			// An item that is there: deleted, a 200.
			w = sendQuery(kind.route, http.MethodDelete, kind.path, kind.query(there))
			if w.Code != http.StatusOK {
				t.Errorf("%s that is there: %d %s, want 200", kind.name, w.Code, w.Body.String())
			}
			if !noFileLeft(kind.name, kind.there(there)) {
				t.Errorf("%s that is there: its file is still there after the delete", kind.name)
			}

			// A delete that fails: a 500 that says so.
			if kind.noFailure {
				return
			}
			w = sendQuery(kind.route, http.MethodDelete, kind.path, kind.query(stuck))
			body = w.Body.String()
			if want := `could not delete ` + kind.name + ` \"` + kind.shown(stuck) + `\"`; w.Code != http.StatusInternalServerError || !strings.Contains(body, want) {
				t.Errorf("%s that cannot be removed: %d %s, want a 500 that says %q", kind.name, w.Code, body, want)
			}
			for _, leak := range leaks {
				if strings.Contains(body, leak) {
					t.Errorf("%s that cannot be removed: the answer holds %q: %s", kind.name, leak, body)
				}
			}
		})
	}
}

// noFileLeft reports whether the file of an item is gone. The delete of a folder in the file
// store of datatug-core does not remove the file of the folder it looks up (it builds another
// path), so a folder's file is not looked at.
func noFileLeft(kind, path string) bool {
	if kind == "folder" {
		return true
	}
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// A db server is one file named for its driver and host: missing, there, and one that
// cannot be removed.
func TestRoutes_ADeleteOfAMissingDbServerIsNotFoundAndAFailedDeleteIsNot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the file of a db server has a ':' in its name, which Windows does not allow")
	}
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	projectDir, _ := api.ProjectDir(scope.Project)
	folder := filepath.Join(projectDir, "dbs", "sqlserver")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	there := filepath.Join(folder, "sqlserver:db1.example.com.dbserver.json")
	if err := os.WriteFile(there, []byte(`{"server":{"driver":"sqlserver","host":"db1.example.com"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stuck := filepath.Join(folder, "sqlserver:db2.example.com.dbserver.json")
	if err := os.MkdirAll(stuck, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stuck, "in-it"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	leaks := []string{projectDir, filepath.Base(projectDir), "no such file", "directory not empty", "is a directory", "failed to load", "remove "}

	for _, tc := range []struct {
		name string
		host string
		want int
		text string
	}{
		{"missing", "db0.example.com", http.StatusNotFound, `db server \"sqlserver:db0.example.com\" not found`},
		{"there", "db1.example.com", http.StatusOK, ""},
		{"stuck", "db2.example.com", http.StatusInternalServerError, `could not delete db server \"sqlserver:db2.example.com\"`},
	} {
		w := sendQuery(deleteDbServer, http.MethodDelete, "/datatug/dbserver-delete", url.Values{"project": {scope.Project}, "driver": {"sqlserver"}, "host": {tc.host}})
		body := w.Body.String()
		if w.Code != tc.want || !strings.Contains(body, tc.text) {
			t.Errorf("%s: %d %s, want %d with %q", tc.name, w.Code, body, tc.want, tc.text)
		}
		for _, leak := range leaks {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the answer holds %q: %s", tc.name, leak, body)
			}
		}
	}
	if _, err := os.Stat(there); !os.IsNotExist(err) {
		t.Errorf("the db server that is there was not deleted: %v", err)
	}
}

// The writer of a delete changes the status of a missing item only, and shows the writer it
// wraps to http.ResponseController.
func TestMissingItemWriter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing bool
		status  int
		want    int
	}{
		{"a missing item is a 404 where apicore gives a 500", true, http.StatusInternalServerError, http.StatusNotFound},
		{"a failed delete is a 500", false, http.StatusInternalServerError, http.StatusInternalServerError},
		{"a bad request stays one", true, http.StatusBadRequest, http.StatusBadRequest},
		{"a delete that is done stays done", false, http.StatusOK, http.StatusOK},
	} {
		recorder := httptest.NewRecorder()
		missing := tc.missing
		writer := missingItemWriter{ResponseWriter: recorder, missing: &missing}
		writer.WriteHeader(tc.status)
		if recorder.Code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, recorder.Code, tc.want)
		}
		if writer.Unwrap() != http.ResponseWriter(recorder) {
			t.Errorf("%s: Unwrap does not give the writer that is wrapped", tc.name)
		}
	}
}
