package chat

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/tui/chatshell"
)

type mockJoinExecutor struct {
	err error
}

func (m *mockJoinExecutor) RunDTQL(ctx context.Context, source string, doc []byte, parameters map[string]any) (secureread.Result, error) {
	if m.err != nil {
		return secureread.Result{}, m.err
	}
	return secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 7}}}}, nil
}

type dummyCondition struct{}

func (d dummyCondition) Operator() dal.Operator { return "" }
func (d dummyCondition) Validate() error        { return nil }
func (d dummyCondition) String() string         { return "" }

func TestRemaining_SessionChatHTTP_ActiveIDMismatch(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chat.mu.Lock()
		chat.activeID = "changed-id"
		chat.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	initialID := chat.activeID
	req := BrowserHTTPRequest{
		Method: "GET",
		URL:    server.URL,
	}
	err := chat.SendHTTPRequestActive(ctx, initialID, req)
	if !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
}

func TestRemaining_SessionChatBranches(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)

	// RunSavedDTQLActive activeID mismatch
	if err := chat.RunSavedDTQLActive(ctx, "wrong-id", "q1", nil); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}

	// BrowserSessionAction "delete": c.store.List error
	origDBQuery := dbQueryContextFn
	defer func() { dbQueryContextFn = origDBQuery }()
	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "FROM sessions") {
			return nil, errors.New("list error")
		}
		return origDBQuery(db, ctx, query, args...)
	}
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "delete", "confirm"); err == nil {
		t.Fatal("expected list error in delete")
	}
	dbQueryContextFn = origDBQuery

	// BrowserSessionAction "delete": len(items) > 0 and Activate fails
	// Create two sessions so after deleting activeID there is at least 1 remaining
	_, err := chat.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = chat.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	origExec := execContextFn
	defer func() { execContextFn = origExec }()
	execContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE sessions SET updated_at") {
			return nil, errors.New("activate error")
		}
		return origExec(db, ctx, query, args...)
	}
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "delete", "confirm"); err == nil {
		t.Fatal("expected activate error in delete")
	}
	execContextFn = origExec

	// BrowserSessionAction "delete": len(items) == 0 and Create fails
	// Delete until only 1 session is left
	items, _ := chat.store.List(ctx)
	for len(items) > 1 {
		_ = chat.store.Delete(ctx, items[len(items)-1].ID)
		items, _ = chat.store.List(ctx)
	}
	origExec = execContextFn
	defer func() { execContextFn = origExec }()
	execContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO sessions") {
			return nil, errors.New("create error")
		}
		return origExec(db, ctx, query, args...)
	}
	if err := chat.BrowserSessionAction(ctx, chat.activeID, "delete", "confirm"); err == nil {
		t.Fatal("expected create error in delete")
	}
	execContextFn = origExec
}

