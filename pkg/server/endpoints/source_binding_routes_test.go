package endpoints

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/access"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/datatug/datatug-core/pkg/storage"
)

func TestBoundSourceRequestGuardKeepsQualifiedReadsAndRefusesOtherRoutes(t *testing.T) {
	previousStore := storage.NewDatatugStore
	t.Cleanup(func() { storage.NewDatatugStore = previousStore })
	root, _ := boundEndpointFixture(t)
	guard := NewRequestGuard("127.0.0.1", 8989)
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodGet, "/datatug/ping", true},
		{http.MethodPost, "/datatug/exec/run_query", true},
		{http.MethodOptions, "/datatug/queries/save_query", true},
		{http.MethodGet, "/datatug/results/held", false},
		{http.MethodPost, "/datatug/queries/save_query", false},
		{http.MethodDelete, "/datatug/exec/run_query", false},
		{http.MethodGet, "/datatug/queries/all_queries?root=shared", true},
		{http.MethodGet, "/datatug/queries/all_queries?root=personal", false},
		{http.MethodGet, "/datatug/queries/all_queries?root=unknown", false},
	} {
		calls := 0
		w := httptest.NewRecorder()
		guard.Wrap(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusNoContent) })(w, httptest.NewRequest(test.method, "http://127.0.0.1:8989"+test.path, nil))
		if test.allowed {
			if w.Code != http.StatusNoContent || calls != 1 {
				t.Fatalf("qualified %s %s refused: status=%d calls=%d", test.method, test.path, w.Code, calls)
			}
		} else if w.Code != http.StatusForbidden || calls != 0 {
			t.Fatalf("unqualified %s %s dispatched: status=%d calls=%d", test.method, test.path, w.Code, calls)
		}
	}
	mustWriteFile(t, filepath.Join(root, "policies", "new-denial.yaml"), "changed")
	calls := 0
	w := httptest.NewRecorder()
	guard.Wrap(func(http.ResponseWriter, *http.Request) { calls++ })(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/datatug/ping", nil))
	if w.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("changed configuration reached guarded read: status=%d calls=%d", w.Code, calls)
	}
}

func TestBoundSourceProjectProjectionAndNativeSelectUseFixedSession(t *testing.T) {
	previousStore, previousContext := storage.NewDatatugStore, getContextFromRequest
	t.Cleanup(func() { storage.NewDatatugStore, getContextFromRequest = previousStore, previousContext })
	getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
	root, project := boundEndpointFixture(t)
	projectRequest := func(id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		getProjectFull(w, httptest.NewRequest(http.MethodGet, "/datatug/projects/project_full?"+url.Values{"id": {id}}.Encode(), nil))
		return w
	}
	w := projectRequest(project)
	var projection struct {
		ID           string `json:"id"`
		Environments []struct {
			ID        string `json:"id"`
			DbServers []struct {
				Catalogs []string `json:"catalogs"`
			} `json:"dbServers"`
		} `json:"environments"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &projection); err != nil || w.Code != http.StatusOK || projection.ID != project || len(projection.Environments) != 1 || projection.Environments[0].ID != "local" || len(projection.Environments[0].DbServers) != 1 || len(projection.Environments[0].DbServers[0].Catalogs) != 1 || projection.Environments[0].DbServers[0].Catalogs[0] != "chinook-local" || strings.Contains(w.Body.String(), ".sqlite") || strings.Contains(w.Body.String(), "support-notes") {
		t.Fatalf("bound metadata projection: status=%d err=%v body=%s", w.Code, err, w.Body.String())
	}
	if w = projectRequest("other-project"); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "chinook-local") {
		t.Fatalf("project substitution exposed metadata: %d %s", w.Code, w.Body.String())
	}
	selectTable := func(ctx context.Context, table string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		q := url.Values{"project": {project}, "env": {"local"}, "db": {"chinook-local"}, "from": {table}, "limit": {"1"}}
		executeSelectHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select?"+q.Encode(), nil).WithContext(ctx))
		return w
	}
	w = selectTable(context.Background(), "Customer")
	var result api.QueryResultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || len(result.Rows) != 1 || result.Rows[0]["CustomerId"] == nil || result.Provenance.ExecutionProfile != apicontract.ExecutionProfileProtected {
		t.Fatalf("allowed native table read: status=%d err=%v body=%s", w.Code, err, w.Body.String())
	}
	if w = selectTable(context.Background(), "Invoice"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "ACCESS_DENIED") || strings.Contains(w.Body.String(), "InvoiceId") {
		t.Fatalf("forbidden table released rows: %d %s", w.Code, w.Body.String())
	}
	denied := access.WithPolicy(context.Background(), access.MustPolicy("mandatory-deny", access.Root(access.Deny(access.Query))))
	if w = selectTable(denied, "Customer"); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "CustomerId") {
		t.Fatalf("mandatory context denial dropped: %d %s", w.Code, w.Body.String())
	}
	mustWriteFile(t, filepath.Join(root, "policies", "revoked.yaml"), "changed")
	if w = projectRequest(project); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "chinook-local") {
		t.Fatalf("changed configuration released projection: %d %s", w.Code, w.Body.String())
	}
}
