package chat

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	godbf "github.com/LindsayBradford/go-dbf"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/secureread"
	_ "github.com/mattn/go-sqlite3"
)

func TestFinalStretch_SessionChatDeleteOnlySessionCreateFail(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)

	origExec := execContextFn
	defer func() { execContextFn = origExec }()

	execContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO sessions") {
			return nil, errors.New("cannot create replacement session")
		}
		return origExec(db, ctx, query, args...)
	}

	if err := chat.BrowserSessionAction(ctx, chat.activeID, "delete", "confirm"); err == nil {
		t.Fatal("expected error when deleting sole session and replacement Create fails")
	}
}

func TestFinalStretch_HTTPSettingsErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// 1. openHTTPSettingsDB: osOpenFile fails when ErrNotExist
	origOpenFile := osOpenFile
	defer func() { osOpenFile = origOpenFile }()
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, errors.New("cannot open file")
	}
	if _, err := openHTTPSettingsDB(filepath.Join(dir, "new.db")); err == nil {
		t.Fatal("expected error from osOpenFile")
	}
	osOpenFile = origOpenFile

	// 2. validateHTTPSetting: cookie value with semicolon fails cookie.Valid()
	if _, err := validateHTTPSetting("cookie", "https://example.com", "cookie_name", "val;ue"); err == nil {
		t.Fatal("expected error for cookie with semicolon in value")
	}

	// 3. HTTPRequestSettings: rows.Scan error with NULL in non-null column
	store := openTestStore(t, testStorePath(t), testScope())
	if _, err := store.settingsDB.ExecContext(ctx, "DROP TABLE IF EXISTS http_request_settings"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.settingsDB.ExecContext(ctx, "CREATE TABLE http_request_settings (project_id TEXT, origin TEXT, kind TEXT, name TEXT, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.settingsDB.ExecContext(ctx, "INSERT INTO http_request_settings VALUES ('@cli', '', 'header', NULL, '1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.HTTPRequestSettings(ctx, "https://example.com"); err == nil {
		t.Fatal("expected scan error from NULL column in HTTPRequestSettings")
	}
}

func TestFinalStretch_HTTPStoreErrors(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sess, err := store.Create(ctx, "HTTP Store Session")
	if err != nil {
		t.Fatal(err)
	}
	origMsg, err := store.AppendUser(ctx, sess.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Unsupported method & default method
	if _, err := store.AppendHTTPResponse(ctx, sess.ID, origMsg.ID, HTTPResponse{Method: "UNSUPPORTED", URL: "https://example.com", Body: []byte{}}, nil); err == nil {
		t.Fatal("expected error for unsupported HTTP method")
	}
	if _, err := store.AppendHTTPResponse(ctx, sess.ID, origMsg.ID, HTTPResponse{Method: "", URL: "https://example.com", Body: []byte{}}, nil); err != nil {
		t.Fatal(err)
	}

	// 2. appendQueryTxFn error
	origAppendQueryTx := appendQueryTxFn
	defer func() { appendQueryTxFn = origAppendQueryTx }()
	appendQueryTxFn = func(s *SessionStore, ctx context.Context, tx *sql.Tx, sessionID, originID, source string, query *QueryResult, now time.Time) error {
		return errors.New("fail appendQueryTx")
	}
	q := &QueryResult{Title: "Q", DTQL: "from: {name: t}", Result: secureread.Result{Columns: []string{"id"}}}
	if _, err := store.AppendHTTPResponse(ctx, sess.ID, origMsg.ID, HTTPResponse{Method: "GET", URL: "https://example.com", Body: []byte{}}, q); err == nil {
		t.Fatal("expected error when appendQueryTxFn fails")
	}
	appendQueryTxFn = origAppendQueryTx

	// 3. insertMessage fails in AppendHTTPResponse (txExecContextFn on INSERT INTO messages)
	origTxExec := txExecContextFn
	defer func() { txExecContextFn = origTxExec }()
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO messages") {
			return nil, errors.New("fail insert message")
		}
		return origTxExec(tx, ctx, query, args...)
	}
	if _, err := store.AppendHTTPResponse(ctx, sess.ID, origMsg.ID, HTTPResponse{Method: "GET", URL: "https://example.com", Body: []byte{}}, nil); err == nil {
		t.Fatal("expected error when insertMessage fails")
	}

	// 4. UPDATE sessions fails in AppendHTTPResponse
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE sessions SET updated_at") {
			return nil, errors.New("fail update sessions")
		}
		return origTxExec(tx, ctx, query, args...)
	}
	if _, err := store.AppendHTTPResponse(ctx, sess.ID, origMsg.ID, HTTPResponse{Method: "GET", URL: "https://example.com", Body: []byte{}}, nil); err == nil {
		t.Fatal("expected error when UPDATE sessions fails")
	}
	txExecContextFn = origTxExec

	// 5. txCommitFn fails in AppendHTTPResponse
	origTxCommit := txCommitFn
	defer func() { txCommitFn = origTxCommit }()
	txCommitFn = func(tx *sql.Tx) error {
		return errors.New("fail commit")
	}
	if _, err := store.AppendHTTPResponse(ctx, sess.ID, origMsg.ID, HTTPResponse{Method: "GET", URL: "https://example.com", Body: []byte{}}, nil); err == nil {
		t.Fatal("expected error when txCommitFn fails")
	}
	txCommitFn = origTxCommit
}