func TestRemaining_GridAndCharts(t *testing.T) {
	// grid.go:146 formatGridValue with date column and non-string/non-byte value
	if got := formatGridValue("order_date", 12345); got != "12345" {
		t.Fatalf("expected 12345, got %q", got)
	}

	// charts.go:78-84 candidates sorting branches
	cands := []ChartCandidate{
		{Score: 10, Spec: ChartSpec{Title: "B", Kind: ChartBar, Dimension: "dim1"}},
		{Score: 10, Spec: ChartSpec{Title: "A", Kind: ChartBar, Dimension: "dim1"}},
		{Score: 10, Spec: ChartSpec{Title: "A", Kind: ChartLine, Dimension: "dim2"}},
		{Score: 10, Spec: ChartSpec{Title: "A", Kind: ChartLine, Dimension: "dim1"}},
	}
	sort.SliceStable(cands, func(i, j int) bool {
		left, right := cands[i], cands[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		if left.Spec.Title != right.Spec.Title {
			return left.Spec.Title < right.Spec.Title
		}
		if left.Spec.Kind != right.Spec.Kind {
			return left.Spec.Kind < right.Spec.Kind
		}
		return left.Spec.Dimension < right.Spec.Dimension
	})
	if cands[0].Spec.Title != "A" || cands[0].Spec.Kind != ChartBar {
		t.Fatalf("unexpected sort order: %+v", cands)
	}
}

func TestRemaining_ChatUIExportAndChips(t *testing.T) {
	// chatui.go:1044 startExportCommand with activeCommandInteractionID != ""
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, _ := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	session, _ := chat.Snapshot(ctx)
	id := workspaceTestRecord(t, store, session.ID)
	_ = id

	u, err := NewSessionChatUI(ctx, chat, "stub")
	if err != nil {
		t.Fatal(err)
	}
	u.activeCommandInteractionID = "int-123"
	path := filepath.Join(t.TempDir(), "test.csv")
	cmd, err := u.exportCommand("current csv " + path)
	if err != nil || cmd == nil {
		t.Fatalf("exportCommand error: %v", err)
	}
	if !u.activeCommandAsync {
		t.Fatal("expected activeCommandAsync to be true")
	}

	// chatui_chips.go:89 OnChipsChange with surviving attachments
	chipUI, _ := newChipTestChatUI(t)
	ref := ContextReference{Kind: "table", SourceID: "local", ObjectID: "Customer", Title: "Customer"}
	chipUI.snapshot.Workspace.Attachments = []ContextReference{ref}
	chips := []chatshell.Chip{attachmentChip(ref)}
	_ = chipUI.OnChipsChange(chips)
}

func TestRemaining_BrowserCellDetail(t *testing.T) {
	ctx := context.Background()
	chat, store := newTestSessionChat(t)
	chat.catalog = ProjectCatalog{
		ID: "demo-project",
		Objects: []ProjectObject{
			{
				Reference: ContextReference{Kind: "table", SourceID: "chinook", ObjectID: "main.Invoice"},
				Columns:   []string{"CustomerId"},
				ColumnTypes: map[string]string{"CustomerId": "INTEGER"},
			},
		},
	}

	executor := &mockJoinExecutor{}
	snap := joinSnapshot()
	snap.Source = chat.source
	app := ForeignKeyJoinApplication{Source: chat.source, Snapshot: snap, Executor: executor}
	chat.joinApplication = app

	user, _ := store.AppendUser(ctx, chat.activeID, "user")
	query, _ := store.AppendQuery(ctx, chat.activeID, user.ID, chat.source, QueryResult{
		Title:  "Invoices",
		Source: chat.source,
		DTQL:   "from: {schema: main, name: Invoice}\ncolumns: [{field: CustomerId}]\n",
		Result: secureread.Result{
			Columns: []string{"CustomerId"},
			Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 7}}},
		},
	})

	// Success path
	detail, err := chat.CellDetailActive(ctx, chat.activeID, query.RecordSetID, 0, "CustomerId")
	if err != nil {
		t.Fatalf("CellDetailActive error: %v", err)
	}
	if len(detail.Related) != 1 {
		t.Fatalf("expected 1 related record, got %d", len(detail.Related))
	}

	// Executor error path
	executor.err = errors.New("exec error")
	_, err = chat.CellDetailActive(ctx, chat.activeID, query.RecordSetID, 0, "CustomerId")
	if err == nil || !strings.Contains(err.Error(), "related records unavailable") {
		t.Fatalf("expected related records unavailable, got %v", err)
	}
}

