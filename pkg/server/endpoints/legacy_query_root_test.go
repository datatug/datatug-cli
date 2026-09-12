package endpoints

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// decodeSavedQuery fails unless w is create_query's or update_query's 201
// for root query id, answered with the root's folderPath "~", and returns
// the response body.
func decodeSavedQuery(t *testing.T, w *httptest.ResponseRecorder, id string) map[string]any {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d, want 201; body %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", w.Body.String(), err)
	}
	if body["id"] != id || body["folderPath"] != "~" {
		t.Errorf("saved query %v, want id %q in folderPath \"~\"", body, id)
	}
	return body
}

// A root query saved through create_query or update_query - folderPath
// "~", the root id get_query answers with - was written and then answered
// with a 500: the write maps "~" to the store's root "" and returned the
// query that way, and apicore's validation of the response refuses an
// empty folderPath. The routes answer 201 with folderPath "~", and a
// get_query response round-trips through update_query.
func TestLegacyQueryWrites_RootQueryRoundTrip(t *testing.T) {
	withApicoreHandle(t)
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	create := `{"storage":"local","project":"` + scope.Project + `","query":{"id":"q1","folderPath":"~","title":"q1","type":"SQL"}}`
	w := httptest.NewRecorder()
	createQuery(w, httptest.NewRequest(http.MethodPost, "/datatug/queries/create_query?project="+scope.Project, strings.NewReader(create)))
	decodeSavedQuery(t, w, "q1")

	w = httptest.NewRecorder()
	getQueryHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/queries/get_query?project="+scope.Project+"&id=q1", nil))
	if w.Code/100 != 2 {
		t.Fatalf("get_query status %d; body %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("get_query body %s: %v", w.Body.String(), err)
	}
	got["title"] = "renamed"
	update, err := json.Marshal(map[string]any{"storage": "local", "project": scope.Project, "ID": "q1", "query": got})
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	updateQuery(w, httptest.NewRequest(http.MethodPut, "/datatug/queries/update_query?project="+scope.Project+"&id=q1", bytes.NewReader(update)))
	if saved := decodeSavedQuery(t, w, "q1"); saved["title"] != "renamed" {
		t.Errorf("saved title %v, want the update's", saved["title"])
	}
}