func TestFinalStretch_BridgeEndpoints(t *testing.T) {
	ctx := context.Background()
	chat, _ := newTestSessionChat(t)
	bridge, err := StartBrowserBridge(chat)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()

	u, err := url.Parse(bridge.URL)
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := url.ParseQuery(u.Fragment)
	token := vals.Get("t")
	handler := bridge.server.Handler

	reqWithToken := func(method, target string, body io.Reader) *http.Request {
		req := httptest.NewRequest(method, target, body)
		req.Header.Set("Origin", "https://datatug.app")
		req.Host = bridge.listener.Addr().String()
		req.Header.Set("X-DataTug-Chat-Capability", token)
		return req
	}

	// 1. /v1/chat/sessions GET List fails
	t.Run("sessions list failure", func(t *testing.T) {
		origQuery := dbQueryContextFn
		defer func() { dbQueryContextFn = origQuery }()
		dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
			if strings.Contains(query, "SELECT id, title, created_at, updated_at FROM sessions") {
				return nil, errors.New("fail list")
			}
			return origQuery(db, ctx, query, args...)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, reqWithToken("GET", "/v1/chat/sessions", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
	})

	// 2. /v1/chat/sessions POST invalid JSON body
	t.Run("sessions action invalid json", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, reqWithToken("POST", "/v1/chat/sessions", strings.NewReader("{invalid-json")))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	// 3. /v1/chat/workspace POST fails with bad request
	t.Run("workspace action failure", func(t *testing.T) {
		rec := httptest.NewRecorder()
		body := `{"sessionId":"` + chat.activeID + `","action":{"action":"unknown"}}`
		handler.ServeHTTP(rec, reqWithToken("POST", "/v1/chat/workspace", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})

	// 4. /v1/chat/results POST unknown action & success (204)
	t.Run("results unknown action and success", func(t *testing.T) {
		rec := httptest.NewRecorder()
		body := `{"sessionId":"` + chat.activeID + `","action":"invalid-action"}`
		handler.ServeHTTP(rec, reqWithToken("POST", "/v1/chat/results", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}

		// Success path (refresh)
		recOk := httptest.NewRecorder()
		chat.ConfigureQueryExecutor(&fakeExecutor{result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": 1}}}}})
		rs := QueryResult{
			Title:    "RS",
			DTQL:     "from: {name: Invoice}\nlimit: 20",
			SourceID: "chinook",
			Result:   secureread.Result{Columns: []string{"id"}},
		}
		userTurn, _ := chat.store.AppendUser(ctx, chat.activeID, "hi")
		appendedQuery, _ := chat.store.AppendQuery(ctx, chat.activeID, userTurn.ID, "sqlite:///chinook.db", rs)

		bodyRefresh := `{"sessionId":"` + chat.activeID + `","action":"refresh","recordSetId":"` + appendedQuery.RecordSetID + `"}`
		handler.ServeHTTP(recOk, reqWithToken("POST", "/v1/chat/results", strings.NewReader(bodyRefresh)))
		if recOk.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d: %s", recOk.Code, recOk.Body.String())
		}
	})

	// 5. /v1/chat/export GET with export too large
	t.Run("export too large", func(t *testing.T) {
		origMax := maxBrowserExportBytes
		maxBrowserExportBytes = 5 // very small
		defer func() { maxBrowserExportBytes = origMax }()

		userTurn, _ := chat.store.AppendUser(ctx, chat.activeID, "hi")
		appended, _ := chat.store.AppendQuery(ctx, chat.activeID, userTurn.ID, "sqlite:///chinook.db", QueryResult{
			Title:  "RS",
			DTQL:   "from: {name: t}",
			Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Key: "1", Data: map[string]any{"id": "a_long_string_to_exceed_limit"}}}},
		})

		req := reqWithToken("GET", "/v1/chat/export?format=csv&recordSetId="+appended.RecordSetID, nil)
		req.Header.Set("X-DataTug-Chat-Session", chat.activeID)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	// 6. /v1/chat/queries GET when ListSavedQueries fails -> 503
	t.Run("queries list failure", func(t *testing.T) {
		chat.savedQueryService = &failingSavedQueryService{}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, reqWithToken("GET", "/v1/chat/queries", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", rec.Code)
		}
	})

	// 7. /v1/chat/http_settings GET when BrowserHTTPSettings fails -> 400
	t.Run("http settings invalid origin", func(t *testing.T) {
		req := reqWithToken("GET", "/v1/chat/http_settings?origin=ftp://invalid", nil)
		req.Header.Set("X-DataTug-Chat-Session", chat.activeID)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})
}

type failingSavedQueryService struct{}

func (f *failingSavedQueryService) List(ctx context.Context) ([]SavedQuery, error) {
	return nil, errors.New("list error")
}
func (f *failingSavedQueryService) Run(ctx context.Context, id string) (QueryResult, error) {
	return QueryResult{}, errors.New("run error")
}
func (f *failingSavedQueryService) RunDTQLWithVariables(ctx context.Context, id string, vars map[string]string) (QueryResult, error) {
	return QueryResult{}, errors.New("run error")
}

func TestFinalStretch_ExportErrors(t *testing.T) {
	ctx := context.Background()

	rs := RecordSet{
		Title: "Test RS",
		Result: secureread.Result{
			Columns: []string{"id", "data", "date", "bin"},
			Rows: []secureread.Row{
				{
					Key: "row1",
					Data: map[string]any{
						"id":   1,
						"data": "hello",
						"date": time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
						"bin":  []byte("blob data"),
						"$ID":  "id123",
					},
				},
			},
		},
	}

	// 1. ExportRecordSets with bucket=true and zip writer create error
	fwZip := &failWriter{failOnWrite: true}
	if err := ExportBucket(ctx, []RecordSet{rs}, ExportCSV, fwZip); err == nil {
		t.Fatal("expected error from zip create on failWriter")
	}

	// 2. exportFlat CSV write columns fail
	fwCSVCol := &failWriter{failOnWrite: true}
	if err := exportFlat(rs, ExportCSV, fwCSVCol); err == nil {
		t.Fatal("expected error on CSV column write")
	}

	// 3. exportFlat INGR WriteHeader fail
	fwIngrHead := &failWriter{failOnWrite: true}
	if err := exportFlat(rs, ExportINGR, fwIngrHead); err == nil {
		t.Fatal("expected error on INGR header write")
	}

	// 4. exportValue with []byte and time.Time
	bVal := exportValue([]byte("test bytes"))
	if _, ok := bVal.([]byte); !ok {
		t.Fatalf("expected []byte, got %T", bVal)
	}
	tVal := exportValue(time.Now())
	if _, ok := tVal.(string); !ok {
		t.Fatalf("expected string from time.Time, got %T", tVal)
	}

	// 5. uniqueExportName with blank base
	used := map[string]bool{}
	name := uniqueExportName("???", 0, used, 64)
	if !strings.HasPrefix(name, "RecordSet_1") {
		t.Fatalf("expected RecordSet_1, got %s", name)
	}

	// 6. exportXLSX with []byte data
	var xlsxBuf bytes.Buffer
	if err := exportXLSX([]RecordSet{rs}, &xlsxBuf); err != nil {
		t.Fatal(err)
	}

	// 7. exportSQLite error branches using seams
	origTemp := osCreateTemp
	defer func() { osCreateTemp = origTemp }()
	osCreateTemp = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("cannot create temp")
	}
	if err := exportSQLite(ctx, []RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error when osCreateTemp fails")
	}
	osCreateTemp = origTemp

	origOpenStore := sqlOpenStore
	defer func() { sqlOpenStore = origOpenStore }()
	sqlOpenStore = func(driverName, dataSourceName string) (*sql.DB, error) {
		return nil, errors.New("cannot open sqlite")
	}
	if err := exportSQLite(ctx, []RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error when sqlOpenStore fails")
	}
	sqlOpenStore = origOpenStore

	origOsOpen := osOpen
	defer func() { osOpen = origOsOpen }()
	osOpen = func(name string) (*os.File, error) {
		return nil, errors.New("cannot open temp file for reading")
	}
	if err := exportSQLite(ctx, []RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error when osOpen fails")
	}
	osOpen = origOsOpen

	// 8. sqliteColumnType: []byte, all nil
	rsBLOB := RecordSet{
		Result: secureread.Result{
			Columns: []string{"b", "n"},
			Rows: []secureread.Row{
				{Data: map[string]any{"b": []byte("bytes"), "n": nil}},
			},
		},
	}
	if typ := sqliteColumnType(rsBLOB, "b"); typ != "BLOB" {
		t.Fatalf("expected BLOB, got %s", typ)
	}
	if typ := sqliteColumnType(rsBLOB, "n"); typ != "BLOB" {
		t.Fatalf("expected BLOB for all nil, got %s", typ)
	}

	// 9. exportDBF error branches
	rsDBFLong := RecordSet{
		Result: secureread.Result{
			Columns: []string{"val"},
			Rows: []secureread.Row{
				{Data: map[string]any{"val": strings.Repeat("A", 300)}},
			},
		},
	}
	if err := exportDBF(rsDBFLong, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for string exceeding DBF limit")
	}

	osCreateTemp = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("temp create fail")
	}
	if err := exportDBF(rs, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from osCreateTemp in exportDBF")
	}
	osCreateTemp = origTemp

	origGodbf := godbfSaveToFile
	defer func() { godbfSaveToFile = origGodbf }()
	godbfSaveToFile = func(table *godbf.DbfTable, filename string) error {
		return errors.New("godbf save fail")
	}
	if err := exportDBF(rs, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from godbfSaveToFile in exportDBF")
	}
	godbfSaveToFile = origGodbf

	origReadFile := osReadFile
	defer func() { osReadFile = origReadFile }()
	osReadFile = func(name string) ([]byte, error) {
		return nil, errors.New("read file fail")
	}
	if err := exportDBF(rs, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from osReadFile in exportDBF")
	}
	osReadFile = origReadFile

	// 10. dbfValue boolean false
	if v := dbfValue(false, "boolean"); v != "F" {
		t.Fatalf("expected F, got %s", v)
	}

	// 11. exportFile error handling: temp creation failure
	targetDir := t.TempDir()
	osCreateTemp = func(dir, pattern string) (*os.File, error) {
		return nil, errors.New("cannot create export file temp")
	}
	if err := ExportRecordSetFile(ctx, rs, ExportCSV, filepath.Join(targetDir, "out.csv")); err == nil {
		t.Fatal("expected error from ExportRecordSetFile when osCreateTemp fails")
	}
	osCreateTemp = origTemp

	// exportFile with ExportRecordSets failure
	if err := ExportRecordSetFile(ctx, rs, ExportFormat("invalid"), filepath.Join(targetDir, "out.invalid")); err == nil {
		t.Fatal("expected error from ExportRecordSetFile for invalid format")
	}
}

func TestFinalStretch_BookmarkAndJoin(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	_, err := store.Create(ctx, "Bookmark Session")
	if err != nil {
		t.Fatal(err)
	}

	// Bookmark title truncation and fallback
	longTitle := strings.Repeat("A", 150)
	normLong := normalizeBookmarkTitle(longTitle, "", "")
	if len([]rune(normLong)) != 120 {
		t.Fatalf("expected 120 runes, got %d", len([]rune(normLong)))
	}
	if normFallback := normalizeBookmarkTitle("", "", ""); normFallback != "Bookmarked result" {
		t.Fatalf("expected 'Bookmarked result', got %q", normFallback)
	}

	// tagsContainText and sameTags
	if !tagsContainText([]string{"Database", "SQL"}, "data") {
		t.Fatal("expected tagsContainText to find data in Database")
	}
	if sameTags([]string{"a", "b"}, []string{"a", "c"}) {
		t.Fatal("expected sameTags to be false when elements differ")
	}

	// validateBookmarkReferences errors
	wsEmptyID := WorkspaceState{
		Attachments: []ContextReference{{Kind: "bookmark", ObjectID: ""}},
	}
	if err := store.validateBookmarkReferences(ctx, store.db, wsEmptyID); err == nil {
		t.Fatal("expected error for empty bookmark ObjectID")
	}

	wsDiffProj := WorkspaceState{
		Attachments: []ContextReference{{Kind: "bookmark", ObjectID: "b1", ProjectID: "diff-project"}},
	}
	if err := store.validateBookmarkReferences(ctx, store.db, wsDiffProj); err == nil {
		t.Fatal("expected error for different ProjectID")
	}

	// workspaceReferencesBookmark dock branch
	wsDock := WorkspaceState{
		Docks: []Dock{{Reference: ContextReference{Kind: "bookmark", ObjectID: "dock-b1"}}},
	}
	if !workspaceReferencesBookmark(wsDock, "dock-b1") {
		t.Fatal("expected workspaceReferencesBookmark to find dock reference")
	}

	// validateBookmarkSnapshot view branch
	validViewSnap := BookmarkSnapshot{
		RecordSet: RecordSet{ID: "r1"},
		View:      &RecordSetView{ID: "v1", RecordSetID: "r1"},
	}
	if err := validateBookmarkSnapshot("view", validViewSnap); err != nil {
		t.Fatalf("expected valid view snapshot, got %v", err)
	}

	// join.go:
	// DiscoverJoinCandidates parse error
	if _, err := DiscoverJoinCandidates([]byte("invalid-yaml: ["), ForeignKeySnapshot{}); err == nil {
		t.Fatal("expected error from DiscoverJoinCandidates on invalid yaml")
	}

	// prepareJoinProjection instances == 0
	emptyQuery := &emptyFromQuery{}
	if _, err := prepareJoinProjection(emptyQuery, ForeignKeySnapshot{}, JoinCandidate{}, "alias"); err == nil {
		t.Fatal("expected error for query with no relation instances")
	}

	// prepareJoinProjection baseColumns empty
	qSingle, _ := dtql.Deserialize([]byte("from: {name: users}\n"))
	if _, err := prepareJoinProjection(qSingle, ForeignKeySnapshot{Columns: map[string][]string{}}, JoinCandidate{Target: RelationInstance{Relation: "orders"}}, "orders"); err == nil {
		t.Fatal("expected error when baseColumns empty")
	}

	// queryHasAggregate binary right and order expression
	qBase, _ := dtql.Deserialize([]byte("from: {name: t}\ncolumns: [{field: a}]\n"))
	qAggOrder := joinPreparedQuery{
		StructuredQuery: qBase,
		order: []dal.OrderExpression{
			dal.Ascending(dal.NewAggregate("COUNT", false, dal.NewFieldRef("t", "id"))),
		},
	}
	if !queryHasAggregate(qAggOrder) {
		t.Fatal("expected queryHasAggregate to find aggregate in order_by")
	}

	qAggBin := joinPreparedQuery{
		StructuredQuery: qBase,
		columns: []dal.Column{
			{Expression: dal.Binary(dal.NewFieldRef("t", "a"), dal.Add, dal.NewAggregate("COUNT", false, dal.NewFieldRef("t", "id")))},
		},
	}
	if !queryHasAggregate(qAggBin) {
		t.Fatal("expected queryHasAggregate to find aggregate in binary right")
	}

	// activeJoinPairs with !targetKnown and non-matching comparison
	fromUnmatched := dal.From(dal.NewRootCollectionRef("parent", "p"))
	fromUnmatched = fromUnmatched.Join(dal.NewJoinedSource(dal.NewRootCollectionRef("child", "c"), dal.JoinInner, dal.NewComparison(dal.NewFieldRef("other1", "id"), dal.Equal, dal.NewFieldRef("other2", "id"))))
	_ = activeJoinPairs(fromUnmatched, nil)
}

type emptyFromQuery struct {
	dal.StructuredQuery
}

func (e *emptyFromQuery) From() dal.FromSource { return nil }
func (e *emptyFromQuery) Columns() []dal.Column { return nil }
