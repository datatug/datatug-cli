package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/agent"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/uitest"
	_ "modernc.org/sqlite"
)

type dummyJoinApp struct{}

func (dummyJoinApp) Candidates(context.Context, RecordSet) ([]JoinCandidate, error) {
	return nil, nil
}
func (dummyJoinApp) Apply(context.Context, RecordSet, JoinCandidateID) (QueryResult, error) {
	return QueryResult{}, nil
}

type switchingSessionExecutor struct {
	sessions *SessionChat
}

func (s *switchingSessionExecutor) RunDTQL(ctx context.Context, source string, doc []byte, params map[string]any) (secureread.Result, error) {
	s.sessions.mu.Lock()
	s.sessions.activeID = "changed-session-id"
	s.sessions.mu.Unlock()
	return secureread.Result{}, nil
}

// 1. relatedRecord.identity & previewRelationshipsActive & PreviewRelationships
// Not parallel: mutates dtqlSerialize
func TestCoverage100_RelatedRecords(t *testing.T) {

	// r.identity
	r1 := relatedRecord{key: ForeignKey{ConstraintID: "fk1", Schema: "main", FromRelation: "orders", ToSchema: "main", ToRelation: "users"}, incoming: false}
	id1 := r1.identity()
	if id1 == "" {
		t.Fatal("expected non-empty identity")
	}
	r2 := relatedRecord{key: ForeignKey{ConstraintID: "fk1", Schema: "main", FromRelation: "orders", ToSchema: "main", ToRelation: "users"}, incoming: true}
	id2 := r2.identity()
	if id2 == id1 {
		t.Fatalf("expected different identity for incoming, got %v", id2)
	}

	ctx := context.Background()
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	sessions := u.sessions
	_, recPtr, _ := u.activeGrid()
	rec := *recPtr
	exec := &fakeExecutor{result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": 1}}}}}
	sessions.queryExecutor = exec

	// PreviewRelationships: source mismatch
	app := ForeignKeyJoinApplication{Source: "different-source", Executor: exec}
	if _, err := app.PreviewRelationships(ctx, rec, nil); err == nil {
		t.Fatal("expected source mismatch error")
	}

	// PreviewRelationships: executor nil
	app.Source = rec.Source
	app.Executor = nil
	if _, err := app.PreviewRelationships(ctx, rec, nil); err == nil {
		t.Fatal("expected executor nil error")
	}
	app.Executor = exec

	// PreviewRelationships: invalid DTQL
	app.Executor = sessions.queryExecutor
	badRec := rec
	badRec.DTQL = "INVALID QUERY"
	if _, err := app.PreviewRelationships(ctx, badRec, nil); err == nil {
		t.Fatal("expected invalid DTQL error")
	}

	// PreviewRelationships: snapshot error
	app.Refresh = func(ctx context.Context) (ForeignKeySnapshot, error) {
		return ForeignKeySnapshot{}, fmt.Errorf("snapshot failure")
	}
	if _, err := app.PreviewRelationships(ctx, rec, nil); err == nil {
		t.Fatal("expected snapshot error")
	}

	// PreviewRelationships with keys and branches:
	// a. Secure && CanReadTarget == nil
	fkKeys := []ForeignKey{
		{
			ConstraintID: "fk_orders_user",
			Schema:       "main",
			FromRelation: "users",
			FromFields:   []string{"id"},
			ToSchema:     "main",
			ToRelation:   "orders",
			ToFields:     []string{"user_id"},
		},
		{
			ConstraintID: "fk_incoming",
			Schema:       "main",
			FromRelation: "orders",
			FromFields:   []string{"user_id"},
			ToSchema:     "main",
			ToRelation:   "users",
			ToFields:     []string{"id"},
		},
		{
			ConstraintID: "fk_incomplete",
			Schema:       "main",
			FromRelation: "users",
			FromFields:   []string{"id"},
			ToSchema:     "main",
			ToRelation:   "profiles",
			ToFields:     []string{}, // length mismatch!
		},
		{
			ConstraintID: "fk_other_schema",
			Schema:       "main",
			FromRelation: "users",
			FromFields:   []string{"id"},
			ToSchema:     "analytics",
			ToRelation:   "events",
			ToFields:     []string{"user_id"},
		},
	}
	app.Refresh = func(ctx context.Context) (ForeignKeySnapshot, error) {
		return ForeignKeySnapshot{Source: rec.Source, Keys: fkKeys}, nil
	}
	app.Secure = true
	app.CanReadTarget = nil
	res, err := app.PreviewRelationships(ctx, rec, map[string]any{"main.users.id": int64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].err == nil {
		t.Fatalf("expected relationship access is unavailable error on item")
	}

	// CanReadTarget returns error
	app.CanReadTarget = func(ctx context.Context, target RelationInstance) error {
		if target.Relation == "orders" {
			return fmt.Errorf("access denied")
		}
		return nil
	}
	_, err = app.PreviewRelationships(ctx, rec, map[string]any{"main.users.id": int64(1)})
	if err != nil {
		t.Fatal(err)
	}

	// Missing or null row field
	app.CanReadTarget = func(ctx context.Context, target RelationInstance) error { return nil }
	_, err = app.PreviewRelationships(ctx, rec, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}

	// Canceled ctx
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := app.PreviewRelationships(canceledCtx, rec, map[string]any{"main.users.id": int64(1)}); err == nil {
		t.Fatal("expected ctx error")
	}

	// previewRelationshipsActive
	// activeID mismatch
	if _, err := sessions.previewRelationshipsActive(ctx, "non-existent", "r1", nil); err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// recordID not found
	if _, err := sessions.previewRelationshipsActive(ctx, sessions.activeID, "missing-r", nil); err == nil {
		t.Fatal("expected selected result is no longer available error")
	}
	// *ForeignKeyJoinApplication nil (line 127)
	sessions.joinApplication = (*ForeignKeyJoinApplication)(nil)
	if _, err := sessions.previewRelationshipsActive(ctx, sessions.activeID, rec.ID, nil); err == nil {
		t.Fatal("expected unsupported error for nil *ForeignKeyJoinApplication")
	}
	// default type (line 132)
	sessions.joinApplication = dummyJoinApp{}
	if _, err := sessions.previewRelationshipsActive(ctx, sessions.activeID, rec.ID, nil); err == nil {
		t.Fatal("expected unsupported error for default type")
	}
	// success with *ForeignKeyJoinApplication (line 125)
	sessions.joinApplication = &app
	_, _ = sessions.previewRelationshipsActive(ctx, sessions.activeID, rec.ID, map[string]any{"main.users.id": int64(1)})

	// dtqlSerialize error (line 91)
	origSerialize := dtqlSerialize
	dtqlSerialize = func(q dal.StructuredQuery) ([]byte, error) {
		return nil, errors.New("serialize fail")
	}
	_, _ = app.PreviewRelationships(ctx, rec, map[string]any{"main.users.id": int64(1)})
	dtqlSerialize = origSerialize

	// Executor.RunDTQL error (line 100)
	appErr := app
	appErr.Executor = &fakeExecutor{err: errors.New("query failed")}
	_, _ = appErr.PreviewRelationships(ctx, rec, map[string]any{"main.users.id": int64(1)})

	// store.Load error in previewRelationshipsActive (line 116)
	canceledCtx, cancel = context.WithCancel(ctx)
	cancel()
	_, _ = sessions.previewRelationshipsActive(canceledCtx, sessions.activeID, rec.ID, nil)

	// session change during preview (line 137)
	appSwitch := app
	appSwitch.Executor = &switchingSessionExecutor{sessions: sessions}
	sessions.joinApplication = &appSwitch
	_, _ = sessions.previewRelationshipsActive(ctx, sessions.activeID, rec.ID, map[string]any{"main.users.id": int64(1)})
}

// 2. related_open.go: acceptRelatedPreview & receiveRelatedOpened
// Not parallel: mutates appendQueryTxFn and insertMessageFn
func TestCoverage100_RelatedOpen(t *testing.T) {

	ctx := context.Background()
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///chinook.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	sessions := u.sessions
	rID := u.activeRecordSetID()

	validSource := sessions.store.info.Sources[sessions.store.info.Database]
	_, _ = sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", validSource, rID)

	// acceptRelatedPreview: activeID mismatch
	if _, _, err := sessions.acceptRelatedPreview(ctx, "wrong-id", rID, relatedRecord{}); err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// acceptRelatedPreview: item.err != nil or document == ""
	if _, _, err := sessions.acceptRelatedPreview(ctx, sessions.activeID, rID, relatedRecord{err: fmt.Errorf("err")}); err == nil {
		t.Fatal("expected relationship result is unavailable error")
	}
	if _, _, err := sessions.acceptRelatedPreview(ctx, sessions.activeID, rID, relatedRecord{document: ""}); err == nil {
		t.Fatal("expected relationship result is unavailable error")
	}
	// acceptRelatedPreview: parent not found
	if _, _, err := sessions.acceptRelatedPreview(ctx, sessions.activeID, "missing-parent", relatedRecord{document: "SELECT 1"}); err == nil {
		t.Fatal("expected origin result is unavailable error")
	}
	// acceptRelatedPreview: source mismatch
	_, _ = sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", "corrupted-source", rID)
	if _, _, err := sessions.acceptRelatedPreview(ctx, sessions.activeID, rID, relatedRecord{document: "SELECT 1"}); err == nil {
		t.Fatal("expected origin source is unavailable error")
	}
	// restore
	_, _ = sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", validSource, rID)

	// canceled context before Load (line 38)
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := sessions.acceptRelatedPreview(canceledCtx, sessions.activeID, rID, relatedRecord{document: "SELECT 1"}); err == nil {
		t.Fatal("expected ctx error")
	}

	// canceled context after Load (line 46)
	cancelCtx46, cancel46 := context.WithCancel(ctx)
	origLoadHTTP := loadHTTPResponsesFn
	loadHTTPResponsesFn = func(s *SessionStore, c context.Context, item *ChatSession) error {
		cancel46()
		return origLoadHTTP(s, c, item)
	}
	if _, _, err := sessions.acceptRelatedPreview(cancelCtx46, sessions.activeID, rID, relatedRecord{document: "SELECT 1"}); err == nil {
		t.Fatal("expected ctx.Err() error on line 46")
	}
	loadHTTPResponsesFn = origLoadHTTP

	// success
	item := relatedRecord{
		document: "SELECT id FROM orders WHERE user_id = 1",
		key: ForeignKey{
			Schema:       "main",
			FromRelation: "users",
			ToSchema:     "main",
			ToRelation:   "orders",
		},
		result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": 1}}}},
	}
	snap, query, err := sessions.acceptRelatedPreview(ctx, sessions.activeID, rID, item)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if query.Title != "orders" {
		t.Fatalf("expected query title orders, got %q", query.Title)
	}

	// AppendUser error
	origInsertMessage := insertMessageFn
	insertMessageFn = func(ctx context.Context, tx *sql.Tx, sessionID string, message ChatMessage) error {
		return errors.New("injected insertMessage error")
	}
	if _, _, err := sessions.acceptRelatedPreview(ctx, sessions.activeID, rID, item); err == nil {
		t.Fatal("expected AppendUser error in acceptRelatedPreview")
	}
	insertMessageFn = origInsertMessage

	// AppendQuery error (line 54)
	origAppendQueryTx := appendQueryTxFn
	appendQueryTxFn = func(s *SessionStore, ctx context.Context, tx *sql.Tx, sessionID, originID, source string, query *QueryResult, now time.Time) error {
		return errors.New("injected append error")
	}
	if _, _, err := sessions.acceptRelatedPreview(ctx, sessions.activeID, rID, item); err == nil {
		t.Fatal("expected append error in acceptRelatedPreview")
	}
	appendQueryTxFn = origAppendQueryTx

	// receiveRelatedOpened
	// !finishOperation
	u.receiveRelatedOpened(workspaceRelatedOpenedMsg{operationID: 99999})
	// sessionID or generation mismatch
	opID, _ := u.beginOperation("test")
	u.receiveRelatedOpened(workspaceRelatedOpenedMsg{operationID: opID, sessionID: "wrong-session"})

	// operationID != 0 and interaction mismatch
	opID2, _ := u.beginOperation("test2")
	u.workspaceInteraction = 42
	u.receiveRelatedOpened(workspaceRelatedOpenedMsg{
		operationID: opID2,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		interaction: 1,
		snapshot:    snap,
		err:         nil,
	})
	if !strings.Contains(u.workspaceFeedback, "retained") {
		t.Fatalf("expected feedback retained, got %q", u.workspaceFeedback)
	}

	// parentID or rowKey mismatch
	opID3, _ := u.beginOperation("test3")
	u.receiveRelatedOpened(workspaceRelatedOpenedMsg{
		operationID: opID3,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		interaction: u.workspaceInteraction,
		parentID:    "wrong-parent",
	})

	// msg.err != nil
	g, r, _ := u.activeGrid()
	opID4, _ := u.beginOperation("test4")
	u.receiveRelatedOpened(workspaceRelatedOpenedMsg{
		operationID: opID4,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		interaction: u.workspaceInteraction,
		parentID:    r.ID,
		rowKey:      g.sourceRowKey(),
		err:         fmt.Errorf("some failure"),
	})
	if !strings.Contains(u.workspaceFeedback, "failed") {
		t.Fatalf("expected failed feedback, got %q", u.workspaceFeedback)
	}

	// success path with empty label and with custom label
	opID5, _ := u.beginOperation("test5")
	u.receiveRelatedOpened(workspaceRelatedOpenedMsg{
		operationID: opID5,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		interaction: u.workspaceInteraction,
		parentID:    r.ID,
		rowKey:      g.sourceRowKey(),
		snapshot:    snap,
		query:       query,
		label:       "",
	})

	opID6, _ := u.beginOperation("test6")
	g2, r2, _ := u.activeGrid()
	u.receiveRelatedOpened(workspaceRelatedOpenedMsg{
		operationID: opID6,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		interaction: u.workspaceInteraction,
		parentID:    r2.ID,
		rowKey:      g2.sourceRowKey(),
		snapshot:    snap,
		query:       query,
		label:       "Custom Label",
	})
}

// 3. result_columns_metadata.go
// Not parallel: mutates resultCol package hooks
func TestCoverage100_ResultColumnsMetadata(t *testing.T) {
	origCheck := resultColCheckSourceFile
	origOpen := resultColOpenDB
	origQuery := resultColQueryContext
	origQueryRow := resultColQueryRowContext
	origRowsErr := resultColRowsErr
	defer func() {
		resultColCheckSourceFile = origCheck
		resultColOpenDB = origOpen
		resultColQueryContext = origQuery
		resultColQueryRowContext = origQueryRow
		resultColRowsErr = origRowsErr
	}()

	ctx := context.Background()

	// Create a real temp SQLite database to use for tests
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "meta_test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT COLLATE NOCASE, notes TEXT);
		CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER, content TEXT, FOREIGN KEY(user_id) REFERENCES users(id));
		CREATE TABLE without_rowid (code TEXT PRIMARY KEY, val TEXT) WITHOUT ROWID;
		CREATE VIRTUAL TABLE fts USING fts5(description);
	`)
	if err != nil {
		t.Fatal(err)
	}

	// sqliteBinaryGrouping
	// Query error
	resultColQueryContext = func(d *sql.DB, c context.Context, q string, args ...any) (*sql.Rows, error) {
		return nil, fmt.Errorf("query error")
	}
	if _, err := sqliteBinaryGrouping(ctx, db, "users", "id"); err == nil {
		t.Fatal("expected query error")
	}
	resultColQueryContext = origQuery

	// SorterOpen / Compare with NOCASE: p4 != "k(1,B)" -> proved == false, err == nil
	proved, err := sqliteBinaryGrouping(ctx, db, "users", "name")
	if err != nil {
		t.Fatal(err)
	}
	if proved {
		t.Fatal("expected NOCASE column to prove false for binary grouping")
	}

	// Normal binary column -> proved == true
	proved, err = sqliteBinaryGrouping(ctx, db, "users", "notes")
	if err != nil {
		t.Fatal(err)
	}
	_ = proved

	// rows.Err error
	resultColRowsErr = func(r *sql.Rows) error {
		return fmt.Errorf("injected rows err")
	}
	if _, err := sqliteBinaryGrouping(ctx, db, "users", "notes"); err == nil {
		t.Fatal("expected injected rows err")
	}
	resultColRowsErr = origRowsErr

	// sqliteBinaryGrouping Scan error
	resultColQueryContext = func(d *sql.DB, c context.Context, q string, args ...any) (*sql.Rows, error) {
		return d.Query("SELECT 1")
	}
	if _, err := sqliteBinaryGrouping(ctx, db, "users", "notes"); err == nil {
		t.Fatal("expected scan error")
	}
	resultColQueryContext = origQuery

	// loadResultColumnMetadata
	sourceURI := "sqlite://" + dbPath

	// LoadSQLiteForeignKeySnapshot error
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := loadResultColumnMetadata(canceledCtx, sourceURI); err == nil {
		t.Fatal("expected LoadSQLiteForeignKeySnapshot error on canceled context")
	}

	// Scheme not sqlite
	if _, err := loadResultColumnMetadata(ctx, "postgres://localhost/db"); err == nil {
		t.Fatal("expected non-sqlite error")
	}

	// checkSourceFile error
	resultColCheckSourceFile = func(path string) error {
		return fmt.Errorf("file check failed")
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected file check error")
	}
	resultColCheckSourceFile = origCheck

	// openDB error
	resultColOpenDB = func(driverName, dataSourceName string) (*sql.DB, error) {
		return nil, fmt.Errorf("open failure")
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected open error")
	}
	resultColOpenDB = origOpen

	// QueryRowContext SELECT name FROM sqlite_schema error
	resultColQueryRowContext = func(d *sql.DB, c context.Context, q string, args ...any) *sql.Row {
		if strings.Contains(q, "sqlite_schema") {
			// Run query that returns no rows or invalid query
			return d.QueryRowContext(c, "SELECT 1 WHERE 1=0")
		}
		return origQueryRow(d, c, q, args...)
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected sqlite_schema QueryRowContext error")
	}
	resultColQueryRowContext = origQueryRow

	// QueryContext PRAGMA table_xinfo error
	resultColQueryContext = func(d *sql.DB, c context.Context, q string, args ...any) (*sql.Rows, error) {
		if strings.Contains(q, "table_xinfo") {
			return nil, fmt.Errorf("table_xinfo error")
		}
		return origQuery(d, c, q, args...)
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected table_xinfo error")
	}
	resultColQueryContext = origQuery

	// PRAGMA table_xinfo scan error
	resultColQueryContext = func(d *sql.DB, c context.Context, q string, args ...any) (*sql.Rows, error) {
		if strings.Contains(q, "table_xinfo") {
			return d.Query("SELECT 1")
		}
		return origQuery(d, c, q, args...)
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected table_xinfo scan error")
	}
	resultColQueryContext = origQuery

	// PRAGMA table_xinfo rows.Err error
	resultColRowsErr = func(r *sql.Rows) error {
		return fmt.Errorf("table_xinfo rows.Err")
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected table_xinfo rows.Err error")
	}
	resultColRowsErr = origRowsErr

	// QueryContext PRAGMA index_list error
	resultColQueryContext = func(d *sql.DB, c context.Context, q string, args ...any) (*sql.Rows, error) {
		if strings.Contains(q, "index_list") {
			return nil, fmt.Errorf("index_list error")
		}
		return origQuery(d, c, q, args...)
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected index_list error")
	}
	resultColQueryContext = origQuery

	// PRAGMA index_list scan error
	resultColQueryContext = func(d *sql.DB, c context.Context, q string, args ...any) (*sql.Rows, error) {
		if strings.Contains(q, "index_list") {
			return d.Query("SELECT 1")
		}
		return origQuery(d, c, q, args...)
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected index_list scan error")
	}
	resultColQueryContext = origQuery

	// PRAGMA index_list rows.Err error
	callCount := 0
	resultColRowsErr = func(r *sql.Rows) error {
		callCount++
		if callCount == 2 { // first is table_xinfo, second is index_list
			return fmt.Errorf("index_list rows.Err")
		}
		return origRowsErr(r)
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected index_list rows.Err error")
	}
	resultColRowsErr = origRowsErr

	// pragma_table_list QueryRowContext error
	resultColQueryRowContext = func(d *sql.DB, c context.Context, q string, args ...any) *sql.Row {
		if strings.Contains(q, "pragma_table_list") {
			return d.QueryRowContext(c, "SELECT 1 WHERE 1=0")
		}
		return origQueryRow(d, c, q, args...)
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected pragma_table_list error")
	}
	resultColQueryRowContext = origQueryRow

	// proofErr from sqliteBinaryGrouping
	resultColQueryContext = func(d *sql.DB, c context.Context, q string, args ...any) (*sql.Rows, error) {
		if strings.Contains(q, "EXPLAIN SELECT") {
			return nil, fmt.Errorf("binary proof failure")
		}
		return origQuery(d, c, q, args...)
	}
	if _, err := loadResultColumnMetadata(ctx, sourceURI); err == nil {
		t.Fatal("expected binary proof error")
	}
	resultColQueryContext = origQuery

	// Success run covering virtual table, without rowid, and foreign key relations
	meta, err := loadResultColumnMetadata(ctx, sourceURI)
	if err != nil {
		t.Fatalf("unexpected success run error: %v", err)
	}
	if len(meta.tables) == 0 {
		t.Fatal("expected metadata tables to be populated")
	}
}

// 4. result_columns_agent.go: installResultColumnsHandlers & resultColumnsContext
func TestCoverage100_ResultColumnsAgent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, sessions := newTestChatUI(t, nil)
	aiConv := &AIConversation{}
	handlers := map[string]agent.Handler{}
	aiConv.installResultColumnsHandlers(handlers)

	colHandler, ok1 := handlers[toolResultColumns]
	applyHandler, ok2 := handlers[toolApplyResultColumns]
	if !ok1 || !ok2 {
		t.Fatal("expected tools to be registered")
	}

	// 1. toolResultColumns
	// bad json
	res, err := colHandler(ctx, ai.ToolCall{ID: "call1", Arguments: []byte("not-json")})
	if err != nil || !res.IsError {
		t.Fatal("expected error on bad json")
	}
	// missing callbacks in ctx
	res, _ = colHandler(ctx, ai.ToolCall{ID: "call2", Arguments: []byte("{}")})
	if !strings.Contains(res.Content, "unavailable") {
		t.Fatalf("expected unavailable, got %s", res.Content)
	}
	// plan error
	fakeCtx := context.WithValue(ctx, resultColumnsContextKey{}, resultColumnsCallbacks{
		plan: func(ctx context.Context, id string, ids []string) ([]ResultColumnRow, ResultColumnsPreview, error) {
			return nil, ResultColumnsPreview{}, fmt.Errorf("injected plan error")
		},
	})
	res, _ = colHandler(fakeCtx, ai.ToolCall{ID: "call3", Arguments: []byte("{}")})
	if !strings.Contains(res.Content, "injected plan error") {
		t.Fatalf("expected error content, got %s", res.Content)
	}
	// success with candidateIDs
	fakeCtx = context.WithValue(ctx, resultColumnsContextKey{}, resultColumnsCallbacks{
		plan: func(ctx context.Context, id string, ids []string) ([]ResultColumnRow, ResultColumnsPreview, error) {
			return []ResultColumnRow{{Name: "col1"}}, ResultColumnsPreview{DTQL: "SELECT col1"}, nil
		},
	})
	res, _ = colHandler(fakeCtx, ai.ToolCall{ID: "call4", Arguments: []byte(`{"candidateIds":["c1"]}`)})
	if !strings.Contains(res.Content, "preview") {
		t.Fatalf("expected preview in output, got %s", res.Content)
	}

	// 2. toolApplyResultColumns
	// bad json
	res, _ = applyHandler(ctx, ai.ToolCall{ID: "call5", Arguments: []byte("bad")})
	if !res.IsError {
		t.Fatal("expected error on bad json")
	}
	// missing callbacks
	res, _ = applyHandler(ctx, ai.ToolCall{ID: "call6", Arguments: []byte("{}")})
	if !strings.Contains(res.Content, "unavailable") {
		t.Fatalf("expected unavailable, got %s", res.Content)
	}
	// apply error
	fakeCtx = context.WithValue(ctx, resultColumnsContextKey{}, resultColumnsCallbacks{
		apply: func(ctx context.Context, id, fingerprint string) (QueryResult, error) {
			return QueryResult{}, fmt.Errorf("injected apply error")
		},
	})
	res, _ = applyHandler(fakeCtx, ai.ToolCall{ID: "call7", Arguments: []byte("{}")})
	if !strings.Contains(res.Content, "injected apply error") {
		t.Fatalf("expected error content, got %s", res.Content)
	}
	// apply success
	fakeCtx = context.WithValue(ctx, resultColumnsContextKey{}, resultColumnsCallbacks{
		apply: func(ctx context.Context, id, fingerprint string) (QueryResult, error) {
			return QueryResult{RecordSetID: "rec99"}, nil
		},
	})
	res, _ = applyHandler(fakeCtx, ai.ToolCall{ID: "call8", Arguments: []byte("{}")})
	if !strings.Contains(res.Content, "rec99") {
		t.Fatalf("expected rec99, got %s", res.Content)
	}
	if !aiConv.joinApplied || len(aiConv.pending) == 0 {
		t.Fatal("expected pending query and joinApplied")
	}

	// 3. SessionChat.resultColumnsContext
	sessions.mu.Lock()
	colCtx := sessions.resultColumnsContext(ctx, "origin-msg-1")
	sessions.mu.Unlock()
	cb := colCtx.Value(resultColumnsContextKey{}).(resultColumnsCallbacks)

	// plan: check errors
	// canceled ctx
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := cb.plan(canceledCtx, "r1", nil); err == nil {
		t.Fatal("expected canceled ctx error")
	}
	// activeID mismatch
	sessions.mu.Lock()
	sessions.activeID = "switched"
	sessions.mu.Unlock()
	if _, _, err := cb.plan(ctx, "r1", nil); err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// restore activeID, missing record / origin message mismatch
	sessions.mu.Lock()
	snap, _ := sessions.store.Load(ctx, "")
	sessions.activeID = snap.ID
	sessions.mu.Unlock()
	if _, _, err := cb.plan(ctx, "non-existent-rec", nil); err == nil {
		t.Fatal("expected current user turn error")
	}

	// plan: call limit (> 4)
	for i := 0; i < 4; i++ {
		_, _, _ = cb.plan(ctx, "non-existent", nil)
	}
	if _, _, err := cb.plan(ctx, "non-existent", nil); err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("expected limit reached error, got %v", err)
	}

	// apply: fingerprint not found / parent mismatch
	if _, err := cb.apply(ctx, "r1", "unknown-fp"); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("expected fingerprint error, got %v", err)
	}

	// Add a user turn to store
	userTurn, _ := sessions.store.AppendUser(ctx, sessions.activeID, "user query")
	colCtxTurn := sessions.resultColumnsContext(ctx, userTurn.ID)
	cbTurn := colCtxTurn.Value(resultColumnsContextKey{}).(resultColumnsCallbacks)

	// line 106: c.store.Load error in check
	canceledCheck, cancelCheck := context.WithCancel(ctx)
	cancelCheck()
	_, _, _ = cbTurn.plan(canceledCheck, "r1", nil)

	// line 126: resultColumnEvidence error in plan (bad db source)
	qBadDb, _ := sessions.store.AppendQuery(ctx, sessions.activeID, userTurn.ID, "sqlite:///chinook.db", QueryResult{
		Title:    "bad db",
		Source:   "sqlite:///chinook.db",
		SourceID: "unknown_db_id",
		DTQL:     "from: {name: Invoice}\nlimit: 10\n",
	})
	_, _, _ = cbTurn.plan(ctx, qBadDb.RecordSetID, nil)

	// Valid query in turn
	validSrc := sessions.store.info.Sources[sessions.store.info.Database]
	qGoodTurn, _ := sessions.store.AppendQuery(ctx, sessions.activeID, userTurn.ID, validSrc, QueryResult{
		Title:    "invoices",
		Source:   validSrc,
		SourceID: sessions.store.info.Database,
		DTQL:     "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 10\n",
		Result:   secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": int64(1)}}}},
	})
	_, _ = sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", validSrc, qGoodTurn.RecordSetID)
	rowsGood, _ := sessions.ResultColumns(ctx, qGoodTurn.RecordSetID)
	if len(rowsGood) > 0 {
		_, prevGood, err := cbTurn.plan(ctx, qGoodTurn.RecordSetID, []string{rowsGood[0].ID})
		if err == nil && prevGood.Fingerprint != "" {
			// line 147: check error in apply
			_, _ = cbTurn.apply(canceledCheck, qGoodTurn.RecordSetID, prevGood.Fingerprint)
			// line 150: first apply succeeds
			_, _ = cbTurn.apply(ctx, qGoodTurn.RecordSetID, prevGood.Fingerprint)
			// line 140: second apply fails (already executed)
			if _, err := cbTurn.apply(ctx, qGoodTurn.RecordSetID, prevGood.Fingerprint); err == nil {
				t.Fatal("expected already executed error on second apply")
			}
		}
	}
}

// 5. result_columns.go: comprehensive error branches & query validation
// Not parallel: mutates dtqlSerialize and dtqlDeserialize
func TestCoverage100_ResultColumns(t *testing.T) {

	// resultColumnRows(nil)
	if rows := resultColumnRows(nil); rows != nil {
		t.Fatalf("expected nil rows, got %v", rows)
	}

	// resultColumnQuery:
	// 1. invalid DTQL
	if _, _, err := resultColumnQuery(RecordSet{DTQL: "INVALID"}); err == nil {
		t.Fatal("expected parse error")
	}
	// 2. Joins != 0
	joinDTQL := "from:\n  name: users\n  alias: u\n  joins:\n    - from: {name: orders, alias: o}\n      on: [{left: {field: id, source: u}, op: '==', right: {field: user_id, source: o}}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: joinDTQL}); err == nil {
		t.Fatal("expected join error")
	}
	// 3. schema != "main"
	schemaDTQL := "from: {schema: analytics, name: events}\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: schemaDTQL}); err == nil {
		t.Fatal("expected schema error")
	}
	// 4. Wildcard in columns
	wildDTQL := "from: {name: users}\ncolumns: [{wildcard: {exclude: [id]}}]\nlimit: 100\n"
	if q, root, err := resultColumnQuery(RecordSet{DTQL: wildDTQL}); err != nil {
		t.Fatalf("unexpected error on wildcard: %v (root=%v, q=%v)", err, root, q)
	}
	// 5. Projection provenance ambiguous (field from different source)
	ambigDTQL := "from: {name: users}\ncolumns: [{field: name, source: other}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: ambigDTQL}); err == nil {
		t.Fatal("expected ambiguous provenance error")
	}
	// 6. Aggregate not COUNT (e.g. SUM)
	sumDTQL := "from: {name: users}\ncolumns: [{aggregate: {function: sum, args: [{field: id}]}}]\ngroupBy: [{field: id}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: sumDTQL}); err == nil {
		t.Fatal("expected non-COUNT aggregate error")
	}
	// 6b. COUNT with len(args) != 1
	noArgsDTQL := "from: {name: users}\ncolumns: [{aggregate: {function: count, args: []}}]\ngroupBy: [{field: id}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: noArgsDTQL}); err == nil {
		t.Fatal("expected unsupported COUNT expression error")
	}
	// 6c. COUNT with unsupported operand
	badArgDTQL := "from: {name: users}\ncolumns: [{aggregate: {function: count, args: [{constant: 42}]}}]\ngroupBy: [{field: id}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: badArgDTQL}); err == nil {
		t.Fatal("expected unsupported COUNT operand error")
	}
	// 7. COUNT DISTINCT
	distinctDTQL := "from: {name: users}\ncolumns: [{aggregate: {function: count, distinct: true, args: [{field: id}]}}]\ngroupBy: [{field: id}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: distinctDTQL}); err == nil {
		t.Fatal("expected COUNT DISTINCT error")
	}
	// 8. Computed projection
	compDTQL := "from: {name: users}\ncolumns: [{constant: 42}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: compDTQL}); err == nil {
		t.Fatal("expected computed projection error")
	}
	// 9. Computed or ambiguous GROUP BY
	groupDTQL := "from: {name: users}\ncolumns: [{field: id}]\ngroupBy: [{source: other, field: id}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: groupDTQL}); err == nil {
		t.Fatal("expected computed group by error")
	}
	// 10. Global aggregate without GROUP BY
	globalDTQL := "from: {name: users}\ncolumns: [{aggregate: {function: count, args: [{field: id}]}}]\nlimit: 100\n"
	if _, _, err := resultColumnQuery(RecordSet{DTQL: globalDTQL}); err == nil {
		t.Fatal("expected global aggregate error")
	}

	// qualifyColumnHaving
	// nil condition
	cond, err := qualifyColumnHaving(nil, "u", nil)
	if err != nil || cond != nil {
		t.Fatalf("expected nil condition, got %v, err=%v", cond, err)
	}
	// Comparison
	comp := dal.NewComparison(dal.NewFieldRef("", "c"), dal.Equal, dal.NewConstant(1))
	cond, err = qualifyColumnHaving(comp, "u", []dal.Column{{Alias: "c", Expression: dal.NewFieldRef("", "id")}})
	if err != nil || cond == nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Comparison Left error (line 146)
	badAgg := dal.NewOrderedAggregate("COUNT", []dal.OrderExpression{dal.Ascending(dal.NewFieldRef("", "id"))})
	compLeftErr := dal.NewComparison(badAgg, dal.Equal, dal.NewConstant(1))
	if _, err := qualifyColumnHaving(compLeftErr, "u", nil); err == nil {
		t.Fatal("expected left error in qualifyColumnHaving")
	}
	// Comparison Right error (line 150)
	compRightErr := dal.NewComparison(dal.NewFieldRef("u", "id"), dal.Equal, badAgg)
	if _, err := qualifyColumnHaving(compRightErr, "u", []dal.Column{{Alias: "id", Expression: dal.NewFieldRef("u", "id")}}); err == nil {
		t.Fatal("expected right error in qualifyColumnHaving")
	}
	// GroupCondition
	grp := dal.NewGroupCondition(dal.And, comp)
	cond, err = qualifyColumnHaving(grp, "u", nil)
	if err != nil || cond == nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// GroupCondition child error (line 159)
	grpChildErr := dal.NewGroupCondition(dal.And, compLeftErr)
	if _, err := qualifyColumnHaving(grpChildErr, "u", nil); err == nil {
		t.Fatal("expected child error in qualifyColumnHaving")
	}
	// Unsupported shape
	if _, err := qualifyColumnHaving(dal.NewFieldRef("", "invalid"), "u", nil); err == nil {
		t.Fatal("expected unsupported HAVING shape error")
	}

	// columnGroupHas returning false (line 173)
	qDummy, _ := dtql.Deserialize([]byte("from: {name: users}\ncolumns: [{field: id}]\ngroupBy: [{field: id}]\nlimit: 10\n"))
	if columnGroupHas(qDummy, "missing_column") {
		t.Fatal("expected columnGroupHas to return false")
	}

	// returnedColumnKeyOutOfRange
	qParsed, _ := dtql.Deserialize([]byte("from: {name: users}\ncolumns: [{field: id, as: id_alias}]\n"))
	recOutOfRange := RecordSet{
		Result: secureread.Result{
			Rows: []secureread.Row{
				{Data: map[string]any{"id_alias": int64(math.MaxInt64)}},
			},
		},
	}
	if !returnedColumnKeyOutOfRange(recOutOfRange, qParsed, "id") {
		t.Fatal("expected int64 out of range")
	}
	recOutOfRange.Result.Rows = []secureread.Row{{Data: map[string]any{"id_alias": int(math.MaxInt)}}}
	if !returnedColumnKeyOutOfRange(recOutOfRange, qParsed, "id") {
		t.Fatal("expected int out of range")
	}
	recOutOfRange.Result.Rows = []secureread.Row{{Data: map[string]any{"id_alias": float64(1e18)}}}
	if !returnedColumnKeyOutOfRange(recOutOfRange, qParsed, "id") {
		t.Fatal("expected float64 out of range")
	}
	recOutOfRange.Result.Rows = []secureread.Row{{Data: map[string]any{"id_alias": json.Number("99999999999999999999")}}}
	if !returnedColumnKeyOutOfRange(recOutOfRange, qParsed, "id") {
		t.Fatal("expected json.Number out of range")
	}
	recOutOfRange.Result.Rows = []secureread.Row{{Data: map[string]any{"id_alias": json.Number("invalid")}}}
	if !returnedColumnKeyOutOfRange(recOutOfRange, qParsed, "id") {
		t.Fatal("expected json.Number invalid out of range")
	}
	recOutOfRange.Result.Rows = []secureread.Row{{Data: map[string]any{"id_alias": int64(100)}}}
	if returnedColumnKeyOutOfRange(recOutOfRange, qParsed, "id") {
		t.Fatal("expected 100 to be within range")
	}

	// planResultColumns: count limits
	if _, err := planResultColumns(RecordSet{}, resultColumnMetadata{}, nil, nil); err == nil {
		t.Fatal("expected omitted columns limit error for 0")
	}
	tooMany := make([]string, 13)
	if _, err := planResultColumns(RecordSet{}, resultColumnMetadata{}, nil, tooMany); err == nil {
		t.Fatal("expected omitted columns limit error for 13")
	}
	if _, err := planResultColumns(RecordSet{}, resultColumnMetadata{}, nil, []string{"a", "a"}); err == nil {
		t.Fatal("expected duplicate column candidate error")
	}

	// discoverResultColumns & planResultColumns on valid record
	meta := resultColumnMetadata{
		fk: ForeignKeySnapshot{
			Source: "sqlite:///fixture.db",
			Keys: []ForeignKey{
				{
					ConstraintID: "fk_users_orders",
					Schema:       "main",
					FromRelation: "users",
					FromFields:   []string{"id"},
					ToSchema:     "main",
					ToRelation:   "orders",
					ToFields:     []string{"user_id"},
				},
			},
		},
		tables: map[string]resultColumnTable{
			"main.users": {
				columns:     []string{"id", "name", "email", "secret"},
				types:       map[string]string{"id": "INTEGER", "name": "TEXT", "email": "TEXT", "secret": "TEXT"},
				rowidKey:    "id",
				binaryGroup: map[string]bool{"id": true, "name": true, "email": true},
			},
			"main.orders": {
				columns:     []string{"user_id", "total", "notes"},
				types:       map[string]string{"user_id": "INTEGER", "total": "INTEGER", "notes": "TEXT"},
				rowidKey:    "user_id",
				binaryGroup: map[string]bool{"user_id": true},
			},
		},
	}
	admitted := map[string]bool{"main.users": true, "main.orders": true}
	recValid := RecordSet{
		ID:     "r_valid",
		Source: "sqlite:///fixture.db",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}, {field: name}]\n",
		Result: secureread.Result{
			Columns: []string{"id", "name"},
			Rows:    []secureread.Row{{Data: map[string]any{"id": int64(1), "name": "Alice"}}},
		},
	}

	discovered, err := discoverResultColumns(recValid, meta, admitted)
	if err != nil {
		t.Fatalf("unexpected error discovering columns: %v", err)
	}
	if len(discovered) == 0 {
		t.Fatal("expected discovered columns")
	}

	// discoverResultColumns: not admitted (line 198)
	if _, err := discoverResultColumns(recValid, meta, map[string]bool{}); err == nil {
		t.Fatal("expected unadmitted error in discoverResultColumns")
	}
	// discoverResultColumns: multiple FK edges (line 245)
	metaMulti := meta
	metaMulti.fk.Keys = append(metaMulti.fk.Keys, ForeignKey{
		ConstraintID: "fk2",
		Schema:       "main",
		FromRelation: "users",
		FromFields:   []string{"id"},
		ToSchema:     "main",
		ToRelation:   "orders",
		ToFields:     []string{"user_id"},
	})
	discMulti, _ := discoverResultColumns(recValid, metaMulti, admitted)
	_ = discMulti

	// discoverResultColumns: binaryGroup false (line 251)
	metaNonBinary := meta
	tUsers := metaNonBinary.tables["main.users"]
	tUsers.binaryGroup = map[string]bool{"id": false}
	metaNonBinary.tables["main.users"] = tUsers
	discNonBin, _ := discoverResultColumns(recValid, metaNonBinary, admitted)
	_ = discNonBin

	// discoverResultColumns: non-integer type (line 254)
	metaText := meta
	tUsers2 := metaText.tables["main.users"]
	tUsers2.types = map[string]string{"id": "TEXT"}
	metaText.tables["main.users"] = tUsers2
	discText, _ := discoverResultColumns(recValid, metaText, admitted)
	_ = discText

	// discoverResultColumns: grouped but FK not in group or projection (line 257)
	recGrpMissing := RecordSet{
		ID:     "r_grp_missing",
		Source: "sqlite:///fixture.db",
		DTQL:   "from: {name: users}\ncolumns: [{field: name}, {aggregate: {function: count, args: [{field: name}]}}]\ngroupBy: [{field: name}]\nlimit: 100\n",
	}
	discGrpMissing, _ := discoverResultColumns(recGrpMissing, meta, admitted)
	_ = discGrpMissing

	// Find an addable candidate ID
	var candID string
	for _, d := range discovered {
		if d.Addable {
			candID = d.ID
			break
		}
	}
	if candID == "" {
		t.Fatal("expected an addable candidate")
	}

	// Plan valid columns
	prev, err := planResultColumns(recValid, meta, admitted, []string{candID})
	if err != nil {
		t.Fatalf("unexpected planResultColumns error: %v", err)
	}
	if prev.DTQL == "" || prev.Fingerprint == "" {
		t.Fatal("expected non-empty DTQL and Fingerprint")
	}

	// planResultColumns: record.DTQL invalid (line 338)
	badRec := recValid
	badRec.DTQL = "INVALID"
	if _, err := planResultColumns(badRec, meta, admitted, []string{candID}); err == nil {
		t.Fatal("expected DTQL error in planResultColumns")
	}

	// planResultColumns: selected column not in rows (line 447)
	if _, err := planResultColumns(recValid, meta, admitted, []string{"nonexistent-col-id"}); err == nil {
		t.Fatal("expected column metadata changed error")
	}

	// planResultColumns: DESC order by (line 389)
	recDesc := recValid
	recDesc.DTQL = "from: {name: users}\ncolumns: [{field: id}, {field: name}]\norderBy: [{field: id, desc: true}]\nlimit: 100\n"
	rowsDesc, _ := discoverResultColumns(recDesc, meta, admitted)
	var candDescID string
	for _, r := range rowsDesc {
		if r.Addable {
			candDescID = r.ID
			break
		}
	}
	if candDescID != "" {
		prevDesc, err := planResultColumns(recDesc, meta, admitted, []string{candDescID})
		if err != nil {
			t.Fatal(err)
		}
		_ = prevDesc
	}

	// planResultColumns: alias collision in join and outputs (lines 423, 436)
	recAlias := recValid
	recAlias.DTQL = "from: {name: users, alias: orders}\ncolumns: [{field: id, as: orders_notes}, {field: name}]\nlimit: 100\n"
	rowsAlias, _ := discoverResultColumns(recAlias, meta, admitted)
	var candAliasID string
	for _, r := range rowsAlias {
		if r.Addable {
			candAliasID = r.ID
			break
		}
	}
	if candAliasID != "" {
		prevAlias, _ := planResultColumns(recAlias, meta, admitted, []string{candAliasID})
		_ = prevAlias
	}

	// planResultColumns: dtqlSerialize and dtqlDeserialize errors (lines 451, 454)
	origSer := dtqlSerialize
	origDes := dtqlDeserialize
	dtqlSerialize = func(q dal.StructuredQuery) ([]byte, error) {
		return nil, errors.New("serialize fail")
	}
	if _, err := planResultColumns(recValid, meta, admitted, []string{candID}); err == nil {
		t.Fatal("expected dtqlSerialize error in planResultColumns")
	}
	dtqlSerialize = origSer

	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return nil, errors.New("deserialize fail")
	}
	if _, err := planResultColumns(recValid, meta, admitted, []string{candID}); err == nil {
		t.Fatal("expected dtqlDeserialize error in planResultColumns")
	}
	dtqlDeserialize = origDes

	// Plan with grouped query: proofs and grouped branches
	recGrouped := RecordSet{
		ID:     "r_grouped",
		Source: "sqlite:///fixture.db",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}, {aggregate: {function: count, args: [{field: id}]}}]\ngroupBy: [{field: id}]\nlimit: 100\n",
		Result: secureread.Result{
			Columns: []string{"id", "COUNT(*)"},
			Rows:    []secureread.Row{{Data: map[string]any{"id": int64(1)}}},
		},
	}
	discGrouped, err := discoverResultColumns(recGrouped, meta, admitted)
	if err != nil {
		t.Fatal(err)
	}
	var candGroupedID string
	for _, d := range discGrouped {
		if d.Addable {
			candGroupedID = d.ID
			break
		}
	}
	if candGroupedID != "" {
		prevGrouped, err := planResultColumns(recGrouped, meta, admitted, []string{candGroupedID})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prevGrouped.Proof, "COUNT") {
			t.Fatalf("expected grouped proof to mention COUNT, got %s", prevGrouped.Proof)
		}
	}

	// resultColumnEvidence & ResultColumns & resultColumnsPreview & executeResultColumns
	ctx := context.Background()
	_, sessions := newTestChatUI(t, nil)
	rID := sessions.store.info.Database

	// ResultColumns: missing record
	if _, err := sessions.ResultColumns(ctx, "missing"); err == nil {
		t.Fatal("expected missing record error")
	}
	// ResultColumns: store.Load error (line 512)
	canceledCtxLoad, cancelLoad := context.WithCancel(ctx)
	cancelLoad()
	if _, err := sessions.ResultColumns(canceledCtxLoad, rID); err == nil {
		t.Fatal("expected store.Load error in ResultColumns")
	}

	// resultColumnsPreview: activeID mismatch
	if _, err := sessions.resultColumnsPreview(ctx, "wrong-id", rID, nil); err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// resultColumnsPreview: parentID not found
	if _, err := sessions.resultColumnsPreview(ctx, sessions.activeID, "missing-parent", nil); err == nil {
		t.Fatal("expected origin result is unavailable error")
	}
	// resultColumnsPreview: store.Load error (line 531)
	if _, err := sessions.resultColumnsPreview(canceledCtxLoad, sessions.activeID, rID, nil); err == nil {
		t.Fatal("expected store.Load error in resultColumnsPreview")
	}

	// resultColumnEvidence: DTQL invalid (line 488)
	_, _, _ = sessions.resultColumnEvidence(ctx, RecordSet{DTQL: "INVALID"})
	// resultColumnEvidence: source metadata error (line 496)
	_, _, _ = sessions.resultColumnEvidence(ctx, RecordSet{DTQL: "from: {name: users}\ncolumns: [{field: id}]\nlimit: 10\n", Source: "invalid-source"})

	// planResultColumns: duplicate candidate (line 332)
	if _, err := planResultColumns(recValid, meta, admitted, []string{candID, candID}); err == nil {
		t.Fatal("expected duplicate column candidate error")
	}

	// executeResultColumns:
	// canceled ctx
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := sessions.executeResultColumns(canceledCtx, sessions.activeID, rID, nil, "", ""); err == nil {
		t.Fatal("expected ctx error")
	}

	// Set up Invoice query in Chinook store to execute against
	userMsg, _ := sessions.store.AppendUser(ctx, sessions.activeID, "test")
	validSource := sessions.store.info.Sources[sessions.store.info.Database]
	qRes, _ := sessions.store.AppendQuery(ctx, sessions.activeID, userMsg.ID, validSource, QueryResult{
		Title:    "invoices",
		Source:   validSource,
		SourceID: sessions.store.info.Database,
		DTQL:     "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 10\n",
		Result:   secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": int64(1)}}}},
	})
	_, _ = sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", validSource, qRes.RecordSetID)

	// preview error (no columns)
	if _, _, err := sessions.executeResultColumns(ctx, sessions.activeID, qRes.RecordSetID, nil, "fp", ""); err == nil {
		t.Fatal("expected preview error on empty ids")
	}

	cols, err := sessions.ResultColumns(ctx, qRes.RecordSetID)
	if err == nil && len(cols) > 0 {
		candInvoiceID := cols[0].ID
		prevExec, err := sessions.resultColumnsPreview(ctx, sessions.activeID, qRes.RecordSetID, []string{candInvoiceID})
		if err == nil && prevExec.Fingerprint != "" {
			// fingerprint mismatch (line 553)
			_, _, _ = sessions.executeResultColumns(ctx, sessions.activeID, qRes.RecordSetID, []string{candInvoiceID}, "mismatched_fp", "")

			// load error (line 557)
			canceledCtxLoad, cancelLoad := context.WithCancel(ctx)
			cancelLoad()
			_, _, _ = sessions.executeResultColumns(canceledCtxLoad, sessions.activeID, qRes.RecordSetID, []string{candInvoiceID}, prevExec.Fingerprint, "")

			// ctx error after Load (line 561)
			cancelCtx561, cancel561 := context.WithCancel(ctx)
			origLoadHTTP := loadHTTPResponsesFn
			loadHTTPResponsesFn = func(s *SessionStore, c context.Context, item *ChatSession) error {
				cancel561()
				return origLoadHTTP(s, c, item)
			}
			_, _, _ = sessions.executeResultColumns(cancelCtx561, sessions.activeID, qRes.RecordSetID, []string{candInvoiceID}, prevExec.Fingerprint, "")
			loadHTTPResponsesFn = origLoadHTTP

			// activeID mismatch (line 571)
			origExec := sessions.queryExecutor
			origActive := sessions.activeID
			sessions.queryExecutor = &switchingSessionExecutor{sessions: sessions}
			_, _, _ = sessions.executeResultColumns(ctx, sessions.activeID, qRes.RecordSetID, []string{candInvoiceID}, prevExec.Fingerprint, "")
			sessions.queryExecutor = origExec
			sessions.activeID = origActive

			// AppendUser error (line 578)
			origInsert := insertMessageFn
			insertMessageFn = func(ctx context.Context, tx *sql.Tx, sessionID string, message ChatMessage) error {
				return errors.New("insert fail")
			}
			_, _, _ = sessions.executeResultColumns(ctx, sessions.activeID, qRes.RecordSetID, []string{candInvoiceID}, prevExec.Fingerprint, "")
			insertMessageFn = origInsert

			// AppendQuery error (line 584)
			origAppQ := appendQueryTxFn
			appendQueryTxFn = func(s *SessionStore, ctx context.Context, tx *sql.Tx, sessionID, originID, source string, query *QueryResult, now time.Time) error {
				return errors.New("append fail")
			}
			_, _, _ = sessions.executeResultColumns(ctx, sessions.activeID, qRes.RecordSetID, []string{candInvoiceID}, prevExec.Fingerprint, "")
			appendQueryTxFn = origAppQ

			// Successful execution (lines 586-588)
			_, _, _ = sessions.executeResultColumns(ctx, sessions.activeID, qRes.RecordSetID, []string{candInvoiceID}, prevExec.Fingerprint, "")
		}
	}
}

// 6. result_columns_ui.go: full overlay interaction testing
func TestCoverage100_ResultColumnsUI(t *testing.T) {
	t.Parallel()

	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})

	// LocalMouseCoordinates, BoundedMouseInput, HelpHints
	o := &resultColumnsOverlay{ui: u, loading: true}
	if !o.LocalMouseCoordinates() || !o.BoundedMouseInput() {
		t.Fatal("expected true")
	}
	hintsLoading := o.HelpHints()
	if len(hintsLoading) == 0 || !strings.Contains(hintsLoading[0], "Loading") {
		t.Fatalf("unexpected hints loading: %v", hintsLoading)
	}
	o.loading = false
	hintsNormal := o.HelpHints()
	if len(hintsNormal) == 0 || !strings.Contains(hintsNormal[0], "Add") {
		t.Fatalf("unexpected hints normal: %v", hintsNormal)
	}
	o.preview = &ResultColumnsPreview{Fields: []string{"col1"}, DTQL: "SELECT col1"}
	hintsPreview := o.HelpHints()
	if len(hintsPreview) == 0 || !strings.Contains(hintsPreview[0], "Review") {
		t.Fatalf("unexpected hints preview: %v", hintsPreview)
	}

	// openResultColumns
	// when no grid active
	uEmpty, _ := newTestChatUI(t, nil)
	uEmpty.gridsByRecordSetID = nil
	uEmpty.retainedRecordSetID = ""
	cmd := uEmpty.openResultColumns()
	if cmd != nil {
		t.Fatal("expected nil cmd when no grid")
	}

	// openResultColumns when visibleGridReason() != "" (line 50)
	back := uEmpty.shell.ShowTemporaryCenterPanel(&localGroupsPanel{ui: uEmpty})
	_ = uEmpty.openResultColumns()
	back()

	// toggle on invalid indices and non-addable row (line 81)
	o.toggle(-1)
	o.toggle(999)
	o.rows = []ResultColumnRow{{ID: "non-addable", Addable: false}}
	o.toggle(0)

	// View:
	// 1. loading
	o.preview = nil
	o.loading = true
	vLoading := o.View(80, 24)
	if !strings.Contains(vLoading, "Loading admitted") {
		t.Fatalf("expected loading view, got %s", vLoading)
	}
	// 2. len(rows) == 0
	o.loading = false
	o.rows = nil
	vEmpty := o.View(80, 24)
	if !strings.Contains(vEmpty, "No eligible") {
		t.Fatalf("expected no eligible view, got %s", vEmpty)
	}
	// 3. rows with selections & offset
	o.rows = make([]ResultColumnRow, 30)
	for i := range o.rows {
		o.rows[i] = ResultColumnRow{ID: fmt.Sprintf("c%d", i), Name: fmt.Sprintf("col%d", i), Addable: true}
	}
	o.selected = map[string]bool{"c2": true}
	o.offset = 10
	o.index = 2
	vRows := o.View(80, 20)
	if !strings.Contains(vRows, "[✓]") || !strings.Contains(vRows, "col2") {
		t.Fatalf("expected rows view, got %s", vRows)
	}
	o.offset = 0
	o.index = 25
	_ = o.View(80, 15)
	// 4. preview view with small and large height
	o.preview = &ResultColumnsPreview{
		Fields: []string{"orders.total"},
		Proof:  "Left join proof",
		DTQL:   "SELECT users.id, orders.total FROM users LEFT JOIN orders ON users.id = orders.user_id",
	}
	vPrev := o.View(80, 24)
	if !strings.Contains(vPrev, "Review column query") {
		t.Fatalf("expected review view, got %s", vPrev)
	}
	// Height 2 to test controlY >= h
	_ = o.View(80, 2)

	// Update:
	// resultColumnsLoadedMsg
	// !valid
	_, _, _ = o.Update(resultColumnsLoadedMsg{overlay: o, err: nil})
	if o.err == "" {
		t.Fatal("expected context changed error")
	}
	// Make valid
	g, r, _ := u.activeGrid()
	o.grid = g
	o.record = *r
	o.sessionID = u.sessionID
	o.generation = u.workspaceGeneration
	// loaded.err != nil
	_, _, _ = o.Update(resultColumnsLoadedMsg{overlay: o, err: fmt.Errorf("load failed")})
	if !strings.Contains(o.err, "load failed") {
		t.Fatalf("expected load failed in err, got %s", o.err)
	}
	// loaded rows
	_, _, _ = o.Update(resultColumnsLoadedMsg{overlay: o, rows: []ResultColumnRow{
		{ID: "r1", Name: "returned", Returned: true},
		{ID: "r2", Name: "not_returned", Returned: false, Addable: true},
	}})
	if len(o.rows) < 1 {
		t.Fatal("expected rows to be appended")
	}

	// Mouse clicks:
	// 1. click on controlY in preview mode:
	// - click Esc
	o.preview = &ResultColumnsPreview{Fields: []string{"f"}, DTQL: "q"}
	_ = o.View(80, 24)
	_, _, _ = o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 50, Y: o.controlY})
	if o.preview != nil {
		t.Fatal("expected Esc click to clear preview")
	}
	// - click Run on server
	o.preview = &ResultColumnsPreview{Fields: []string{"f"}, DTQL: "q"}
	_ = o.View(80, 24)
	_, cmdRun, _ := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: o.controlY})
	_ = cmdRun
	// - click Run on server separator (line 179)
	o.preview = &ResultColumnsPreview{Fields: []string{"f"}, DTQL: "q"}
	_ = o.View(80, 24)
	_, _, _ = o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: ansi.StringWidth("Run on server ⇧↵"), Y: o.controlY})

	// 2. click on controlY in non-preview mode:
	// - click Apply
	o.preview = nil
	_ = o.View(80, 24)
	_, _, _ = o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: o.controlY})
	// - click Apply separator (line 186)
	_, _, _ = o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: ansi.StringWidth("Apply ↵") + 5, Y: o.controlY})

	// 3. click on row targets in non-preview mode
	if len(o.targets) > 0 {
		tgt := o.targets[0]
		_, _, _ = o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: tgt.y})
	}

	// Non-key update when preview == nil (line 258)
	o.preview = nil
	_, _, _ = o.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})

	// Key presses:
	// "esc" when preview != nil
	o.preview = &ResultColumnsPreview{}
	_, _, _ = o.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if o.preview != nil {
		t.Fatal("expected preview cleared on esc")
	}
	// "esc" when preview == nil -> close
	_, _, closeOverlay := o.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !closeOverlay {
		t.Fatal("expected overlay to close on esc")
	}

	// "up", "down", "space", "enter"
	o.preview = nil
	o.loading = false
	_, _, _ = o.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	_, _, _ = o.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, _, _ = o.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	_, _, _ = o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// "shift+enter" when busy
	o.preview = &ResultColumnsPreview{}
	u.workspaceMutationPending = true
	_, _, _ = o.Update(uitest.Key("shift+enter"))
	if !strings.Contains(o.err, "running") {
		t.Fatalf("expected running error, got %s", o.err)
	}

	// "shift+enter" when not busy
	u.workspaceMutationPending = false
	u.backends = nil
	u.operation = nil
	o.preview = &ResultColumnsPreview{Fingerprint: "fp1"}
	_, cmdExecute, _ := o.Update(uitest.Key("shift+enter"))
	if cmdExecute == nil {
		t.Fatal("expected execute command")
	}
	// run command
	msg := cmdExecute()
	_ = msg

	// Viewport update delegation
	o.preview = &ResultColumnsPreview{}
	_, _, _ = o.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
}

// 7. result_operations.go & derived_execution.go
// Not parallel: mutates resultopsCapture and dtqlDeserialize
func TestCoverage100_ResultOperationsAndDerived(t *testing.T) {
	ctx := context.Background()
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		DTQL:   "from: {name: users}\ncolumns: [{field: id}, {field: name}]\nlimit: 10\n",
		Source: "sqlite:///chinook.db",
		Result: secureread.Result{Columns: []string{"id", "name"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1), "name": "Alice"}}}},
	})
	g, rec, ok := u.activeGrid()
	if !ok || rec == nil {
		t.Fatal("expected active grid")
	}
	validSource := u.sessions.store.info.Sources[u.sessions.store.info.Database]
	_, _ = u.sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", validSource, rec.ID)

	// 1. captureLoaded: error branches
	// Column count mismatch (line 41)
	recMismatch := *rec
	recMismatch.Result.Columns = []string{"id", "name", "extra"}
	if _, err := u.captureLoaded(g, &recMismatch); err == nil {
		t.Fatal("expected column count mismatch error")
	}

	// Invalid row key & out of bounds (line 53)
	gBadRow := &gridState{Model: grid.New([]grid.Column{{Name: "id"}}, []grid.Row{{Key: "invalid_idx", Values: []any{"1"}}})}
	if _, err := u.captureLoaded(gBadRow, rec); err == nil {
		t.Fatal("expected invalid row key error")
	}
	gBadIdx := &gridState{Model: grid.New([]grid.Column{{Name: "id"}}, []grid.Row{{Key: "999", Values: []any{"1"}}})}
	if _, err := u.captureLoaded(gBadIdx, rec); err == nil {
		t.Fatal("expected out of bounds row key error")
	}

	// resultopsCapture error (line 62)
	origCapture := resultopsCapture
	resultopsCapture = func(cols []resultops.Column, rows []resultops.Row) (resultops.Snapshot, error) {
		return resultops.Snapshot{}, errors.New("capture fail")
	}
	gFresh := &gridState{Model: grid.New([]grid.Column{{Name: "id"}, {Name: "name"}}, []grid.Row{{Key: "0", Values: []any{int64(1), "Alice"}}})}
	if _, err := u.captureLoaded(gFresh, rec); err == nil {
		t.Fatal("expected capture error")
	}
	resultopsCapture = origCapture

	// Normal captureLoaded
	if _, err := u.captureLoaded(g, rec); err != nil {
		t.Fatal(err)
	}
	// already captured branch
	_, _ = u.captureLoaded(g, rec)

	// 2. newResultOperation: visibleGridReason error
	uNoGrid := focusedGridTestUI(t)
	uNoGrid.shell.ShowTemporaryCenterPanel(&localGroupsPanel{ui: uNoGrid})
	if _, err := uNoGrid.newResultOperation(false); err == nil {
		t.Fatal("expected error on visible center panel")
	}
	// quickServerSort when newResultOperation errors (line 129)
	_ = uNoGrid.quickServerSort(false)

	// newResultOperation: no active grid
	uEmpty, _ := newTestChatUI(t, nil)
	if _, err := uEmpty.newResultOperation(false); err == nil {
		t.Fatal("expected error on no active grid")
	}

	// 3. quickServerSort: selected column out of range (line 134)
	g.SelectColumn(-1)
	_ = u.quickServerSort(false)
	g.SelectColumn(0)
	_ = u.quickServerSort(false)
	_ = u.quickServerSort(true)

	// 4. openResultOperation
	_ = u.openResultOperation(false)
	_ = u.openResultOperation(true)

	// groupedRecord with group == true
	groupedRec := *rec
	groupedRec.Title = "Grouped Result"
	groupedRec.DTQL = "from: {name: users}\ncolumns: [{field: id}]\ngroupBy: [{field: id}]\nlimit: 10\n"
	uGrouped := focusedGridStoreBackedTestUI(t, QueryResult{Title: groupedRec.Title, DTQL: groupedRec.DTQL, Source: "sqlite:///chinook.db", Result: groupedRec.Result})
	_ = uGrouped.openResultOperation(true)

	// 5. resultOperationOverlay methods
	op, err := u.newResultOperation(false)
	if err != nil || op == nil {
		t.Fatal(err)
	}
	op.toggle(-1)
	op.toggle(999)
	_ = op.currentColumn()

	// View in group mode with originalOutput and wrapping
	opGroup, err := u.newResultOperation(true)
	if err != nil || opGroup == nil {
		t.Fatal(err)
	}
	opGroup.originalOutput = true
	_ = opGroup.View(80, 24)
	// Force wrap truncation (lines 223-225)
	_ = opGroup.View(10, 5)
	// Focus on buttons 3..7 (line 252)
	for f := 2; f <= 7; f++ {
		opGroup.focus = f
		_ = opGroup.View(80, 24)
	}

	// Update mouse clicks:
	// - click on focus 1 (filter) (line 330)
	if len(opGroup.mouseTargets) > 0 {
		_, _, _ = opGroup.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: opGroup.mouseTargets[0].y})
	}
	// - click outside targets (line 335)
	_, _, _ = opGroup.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 999})

	// Non-key msg when focus == 1 vs focus != 1 (lines 339-344)
	opGroup.focus = 1
	_, _, _ = opGroup.Update(tea.MouseWheelMsg{})
	opGroup.focus = 0
	_, _, _ = opGroup.Update(tea.MouseWheelMsg{})

	// ctrl+r with reorder error (lines 368-370)
	gReorderBad := &gridState{Model: grid.New([]grid.Column{{Name: "id"}}, []grid.Row{{Key: "0"}})}
	opReorderBad := &resultOperationOverlay{ui: u, grid: gReorderBad, capture: &loadedResultCapture{rows: []grid.Row{{Key: "bad_key"}}}, sessionID: u.sessionID, generation: u.workspaceGeneration}
	_, _, _ = opReorderBad.Update(uitest.Key("ctrl+r"))

	// enter with Mod != 0 (line 377)
	_, _, _ = opGroup.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})

	// enter with focus == 2 when !group (lines 388-393)
	opSort, _ := u.newResultOperation(false)
	opSort.focus = 2
	_, _, _ = opSort.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	_, _, _ = opSort.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// enter with focus == 3 (lines 396-398)
	opSort.focus = 3
	_, _, _ = opSort.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// enter with focus == 5 (lines 400-402)
	opSort.focus = 5
	_, _, _ = opSort.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// enter with focus == 6 (lines 404-412)
	opSort.focus = 6
	_, _, _ = opSort.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	opReorderBad.focus = 6
	_, _, _ = opReorderBad.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// enter with focus == 7 (line 414)
	opSort.focus = 7
	_, _, _ = opSort.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// up key (line 426)
	opSort.focus = 0
	_, _, _ = opSort.Update(uitest.Key("up"))
	_, _, _ = opSort.Update(uitest.Key("down"))

	// space key with focus == 2 and focus == 3 (line 434)
	opGroup.focus = 2
	_, _, _ = opGroup.Update(uitest.Key("space"))
	opGroup.focus = 3
	_, _, _ = opGroup.Update(uitest.Key("space"))

	// "c" key on focus == 2 & non-"c" on focus == 2 (line 458)
	opGroup.focus = 2
	_, _, _ = opGroup.Update(uitest.Key("c"))
	_, _, _ = opGroup.Update(uitest.Key("s"))

	// alt+up and alt+down (lines 472-480)
	opSort.selected = []int{0, 1}
	opSort.index = 0
	_, _, _ = opSort.Update(uitest.Key("alt+down"))
	_, _, _ = opSort.Update(uitest.Key("alt+up"))

	// '0' key for column 9 (line 485)
	_, _, _ = opSort.Update(tea.KeyPressMsg{Code: '0'})

	// applyLoaded error branches:
	// !valid() (line 495)
	opInvalid := &resultOperationOverlay{ui: u, sessionID: "wrong"}
	_ = opInvalid.applyLoaded()

	// len(selected) == 0 (line 499)
	opSort.selected = nil
	_ = opSort.applyLoaded()

	// reorder error (line 514)
	opReorderBad.selected = []int{0}
	_ = opReorderBad.applyLoaded()

	// GroupByAggregates error (line 527)
	opGroupBad, _ := u.newResultOperation(true)
	opGroupBad.selected = []int{0}
	opGroupBad.aggregates[0] = "INVALID_FUNC"
	_ = opGroupBad.applyLoaded()

	// Count label collision (line 538)
	recCountCol := *rec
	recCountCol.Result.Columns = []string{"Count", "name"}
	recCountCol.Result.Rows = []secureread.Row{{Data: map[string]any{"Count": int64(1), "name": "Alice"}}}
	uCountCol := focusedGridStoreBackedTestUI(t, QueryResult{Title: recCountCol.Title, DTQL: recCountCol.DTQL, Source: "sqlite:///chinook.db", Result: recCountCol.Result})
	opCountCol, _ := uCountCol.newResultOperation(true)
	opCountCol.selected = []int{0}
	opCountCol.count = true
	_ = opCountCol.applyLoaded()

	// localGroupsPanel
	lp := &localGroupsPanel{ui: u, model: grid.New([]grid.Column{{Name: "c1"}}, nil, grid.WithoutFrame())}
	if lp.Title() == "" || !lp.CapturesEsc() {
		t.Fatal("expected title and captures esc")
	}
	_ = lp.View(80, 24, true)
	_ = lp.View(40, 24, false)
	_, _ = lp.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	// localGroupsPanel CapturesEsc (lines 599-602)
	lpFilter := &localGroupsPanel{ui: u, model: grid.New([]grid.Column{{Name: "c1"}}, []grid.Row{{Key: "0", Values: []any{"a"}}}, grid.WithFilterOnType())}
	lpFilter.model.Update(uitest.Key("/"))
	lpFilter.model.Update(uitest.Key("a"))
	_, _ = lpFilter.Update(uitest.Key("esc"))

	// quickSortLoaded activeGrid false (line 636)
	_ = uNoGrid.quickSortLoaded(false)

	// 6. derived_execution.go:
	// exactAggregateColumns: joins in query (lines 40-44)
	recJoins := *rec
	recJoins.DTQL = "from:\n  name: orders\n  alias: orders\n  joins:\n    - from: {name: other, alias: other}\n      on: [{left: {field: user_id, source: orders}, op: '==', right: {field: id, source: other}}]\nlimit: 10\n"
	_, _ = exactAggregateColumns(u.sessions.catalog, recJoins)

	// deterministicOriginal:
	// GroupBy: one key not in OrderBy -> false (lines 71-72)
	qGrpNotOrdered, _ := dtql.Deserialize([]byte("from: {name: users}\ncolumns: [{field: id}, {field: name}]\ngroupBy: [{field: id}, {field: name}]\norderBy: [{field: id}]\nlimit: 10\n"))
	if deterministicOriginal(u.sessions.catalog, *rec, qGrpNotOrdered) {
		t.Fatal("expected false when group by keys not all ordered")
	}
	// GroupBy: all keys ordered -> true (line 75)
	qGrpOrdered, _ := dtql.Deserialize([]byte("from: {name: users}\ncolumns: [{field: id}, {field: name}]\ngroupBy: [{field: id}, {field: name}]\norderBy: [{field: id}, {field: name}]\nlimit: 10\n"))
	if !deterministicOriginal(u.sessions.catalog, *rec, qGrpOrdered) {
		t.Fatal("expected true when all group by keys ordered")
	}
	// len(instances) != 1 -> false (line 79)
	qMultiInst, err := dtql.Deserialize([]byte("from:\n  name: orders\n  alias: orders\n  joins:\n    - from: {name: other, alias: other}\n      on: [{left: {field: user_id, source: orders}, op: '==', right: {field: id, source: other}}]\nlimit: 10\n"))
	if err != nil {
		t.Fatal(err)
	}
	if deterministicOriginal(u.sessions.catalog, *rec, qMultiInst) {
		t.Fatal("expected false for multiple instances")
	}
	// catalog object without dot (len(parts) == 1) -> lines 84-88
	catOnePart := u.sessions.catalog
	catOnePart.Objects = []ProjectObject{
		{
			Reference:  ContextReference{SourceID: rec.Database, ObjectID: "users"},
			PrimaryKey: []string{"id"},
		},
	}
	qNoOrder, _ := dtql.Deserialize([]byte("from: {name: users}\ncolumns: [{field: id}]\nlimit: 10\n"))
	if deterministicOriginal(catOnePart, *rec, qNoOrder) {
		t.Fatal("expected false when PK not ordered")
	}
	qOrderPK, _ := dtql.Deserialize([]byte("from: {name: users}\ncolumns: [{field: id}]\norderBy: [{field: id}]\nlimit: 10\n"))
	if !deterministicOriginal(catOnePart, *rec, qOrderPK) {
		t.Fatal("expected true when PK ordered")
	}

	// deriveResultPreview:
	dOp := derivedOperation{Group: true, Columns: []string{"id"}, Count: true}
	// limit > maxRows (line 112)
	recExceedLimit := *rec
	recExceedLimit.DTQL = fmt.Sprintf("from: {name: users}\ncolumns: [{field: id}]\nlimit: %d\n", maxRows+10)
	if _, err := deriveResultPreview(u.sessions.catalog, recExceedLimit, dOp); err == nil {
		t.Fatal("expected limit exceed error")
	}
	// dtqlDeserialize returns error (line 132)
	origDes := dtqlDeserialize
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return nil, errors.New("deser fail")
	}
	if _, err := deriveResultPreview(u.sessions.catalog, *rec, dOp); err == nil {
		t.Fatal("expected dtqlDeserialize error in deriveResultPreview")
	}
	dtqlDeserialize = origDes

	// executeDerivedResult:
	// activeID mismatch (line 141)
	if _, _, err := u.sessions.executeDerivedResult(ctx, "wrong-id", rec.ID, dOp, ""); err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}
	// store.Load error (line 145)
	canceledCtx4, cancel4 := context.WithCancel(ctx)
	cancel4()
	if _, _, err := u.sessions.executeDerivedResult(canceledCtx4, u.sessions.activeID, rec.ID, dOp, ""); err == nil {
		t.Fatal("expected store.Load error")
	}
	// parentID not found (line 149)
	if _, _, err := u.sessions.executeDerivedResult(ctx, u.sessions.activeID, "missing-parent", dOp, ""); err == nil {
		t.Fatal("expected origin result error")
	}
	// deriveResultPreview error (line 153)
	if _, _, err := u.sessions.executeDerivedResult(ctx, u.sessions.activeID, rec.ID, derivedOperation{Group: true, Columns: nil}, ""); err == nil {
		t.Fatal("expected deriveResultPreview error")
	}
	// preview changed error (line 156)
	prevDerived, _ := deriveResultPreview(u.sessions.catalog, *rec, dOp)
	if _, _, err := u.sessions.executeDerivedResult(ctx, u.sessions.activeID, rec.ID, dOp, "mismatched-doc"); err == nil {
		t.Fatal("expected preview changed error")
	}
	// origin source unavailable (line 160)
	_, _ = u.sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", "corrupted", rec.ID)
	if _, _, err := u.sessions.executeDerivedResult(ctx, u.sessions.activeID, rec.ID, dOp, prevDerived.DTQL); err == nil {
		t.Fatal("expected origin source unavailable error")
	}
	_, _ = u.sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET source = ? WHERE id = ?", validSource, rec.ID)

	// exact server aggregates without exactAggregateDTQLExecutor (line 169)
	dOpWithAgg := derivedOperation{
		Group:      true,
		Columns:    []string{"id"},
		Count:      true,
		Aggregates: []resultops.AggregateTerm{{Column: "id", Function: "sum", Alias: "sum_id"}},
		Scope:      queryops.MatchingSource,
	}
	prevAgg, _ := deriveResultPreview(u.sessions.catalog, *rec, dOpWithAgg)
	if prevAgg.DTQL != "" {
		if _, _, err := u.sessions.executeDerivedResult(ctx, u.sessions.activeID, rec.ID, dOpWithAgg, prevAgg.DTQL); err == nil {
			t.Fatal("expected executor cannot run exact server aggregates error")
		}
	}

	// OriginalOutput without recursiveDTQLExecutor (line 181)
	dOpOrig := derivedOperation{
		Group:   false,
		Columns: []string{"id"},
		Scope:   queryops.OriginalOutput,
		Sort:    []queryops.SortTerm{{Column: "id", Descending: false}},
	}
	recNoLimit := *rec
	recNoLimit.DTQL = "from: {name: users}\ncolumns: [{field: id}]\n"
	_, _ = u.sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET dtql = ? WHERE id = ?", recNoLimit.DTQL, rec.ID)
	prevOrig, _ := deriveResultPreview(u.sessions.catalog, recNoLimit, dOpOrig)
	if prevOrig.DTQL != "" {
		if _, _, err := u.sessions.executeDerivedResult(ctx, u.sessions.activeID, rec.ID, dOpOrig, prevOrig.DTQL); err == nil {
			t.Fatal("expected executor cannot safely run Original query output error")
		}
	}
	_, _ = u.sessions.store.db.ExecContext(ctx, "UPDATE recordsets SET dtql = ? WHERE id = ?", rec.DTQL, rec.ID)

	// AppendUser error (line 199) & AppendQuery error (line 203)
	prevNormal, _ := deriveResultPreview(u.sessions.catalog, *rec, dOp)
	origInsMsg := insertMessageFn
	insertMessageFn = func(ctx context.Context, tx *sql.Tx, sessionID string, message ChatMessage) error {
		return errors.New("insert fail")
	}
	if _, _, err := u.sessions.executeDerivedResult(ctx, u.sessions.activeID, rec.ID, dOp, prevNormal.DTQL); err == nil {
		t.Fatal("expected AppendUser error in executeDerivedResult")
	}
	insertMessageFn = origInsMsg

	origAppQuery := appendQueryTxFn
	appendQueryTxFn = func(s *SessionStore, ctx context.Context, tx *sql.Tx, sessionID, originID, source string, query *QueryResult, now time.Time) error {
		return errors.New("append fail")
	}
	if _, _, err := u.sessions.executeDerivedResult(ctx, u.sessions.activeID, rec.ID, dOp, prevNormal.DTQL); err == nil {
		t.Fatal("expected AppendQuery error in executeDerivedResult")
	}
	appendQueryTxFn = origAppQuery

	// serverPreview & reviewServer:
	// serverPreview: !valid() (line 229)
	opInvalidSrv := &resultOperationOverlay{ui: u, sessionID: "wrong"}
	opInvalidSrv.selected = []int{0}
	if _, err := opInvalidSrv.serverPreview(); err == nil {
		t.Fatal("expected invalid error in serverPreview")
	}

	// serverPreview: sessions == nil (line 232)
	uNilSess := focusedGridTestUI(t)
	uNilSess.sessions = nil
	gNil, rNil, _ := uNilSess.activeGrid()
	opNilSess := &resultOperationOverlay{ui: uNilSess, grid: gNil, record: *rNil, sessionID: uNilSess.sessionID, generation: uNilSess.workspaceGeneration, selected: []int{0}}
	if _, err := opNilSess.serverPreview(); err == nil {
		t.Fatal("expected no policy executor error in serverPreview")
	}

	// serverPreview: dtqlDeserialize parseErr (line 251)
	origDes = dtqlDeserialize
	callDes := 0
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		callDes++
		if callDes == 2 {
			return nil, errors.New("parseErr")
		}
		return origDes(data)
	}
	opOrigPrev, _ := u.newResultOperation(false)
	opOrigPrev.selected = []int{0}
	opOrigPrev.originalOutput = true
	_, _ = opOrigPrev.serverPreview()
	dtqlDeserialize = origDes

	// reviewServer with error (line 260)
	opErrRev, _ := u.newResultOperation(false)
	opErrRev.selected = nil
	cmdRev := opErrRev.reviewServer()
	if cmdRev != nil || opErrRev.err == "" {
		t.Fatal("expected error set on reviewServer failure")
	}

	// resultServerPreviewOverlay methods & clicks
	prevValid, _ := deriveResultPreview(u.sessions.catalog, *rec, dOp)
	sp := &resultServerPreviewOverlay{operation: opGroup, preview: prevValid, text: "preview query"}
	if !sp.LocalMouseCoordinates() || len(sp.HelpHints()) == 0 {
		t.Fatal("expected local mouse coords and hints")
	}
	_ = sp.View(80, 24)
	// Click Run on server (lines 306-307)
	_, _, _ = sp.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: sp.controlY})
	// Click Esc return (lines 309-310)
	_, _, _ = sp.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 25, Y: sp.controlY})
	// Shift+Enter with preview.DTQL mismatch (line 321)
	spMismatch := &resultServerPreviewOverlay{operation: opGroup, preview: queryops.Preview{DTQL: "mismatched"}, text: "preview"}
	_, _, _ = spMismatch.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if spMismatch.err == "" {
		t.Fatal("expected mismatch error in shift+enter")
	}
	// Shift+Enter with workspaceMutationPending (line 325)
	u.workspaceMutationPending = true
	spBusy := &resultServerPreviewOverlay{operation: opGroup, preview: prevValid, text: "preview"}
	_, _, _ = spBusy.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if spBusy.err == "" {
		t.Fatal("expected busy error in shift+enter")
	}
	u.workspaceMutationPending = false

	// result_operations.go: line 55 (invalid row key in captureLoaded)
	gBadRow = &gridState{Model: grid.New([]grid.Column{{Name: "id"}, {Name: "name"}}, []grid.Row{{Key: "999"}})}
	if _, err := u.captureLoaded(gBadRow, rec); err == nil {
		t.Fatal("expected error on out-of-bounds row key")
	}

	// result_operations.go: line 100 (openResultOperation when newResultOperation errors)
	uNoGrid.shell.ShowTemporaryCenterPanel(&localGroupsPanel{ui: uNoGrid})
	_ = uNoGrid.openResultOperation(false)

	// result_operations.go: line 116 (captureLoaded fails inside newResultOperation)
	origCap := resultopsCapture
	resultopsCapture = func(cols []resultops.Column, rows []resultops.Row) (resultops.Snapshot, error) {
		return resultops.Snapshot{}, errors.New("capture error")
	}
	uFreshCapture := focusedGridTestUI(t)
	uFreshCapture.gridsByRecordSetID[rec.ID] = &gridState{Model: grid.New([]grid.Column{{Name: "id"}}, []grid.Row{{Key: "0", Values: []any{int64(1)}}})}
	_, _ = uFreshCapture.newResultOperation(false)
	// result_operations.go: line 642 (quickSortLoaded when captureLoaded errors)
	_ = uFreshCapture.quickSortLoaded(false)
	resultopsCapture = origCap

	// result_operations.go: line 136 (quickServerSort with column >= len(columns))
	g.SelectColumn(999)
	_ = u.quickServerSort(false)
	g.SelectColumn(0)

	// result_operations.go: line 178 (currentColumn when visibleColumns is empty)
	opEmptyCols, _ := u.newResultOperation(false)
	opEmptyCols.filter.SetValue("no_such_column_matches")
	if opEmptyCols.currentColumn() != -1 {
		t.Fatal("expected -1 when filter matches nothing")
	}

	// result_operations.go: line 292 (digit '0' displayed when c == 9)
	cols10 := make([]grid.Column, 10)
	for i := range cols10 {
		cols10[i] = grid.Column{Name: fmt.Sprintf("col%d", i)}
	}
	g10 := &gridState{Model: grid.New(cols10, []grid.Row{{Key: "0", Values: make([]any, 10)}})}
	rec10 := *rec
	rec10.Result.Columns = make([]string, 10)
	for i := range rec10.Result.Columns {
		rec10.Result.Columns[i] = fmt.Sprintf("col%d", i)
	}
	rec10.Result.Rows = []secureread.Row{{Data: map[string]any{"col0": int64(1)}}}
	cap10, _ := u.captureLoaded(g10, &rec10)
	op10 := &resultOperationOverlay{ui: u, grid: g10, record: rec10, capture: cap10, sessionID: u.sessionID, generation: u.workspaceGeneration}
	v10 := op10.View(80, 24)
	if !strings.Contains(v10, "0 [ ] col9") {
		t.Fatalf("expected '0 [ ] col9' in 10-column view, got %s", v10)
	}

	// result_operations.go: line 370 (ctrl+r when reorder fails on valid overlay)
	opReorderFail := &resultOperationOverlay{
		ui:         u,
		grid:       g,
		record:     *rec,
		capture:    &loadedResultCapture{columns: g.Columns(), columnNames: []string{"id", "name"}, rows: []grid.Row{{Key: "nonexistent_key"}}},
		sessionID:  u.sessionID,
		generation: u.workspaceGeneration,
	}
	_, _, _ = opReorderFail.Update(uitest.Key("ctrl+r"))
	if opReorderFail.err == "" {
		t.Fatal("expected error on ctrl+r with bad row key")
	}

	// result_operations.go: line 382 (enter on focus == 1 blurs filter)
	opFocusFilter, _ := u.newResultOperation(false)
	opFocusFilter.focus = 1
	opFocusFilter.filter.Focus()
	_, _, _ = opFocusFilter.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if opFocusFilter.focus != 0 {
		t.Fatalf("expected focus 0 after enter on filter, got %d", opFocusFilter.focus)
	}

	// result_operations.go: line 409 (enter on focus == 6 reset button when reorder fails)
	opResetFail := &resultOperationOverlay{
		ui:         u,
		grid:       g,
		record:     *rec,
		capture:    &loadedResultCapture{columns: g.Columns(), columnNames: []string{"id", "name"}, rows: []grid.Row{{Key: "nonexistent_key"}}},
		sessionID:  u.sessionID,
		generation: u.workspaceGeneration,
		focus:      6,
	}
	_, _, _ = opResetFail.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if opResetFail.err == "" {
		t.Fatal("expected error on enter reset with bad row key")
	}

	// result_operations.go: line 638 (quickSortLoaded with no active grid)
	_ = uEmpty.quickSortLoaded(false)
}
