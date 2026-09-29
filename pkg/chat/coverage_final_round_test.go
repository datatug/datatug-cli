package chat

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/google/uuid"
)

type failWriter struct {
	failOnWrite bool
	err         error
}

func (w *failWriter) Write(p []byte) (n int, err error) {
	if w.failOnWrite {
		if w.err != nil {
			return 0, w.err
		}
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestFinalRound_ChartsAndSorting(t *testing.T) {
	candidates := []ChartCandidate{
		{Score: 10, Spec: ChartSpec{Title: "B", Kind: "bar", Dimension: "d1"}},
		{Score: 10, Spec: ChartSpec{Title: "A", Kind: "bar", Dimension: "d1"}},
		{Score: 10, Spec: ChartSpec{Title: "A", Kind: "line", Dimension: "d1"}},
		{Score: 10, Spec: ChartSpec{Title: "A", Kind: "bar", Dimension: "d2"}},
		{Score: 20, Spec: ChartSpec{Title: "Z", Kind: "pie", Dimension: "d1"}},
	}
	sortChartCandidates(candidates)
	if candidates[0].Score != 20 {
		t.Fatalf("expected top score 20, got %d", candidates[0].Score)
	}
	if candidates[1].Spec.Title != "A" || candidates[1].Spec.Kind != "bar" || candidates[1].Spec.Dimension != "d1" {
		t.Fatalf("unexpected sorting order: %+v", candidates[1])
	}
}

func TestFinalRound_AttachedJoinAndSessionChat(t *testing.T) {
	ctx := context.Background()

	// 1. AttachedJoin with failing Candidates
	chat, _ := newTestSessionChat(t)
	failApp := &mockJoinAppWithError{candErr: errors.New("candidates error")}
	chat.ConfigureJoinApplication(failApp)
	chat.source = "sqlite:///chinook.db"
	session := ChatSession{
		Workspace: WorkspaceState{
			Attachments: []ContextReference{
				{Kind: "table", SourceID: "chinook", ObjectID: "public.other"},
			},
		},
	}
	q := QueryResult{Title: "T", Source: "sqlite:///chinook.db", DTQL: "from: {name: some_table}"}
	_, _, err := chat.joinAttachedQuery(ctx, session, "prompt", q)
	if err == nil {
		t.Fatal("expected error from joinAttachedQuery")
	}

	// 2. SessionChat: runSavedQueryActive active session changed mid-execution
	switching := &switchingRunner{chat: chat}
	chat.savedQueryService = switching
	chat.activeID = "s1"
	if err := chat.RunSavedDTQLActive(ctx, "s1", "q1", nil); !errors.Is(err, ErrActiveSessionChanged) {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}

	// 3. SessionChat: delete only session, failing store.Create
	chat2, _ := newTestSessionChat(t)
	origExec := execContextFn
	defer func() { execContextFn = origExec }()
	execContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO sessions") {
			return nil, errors.New("cannot create session")
		}
		return origExec(db, ctx, query, args...)
	}
	if err := chat2.BrowserSessionAction(ctx, chat2.activeID, "delete", ""); err == nil {
		t.Fatal("expected error when deleting only session and Create fails")
	}
}

type switchingRunner struct {
	chat *SessionChat
}

func (s *switchingRunner) List(ctx context.Context) ([]SavedQuery, error) {
	return []SavedQuery{{ID: "q1", Type: "DTQL"}}, nil
}

func (s *switchingRunner) Run(ctx context.Context, id string) (QueryResult, error) {
	return QueryResult{Title: "Res"}, nil
}

func (s *switchingRunner) RunDTQLWithVariables(ctx context.Context, id string, vars map[string]string) (QueryResult, error) {
	s.chat.activeID = "s2"
	return QueryResult{Title: "Res"}, nil
}

type mockJoinAppWithError struct {
	candErr  error
	applyErr error
}

func (m *mockJoinAppWithError) Candidates(ctx context.Context, record RecordSet) ([]JoinCandidate, error) {
	if m.candErr != nil {
		return nil, m.candErr
	}
	return []JoinCandidate{}, nil
}