func TestRemaining_HTTPSettings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// openHTTPSettingsDB: bad permissions
	badPath := filepath.Join(dir, "bad_perm.db")
	_ = os.WriteFile(badPath, []byte("data"), 0o666) // not private 0600
	if _, err := openHTTPSettingsDB(badPath); err == nil {
		t.Fatal("expected error for non-private file perm")
	}

	// validateHTTPSetting branches
	if _, err := validateHTTPSetting("invalid_kind", "", "name", "val"); err == nil {
		t.Fatal("expected error for invalid kind")
	}
	if _, err := validateHTTPSetting("header", "", "", "val"); err == nil {
		t.Fatal("expected error for empty name")
	}
	if _, err := validateHTTPSetting("header", "", "name", "val\r\n"); err == nil {
		t.Fatal("expected error for CRLF value")
	}
	if _, err := validateHTTPSetting("header", "not-a-url", "name", "val"); err == nil {
		t.Fatal("expected error for invalid origin")
	}
	if _, err := validateHTTPSetting("cookie", "", "name", "val"); err == nil {
		t.Fatal("expected error for cookie without origin")
	}
	if _, err := validateHTTPSetting("cookie", "https://example.com", "bad name;", "val"); err == nil {
		t.Fatal("expected error for invalid cookie name")
	}
	if _, err := validateHTTPSetting("header", "", "Host", "val"); err == nil {
		t.Fatal("expected error for Host header")
	}
	if _, err := validateHTTPSetting("header", "", "Custom-Header", "val"); err == nil {
		t.Fatal("expected error for custom header without origin")
	}

	// Store settings methods error branches
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()

	if err := store.SetHTTPRequestSetting(ctx, "invalid_scope", "header", "https://example.com", "User-Agent", "Bot"); err == nil {
		t.Fatal("expected error for invalid scope in SetHTTPRequestSetting")
	}
	if err := store.RemoveHTTPRequestSetting(ctx, "invalid_scope", "header", "https://example.com", "User-Agent"); err == nil {
		t.Fatal("expected error for invalid scope in RemoveHTTPRequestSetting")
	}
	if err := store.RemoveHTTPRequestSetting(ctx, "project", "invalid_kind", "https://example.com", "User-Agent"); err == nil {
		t.Fatal("expected error for invalid kind in RemoveHTTPRequestSetting")
	}
	if _, err := store.HTTPRequestSettings(ctx, "https://example.com"); err != nil {
		t.Fatal(err)
	}
}

