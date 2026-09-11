//go:build datatug_query_capture

package endpoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/datatug"
)

// This build's datatug-core has the capture field: create_query refuses
// the block with a 400 naming query.capture and writes nothing.
func assertClientCaptureOutcome(t *testing.T, w *httptest.ResponseRecorder, queryFile string) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "query.capture") {
		t.Errorf("body %s does not name query.capture", w.Body.String())
	}
	if _, err := os.Stat(queryFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists (stat err %v); nothing should have been written", queryFile, err)
	}
}

// Through apicore.Execute, a captured query's get_query response goes back
// through update_query with its capture block, and the stored capture is
// kept; the same body with the author changed is refused and changes
// nothing.
func TestLegacyUpdateQuery_CaptureRoundTrip(t *testing.T) {
	withApicoreHandle(t)
	scope, _ := captureTestSetup(t, "admin", []string{"admin"}, true)
	projStore, err := api.ProjectStoreFor(scope.Project)
	if err != nil {
		t.Fatal(err)
	}
	store := projStore.(datatug.RevisionedQueriesStore)
	seeded := datatug.QueryDefWithFolderPath{QueryDef: datatug.QueryDef{
		ProjectItem: datatug.ProjectItem{ProjItemBrief: datatug.ProjItemBrief{ID: "q1", Title: "q1"}},
		Type:        datatug.QueryTypeSQL,
		Capture:     &datatug.QueryCapture{Author: "alice", Environment: semanticTestEnv, Source: semanticTestSource},
	}}
	if _, err := store.PutQuery(context.Background(), &seeded, datatug.QueryWriteCondition{IfNoneMatch: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := httptest.NewRecorder()
	getQueryHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/queries/get_query?project="+scope.Project+"&id=q1", nil))
	if w.Code/100 != 2 {
		t.Fatalf("get_query status %d; body %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("get_query body %s: %v", w.Body.String(), err)
	}
	if _, ok := got["capture"].(map[string]any); !ok {
		t.Fatalf("get_query body %s carries no capture", w.Body.String())
	}
	update := func(t *testing.T, query map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"storage": "local", "project": scope.Project, "ID": "q1", "query": query})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		updateQuery(w, httptest.NewRequest(http.MethodPut, "/datatug/queries/update_query?project="+scope.Project+"&id=q1", bytes.NewReader(body)))
		return w
	}
	stored := func(t *testing.T) datatug.QueryDefWithFolderPath {
		t.Helper()
		current, err := store.LoadQueryRevision(context.Background(), "q1")
		if err != nil {
			t.Fatal(err)
		}
		return current.Query
	}

	t.Run("a forged author is refused", func(t *testing.T) {
		forged := maps.Clone(got)
		forgedCapture := maps.Clone(got["capture"].(map[string]any))
		forgedCapture["author"] = "mallory"
		forged["capture"], forged["title"] = forgedCapture, "forged"
		w := update(t, forged)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "query.capture") {
			t.Fatalf("status %d, want 400 naming query.capture; body %s", w.Code, w.Body.String())
		}
		if q := stored(t); q.Title != "q1" || q.Capture == nil || q.Capture.Author != "alice" {
			t.Errorf("stored %+v, want it unchanged", q)
		}
	})
	t.Run("the echoed capture is kept", func(t *testing.T) {
		echoed := maps.Clone(got)
		echoed["title"] = "renamed"
		w := update(t, echoed)
		if w.Code != http.StatusCreated {
			t.Fatalf("status %d, want 201; body %s", w.Code, w.Body.String())
		}
		if q := stored(t); q.Title != "renamed" || q.Capture == nil || q.Capture.Author != "alice" {
			t.Errorf("stored %+v, want the new title and alice's capture", q)
		}
	})
}