func (m *mockJoinAppWithError) Apply(ctx context.Context, record RecordSet, id JoinCandidateID) (QueryResult, error) {
	if m.applyErr != nil {
		return QueryResult{}, m.applyErr
	}
	return QueryResult{}, nil
}

func (m *mockJoinAppWithError) ApplyAttached(ctx context.Context, record RecordSet, id JoinCandidateID) (QueryResult, error) {
	if m.applyErr != nil {
		return QueryResult{}, m.applyErr
	}
	return QueryResult{}, nil
}

func TestFinalRound_HTTPSettings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// 1. openHTTPSettingsDB: osLstat error (other than ErrNotExist)
	origLstat := osLstat
	defer func() { osLstat = origLstat }()
	osLstat = func(name string) (os.FileInfo, error) {
		return nil, errors.New("lstat error")
	}
	if _, err := openHTTPSettingsDB(filepath.Join(dir, "s1.db")); err == nil {
		t.Fatal("expected error from osLstat")
	}
	osLstat = origLstat

	// 2. openHTTPSettingsDB: sqlOpenStore error
	origOpen := sqlOpenStore
	defer func() { sqlOpenStore = origOpen }()
	sqlOpenStore = func(driverName, dataSourceName string) (*sql.DB, error) {
		return nil, errors.New("sql open error")
	}
	if _, err := openHTTPSettingsDB(filepath.Join(dir, "s2.db")); err == nil {
		t.Fatal("expected error from sqlOpenStore")
	}
	sqlOpenStore = origOpen

	// 3. openHTTPSettingsDB: settingsDBExec error
	origExec := settingsDBExec
	defer func() { settingsDBExec = origExec }()
	settingsDBExec = func(db *sql.DB, query string) (sql.Result, error) {
		return nil, errors.New("exec error")
	}
	if _, err := openHTTPSettingsDB(filepath.Join(dir, "s3.db")); err == nil {
		t.Fatal("expected error from settingsDBExec")
	}
	settingsDBExec = origExec

	// 4. validateHTTPSetting invalid cookie
	if _, err := validateHTTPSetting("cookie", "http://localhost", "c", "val\x00ue"); err == nil {
		t.Fatal("expected error for invalid cookie")
	}

	// 5. HTTPRequestSettings query scan error
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	// Replace http_request_settings table schema to force scan error
	_, _ = store.settingsDB.Exec("DROP TABLE http_request_settings")
	_, _ = store.settingsDB.Exec("CREATE TABLE http_request_settings (project_id INT)")
	_, _ = store.settingsDB.Exec("INSERT INTO http_request_settings VALUES (1)")
	if _, err := store.HTTPRequestSettings(ctx, "http://localhost"); err == nil {
		t.Fatal("expected scan error from HTTPRequestSettings")
	}
}

