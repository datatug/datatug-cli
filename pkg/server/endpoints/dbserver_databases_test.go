package endpoints

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug-core/pkg/dto"
	"github.com/datatug/datatug-core/pkg/storage"
)

// GET /datatug/dbserver-databases connects only to a server that the served project records.
// These tests send it requests that pass every check of a name, over the real request
// lifecycle and the file store of a real project folder, and none of them reaches a server:
// a server that the project does not record is refused with a fixed 400 before anything is
// dialled (the executor is counted by the tests of api.GetServerDatabases, where a seam for
// it is).

const notRecordedSentence = "the project does not record this db server"

func TestRoutes_DbServerDatabasesRefusesAServerTheProjectDoesNotRecord(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	asked := countProjectStoreAsks(t)

	for name, query := range map[string]url.Values{
		"a host that is not recorded":             {"project": {scope.Project}, "driver": {"sqlserver"}, "host": {"not-recorded.invalid"}},
		"a host that is not recorded with a port": {"project": {scope.Project}, "proj": {scope.Project}, "driver": {"sqlserver"}, "host": {"not-recorded.invalid"}, "port": {"1433"}},
		"an address": {"proj": {scope.Project}, "env": {scope.Environment}, "driver": {"sqlserver"}, "host": {"10.255.255.1"}},
	} {
		*asked = 0
		w := sendQuery(getServerDatabases, http.MethodGet, "/datatug/dbserver-databases", query)
		body := w.Body.String()
		if w.Code != http.StatusBadRequest || !strings.Contains(body, notRecordedSentence) {
			t.Errorf("%s: %d %s, want a 400 that says %q", name, w.Code, body, notRecordedSentence)
		}
		if host := query.Get("host"); strings.Contains(body, host) {
			t.Errorf("%s: the answer names the host the client sent: %s", name, body)
		}
		if *asked != 1 {
			t.Errorf("%s: the project store was asked for %d times, want once: the db servers of the project are looked up first", name, *asked)
		}
	}

	// A driver that has no connection to make: refused before the project is asked.
	*asked = 0
	w := sendQuery(getServerDatabases, http.MethodGet, "/datatug/dbserver-databases", url.Values{"project": {scope.Project}, "env": {scope.Environment}, "driver": {"sqlite3"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "sqlserver server only") || *asked != 0 {
		t.Errorf("a sqlite3 server: %d %s (%d project stores), want the fixed 400 and no project store", w.Code, w.Body.String(), *asked)
	}
}

func TestRoutes_DbServerDatabasesRefusesAProjectThatIsNotServed(t *testing.T) {
	withApicoreHandle(t)
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	scope, _ := captureTestSetup(t, "alice", []string{"admin"}, true)
	asked := countProjectStoreAsks(t)

	w := sendQuery(getServerDatabases, http.MethodGet, "/datatug/dbserver-databases", url.Values{"project": {"not-served"}, "driver": {"sqlserver"}, "host": {"db1.invalid"}})
	body := w.Body.String()
	if w.Code != http.StatusBadRequest || !strings.Contains(body, "no store configured for project") || !strings.Contains(body, `\"not-served\"`) {
		t.Errorf("a project that is not served: %d %s, want a 400 that says no store is configured for it", w.Code, body)
	}
	if *asked != 0 {
		t.Errorf("a project that is not served reached %d project stores, want 0", *asked)
	}
	_ = scope

	// The answer is the one of the other routes that take the project from the query: a bad
	// request that names the storage, with the same sentence.
	var answer struct {
		Error, Code, Field string
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Code != "INVALID_REQUEST" || answer.Field != "storage" {
		t.Errorf("a project that is not served: code %q and field %q, want INVALID_REQUEST and storage: %s", answer.Code, answer.Field, body)
	}
	other := sendQuery(getBoard, http.MethodGet, "/datatug/boards/board", url.Values{"project": {"not-served"}, "id": {"b1"}})
	if other.Code != w.Code || other.Body.String() != body {
		t.Errorf("the board route answers %d %s for a project that is not served, the route of the databases %d %s: want the same", other.Code, other.Body.String(), w.Code, body)
	}
}

// A request whose context cannot be made is answered as an error, and nothing is looked up.
func TestRoutes_DbServerDatabasesAnswersAContextThatCannotBeMade(t *testing.T) {
	saved, savedFunc := getContextFromRequest, getServerDatabasesFunc
	t.Cleanup(func() { getContextFromRequest, getServerDatabasesFunc = saved, savedFunc })
	getContextFromRequest = func(*http.Request) (context.Context, error) { return nil, errors.New("no context") }
	reached := false
	getServerDatabasesFunc = func(context.Context, dto.GetServerDatabasesRequest) ([]*datatug.DbCatalog, error) {
		reached = true
		return nil, nil
	}

	w := sendQuery(getServerDatabases, http.MethodGet, "/datatug/dbserver-databases", url.Values{"proj": {"p1"}, "driver": {"sqlserver"}, "host": {"db1.invalid"}})

	if w.Code != http.StatusInternalServerError || reached {
		t.Errorf("%d %s (reached the api: %v), want an error answer and nothing looked up", w.Code, w.Body.String(), reached)
	}
}
