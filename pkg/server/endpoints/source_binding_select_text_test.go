package endpoints

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/datatug/datatug-cli/pkg/api"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundSelectQueryTextNeverReadsDeniedTable(t *testing.T) {
	root, project := aliasEndpointFixture(t)
	const deniedSentinel = 987654321
	native, err := sql.Open("sqlite", filepath.Join(root, "chinook.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = native.Exec(`INSERT INTO Invoice(InvoiceId,CustomerId,InvoiceDate,Total) SELECT ?,CustomerId,InvoiceDate,Total FROM Invoice LIMIT 1`, deniedSentinel); err != nil {
		t.Fatal(err)
	}
	var customerMatches, invoiceMatches int
	if err = native.QueryRow(`SELECT COUNT(*) FROM Customer WHERE CustomerId = ?`, deniedSentinel).Scan(&customerMatches); err != nil {
		t.Fatal(err)
	}
	if err = native.QueryRow(`SELECT COUNT(*) FROM Invoice WHERE InvoiceId = ?`, deniedSentinel).Scan(&invoiceMatches); err != nil {
		t.Fatal(err)
	}
	if customerMatches != 0 || invoiceMatches != 1 {
		t.Fatal("sentinel must be unique to denied Invoice", customerMatches, invoiceMatches)
	}
	if err = native.Close(); err != nil {
		t.Fatal(err)
	}
	// Admit a fresh profile only after constructing the owned native fixture.
	session, declared, err := secureread.LoadBoundSession(context.Background(), root, secureread.BoundSessionOptions{Required: true})
	if err != nil || !declared {
		t.Fatal("quote fixture admission", declared, err)
	}
	api.ConfigureSecureSession(session, map[string]string{project: root}, api.Capabilities{})
	t.Logf("owned Invoice sentinel=%d, Customer matches=%d, Invoice matches=%d", deniedSentinel, customerMatches, invoiceMatches)
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
		{name: "cols-doublequote-breakout", cols: "CustomerId\" FROM Customer UNION SELECT InvoiceId FROM Invoice --"},
		{name: "cols-backtick-breakout", cols: "CustomerId` FROM Customer UNION SELECT InvoiceId FROM Invoice --"},
		{name: "cols-bracket-breakout", cols: "CustomerId] FROM Customer UNION SELECT InvoiceId FROM Invoice --"},
		{name: "where-doublequote-breakout", where: "CustomerId\" IN (SELECT InvoiceId FROM Invoice) OR \"CustomerId:1"},
		{name: "where-backtick-breakout", where: "CustomerId` IN (SELECT InvoiceId FROM Invoice) OR `CustomerId:1"},
		{name: "where-bracket-breakout", where: "CustomerId] IN (SELECT InvoiceId FROM Invoice) OR [CustomerId:1"},
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
			// UNION can label denied Invoice IDs as CustomerId. Inspect the unique
			// denied value independently of every response column/key name.
			if strings.Contains(w.Body.String(), "987654321") {
				t.Fatal("denied sentinel released", w.Code, w.Body.String())
			}
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