func TestFinalRound_HTTPStore(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()

	sess, err := store.Create(ctx, "S")
	if err != nil {
		t.Fatal(err)
	}

	// 1. AppendHTTPResponse with unsupported method
	if _, err := store.AppendHTTPResponse(ctx, sess.ID, "orig", HTTPResponse{Method: "UNSUPPORTED"}, nil); err == nil {
		t.Fatal("expected unsupported HTTP method error")
	}

	// 2. Parent loop with cycle break
	u, _ := store.AppendUser(ctx, sess.ID, "user")
	resp1, err := store.AppendHTTPResponse(ctx, sess.ID, u.ID, HTTPResponse{Method: "GET", URL: "http://example.com/1", Body: []byte{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Make self-referential cycle: refresh_parent_id = resp1.ID
	_, _ = store.db.Exec("UPDATE http_responses SET refresh_parent_id = ? WHERE id = ?", resp1.ID, resp1.ID)
	q := &QueryResult{Title: "Q", Result: secureread.Result{Columns: []string{"a"}}}
	_, err = store.AppendHTTPResponse(ctx, sess.ID, u.ID, HTTPResponse{Method: "GET", URL: "http://example.com/2", RefreshParentID: resp1.ID, Body: []byte{}}, q)
	if err != nil {
		t.Fatalf("expected successful cycle break, got %v", err)
	}

	// 3. Corrupt entries in http_responses for loadHTTPResponses
	sess2, _ := store.Create(ctx, "Corrupt")
	insertCorrupt := func(field, badVal string) {
		_, err := store.db.Exec("INSERT INTO http_responses (id, session_id, origin_message_id, method, url, status_code, content_type, headers_json, request_headers_json, response_nanos, download_nanos, final_url, redirects_json, request_has_query, body, refresh_parent_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			uuid.NewString(), sess2.ID, u.ID, "GET", "http://test", 200, "text/plain",
			func() string { if field == "headers" { return badVal }; return "{}" }(),
			func() string { if field == "req_headers" { return badVal }; return "{}" }(),
			0, 0, "",
			func() string { if field == "redirects" { return badVal }; return "[]" }(),
			0, []byte{}, "",
			func() string { if field == "created_at" { return badVal }; return stamp(time.Now().UTC()) }(),
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Corrupt headers
	insertCorrupt("headers", "not-json")
	if _, err := store.Load(ctx, sess2.ID); err == nil {
		t.Fatal("expected error for corrupt headers")
	}
	_, _ = store.db.Exec("DELETE FROM http_responses WHERE session_id = ?", sess2.ID)

	// Corrupt request headers
	insertCorrupt("req_headers", "not-json")
	if _, err := store.Load(ctx, sess2.ID); err == nil {
		t.Fatal("expected error for corrupt req headers")
	}
	_, _ = store.db.Exec("DELETE FROM http_responses WHERE session_id = ?", sess2.ID)

	// Corrupt redirects
	insertCorrupt("redirects", "not-json")
	if _, err := store.Load(ctx, sess2.ID); err == nil {
		t.Fatal("expected error for corrupt redirects")
	}
	_, _ = store.db.Exec("DELETE FROM http_responses WHERE session_id = ?", sess2.ID)

	// Corrupt created_at
	insertCorrupt("created_at", "not-a-timestamp")
	if _, err := store.Load(ctx, sess2.ID); err == nil {
		t.Fatal("expected error for corrupt created_at")
	}
	_, _ = store.db.Exec("DELETE FROM http_responses WHERE session_id = ?", sess2.ID)

	// Scan error in loadHTTPResponses
	_, err = store.db.Exec("INSERT INTO http_responses (id, session_id, origin_message_id, method, url, status_code, content_type, headers_json, request_headers_json, response_nanos, download_nanos, final_url, redirects_json, request_has_query, body, refresh_parent_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		uuid.NewString(), sess2.ID, u.ID, "GET", "http://test", 200, "text/plain", "{}", "{}", "not-an-int", 0, "", "[]", 0, []byte{}, "", stamp(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx, sess2.ID); err == nil {
		t.Fatal("expected scan error from loadHTTPResponses")
	}
}

func TestFinalRound_BridgeEndpoints(t *testing.T) {
	chat, _ := newTestSessionChat(t)
	bridge, err := StartBrowserBridge(chat)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()

	u, _ := url.Parse(bridge.URL)
	var token string
	for _, part := range strings.Split(u.Fragment, "&") {
		if strings.HasPrefix(part, "t=") {
			token = strings.TrimPrefix(part, "t=")
		}
	}

	newReq := func(method, path string, body io.Reader) *http.Request {
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Origin", "https://datatug.app")
		req.Host = bridge.listener.Addr().String()
		req.Header.Set("X-DataTug-Chat-Capability", token)
		req.Header.Set("X-DataTug-Chat-Session", chat.activeID)
		return req
	}
	handler := bridge.server.Handler

	// 1. NetListenTCP failure in StartBrowserBridge
	origListen := netListenTCP
	defer func() { netListenTCP = origListen }()
	netListenTCP = func(network, address string) (net.Listener, error) {
		return nil, errors.New("listen failed")
	}
	if _, err := StartBrowserBridge(chat); err == nil {
		t.Fatal("expected error from netListenTCP")
	}
	netListenTCP = origListen

	// 2. /v1/chat/results POST invalid body
	req := newReq(http.MethodPost, "/v1/chat/results", strings.NewReader("invalid-json"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for results invalid body, got %d", rec.Code)
	}

	// 3. /v1/chat/results POST error and success
	bodyFail, _ := json.Marshal(map[string]any{"sessionId": "wrong-id", "recordSetId": "rs1", "action": "join"})
	reqFail := newReq(http.MethodPost, "/v1/chat/results", bytes.NewReader(bodyFail))
	recFail := httptest.NewRecorder()
	handler.ServeHTTP(recFail, reqFail)
	if recFail.Code != http.StatusConflict {
		t.Fatalf("expected 409 for wrong session, got %d", recFail.Code)
	}

	// 4. /v1/chat/queries POST invalid body
	reqQB := newReq(http.MethodPost, "/v1/chat/queries", strings.NewReader("bad-json"))
	recQB := httptest.NewRecorder()
	handler.ServeHTTP(recQB, reqQB)
	if recQB.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recQB.Code)
	}

	// 5. /v1/chat/queries POST unknown action
	bodyAct, _ := json.Marshal(map[string]any{"sessionId": chat.activeID, "action": "unknown"})
	reqAct := newReq(http.MethodPost, "/v1/chat/queries", bytes.NewReader(bodyAct))
	recAct := httptest.NewRecorder()
	handler.ServeHTTP(recAct, reqAct)
	if recAct.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown action, got %d", recAct.Code)
	}

	// 6. /v1/chat/queries POST conflict
	bodyRun, _ := json.Marshal(map[string]any{"sessionId": "wrong-id", "action": "run_dtql", "queryId": "q1"})
	reqRun := newReq(http.MethodPost, "/v1/chat/queries", bytes.NewReader(bodyRun))
	recRun := httptest.NewRecorder()
	handler.ServeHTTP(recRun, reqRun)
	if recRun.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", recRun.Code)
	}

	// 7. /v1/chat/http POST conflict
	bodyHTTP, _ := json.Marshal(map[string]any{"sessionId": "wrong-id", "method": "GET", "url": "http://example.com"})
	reqHTTP := newReq(http.MethodPost, "/v1/chat/http", bytes.NewReader(bodyHTTP))
	recHTTP := httptest.NewRecorder()
	handler.ServeHTTP(recHTTP, reqHTTP)
	if recHTTP.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", recHTTP.Code)
	}

	// 8. /v1/chat/http_settings POST success and conflict
	bodyHS, _ := json.Marshal(map[string]any{"sessionId": chat.activeID, "action": "set", "scope": "cli", "kind": "header", "origin": "https://api.example.com", "name": "Accept", "value": "text/html"})
	reqHS := newReq(http.MethodPost, "/v1/chat/http_settings", bytes.NewReader(bodyHS))
	recHS := httptest.NewRecorder()
	handler.ServeHTTP(recHS, reqHS)
	if recHS.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for http_settings, got %d", recHS.Code)
	}

	bodyHSWrong, _ := json.Marshal(map[string]any{"sessionId": "wrong-id", "action": "set", "scope": "cli", "kind": "header", "origin": "https://api.example.com", "name": "Accept", "value": "text/html"})
	reqHSWrong := newReq(http.MethodPost, "/v1/chat/http_settings", bytes.NewReader(bodyHSWrong))
	recHSWrong := httptest.NewRecorder()
	handler.ServeHTTP(recHSWrong, reqHSWrong)
	if recHSWrong.Code != http.StatusConflict {
		t.Fatalf("expected 409 for http_settings wrong session, got %d", recHSWrong.Code)
	}

	// 9. /v1/chat/settings GET and POST
	reqSetGet := newReq(http.MethodGet, "/v1/chat/settings", nil)
	recSetGet := httptest.NewRecorder()
	handler.ServeHTTP(recSetGet, reqSetGet)
	if recSetGet.Code != http.StatusOK {
		t.Fatalf("expected 200 for settings GET, got %d", recSetGet.Code)
	}

	reqSetGetConflict := newReq(http.MethodGet, "/v1/chat/settings", nil)
	reqSetGetConflict.Header.Set("X-DataTug-Chat-Session", "wrong-id")
	recSetGetConflict := httptest.NewRecorder()
	handler.ServeHTTP(recSetGetConflict, reqSetGetConflict)
	if recSetGetConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 for settings GET wrong session, got %d", recSetGetConflict.Code)
	}

	bodySetPost, _ := json.Marshal(map[string]any{"sessionId": chat.activeID, "versions": 5})
	reqSetPost := newReq(http.MethodPost, "/v1/chat/settings", bytes.NewReader(bodySetPost))
	recSetPost := httptest.NewRecorder()
	handler.ServeHTTP(recSetPost, reqSetPost)
	if recSetPost.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for settings POST, got %d", recSetPost.Code)
	}

	bodySetPostConflict, _ := json.Marshal(map[string]any{"sessionId": "wrong-id", "versions": 5})
	reqSetPostConflict := newReq(http.MethodPost, "/v1/chat/settings", bytes.NewReader(bodySetPostConflict))
	recSetPostConflict := httptest.NewRecorder()
	handler.ServeHTTP(recSetPostConflict, reqSetPostConflict)
	if recSetPostConflict.Code != http.StatusConflict {
		t.Fatalf("expected 409 for settings POST wrong session, got %d", recSetPostConflict.Code)
	}
}

