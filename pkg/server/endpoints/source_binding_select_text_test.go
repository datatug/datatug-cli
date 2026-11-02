package endpoints

import (
	"context"
	"encoding/json"
	"github.com/datatug/datatug-cli/pkg/api"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundSelectQueryTextNeverReadsDeniedTable(t *testing.T) {
	_, project := aliasEndpointFixture(t)
	for _, tc := range []struct {
		name, where, cols string
		positive, literal bool
	}{
		{name: "ordinary", positive: true},
		{name: "supported-equality", where: "FirstName:Luís", cols: "CustomerId", positive: true},
		{name: "where-subselect-without-separator", where: "id IN (SELECT InvoiceId FROM Invoice)"},
		{name: "where-field-subselect", where: "CustomerId IN (SELECT InvoiceId FROM Invoice):1"},
		{name: "where-second-field-subselect", where: "CustomerId:1;CustomerId IN (SELECT InvoiceId FROM Invoice):1"},
		{name: "where-value-is-bound-literal", where: "CustomerId:1 OR CustomerId IN (SELECT InvoiceId FROM Invoice)", literal: true},
		{name: "cols-subselect", cols: "(SELECT InvoiceId FROM Invoice LIMIT 1)"},
		{name: "cols-mixed-subselect", cols: "CustomerId,(SELECT InvoiceId FROM Invoice LIMIT 1) AS InvoiceId"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{"project": {project}, "env": {"local"}, "db": {"chinook-local"}, "from": {"Customer"}, "limit": {"1"}}
			if tc.where != "" {
				q.Set("where", tc.where)
			}
			if tc.cols != "" {
				q.Set("cols", tc.cols)
			}
			w := httptest.NewRecorder()
			executeSelectHandler(w, httptest.NewRequest(http.MethodGet, "/datatug/exec/select?"+q.Encode(), nil))
			t.Logf("status=%d body=%s", w.Code, w.Body.String())
			if tc.positive {
				assertBoundNativeIdentityResponse(t, w, true)
				return
			}
			if !tc.literal && w.Code == http.StatusOK {
				t.Fatal("query text accepted", w.Body.String())
			}
			if strings.Contains(w.Body.String(), `"InvoiceId"`) {
				t.Fatal("denied identity released", w.Body.String())
			}
			if tc.literal && w.Code == http.StatusOK {
				var response api.QueryResultResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				// SQL-looking text remains one bound equality value.
				if len(response.Rows) != 0 {
					t.Fatal("literal became SQL", response.Rows)
				}
			}
		})
	}
}
func TestBoundGetQueryPersonalRootCannotReachPersonalStorage(t *testing.T) {
	withApicoreHandle(t)
	setPersonalQueriesBaseDir(t)
	root, project := aliasEndpointFixture(t)
	const id = "bound-root-check"
	const shared = "SHARED_VISIBLE_BOUND_ROOT"
	const personal = "PERSONAL_SECRET_BOUND_ROOT"
	mustWriteFile(t, filepath.Join(root, "queries", id+".query.json"), `{"createdAt":"2026-01-01T00:00:00Z","id":"`+id+`","title":"`+shared+`","type":"SQL"}`)
	mustWriteFile(t, filepath.Join(root, "queries", id+".query.sql"), "SELECT CustomerId FROM Customer")
	writePersonalQuery(t, project, id, personal)
	guard := NewRequestGuard("127.0.0.1", 8989)
	for _, rootName := range []string{"shared", "personal", "unknown"} {
		t.Run(rootName, func(t *testing.T) {
			q := url.Values{"project": {project}, "query": {id}, "root": {rootName}}
			w := httptest.NewRecorder()
			guard.Wrap(getQueryHandler)(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/datatug/queries/get_query?"+q.Encode(), nil).WithContext(context.Background()))
			t.Logf("root=%s status=%d body=%s", rootName, w.Code, w.Body.String())
			if strings.Contains(w.Body.String(), personal) {
				t.Fatal("personal storage reached", w.Body.String())
			}
			if w.Code >= 200 && w.Code < 300 && !strings.Contains(w.Body.String(), shared) {
				t.Fatal("shared query positive missing", w.Body.String())
			}
			if rootName == "shared" && w.Code != http.StatusCreated {
				t.Fatal("shared query refused", w.Body.String())
			}
		})
	}
}

func TestBoundGetQueryPersonalOnlyIDStaysOutsideSharedLookup(t *testing.T) {
	withApicoreHandle(t)
	setPersonalQueriesBaseDir(t)
	_, project := aliasEndpointFixture(t)
	const secret = "PERSONAL_ONLY_SECRET_BOUND_ROOT"
	writePersonalQuery(t, project, "personal-only", secret)
	q := url.Values{"project": {project}, "query": {"personal-only"}, "root": {"personal"}}
	w := httptest.NewRecorder()
	NewRequestGuard("127.0.0.1", 8989).Wrap(getQueryHandler)(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8989/datatug/queries/get_query?"+q.Encode(), nil))
	t.Logf("personal-only status=%d body=%s", w.Code, w.Body.String())
	if w.Code >= 200 && w.Code < 300 || strings.Contains(w.Body.String(), secret) {
		t.Fatal("personal-only query reached", w.Code, w.Body.String())
	}
}