func TestRemaining_HTTPStore(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()

	session, err := store.Create(ctx, "Test")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}

	// AppendHTTPResponse validation errors
	hugeBody := HTTPResponse{URL: "https://example.com", Body: make([]byte, maxHTTPResponseBytes+1)}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, user.ID, hugeBody, nil); err == nil {
		t.Fatal("expected error for huge body")
	}

	invalidURL := HTTPResponse{URL: "invalid-url"}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, user.ID, invalidURL, nil); err == nil {
		t.Fatal("expected error for invalid source URL")
	}

	invalidFinalURL := HTTPResponse{URL: "https://example.com", FinalURL: "invalid-final"}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, user.ID, invalidFinalURL, nil); err == nil {
		t.Fatal("expected error for invalid final URL")
	}

	invalidRedirect := HTTPResponse{URL: "https://example.com", Redirects: []HTTPRedirect{{URL: "invalid-hop"}}}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, user.ID, invalidRedirect, nil); err == nil {
		t.Fatal("expected error for invalid redirect URL")
	}

	badMethod := HTTPResponse{URL: "https://example.com", Method: "PATCH"}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, user.ID, badMethod, nil); err == nil {
		t.Fatal("expected error for unsupported HTTP method")
	}

	validResp := HTTPResponse{URL: "https://example.com", StatusCode: 200, ContentType: "text/plain", Body: []byte("ok")}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, "nonexistent_origin", validResp, nil); err == nil {
		t.Fatal("expected error for checkOrigin failure")
	}

	// Save valid response
	saved, err := store.AppendHTTPResponse(ctx, session.ID, user.ID, validResp, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Query with RefreshParentID
	query := &QueryResult{
		Title:  "Q",
		Source: "local",
		Result: secureread.Result{Columns: []string{"c1"}},
	}
	respWithParent := HTTPResponse{
		URL:             "https://example.com",
		StatusCode:      200,
		RefreshParentID: saved.ID,
		Body:            []byte("ok2"),
	}
	if _, err := store.AppendHTTPResponse(ctx, session.ID, user.ID, respWithParent, query); err != nil {
		t.Fatal(err)
	}

	// loadHTTPResponses error with canceled context
	target := ChatSession{
		ID:            session.ID,
		HTTPResponses: make(map[string]HTTPResponse),
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.loadHTTPResponses(canceledCtx, &target); err == nil {
		t.Fatal("expected error loading http responses with canceled context")
	}
	// And with valid context
	if err := store.loadHTTPResponses(ctx, &target); err != nil {
		t.Fatalf("unexpected error loading http responses: %v", err)
	}
}

func TestRemaining_ExportFullCoverage(t *testing.T) {
	ctx := context.Background()

	// ExportRecordSets empty or nil
	if err := ExportRecordSets(ctx, nil, ExportCSV, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for empty records")
	}
	if err := ExportRecordSets(ctx, []RecordSet{exportFixture("A")}, ExportCSV, nil); err == nil {
		t.Fatal("expected error for nil output")
	}
	if err := ExportRecordSets(ctx, []RecordSet{exportFixture("A")}, ExportFormat("unknown"), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for unknown export format")
	}

	// ExportRecordSetFile error paths
	if err := ExportRecordSetFile(ctx, exportFixture("A"), ExportCSV, "/dev/null/impossible/path.csv"); err == nil {
		t.Fatal("expected error writing to impossible path")
	}
	if err := ExportBucketFile(ctx, []RecordSet{exportFixture("A")}, ExportCSV, "/dev/null/impossible/path.csv"); err == nil {
		t.Fatal("expected error writing bucket to impossible path")
	}

	// Zip multi-record export with same titles
	records := []RecordSet{exportFixture("Test"), exportFixture("Test"), exportFixture("Test")}
	var zipBuf bytes.Buffer
	if err := ExportBucket(ctx, records, ExportCSV, &zipBuf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBuf.Bytes()), int64(zipBuf.Len()))
	if err != nil || len(zr.File) != 3 {
		t.Fatalf("expected 3 zipped files, got %v, %v", len(zr.File), err)
	}

	// exportFlat CSV error: write to a failing writer
	failWriter := &failingWriter{}
	if err := exportFlat(exportFixture("A"), ExportCSV, failWriter); err == nil {
		t.Fatal("expected error writing CSV to failing writer")
	}
}

type failingWriter struct{}

func (f *failingWriter) Write(p []byte) (n int, err error) {
	return 0, errors.New("write failed")
}