func TestFinalRound_ExportDetails(t *testing.T) {
	ctx := context.Background()

	// 1. exportFlat error on CSV header write
	rec := RecordSet{Title: "R", Result: secureread.Result{Columns: []string{"col1"}, Rows: []secureread.Row{{Data: map[string]any{"col1": "val1"}}}}}
	fw := &failWriter{failOnWrite: true}
	if err := exportFlat(rec, ExportCSV, fw); err == nil {
		t.Fatal("expected error from CSV write")
	}

	// 2. exportFlat unsupported format
	if err := exportFlat(rec, ExportFormat("UNSUPPORTED"), &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for unsupported flat format")
	}

	// 3. exportSQLite with empty columns
	emptyRec := RecordSet{Title: "Empty", Result: secureread.Result{Columns: nil}}
	if err := exportSQLite(ctx, []RecordSet{emptyRec}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for empty recordset in SQLite export")
	}

	// 4. dbfColumnKind variations: float > 20 chars, places > 8, mixed types, all nil
	r1 := RecordSet{Result: secureread.Result{Columns: []string{"big", "places", "mixed", "allnil"}, Rows: []secureread.Row{
		{Data: map[string]any{"big": 1e25, "places": 1.12345678901, "mixed": true, "allnil": nil}},
		{Data: map[string]any{"big": 1e25, "places": 1.12345678901, "mixed": 10, "allnil": nil}},
	}}}
	k, _ := dbfColumnKind(r1, "big")
	if k != "text" {
		t.Errorf("expected text for huge float, got %s", k)
	}
	k, _ = dbfColumnKind(r1, "places")
	if k != "text" {
		t.Errorf("expected text for >8 places, got %s", k)
	}
	k, _ = dbfColumnKind(r1, "mixed")
	if k != "text" {
		t.Errorf("expected text for mixed bool and int, got %s", k)
	}
	k, _ = dbfColumnKind(r1, "allnil")
	if k != "text" {
		t.Errorf("expected text for allnil, got %s", k)
	}

	// 5. ExportBucket with zip write errors
	fwZip := &failWriter{failOnWrite: true}
	if err := ExportBucket(ctx, []RecordSet{rec, rec}, ExportCSV, fwZip); err == nil {
		t.Fatal("expected error from ExportBucket on failWriter")
	}

	// 6. exportFile failure path
	dir := t.TempDir()
	badFile := filepath.Join(dir, "no_perm", "out.csv")
	if err := ExportRecordSetFile(ctx, rec, ExportCSV, badFile); err == nil {
		t.Fatal("expected error for export file in non-existent directory")
	}
}

