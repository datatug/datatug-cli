package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestResultVersionPreferencePersistsAndLimitsVisibleHistory(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	if got, err := store.ResultVersionsToKeep(ctx); err != nil || got != 2 {
		t.Fatalf("default version limit = %d, %v", got, err)
	}
	if err := store.SetResultVersionsToKeep(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err := store.SetResultVersionsToKeep(ctx, 0); err == nil {
		t.Fatal("zero version limit accepted")
	}
	_ = store.Close()
	reopened := openTestStore(t, path, testScope())
	if got, err := reopened.ResultVersionsToKeep(ctx); err != nil || got != 3 {
		t.Fatalf("restored version limit = %d, %v", got, err)
	}
	now := time.Now()
	session := ChatSession{RecordSets: map[string]RecordSet{
		"first":  {ID: "first", CreatedAt: now},
		"second": {ID: "second", RefreshParentID: "first", CreatedAt: now.Add(time.Second)},
		"third":  {ID: "third", RefreshParentID: "second", CreatedAt: now.Add(2 * time.Second)},
	}}
	hidden, _ := hiddenRefreshVersions(session, 2)
	if !hidden["first"] || hidden["second"] || hidden["third"] {
		t.Fatalf("wrong visible versions: %#v", hidden)
	}
}

func TestHTTPChangeIndicatorIncludesStatusAndContentType(t *testing.T) {
	previous := HTTPResponse{StatusCode: 200, ContentType: "text/plain", Body: []byte("same")}
	if httpResponseChanged(previous, previous) {
		t.Fatal("identical response should be unchanged")
	}
	current := previous
	current.StatusCode = 500
	if !httpResponseChanged(current, previous) {
		t.Fatal("status change not indicated")
	}
	current = previous
	current.ContentType = "application/json"
	if !httpResponseChanged(current, previous) {
		t.Fatal("content type change not indicated")
	}
}

func TestRefreshRecordSetUsesSavedDTQLAndPreservesPrevious(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	first := sessions.activeID
	origin, err := store.AppendUser(ctx, first, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	doc := "from: {name: Invoice}\nlimit: 20"
	query, err := store.AppendQuery(ctx, first, origin.ID, "sqlite:///chinook.db", QueryResult{Title: "Invoices", DTQL: doc, SourceID: "chinook", Result: secureread.Result{Columns: []string{"InvoiceId"}}})
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 412}}}}}
	sessions.ConfigureQueryExecutor(executor)
	snapshot, err := sessions.RefreshRecordSet(ctx, first, query.RecordSetID)
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || executor.doc != doc || executor.source != "sqlite:///chinook.db" || len(snapshot.RecordSets) != 2 {
		t.Fatalf("refresh did not reuse saved query: %+v, %#v", executor, snapshot.RecordSets)
	}
	linked := false
	for _, record := range snapshot.RecordSets {
		linked = linked || record.RefreshParentID == query.RecordSetID
	}
	if !linked {
		t.Fatal("new immutable RecordSet has no parent version")
	}
}

func TestHTTPHeadersAndTimingsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Data-Version", "v2")
		w.Header().Set("Set-Cookie", "secret=value")
		w.Header().Set("Authentication-Info", "nextnonce=secret")
		w.Header().Set("X-Access-Key", "secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"Ada"}]`))
	}))
	defer server.Close()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	session, err := store.Create(ctx, "HTTP")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := store.AppendUser(ctx, session.ID, "/http get "+server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, query, failure := fetchHTTPResult(ctx, server.URL, server.URL)
	if failure != "" || response.TimeToResponse <= 0 || response.DownloadTime < 0 {
		t.Fatalf("timing/fetch failed: %q, %+v", failure, response)
	}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, origin.ID, response, query); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	reopened := openTestStore(t, path, testScope())
	loaded, err := reopened.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range loaded.HTTPResponses {
		if saved.Headers["X-Data-Version"][0] != "[redacted]" || !strings.Contains(saved.Headers["Set-Cookie"][0], "redacted") || saved.Headers["Authentication-Info"][0] != "[redacted]" || saved.Headers["X-Access-Key"][0] != "[redacted]" || saved.TimeToResponse <= 0 {
			t.Fatalf("HTTP metadata was not safely persisted: %+v", saved)
		}
	}
}

func TestHTTPRedirectTraceIsPersistedWithoutQuerySecrets(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/start" {
			http.Redirect(w, request, "/final?token=secret", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	defer server.Close()
	response, query, failure := fetchHTTPResult(ctx, server.URL+"/start", server.URL+"/start")
	if failure != "" || query == nil || len(response.Redirects) != 1 || response.Redirects[0].StatusCode != http.StatusFound || response.RequestHasQuery {
		t.Fatalf("redirect trace = %+v, failure=%q", response, failure)
	}
	if strings.Contains(response.FinalURL, "secret") || strings.Contains(response.Redirects[0].URL, "secret") {
		t.Fatal("redirect token leaked into saved URL")
	}
	store := openTestStore(t, testStorePath(t), testScope())
	session, err := store.Create(ctx, "Redirect")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := store.AppendUser(ctx, session.ID, "/http get "+server.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, origin.ID, response, query); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range loaded.HTTPResponses {
		if len(saved.Redirects) != 1 || !strings.Contains(saved.FinalURL, "/final") || strings.Contains(saved.FinalURL, "secret") {
			t.Fatalf("saved redirect trace = %+v", saved)
		}
	}
}