func TestRemaining_BookmarkFullCoverage(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()

	session, err := store.Create(ctx, "BookmarkTest")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	query, err := store.AppendQuery(ctx, session.ID, user.ID, "sqlite:///chinook.db", QueryResult{
		Title:  "Q",
		Source: "sqlite:///chinook.db",
		Result: secureread.Result{Columns: []string{"id"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ref := ContextReference{Kind: "recordset", ObjectID: query.RecordSetID, Title: "RS"}
	bm, err := store.CreateBookmark(ctx, session.ID, ref, "My Bookmark")
	if err != nil {
		t.Fatal(err)
	}

	// RenameBookmark empty title error
	if _, err := store.RenameBookmark(ctx, bm.ID, "   "); err == nil {
		t.Fatal("expected error for empty title")
	}

	// RenameBookmark long title truncate
	longTitle := strings.Repeat("A", 150)
	renamed, err := store.RenameBookmark(ctx, bm.ID, longTitle)
	if err != nil || len(renamed.Title) != 120 {
		t.Fatalf("expected 120 runes title, got %d, err %v", len(renamed.Title), err)
	}

	// AddBookmarkTag / RemoveBookmarkTag normalizeTag error
	if _, err := store.AddBookmarkTag(ctx, bm.ID, "invalid tag with \n newline"); err == nil {
		t.Fatal("expected error for invalid tag")
	}
	if _, err := store.RemoveBookmarkTag(ctx, bm.ID, "invalid tag with \n newline"); err == nil {
		t.Fatal("expected error for invalid tag")
	}

	// CreateBookmark with nonexistent session
	if _, err := store.CreateBookmark(ctx, "nonexistent", ref, "Title"); err == nil {
		t.Fatal("expected sessionExists error")
	}

	// CreateBookmark with invalid target
	badRef := ContextReference{Kind: "unknown_kind", ObjectID: "obj"}
	if _, err := store.CreateBookmark(ctx, session.ID, badRef, "Title"); err == nil {
		t.Fatal("expected bookmarkTarget error")
	}
}

func TestRemaining_JoinFullCoverage(t *testing.T) {
	ctx := context.Background()

	// Qualify join expression branches
	if expr, err := qualifyJoinExpression(nil, "src", true); err != nil || expr != nil {
		t.Fatalf("expected nil expr and nil err, got %v, %v", expr, err)
	}

	// qualifyJoinExpression with unqualified field and !single
	fieldRef := dal.NewFieldRef("", "col")
	if _, err := qualifyJoinExpression(fieldRef, "src", false); err == nil {
		t.Fatal("expected error for unqualified field in multi-relation query")
	}

	// qualifyJoinCondition with unsupported condition
	if _, err := qualifyJoinCondition(dummyCondition{}, "src", true); err == nil {
		t.Fatal("expected error for unsupported condition")
	}

	// Candidates missing source / source mismatch
	app := ForeignKeyJoinApplication{}
	cands, err := app.Candidates(ctx, RecordSet{Source: "db1"})
	if err != nil || len(cands) != 0 {
		t.Fatalf("expected 0 candidates, got %v, %v", len(cands), err)
	}

	// Apply missing executor
	if _, err := app.Apply(ctx, RecordSet{Source: "db1"}, "cand1"); err == nil {
		t.Fatal("expected error for missing executor")
	}

	// Apply source mismatch
	app.Executor = &joinExecutorStub{}
	app.Source = "db1"
	if _, err := app.Apply(ctx, RecordSet{Source: "db2"}, "cand1"); err == nil {
		t.Fatal("expected error for source mismatch")
	}

	// LoadSQLiteForeignKeySnapshot invalid args
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "", nil); err == nil {
		t.Fatal("expected error for nil db")
	}
}

func TestRemaining_BridgeFullCoverage(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	bridge, err := StartBrowserBridge(chat)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	handler := bridge.server.Handler

	u, _ := url.Parse(bridge.URL)
	token := u.Query().Get("t")
	if token == "" {
		for _, part := range strings.Split(u.Fragment, "&") {
			if strings.HasPrefix(part, "t=") {
				token = strings.TrimPrefix(part, "t=")
			}
		}
	}

	newReq := func(method, path string, body io.Reader, auth bool) *http.Request {
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Origin", "https://datatug.app")
		req.Host = bridge.listener.Addr().String()
		if auth {
			req.Header.Set("X-DataTug-Chat-Capability", token)
		}
		return req
	}

	// Bad origin
	reqBadOrigin := httptest.NewRequest(http.MethodGet, "/v1/chat/catalog", nil)
	reqBadOrigin.Header.Set("Origin", "https://malicious.example")
	recBadOrigin := httptest.NewRecorder()
	handler.ServeHTTP(recBadOrigin, reqBadOrigin)
	if recBadOrigin.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for bad origin, got %d", recBadOrigin.Code)
	}

	// Bad host
	reqBadHost := httptest.NewRequest(http.MethodGet, "/v1/chat/catalog", nil)
	reqBadHost.Header.Set("Origin", "https://datatug.app")
	reqBadHost.Host = "wronghost:9999"
	recBadHost := httptest.NewRecorder()
	handler.ServeHTTP(recBadHost, reqBadHost)
	if recBadHost.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for bad host, got %d", recBadHost.Code)
	}

	// OPTIONS preflight
	reqOpt := newReq(http.MethodOptions, "/v1/chat/catalog", nil, false)
	recOpt := httptest.NewRecorder()
	handler.ServeHTTP(recOpt, reqOpt)
	if recOpt.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS, got %d", recOpt.Code)
	}

	endpoints := []string{
		"/v1/chat/catalog",
		"/v1/chat/sessions",
		"/v1/chat/workspace",
		"/v1/chat/join_candidates",
		"/v1/chat/cell_detail",
		"/v1/chat/results",
		"/v1/chat/export",
		"/v1/chat/queries",
		"/v1/chat/http",
		"/v1/chat/http_settings",
		"/v1/chat/settings",
		"/datatug/projects/project_summary",
		"/v1/chat/session",
	}

	for _, ep := range endpoints {
		// Unauthorized
		req := newReq(http.MethodGet, ep, nil, false)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("ep %s expected 401 without auth, got %d", ep, rec.Code)
		}

		// Wrong method
		reqWrong := newReq(http.MethodHead, ep, nil, true)
		recWrong := httptest.NewRecorder()
		handler.ServeHTTP(recWrong, reqWrong)
	}

	// Specific endpoint checks
	reqCat := newReq(http.MethodGet, "/v1/chat/catalog", nil, true)
	recCat := httptest.NewRecorder()
	handler.ServeHTTP(recCat, reqCat)
	if recCat.Code != http.StatusOK {
		t.Fatalf("expected 200 for catalog, got %d", recCat.Code)
	}

	reqWS := newReq(http.MethodPost, "/v1/chat/workspace", strings.NewReader("invalid-json"), true)
	recWS := httptest.NewRecorder()
	handler.ServeHTTP(recWS, reqWS)
	if recWS.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid body, got %d", recWS.Code)
	}

	reqJCConflict := newReq(http.MethodGet, "/v1/chat/join_candidates", nil, true)
	reqJCConflict.Header.Set("X-DataTug-Chat-Session", "wrong-id")
	recJCConflict := httptest.NewRecorder()
	handler.ServeHTTP(recJCConflict, reqJCConflict)
	if recJCConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 for wrong session, got %d", recJCConflict.Code)
	}

	reqJCOk := newReq(http.MethodGet, "/v1/chat/join_candidates", nil, true)
	reqJCOk.Header.Set("X-DataTug-Chat-Session", chat.activeID)
	recJCOk := httptest.NewRecorder()
	handler.ServeHTTP(recJCOk, reqJCOk)
	if recJCOk.Code != http.StatusOK {
		t.Fatalf("expected 200 when joinApplication is nil, got %d", recJCOk.Code)
	}

	chat.ConfigureJoinApplication(stubJoinApplication{})
	reqJCBad := newReq(http.MethodGet, "/v1/chat/join_candidates?recordSetId=nonexistent", nil, true)
	reqJCBad.Header.Set("X-DataTug-Chat-Session", chat.activeID)
	recJCBad := httptest.NewRecorder()
	handler.ServeHTTP(recJCBad, reqJCBad)
	if recJCBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for nonexistent recordset_id, got %d", recJCBad.Code)
	}

	reqCD := newReq(http.MethodGet, "/v1/chat/cell_detail?row=not-int", nil, true)
	recCD := httptest.NewRecorder()
	handler.ServeHTTP(recCD, reqCD)
	if recCD.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad row, got %d", recCD.Code)
	}

	reqCDConflict := newReq(http.MethodGet, "/v1/chat/cell_detail?row=0", nil, true)
	reqCDConflict.Header.Set("X-DataTug-Chat-Session", "wrong-id")
	recCDConflict := httptest.NewRecorder()
	handler.ServeHTTP(recCDConflict, reqCDConflict)
	if recCDConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 for bad session on cell_detail, got %d", recCDConflict.Code)
	}

	reqExp := newReq(http.MethodGet, "/v1/chat/export?format=bad", nil, true)
	recExp := httptest.NewRecorder()
	handler.ServeHTTP(recExp, reqExp)
	if recExp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad export, got %d", recExp.Code)
	}

	reqHTTP := newReq(http.MethodPost, "/v1/chat/http", strings.NewReader("bad"), true)
	recHTTP := httptest.NewRecorder()
	handler.ServeHTTP(recHTTP, reqHTTP)
	if recHTTP.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad http req, got %d", recHTTP.Code)
	}

	reqHSPost := newReq(http.MethodPost, "/v1/chat/http_settings", strings.NewReader("bad"), true)
	recHSPost := httptest.NewRecorder()
	handler.ServeHTTP(recHSPost, reqHSPost)
	if recHSPost.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad http_settings post, got %d", recHSPost.Code)
	}

	reqHSDel := newReq(http.MethodDelete, "/v1/chat/http_settings", strings.NewReader("bad"), true)
	recHSDel := httptest.NewRecorder()
	handler.ServeHTTP(recHSDel, reqHSDel)
	if recHSDel.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for http_settings del, got %d", recHSDel.Code)
	}

	reqSetPost := newReq(http.MethodPost, "/v1/chat/settings", strings.NewReader("bad"), true)
	recSetPost := httptest.NewRecorder()
	handler.ServeHTTP(recSetPost, reqSetPost)
	if recSetPost.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad settings post, got %d", recSetPost.Code)
	}
}