func TestFinalRound_BookmarkFullBranches(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()

	// 1. validateBookmarkSnapshot error cases
	if err := validateBookmarkSnapshot("recordset", BookmarkSnapshot{RecordSet: RecordSet{ID: ""}}); err == nil {
		t.Fatal("expected missing recordset identity error")
	}
	if err := validateBookmarkSnapshot("recordset", BookmarkSnapshot{RecordSet: RecordSet{ID: "r1"}, View: &RecordSetView{}}); err == nil {
		t.Fatal("expected unexpected workspace state error")
	}
	if err := validateBookmarkSnapshot("view", BookmarkSnapshot{RecordSet: RecordSet{ID: "r1"}, View: nil}); err == nil {
		t.Fatal("expected invalid workspace state error")
	}
	if err := validateBookmarkSnapshot("selection", BookmarkSnapshot{RecordSet: RecordSet{ID: "r1"}, View: nil}); err == nil {
		t.Fatal("expected incomplete workspace state error")
	}
	if err := validateBookmarkSnapshot("unknown", BookmarkSnapshot{RecordSet: RecordSet{ID: "r1"}}); err == nil {
		t.Fatal("expected unsupported bookmark target kind error")
	}

	// 2. decodeBookmarkSnapshot error cases
	if _, err := decodeBookmarkSnapshot("recordset", []byte("invalid-json")); err == nil {
		t.Fatal("expected json decode error")
	}
	storedNoResult, _ := json.Marshal(storedBookmarkSnapshot{RecordSet: storedBookmarkRecordSet{ID: "r1"}})
	if _, err := decodeBookmarkSnapshot("recordset", storedNoResult); err == nil {
		t.Fatal("expected missing result error")
	}

	// 3. encodeBookmarkSnapshot error cases
	if _, err := encodeBookmarkSnapshot(BookmarkSnapshot{RecordSet: RecordSet{Result: secureread.Result{Columns: []string{"a"}, Rows: []secureread.Row{{Data: map[string]any{"a": make(chan int)}}}}}}); err == nil {
		t.Fatal("expected encode error")
	}

	// 4. bookmarkSourceID error cases
	if _, err := store.bookmarkSourceID(BookmarkSnapshot{SourceID: "mismatch", RecordSet: RecordSet{Database: "db"}}); err == nil {
		t.Fatal("expected source mismatch error")
	}
	if _, err := store.bookmarkSourceID(BookmarkSnapshot{SourceID: "unknown_db", RecordSet: RecordSet{Database: "unknown_db"}}); err == nil {
		t.Fatal("expected unknown source error")
	}

	// 5. bookmarkTarget error cases
	sess, _ := store.Create(ctx, "S")
	tx, _ := store.db.BeginTx(ctx, nil)
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ContextReference{Kind: "recordset", ObjectID: "missing"}); err == nil {
		t.Fatal("expected target unavailable error")
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ContextReference{Kind: "view", ObjectID: "missing"}); err == nil {
		t.Fatal("expected view unavailable error")
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ContextReference{Kind: "selection", ObjectID: "missing"}); err == nil {
		t.Fatal("expected selection unavailable error")
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ContextReference{Kind: "invalid"}); err == nil {
		t.Fatal("expected invalid kind error")
	}
	_ = tx.Rollback()

	// 6. DeleteBookmark error
	if err := store.DeleteBookmark(ctx, "nonexistent-id"); err == nil {
		t.Fatal("expected not found error")
	}
}