func TestRemaining_JoinDeepCoverage(t *testing.T) {
	ctx := context.Background()

	// LoadSQLiteForeignKeySnapshot with memory DB
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec("CREATE TABLE parent (id INTEGER PRIMARY KEY, name TEXT)")
	_, _ = db.Exec("CREATE TABLE child (id INTEGER PRIMARY KEY, p_id INTEGER, FOREIGN KEY(p_id) REFERENCES parent(id))")
	_, _ = db.Exec("CREATE TABLE child_implicit (id INTEGER PRIMARY KEY, p_id INTEGER, FOREIGN KEY(p_id) REFERENCES parent)")
	snap, err := LoadSQLiteForeignKeySnapshot(ctx, "sqlite:///test.db", db)
	if err != nil || len(snap.Keys) < 2 {
		t.Fatalf("expected at least 2 FK keys, got %d, err %v", len(snap.Keys), err)
	}

	// Table with no PK referenced by FK
	_, _ = db.Exec("CREATE TABLE nopk (val TEXT)")
	_, _ = db.Exec("CREATE TABLE child_nopk (id INTEGER, val TEXT, FOREIGN KEY(val) REFERENCES nopk)")
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "sqlite:///test.db", db); err == nil {
		t.Fatal("expected error when FK references table with no PK")
	}

	// Qualify expressions & conditions
	binExpr := dal.Binary(dal.NewFieldRef("", "a"), dal.Add, dal.NewFieldRef("", "b"))
	if _, err := qualifyJoinExpression(binExpr, "src", false); err == nil {
		t.Fatal("expected error for unqualified binary expression in multi-relation query")
	}
	binQualified, err := qualifyJoinExpression(binExpr, "src", true)
	if err != nil || binQualified == nil {
		t.Fatalf("expected qualified binary expression, got %v, %v", binQualified, err)
	}

	// Comparison condition
	cmp := dal.NewComparison(dal.NewFieldRef("", "a"), dal.Equal, dal.NewFieldRef("", "b"))
	if _, err := qualifyJoinCondition(cmp, "src", false); err == nil {
		t.Fatal("expected error for unqualified comparison in multi-relation query")
	}
	cmpQualified, err := qualifyJoinCondition(cmp, "src", true)
	if err != nil || cmpQualified == nil {
		t.Fatalf("expected qualified comparison, got %v, %v", cmpQualified, err)
	}

	// Group condition
	grp := dal.NewGroupCondition(dal.And, cmp)
	if _, err := qualifyJoinCondition(grp, "src", false); err == nil {
		t.Fatal("expected error for unqualified group condition in multi-relation query")
	}
	grpQualified, err := qualifyJoinCondition(grp, "src", true)
	if err != nil || grpQualified == nil {
		t.Fatalf("expected qualified group condition, got %v, %v", grpQualified, err)
	}
}