func TestFinalRound_JoinCompleteBranches(t *testing.T) {
	ctx := context.Background()

	// 1. activeJoinPairs branches:
	// - comparison not Equal
	fromNotEq := dal.From(dal.NewRootCollectionRef("parent", "p"))
	fromNotEq = fromNotEq.Join(dal.NewJoinedSource(dal.NewRootCollectionRef("child", "c"), dal.JoinInner, dal.NewComparison(dal.NewFieldRef("p", "id"), dal.Operator("!="), dal.NewFieldRef("c", "p_id"))))
	_ = activeJoinPairs(fromNotEq, nil)

	// - comparison with non-FieldRef
	fromNonField := dal.From(dal.NewRootCollectionRef("parent", "p"))
	fromNonField = fromNonField.Join(dal.NewJoinedSource(dal.NewRootCollectionRef("child", "c"), dal.JoinInner, dal.NewComparison(dal.NewFieldRef("p", "id"), dal.Equal, dal.NewConstant(1))))
	_ = activeJoinPairs(fromNonField, nil)

	// - target not known alias
	fromUnknown := dal.From(dal.NewRootCollectionRef("parent", "p"))
	fromUnknown = fromUnknown.Join(dal.NewJoinedSource(dal.NewRootCollectionRef("child", "c"), dal.JoinInner, dal.NewComparison(dal.NewFieldRef("unknown", "id"), dal.Equal, dal.NewFieldRef("c", "p_id"))))
	_ = activeJoinPairs(fromUnknown, nil)

	// 2. qualifyJoinExpression with distinct aggregate
	agg := dal.NewAggregate("COUNT", true, dal.NewFieldRef("", "col"))
	qualAgg, err := qualifyJoinExpression(agg, "src", true)
	if err != nil || qualAgg == nil {
		t.Fatalf("expected qualified distinct aggregate, got %v, %v", qualAgg, err)
	}

	// 3. qualifyJoinExpression with binary expression right error
	binBadRight := dal.Binary(dal.NewFieldRef("", "a"), dal.Add, dal.NewFieldRef("", "unqualified"))
	if _, err := qualifyJoinExpression(binBadRight, "src", false); err == nil {
		t.Fatal("expected error from binary expr with bad right in multi-relation")
	}

	// 4. fromAtPath errors
	root := dal.From(dal.NewRootCollectionRef("table", "t"))
	if node := fromAtPath(root, "root/invalid"); node != nil {
		t.Fatal("expected nil node for invalid path")
	}
	if node := fromAtPath(root, "root/99"); node != nil {
		t.Fatal("expected nil node for out of range path")
	}

	// 5. prepareJoinProjection wildcard errors
	dtqlDoc := []byte("from:\n  name: a\n  alias: a\n  joins:\n    - from: {name: b, alias: b}\n      on: [{left: {field: id, source: a}, op: '==', right: {field: a_id, source: b}}]\n")
	qBase, err := dtql.Deserialize(dtqlDoc)
	if err != nil {
		t.Fatal(err)
	}
	qMultiWildcard := dal.WithColumns(qBase, []dal.Column{dal.AllColumnsExcept("id")})
	snap := ForeignKeySnapshot{Columns: map[string][]string{}}
	cand := JoinCandidate{Source: RelationInstance{Relation: "a"}, Target: RelationInstance{Relation: "c"}}
	if _, err := prepareJoinProjection(qMultiWildcard, snap, cand, "c"); err == nil {
		t.Fatal("expected error for multi-relation unqualified wildcard")
	}

	// 6. ForeignKeyJoinApplication secure checks
	fkApp := ForeignKeyJoinApplication{
		Source:        "src",
		Snapshot:      ForeignKeySnapshot{Source: "src"},
		Secure:        true,
		CanReadTarget: nil, // denies
	}
	validDTQL := "from: {name: t}\ncolumns: [{field: id}]\n"
	cands, err := fkApp.Candidates(ctx, RecordSet{Source: "src", DTQL: validDTQL})
	if err != nil || len(cands) != 0 {
		t.Fatalf("expected empty candidates when CanReadTarget is nil and Secure=true, got %v, %v", cands, err)
	}

	rec := RecordSet{Source: "src", DTQL: validDTQL}
	if _, err := fkApp.Apply(ctx, rec, "cand1"); err == nil {
		t.Fatal("expected error when applying with nil CanReadTarget and Secure=true")
	}

	fkApp.CanReadTarget = func(ctx context.Context, inst RelationInstance) error {
		return errors.New("access denied")
	}
	if _, err := fkApp.Apply(ctx, rec, "cand1"); err == nil {
		t.Fatal("expected error when CanReadTarget denies")
	}
}