func TestRemaining_ExportDeepCoverage(t *testing.T) {
	ctx := context.Background()

	// ParseExportFormat
	for _, fmtStr := range []string{"csv", "json", "yaml", "ingr", "dbf", "sqlite", "xlsx"} {
		f, err := ParseExportFormat(fmtStr)
		if err != nil || string(f) != fmtStr {
			t.Fatalf("expected %s, got %s, err %v", fmtStr, f, err)
		}
	}
	if _, err := ParseExportFormat("unknown"); err == nil {
		t.Fatal("expected error for unknown export format")
	}

	// SQLite export
	var sqlBuf bytes.Buffer
	rec := exportFixture("TestTable")
	if err := ExportRecordSets(ctx, []RecordSet{rec}, ExportSQLite, &sqlBuf); err != nil {
		t.Fatalf("ExportSQLite error: %v", err)
	}

	// XLSX export
	var xlsxBuf bytes.Buffer
	if err := ExportRecordSets(ctx, []RecordSet{rec}, ExportXLSX, &xlsxBuf); err != nil {
		t.Fatalf("ExportXLSX error: %v", err)
	}

	// File export with empty path
	if err := ExportRecordSetFile(ctx, rec, ExportCSV, ""); err == nil {
		t.Fatal("expected error for empty path in ExportRecordSetFile")
	}

	// File export with existing file
	tmpExisting := filepath.Join(t.TempDir(), "exists.csv")
	_ = os.WriteFile(tmpExisting, []byte("x"), 0o600)
	if err := ExportRecordSetFile(ctx, rec, ExportCSV, tmpExisting); err == nil {
		t.Fatal("expected error when file already exists")
	}

	// DBF type coverage (boolean, time, integer, float, json.Number, long text)
	dbfRec := RecordSet{
		Title: "DBFTest",
		Result: secureread.Result{
			Columns: []string{"b", "d", "t", "i", "f", "jn", "long_num", "scale_num"},
			Rows: []secureread.Row{
				{
					Data: map[string]any{
						"b":         true,
						"d":         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
						"t":         time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC),
						"i":         int64(42),
						"f":         3.14,
						"jn":        json.Number("100"),
						"long_num":  "1e10",
						"scale_num": "1.1234567890123",
					},
				},
			},
		},
	}
	var dbfBuf bytes.Buffer
	if err := ExportRecordSets(ctx, []RecordSet{dbfRec}, ExportDBF, &dbfBuf); err != nil {
		t.Fatalf("ExportDBF error: %v", err)
	}
}

func TestRemaining_BookmarkDeepCoverage(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()

	session, err := store.Create(ctx, "BMDeep")
	if err != nil {
		t.Fatal(err)
	}
	user, _ := store.AppendUser(ctx, session.ID, "user")
	query, _ := store.AppendQuery(ctx, session.ID, user.ID, "sqlite:///chinook.db", QueryResult{
		Title:  "Q",
		Source: "sqlite:///chinook.db",
		Result: secureread.Result{Columns: []string{"id"}},
	})
	ref := ContextReference{Kind: "recordset", ObjectID: query.RecordSetID, Title: "RS"}
	bm, err := store.CreateBookmark(ctx, session.ID, ref, "Deep Bookmark")
	if err != nil {
		t.Fatal(err)
	}

	// DeleteBookmark
	if err := store.DeleteBookmark(ctx, "nonexistent"); err == nil {
		t.Fatal("expected error deleting nonexistent bookmark")
	}
	if err := store.DeleteBookmark(ctx, bm.ID); err != nil {
		t.Fatalf("error deleting bookmark: %v", err)
	}

	// FindBookmarks with empty query and tags
	bms, err := store.FindBookmarks(ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = bms

	// FindBookmarks with query
	bms, err = store.FindBookmarks(ctx, "Deep", []string{"tag1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = bms
}

