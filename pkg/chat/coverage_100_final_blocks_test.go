package chat

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	godbf "github.com/LindsayBradford/go-dbf"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	"github.com/datatug/datatug-cli/pkg/secureread"
	_ "github.com/mattn/go-sqlite3"
	"github.com/xuri/excelize/v2"
)

type countingLimitWriter struct {
	limit int
	count int
}

func (c *countingLimitWriter) Write(p []byte) (int, error) {
	if c.count >= c.limit {
		return 0, errors.New("write limit exceeded")
	}
	c.count++
	return len(p), nil
}

type fakeResultRowsAffectedErr struct{}

func (f fakeResultRowsAffectedErr) LastInsertId() (int64, error) { return 0, nil }
func (f fakeResultRowsAffectedErr) RowsAffected() (int64, error) {
	return 0, errors.New("rows affected error")
}

type fakeResultRowsAffectedCount struct {
	count int64
}

func (f fakeResultRowsAffectedCount) LastInsertId() (int64, error) { return 0, nil }
func (f fakeResultRowsAffectedCount) RowsAffected() (int64, error) {
	return f.count, nil
}

type mockBookmarkSQL struct {
	bookmarkSQL
	queryContext func(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (m mockBookmarkSQL) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if m.queryContext != nil {
		return m.queryContext(ctx, query, args...)
	}
	return m.bookmarkSQL.QueryContext(ctx, query, args...)
}

func TestFinalBlocks_Export(t *testing.T) {
	ctx := context.Background()
	rs := RecordSet{
		Title: "TestRS",
		Result: secureread.Result{
			Columns: []string{"id", "val"},
			Rows: []secureread.Row{
				{Data: map[string]any{"id": 1, "val": "foo"}},
			},
		},
	}

	// 1. ExportRecordSets writer.Create failure on 2nd record
	origZipCreate := zipWriterCreate
	defer func() { zipWriterCreate = origZipCreate }()
	zipWriterCreate = func(w *zip.Writer, name string) (io.Writer, error) {
		return nil, errors.New("zip create error")
	}
	if err := ExportRecordSets(ctx, []RecordSet{rs, rs}, ExportCSV, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error on writer.Create failure")
	}
	zipWriterCreate = origZipCreate

	// 2. ExportRecordSets exportFlat failure inside loop
	origAddField := godbfAddField
	defer func() { godbfAddField = origAddField }()
	godbfAddField = func(t *godbf.DbfTable, kind, name string, scale uint8) error {
		return errors.New("add field fail")
	}
	if err := ExportRecordSets(ctx, []RecordSet{rs, rs}, ExportDBF, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error on exportFlat fail in ExportRecordSets")
	}
	godbfAddField = origAddField

	// 3. exportFlat CSV write columns error (> 4096 bytes buffer fill)
	rsLongCol := RecordSet{
		Result: secureread.Result{
			Columns: []string{strings.Repeat("C", 5000)},
		},
	}
	if err := exportFlat(rsLongCol, ExportCSV, &failWriter{failOnWrite: true}); err == nil {
		t.Fatal("expected error on CSV column write")
	}

	// 4. exportFlat CSV write rows error (> 4096 bytes buffer fill)
	rsLongRow := RecordSet{
		Result: secureread.Result{
			Columns: []string{"col"},
			Rows: []secureread.Row{
				{Data: map[string]any{"col": strings.Repeat("R", 5000)}},
			},
		},
	}
	cwRow := &countingLimitWriter{limit: 0}
	if err := exportFlat(rsLongRow, ExportCSV, cwRow); err == nil {
		t.Fatal("expected error on CSV row write")
	}

	// 5. exportFlat INGR records write error and $ID
	rsIngr := RecordSet{
		Result: secureread.Result{
			Columns: []string{"$ID", "name"},
			Rows: []secureread.Row{
				{Data: map[string]any{"$ID": 100, "name": "val"}},
			},
		},
	}
	cwIngr := &countingLimitWriter{limit: 1}
	if err := exportFlat(rsIngr, ExportINGR, cwIngr); err == nil {
		t.Fatal("expected error on INGR record write")
	}

	// 6. excelize errors in exportXLSX
	origSetSheetName := excelizeSetSheetName
	defer func() { excelizeSetSheetName = origSetSheetName }()
	excelizeSetSheetName = func(f *excelize.File, old, new string) error {
		return errors.New("sheet name error")
	}
	if err := exportXLSX([]RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from excelizeSetSheetName")
	}
	excelizeSetSheetName = origSetSheetName

	origNewSheet := excelizeNewSheet
	defer func() { excelizeNewSheet = origNewSheet }()
	excelizeNewSheet = func(f *excelize.File, name string) (int, error) {
		return 0, errors.New("new sheet error")
	}
	if err := exportXLSX([]RecordSet{rs, rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from excelizeNewSheet")
	}
	excelizeNewSheet = origNewSheet

	origSetCellValue := excelizeSetCellValue
	defer func() { excelizeSetCellValue = origSetCellValue }()
	// Fail on column header
	excelizeSetCellValue = func(f *excelize.File, sheet, cell string, value any) error {
		return errors.New("cell value header error")
	}
	if err := exportXLSX([]RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from excelizeSetCellValue on header")
	}
	// Fail on row
	cellCalls := 0
	excelizeSetCellValue = func(f *excelize.File, sheet, cell string, value any) error {
		cellCalls++
		if cellCalls > len(rs.Result.Columns) {
			return errors.New("cell value row error")
		}
		return nil
	}
	if err := exportXLSX([]RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from excelizeSetCellValue on row")
	}
	excelizeSetCellValue = origSetCellValue

	// 7. sqliteExecContext, sqlitePrepareContext, sqliteStmtExecContext, sqliteDBClose
	origSqliteExec := sqliteExecContext
	defer func() { sqliteExecContext = origSqliteExec }()
	sqliteExecContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		return nil, errors.New("sqlite exec fail")
	}
	if err := exportSQLite(ctx, []RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from sqliteExecContext")
	}
	sqliteExecContext = origSqliteExec

	origSqlitePrepare := sqlitePrepareContext
	defer func() { sqlitePrepareContext = origSqlitePrepare }()
	sqlitePrepareContext = func(db *sql.DB, ctx context.Context, query string) (*sql.Stmt, error) {
		return nil, errors.New("sqlite prepare fail")
	}
	if err := exportSQLite(ctx, []RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from sqlitePrepareContext")
	}
	sqlitePrepareContext = origSqlitePrepare

	origSqliteStmtExec := sqliteStmtExecContext
	defer func() { sqliteStmtExecContext = origSqliteStmtExec }()
	sqliteStmtExecContext = func(stmt *sql.Stmt, ctx context.Context, args ...any) (sql.Result, error) {
		return nil, errors.New("sqlite stmt exec fail")
	}
	if err := exportSQLite(ctx, []RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from sqliteStmtExecContext")
	}
	sqliteStmtExecContext = origSqliteStmtExec

	origSqliteDBClose := sqliteDBClose
	defer func() { sqliteDBClose = origSqliteDBClose }()
	sqliteDBClose = func(db *sql.DB) error {
		_ = db.Close()
		return errors.New("sqlite close fail")
	}
	if err := exportSQLite(ctx, []RecordSet{rs}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from sqliteDBClose")
	}
	sqliteDBClose = origSqliteDBClose

	// 8. godbfAddNewRecord, godbfSetFieldValueByName
	origGodbfAddNewRecord := godbfAddNewRecord
	defer func() { godbfAddNewRecord = origGodbfAddNewRecord }()
	godbfAddNewRecord = func(t *godbf.DbfTable) (int, error) {
		return 0, errors.New("godbf add record fail")
	}
	if err := exportDBF(rs, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from godbfAddNewRecord")
	}
	godbfAddNewRecord = origGodbfAddNewRecord

	origGodbfSetFieldValueByName := godbfSetFieldValueByName
	defer func() { godbfSetFieldValueByName = origGodbfSetFieldValueByName }()
	godbfSetFieldValueByName = func(t *godbf.DbfTable, recordIndex int, fieldName, value string) error {
		return errors.New("godbf set field fail")
	}
	if err := exportDBF(rs, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error from godbfSetFieldValueByName")
	}
	godbfSetFieldValueByName = origGodbfSetFieldValueByName

	// 9. fileClose error in exportFile
	origFileClose := fileClose
	defer func() { fileClose = origFileClose }()
	fileClose = func(f *os.File) error {
		_ = f.Close()
		return errors.New("file close error")
	}
	tmpFile := filepath.Join(t.TempDir(), "out.csv")
	if err := exportFile(ctx, []RecordSet{rs}, ExportCSV, tmpFile, false); err == nil {
		t.Fatal("expected error from fileClose in exportFile")
	}
	fileClose = origFileClose
}

func TestFinalBlocks_Bookmark(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sess, err := store.Create(ctx, "Bookmark Session")
	if err != nil {
		t.Fatal(err)
	}
	uTurn, err := store.AppendUser(ctx, sess.ID, "query")
	if err != nil {
		t.Fatal(err)
	}
	qResult := QueryResult{
		Title:    "Test Query",
		DTQL:     "from: {name: Invoice}\n",
		SourceID: "chinook",
		Result:   secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": 1}}}},
	}
	appended, err := store.AppendQuery(ctx, sess.ID, uTurn.ID, "chinook", qResult)
	if err != nil {
		t.Fatal(err)
	}
	ref := ContextReference{Kind: "recordset", ObjectID: appended.RecordSetID, Title: "My RS"}

	// 1. CreateBookmark: bookmarkBeginTxFn fail
	origBeginTx := bookmarkBeginTxFn
	defer func() { bookmarkBeginTxFn = origBeginTx }()
	bookmarkBeginTxFn = func(db *sql.DB, ctx context.Context) (*sql.Tx, error) {
		return nil, errors.New("begin tx fail")
	}
	if _, err := store.CreateBookmark(ctx, sess.ID, ref, "title"); err == nil {
		t.Fatal("expected error when bookmarkBeginTxFn fails")
	}
	bookmarkBeginTxFn = origBeginTx

	// 2. CreateBookmark: encodeBookmarkSnapshotFn fail
	origEncode := encodeBookmarkSnapshotFn
	defer func() { encodeBookmarkSnapshotFn = origEncode }()
	encodeBookmarkSnapshotFn = func(snapshot BookmarkSnapshot) ([]byte, error) {
		return nil, errors.New("encode snapshot fail")
	}
	if _, err := store.CreateBookmark(ctx, sess.ID, ref, "title"); err == nil {
		t.Fatal("expected error when encodeBookmarkSnapshotFn fails")
	}
	encodeBookmarkSnapshotFn = origEncode

	// 3. CreateBookmark: txExecContextFn fail
	origTxExec := txExecContextFn
	defer func() { txExecContextFn = origTxExec }()
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO bookmarks") {
			return nil, errors.New("insert bookmark fail")
		}
		return origTxExec(tx, ctx, query, args...)
	}
	if _, err := store.CreateBookmark(ctx, sess.ID, ref, "title"); err == nil {
		t.Fatal("expected error when INSERT INTO bookmarks fails")
	}
	txExecContextFn = origTxExec

	// 4. CreateBookmark: txCommitFn fail
	origTxCommit := txCommitFn
	defer func() { txCommitFn = origTxCommit }()
	txCommitFn = func(tx *sql.Tx) error {
		return errors.New("tx commit fail")
	}
	if _, err := store.CreateBookmark(ctx, sess.ID, ref, "title"); err == nil {
		t.Fatal("expected error when txCommitFn fails")
	}
	txCommitFn = origTxCommit

	// Line 90: s.bookmarkSourceID error in CreateBookmark
	origTarget := recordSetsForSessionFn
	defer func() { recordSetsForSessionFn = origTarget }()
	recordSetsForSessionFn = func(ctx context.Context, q bookmarkSQL, sessionID string) (map[string]RecordSet, error) {
		return map[string]RecordSet{
			appended.RecordSetID: {ID: appended.RecordSetID, Database: "unknown_db"},
		}, nil
	}
	if _, err := store.CreateBookmark(ctx, sess.ID, ref, "title"); err == nil {
		t.Fatal("expected error on unknown source in CreateBookmark")
	}
	recordSetsForSessionFn = origTarget

	// Create a real bookmark for following tests
	bm, err := store.CreateBookmark(ctx, sess.ID, ref, "My Valid Bookmark")
	if err != nil {
		t.Fatal(err)
	}

	// 5. RenameBookmark: ExecContext fail with canceled context
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.RenameBookmark(canceledCtx, bm.ID, "new title"); err == nil {
		t.Fatal("expected error from RenameBookmark on canceled context")
	}

	// 6. changeBookmarkTags: bookmarkBeginTxFn fail
	bookmarkBeginTxFn = func(db *sql.DB, ctx context.Context) (*sql.Tx, error) {
		return nil, errors.New("begin tx fail")
	}
	if _, err := store.AddBookmarkTag(ctx, bm.ID, "tag1"); err == nil {
		t.Fatal("expected error from bookmarkBeginTxFn in AddBookmarkTag")
	}
	bookmarkBeginTxFn = origBeginTx

	// 7. changeBookmarkTags: s.bookmark fail
	if _, err := store.AddBookmarkTag(ctx, "nonexistent", "tag1"); err == nil {
		t.Fatal("expected error for nonexistent bookmark in AddBookmarkTag")
	}

	// 8. changeBookmarkTags: mutate fail (empty tag)
	if _, err := store.AddBookmarkTag(ctx, bm.ID, ""); err == nil {
		t.Fatal("expected error for empty tag in AddBookmarkTag")
	}

	// 9. changeBookmarkTags: normalizedTags fail
	if _, err := store.changeBookmarkTags(ctx, bm.ID, func(tags []string) ([]string, error) {
		return []string{"\x00invalid"}, nil
	}); err == nil {
		t.Fatal("expected error for control character tag")
	}

	// 10. changeBookmarkTags: txExecContextFn fail
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE bookmarks SET tags_json") {
			return nil, errors.New("update tags fail")
		}
		return origTxExec(tx, ctx, query, args...)
	}
	if _, err := store.AddBookmarkTag(ctx, bm.ID, "newtag"); err == nil {
		t.Fatal("expected error when UPDATE tags fails")
	}
	txExecContextFn = origTxExec

	// 11. changeBookmarkTags: txCommitFn fail
	txCommitFn = func(tx *sql.Tx) error {
		return errors.New("commit fail")
	}
	if _, err := store.AddBookmarkTag(ctx, bm.ID, "newtag"); err == nil {
		t.Fatal("expected error when commit fails in changeBookmarkTags")
	}
	txCommitFn = origTxCommit

	// 12. DeleteBookmark: bookmarkBeginTxFn fail
	bookmarkBeginTxFn = func(db *sql.DB, ctx context.Context) (*sql.Tx, error) {
		return nil, errors.New("begin tx fail")
	}
	if err := store.DeleteBookmark(ctx, bm.ID); err == nil {
		t.Fatal("expected error from bookmarkBeginTxFn in DeleteBookmark")
	}
	bookmarkBeginTxFn = origBeginTx

	// 13. DeleteBookmark: s.bookmark fail
	if err := store.DeleteBookmark(ctx, "nonexistent"); err == nil {
		t.Fatal("expected error for nonexistent bookmark in DeleteBookmark")
	}

	// 14. DeleteBookmark: ensureBookmarkUnreferencedFn fail
	origEnsure := ensureBookmarkUnreferencedFn
	defer func() { ensureBookmarkUnreferencedFn = origEnsure }()
	ensureBookmarkUnreferencedFn = func(s *SessionStore, ctx context.Context, q bookmarkSQL, id string) error {
		return errors.New("ensure unreferenced fail")
	}
	if err := store.DeleteBookmark(ctx, bm.ID); err == nil {
		t.Fatal("expected error from ensureBookmarkUnreferencedFn in DeleteBookmark")
	}
	ensureBookmarkUnreferencedFn = origEnsure

	// 15. DeleteBookmark: txExecContextFn fail
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM bookmarks") {
			return nil, errors.New("delete bookmarks fail")
		}
		return origTxExec(tx, ctx, query, args...)
	}
	if err := store.DeleteBookmark(ctx, bm.ID); err == nil {
		t.Fatal("expected error when DELETE FROM bookmarks fails")
	}
	txExecContextFn = origTxExec

	// 16. DeleteBookmark: RowsAffected fail
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM bookmarks") {
			return fakeResultRowsAffectedErr{}, nil
		}
		return origTxExec(tx, ctx, query, args...)
	}
	if err := store.DeleteBookmark(ctx, bm.ID); err == nil {
		t.Fatal("expected error when RowsAffected fails in DeleteBookmark")
	}
	txExecContextFn = origTxExec

	// 17. DeleteBookmark: count != 1
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM bookmarks") {
			return fakeResultRowsAffectedCount{count: 0}, nil
		}
		return origTxExec(tx, ctx, query, args...)
	}
	if err := store.DeleteBookmark(ctx, bm.ID); err == nil {
		t.Fatal("expected error when count != 1 in DeleteBookmark")
	}
	txExecContextFn = origTxExec

	// 18. scanBookmark corrupt fields
	_, _ = store.db.ExecContext(ctx, "INSERT INTO bookmarks (id, project_id, scope, title, tags_json, target_kind, created_at, updated_at, snapshot_json) VALUES ('corrupt1', ?, ?, 't', 'invalid json', 'recordset', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z', '{}')", store.info.ProjectID, store.scope)
	if _, err := store.bookmark(ctx, store.db, "corrupt1"); err == nil {
		t.Fatal("expected error on corrupt tags JSON")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM bookmarks WHERE id = 'corrupt1'")

	_, _ = store.db.ExecContext(ctx, "INSERT INTO bookmarks (id, project_id, scope, title, tags_json, target_kind, created_at, updated_at, snapshot_json) VALUES ('corrupt2', ?, ?, 't', '[\"tag\", \"TAG\"]', 'recordset', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z', '{}')", store.info.ProjectID, store.scope)
	if _, err := store.bookmark(ctx, store.db, "corrupt2"); err == nil {
		t.Fatal("expected error on duplicate un-normalized tags")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM bookmarks WHERE id = 'corrupt2'")

	_, _ = store.db.ExecContext(ctx, "INSERT INTO bookmarks (id, project_id, scope, title, tags_json, target_kind, created_at, updated_at, snapshot_json) VALUES ('corrupt3', ?, ?, 't', '[]', 'recordset', 'bad-date', '2025-01-01T00:00:00Z', '{}')", store.info.ProjectID, store.scope)
	if _, err := store.bookmark(ctx, store.db, "corrupt3"); err == nil {
		t.Fatal("expected error on bad created timestamp")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM bookmarks WHERE id = 'corrupt3'")

	_, _ = store.db.ExecContext(ctx, "INSERT INTO bookmarks (id, project_id, scope, title, tags_json, target_kind, created_at, updated_at, snapshot_json) VALUES ('corrupt4', ?, ?, 't', '[]', 'recordset', '2025-01-01T00:00:00Z', 'bad-date', '{}')", store.info.ProjectID, store.scope)
	if _, err := store.bookmark(ctx, store.db, "corrupt4"); err == nil {
		t.Fatal("expected error on bad updated timestamp")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM bookmarks WHERE id = 'corrupt4'")

	// 19. bookmarkTarget failures: view recordset not found, selection view not found, selection recordset not found
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	// View with non-existent RecordSet
	origWS := workspaceForSessionFn
	defer func() { workspaceForSessionFn = origWS }()
	workspaceForSessionFn = func(ctx context.Context, q bookmarkSQL, sessionID string, records map[string]RecordSet) (WorkspaceState, error) {
		return WorkspaceState{
			Views: map[string]RecordSetView{
				"v1": {ID: "v1", RecordSetID: "missing_rs"},
			},
			Selections: map[string]Selection{
				"s1": {ID: "s1", ViewID: "missing_view"},
				"s2": {ID: "s2", ViewID: "v1"},
			},
		}, nil
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ContextReference{Kind: "view", ObjectID: "v1"}); err == nil {
		t.Fatal("expected error when view RecordSet not found")
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ContextReference{Kind: "selection", ObjectID: "s1"}); err == nil {
		t.Fatal("expected error when selection View not found")
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ContextReference{Kind: "selection", ObjectID: "s2"}); err == nil {
		t.Fatal("expected error when selection View RecordSet not found")
	}

	// recordSetsForSessionFn error
	origRS := recordSetsForSessionFn
	defer func() { recordSetsForSessionFn = origRS }()
	recordSetsForSessionFn = func(ctx context.Context, q bookmarkSQL, sessionID string) (map[string]RecordSet, error) {
		return nil, errors.New("rs session fail")
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ref); err == nil {
		t.Fatal("expected error from recordSetsForSessionFn")
	}
	recordSetsForSessionFn = origRS

	// workspaceForSessionFn error
	workspaceForSessionFn = func(ctx context.Context, q bookmarkSQL, sessionID string, records map[string]RecordSet) (WorkspaceState, error) {
		return WorkspaceState{}, errors.New("ws session fail")
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ref); err == nil {
		t.Fatal("expected error from workspaceForSessionFn")
	}
	workspaceForSessionFn = origWS

	// validateBookmarkSnapshotFn error
	origValidateSnap := validateBookmarkSnapshotFn
	defer func() { validateBookmarkSnapshotFn = origValidateSnap }()
	validateBookmarkSnapshotFn = func(targetKind string, snapshot BookmarkSnapshot) error {
		return errors.New("validate snapshot fail")
	}
	if _, _, _, err := store.bookmarkTarget(ctx, tx, sess.ID, ref); err == nil {
		t.Fatal("expected error from validateBookmarkSnapshotFn")
	}
	validateBookmarkSnapshotFn = origValidateSnap
	_ = tx.Rollback()

	// 20. recordSetsForSession errors
	// Corrupt parameters JSON in recordsets table
	_, _ = store.db.ExecContext(ctx, "INSERT INTO recordsets (session_id, id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json) VALUES (?, 'r_corrupt1', ?, '', 't', '', '', '', '', 'bad json', '2025-01-01T00:00:00Z', '[]')", sess.ID, appended.QueryID)
	if _, err := recordSetsForSession(ctx, store.db, sess.ID); err == nil {
		t.Fatal("expected error on corrupt parameters JSON")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM recordsets WHERE id = 'r_corrupt1'")

	// Corrupt created_at timestamp
	_, _ = store.db.ExecContext(ctx, "INSERT INTO recordsets (session_id, id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json) VALUES (?, 'r_corrupt2', ?, '', 't', '', '', '', '', '{}', 'bad-date', '[]')", sess.ID, appended.QueryID)
	if _, err := recordSetsForSession(ctx, store.db, sess.ID); err == nil {
		t.Fatal("expected error on corrupt created_at timestamp")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM recordsets WHERE id = 'r_corrupt2'")

	// Corrupt result payload
	_, _ = store.db.ExecContext(ctx, "INSERT INTO recordsets (session_id, id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json) VALUES (?, 'r_corrupt3', ?, '', 't', '', '', '', '', '{}', '2025-01-01T00:00:00Z', 'bad-result')", sess.ID, appended.QueryID)
	if _, err := recordSetsForSession(ctx, store.db, sess.ID); err == nil {
		t.Fatal("expected error on corrupt result payload")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM recordsets WHERE id = 'r_corrupt3'")

	// 21. workspaceForSession errors
	_, _ = store.db.ExecContext(ctx, "INSERT INTO session_workspace (session_id, state_json) VALUES (?, 'bad json')", sess.ID)
	if _, err := workspaceForSession(ctx, store.db, sess.ID, nil); err == nil {
		t.Fatal("expected error on corrupt workspace JSON")
	}
	// Invalid loaded workspace
	_, _ = store.db.ExecContext(ctx, "UPDATE session_workspace SET state_json = '{\"views\":{\"v\":{\"id\":\"v\",\"recordSetId\":\"missing\"}}}' WHERE session_id = ?", sess.ID)
	if _, err := workspaceForSession(ctx, store.db, sess.ID, nil); err == nil {
		t.Fatal("expected error from validateLoadedWorkspace")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM session_workspace WHERE session_id = ?", sess.ID)

	// 22. decodeBookmarkSnapshot validateBookmarkSnapshotFn fail
	validateBookmarkSnapshotFn = func(targetKind string, snapshot BookmarkSnapshot) error {
		return errors.New("validate snapshot fail")
	}
	validRes, _ := encodeResult(secureread.Result{Columns: []string{"id"}})
	validSnapPayload, _ := json.Marshal(storedBookmarkSnapshot{RecordSet: storedBookmarkRecordSet{ID: "r1", Result: validRes}})
	if _, err := decodeBookmarkSnapshot("recordset", validSnapPayload); err == nil {
		t.Fatal("expected error from decodeBookmarkSnapshot on validate fail")
	}
	validateBookmarkSnapshotFn = origValidateSnap

	// 23. ensureBookmarkUnreferenced errors
	_, _ = store.db.ExecContext(ctx, "INSERT INTO session_workspace (session_id, state_json) VALUES (?, 'bad json')", sess.ID)
	if err := store.ensureBookmarkUnreferenced(ctx, store.db, "b1"); err == nil {
		t.Fatal("expected error on corrupt workspace in ensureBookmarkUnreferenced")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM session_workspace WHERE session_id = ?", sess.ID)

	// ensureBookmarkUnreferenced canceled context (line 497)
	if err := store.ensureBookmarkUnreferenced(canceledCtx, store.db, "b1"); err == nil {
		t.Fatal("expected error on canceled context in ensureBookmarkUnreferenced")
	}

	// ensureBookmarkUnreferenced scan error on NULL state_json (line 503)
	mockWS := mockBookmarkSQL{
		bookmarkSQL: store.db,
		queryContext: func(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
			return store.db.QueryContext(ctx, "SELECT NULL")
		},
	}
	if err := store.ensureBookmarkUnreferenced(ctx, mockWS, "b1"); err == nil {
		t.Fatal("expected scan error on NULL state_json in ensureBookmarkUnreferenced")
	}

	// 24. validateBookmarkReferences with mismatched source
	wsDiffSource := WorkspaceState{
		Attachments: []ContextReference{
			{Kind: "bookmark", ObjectID: bm.ID, SourceID: "completely_different_source"},
		},
	}
	if err := store.validateBookmarkReferences(ctx, store.db, wsDiffSource); err == nil {
		t.Fatal("expected error when bookmark context source does not match")
	}

	// 25. normalizeBookmarkTitle fallback candidate
	if title := normalizeBookmarkTitle("", "", ""); title != "Bookmarked result" {
		t.Fatalf("expected Bookmarked result, got %s", title)
	}

	// 26. loadBookmarks with item.Bookmarks == nil
	item := &ChatSession{Bookmarks: nil}
	if err := store.loadBookmarks(ctx, item); err != nil {
		t.Fatal(err)
	}
	if item.Bookmarks == nil {
		t.Fatal("expected item.Bookmarks to be initialized")
	}

	// 27. recordSetsForSession and workspaceForSession on canceled context & scan error
	if _, err := recordSetsForSession(canceledCtx, store.db, sess.ID); err == nil {
		t.Fatal("expected error from recordSetsForSession on canceled context")
	}
	mockRS := mockBookmarkSQL{
		bookmarkSQL: store.db,
		queryContext: func(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
			return store.db.QueryContext(ctx, "SELECT '1', '2', '3', '4', '5', '6', '7', '8', NULL, '2025-01-01T00:00:00Z', '[]'")
		},
	}
	if _, err := recordSetsForSession(ctx, mockRS, sess.ID); err == nil {
		t.Fatal("expected scan error on null parameters")
	}

	if _, err := workspaceForSession(canceledCtx, store.db, sess.ID, nil); err == nil {
		t.Fatal("expected error from workspaceForSession on canceled context")
	}

	// 28. Valid view bookmark creation (lines 358-359)
	_, _ = store.db.ExecContext(ctx, "INSERT INTO session_workspace (session_id, state_json) VALUES (?, ?)", sess.ID, `{"views":{"view1":{"id":"view1","recordSetId":"`+appended.RecordSetID+`","title":"View 1"}}}`)
	if bmView, err := store.CreateBookmark(ctx, sess.ID, ContextReference{Kind: "view", ObjectID: "view1"}, "View Bookmark"); err != nil {
		t.Fatalf("expected view bookmark creation to succeed, got %v", err)
	} else if bmView.TargetKind != "view" {
		t.Fatalf("expected view target kind, got %s", bmView.TargetKind)
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM session_workspace WHERE session_id = ?", sess.ID)

	// 29. ListBookmarks unknown source error (line 122) and loadBookmarks error (line 132)
	snapUnknown, _ := encodeBookmarkSnapshotFn(BookmarkSnapshot{SourceID: "unknown_source", RecordSet: RecordSet{ID: "r_unk", Database: "unknown_source"}})
	_, _ = store.db.ExecContext(ctx, "INSERT INTO bookmarks (id, project_id, scope, title, tags_json, target_kind, created_at, updated_at, snapshot_json) VALUES ('bm_unk_src', ?, ?, 't', '[]', 'recordset', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z', ?)", store.info.ProjectID, store.scope, snapUnknown)
	if _, err := store.ListBookmarks(ctx); err == nil {
		t.Fatal("expected error from ListBookmarks on unknown source")
	}
	if err := store.loadBookmarks(ctx, &ChatSession{}); err == nil {
		t.Fatal("expected error from loadBookmarks when ListBookmarks fails")
	}
	_, _ = store.db.ExecContext(ctx, "DELETE FROM bookmarks WHERE id = 'bm_unk_src'")
}

func TestFinalBlocks_Join(t *testing.T) {
	ctx := context.Background()

	// 1. Candidates: Refresh error
	appRefreshErr := ForeignKeyJoinApplication{
		Source: "s1",
		Refresh: func(ctx context.Context) (ForeignKeySnapshot, error) {
			return ForeignKeySnapshot{}, errors.New("refresh fail")
		},
	}
	if _, err := appRefreshErr.Candidates(ctx, RecordSet{Source: "s1"}); err == nil {
		t.Fatal("expected error from Refresh in Candidates")
	}

	// 2. Candidates: invalid DTQL
	appValid := ForeignKeyJoinApplication{
		Source:   "s1",
		Snapshot: ForeignKeySnapshot{Source: "s1"},
	}
	if _, err := appValid.Candidates(ctx, RecordSet{Source: "s1", DTQL: "invalid-yaml: ["}); err == nil {
		t.Fatal("expected error from invalid DTQL in Candidates")
	}

	// 3. Candidates: DiscoverJoinCandidates error (unsupported condition or bad query)
	origDeserialize := dtqlDeserialize
	defer func() { dtqlDeserialize = origDeserialize }()
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return nil, errors.New("deserialize fail")
	}
	if _, err := appValid.Candidates(ctx, RecordSet{Source: "s1", DTQL: "from: {name: t}"}); err == nil {
		t.Fatal("expected error when DiscoverJoinCandidates fails")
	}
	dtqlDeserialize = origDeserialize

	// 4. Candidates: Secure && CanReadTarget == nil
	appSecureNoTarget := ForeignKeyJoinApplication{
		Source:        "s1",
		Snapshot:      ForeignKeySnapshot{Source: "s1"},
		Secure:        true,
		CanReadTarget: nil,
	}
	cands, err := appSecureNoTarget.Candidates(ctx, RecordSet{Source: "s1", DTQL: "from: {name: t}"})
	if err != nil || len(cands) != 0 {
		t.Fatalf("expected empty candidates for secure without CanReadTarget, got %v, %v", cands, err)
	}

	// 5. Candidates: CanReadTarget filtering (allowed vs denied)
	appFilter := ForeignKeyJoinApplication{
		Source: "s1",
		Snapshot: ForeignKeySnapshot{
			Source: "s1",
			Keys: []ForeignKey{
				{ConstraintID: "fk1", Schema: "main", FromRelation: "orders", FromFields: []string{"user_id"}, ToSchema: "main", ToRelation: "users", ToFields: []string{"id"}},
			},
			Columns: map[string][]string{
				relationKey("main", "orders"): {"id", "user_id"},
				relationKey("main", "users"):  {"id", "name"},
			},
		},
		CanReadTarget: func(ctx context.Context, target RelationInstance) error {
			if target.Relation == "users" {
				return errors.New("access denied to users")
			}
			return nil
		},
	}
	candsFilter, err := appFilter.Candidates(ctx, RecordSet{Source: "s1", DTQL: "from: {name: orders}\n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candsFilter) != 0 {
		t.Fatalf("expected denied candidate to be filtered out, got %d", len(candsFilter))
	}

	// 6. canExpandQuery returns true when all pass
	appExpandTrue := ForeignKeyJoinApplication{
		CanReadTarget: func(ctx context.Context, target RelationInstance) error {
			return nil
		},
	}
	qFrom, _ := dtql.Deserialize([]byte("from: {name: orders}\n"))
	if !appExpandTrue.canExpandQuery(ctx, qFrom.From()) {
		t.Fatal("expected canExpandQuery to return true when CanReadTarget allows all")
	}

	// 7. Apply: error branches
	rsOrders := RecordSet{
		ID:     "rs1",
		Source: "s1",
		DTQL:   "from: {name: orders}\n",
	}
	fkSnap := ForeignKeySnapshot{
		Source: "s1",
		Keys: []ForeignKey{
			{ConstraintID: "fk1", Schema: "main", FromRelation: "orders", FromFields: []string{"user_id"}, ToSchema: "main", ToRelation: "users", ToFields: []string{"id"}},
		},
		Columns: map[string][]string{
			relationKey("main", "orders"): {"id", "user_id"},
			"orders":                      {"id", "user_id"},
			relationKey("main", "users"):  {"id", "name"},
			"users":                       {"id", "name"},
		},
	}

	// 5b. Candidates allowed (line 113-116)
	appAllowed := ForeignKeyJoinApplication{
		Source:   "s1",
		Snapshot: fkSnap,
		CanReadTarget: func(ctx context.Context, target RelationInstance) error {
			return nil
		},
	}
	candsAllowed, err := appAllowed.Candidates(ctx, RecordSet{Source: "s1", DTQL: "from: {name: orders}\n"})
	if err != nil || len(candsAllowed) == 0 {
		t.Fatalf("expected allowed candidate, got %v", err)
	}

	// Apply snapshot error
	appApplySnapErr := ForeignKeyJoinApplication{
		Source: "s1",
		Refresh: func(ctx context.Context) (ForeignKeySnapshot, error) {
			return ForeignKeySnapshot{}, errors.New("refresh err")
		},
		Executor: &fakeExecutor{},
	}
	if _, err := appApplySnapErr.Apply(ctx, rsOrders, "cand1"); err == nil {
		t.Fatal("expected error on snapshot failure in Apply")
	}

	// Apply DTQL deserialize error
	appApplyValid := ForeignKeyJoinApplication{
		Source:   "s1",
		Snapshot: fkSnap,
		Executor: &fakeExecutor{},
	}
	if _, err := appApplyValid.Apply(ctx, RecordSet{Source: "s1", DTQL: "bad-yaml: ["}, "cand1"); err == nil {
		t.Fatal("expected error on deserialize failure in Apply")
	}

	// Apply deriveJoinDTQL error (candidate not found)
	if _, err := appApplyValid.Apply(ctx, rsOrders, "nonexistent-candidate"); err == nil {
		t.Fatal("expected error when candidate not found in Apply")
	}

	// Apply Secure && CanReadTarget == nil
	appApplySecureNoTarget := ForeignKeyJoinApplication{
		Source:        "s1",
		Snapshot:      fkSnap,
		Executor:      &fakeExecutor{},
		Secure:        true,
		CanReadTarget: nil,
	}
	// Get valid candidate ID
	candsValid, _ := DiscoverJoinCandidates([]byte(rsOrders.DTQL), fkSnap)
	if len(candsValid) == 0 {
		t.Fatal("expected valid candidate")
	}
	validCandID := candsValid[0].ID
	if _, err := appApplySecureNoTarget.Apply(ctx, rsOrders, validCandID); err == nil {
		t.Fatal("expected error for secure without CanReadTarget in Apply")
	}

	// Candidates and Apply reaching CanReadTarget == nil lines (119 and 166) when canExpandQuery returns true
	origCanExpand := canExpandQueryFn
	canExpandQueryFn = func(a ForeignKeyJoinApplication, ctx context.Context, from dal.FromSource) bool {
		return true
	}
	if cands, err := appApplySecureNoTarget.Candidates(ctx, rsOrders); err != nil || len(cands) != 0 {
		t.Fatal("expected empty candidates on secure with CanReadTarget == nil (line 119)")
	}
	if _, err := appApplySecureNoTarget.Apply(ctx, rsOrders, validCandID); err == nil {
		t.Fatal("expected error on Apply with CanReadTarget == nil (line 166)")
	}
	canExpandQueryFn = origCanExpand

	// Candidates DiscoverJoinCandidates error (line 116)
	deserCallCount := 0
	origDeser := dtqlDeserialize
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		deserCallCount++
		if deserCallCount == 2 {
			return nil, errors.New("discover candidate fail")
		}
		return origDeser(data)
	}
	appDiscoverErr := ForeignKeyJoinApplication{Source: "s1", Snapshot: fkSnap}
	if _, err := appDiscoverErr.Candidates(ctx, rsOrders); err == nil {
		t.Fatal("expected error from Candidates when DiscoverJoinCandidates fails")
	}
	dtqlDeserialize = origDeser

	// Apply CanReadTarget denied
	appApplyDenied := ForeignKeyJoinApplication{
		Source:   "s1",
		Snapshot: fkSnap,
		Executor: &fakeExecutor{},
		CanReadTarget: func(ctx context.Context, target RelationInstance) error {
			return errors.New("denied")
		},
	}
	if _, err := appApplyDenied.Apply(ctx, rsOrders, validCandID); err == nil {
		t.Fatal("expected error for denied target in Apply")
	}

	// Apply Executor RunDTQL error (line 163)
	appApplyExecErr := ForeignKeyJoinApplication{
		Source:   "s1",
		Snapshot: fkSnap,
		Executor: &fakeExecutor{err: errors.New("run dtql error")},
	}
	if _, err := appApplyExecErr.Apply(ctx, rsOrders, validCandID); err == nil {
		t.Fatal("expected error from Executor.RunDTQL in Apply")
	}

	// Apply success (line 124)
	appApplySuccess := ForeignKeyJoinApplication{
		Source:   "s1",
		Snapshot: fkSnap,
		Executor: &fakeExecutor{result: secureread.Result{Columns: []string{"id"}}},
	}
	if _, err := appApplySuccess.Apply(ctx, rsOrders, validCandID); err != nil {
		t.Fatalf("expected successful Apply, got %v", err)
	}

	// ApplyAttached (Left join)
	appApplyAttached := ForeignKeyJoinApplication{
		Source:   "s1",
		Snapshot: fkSnap,
		Executor: &fakeExecutor{result: secureread.Result{Columns: []string{"id"}}},
	}
	if _, err := appApplyAttached.ApplyAttached(ctx, rsOrders, validCandID); err != nil {
		t.Fatalf("expected successful ApplyAttached, got %v", err)
	}

	// 8. LoadSQLiteForeignKeySnapshot errors using sqliteQueryContext seam
	testDB, err := sqlOpenStore("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = testDB.Close() }()
	if _, err := testDB.Exec("CREATE TABLE orders (id INT, user_id INT)"); err != nil {
		t.Fatal(err)
	}

	origSqliteQuery := sqliteQueryContext
	defer func() { sqliteQueryContext = origSqliteQuery }()

	sqliteQueryContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		return nil, errors.New("schema query fail")
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected error from sqliteQueryContext on schema query")
	}

	// PRAGMA table_info query error
	tableInfoCount := 0
	sqliteQueryContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "PRAGMA table_info") {
			tableInfoCount++
			return nil, errors.New("table_info query fail")
		}
		return origSqliteQuery(db, ctx, query, args...)
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected error from PRAGMA table_info")
	}

	// PRAGMA foreign_key_list query error
	sqliteQueryContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "PRAGMA foreign_key_list") {
			return nil, errors.New("foreign_key_list query fail")
		}
		return origSqliteQuery(db, ctx, query, args...)
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected error from PRAGMA foreign_key_list")
	}

	// Scan error on schema (line 226)
	sqliteQueryContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "sqlite_schema") {
			return origSqliteQuery(db, ctx, "SELECT 1, 2")
		}
		return origSqliteQuery(db, ctx, query, args...)
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected scan error on schema")
	}

	// sqliteRowsErr on schema (line 231)
	sqliteQueryContext = origSqliteQuery
	origRowsErr := sqliteRowsErr
	defer func() { sqliteRowsErr = origRowsErr }()
	sqliteRowsErr = func(rows *sql.Rows) error {
		return errors.New("schema rows err")
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected error from sqliteRowsErr on schema")
	}

	// Scan error on PRAGMA table_info (line 251)
	sqliteRowsErr = origRowsErr
	sqliteQueryContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "PRAGMA table_info") {
			return origSqliteQuery(db, ctx, "SELECT 1")
		}
		return origSqliteQuery(db, ctx, query, args...)
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected scan error on table_info")
	}

	// sqliteRowsErr on columnRows (line 260)
	sqliteQueryContext = origSqliteQuery
	rowsErrCalls := 0
	sqliteRowsErr = func(rows *sql.Rows) error {
		rowsErrCalls++
		if rowsErrCalls == 2 {
			return errors.New("columnRows err")
		}
		return origRowsErr(rows)
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected error from sqliteRowsErr on columnRows")
	}

	// Scan error on PRAGMA foreign_key_list (line 283)
	sqliteRowsErr = origRowsErr
	sqliteQueryContext = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "PRAGMA foreign_key_list") {
			return origSqliteQuery(db, ctx, "SELECT 1")
		}
		return origSqliteQuery(db, ctx, query, args...)
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected scan error on foreign_key_list")
	}

	// sqliteRowsErr on fkRows (line 297)
	sqliteQueryContext = origSqliteQuery
	rowsErrCalls = 0
	sqliteRowsErr = func(rows *sql.Rows) error {
		rowsErrCalls++
		if rowsErrCalls == 3 {
			return errors.New("fkRows err")
		}
		return origRowsErr(rows)
	}
	if _, err := LoadSQLiteForeignKeySnapshot(ctx, "s1", testDB); err == nil {
		t.Fatal("expected error from sqliteRowsErr on fkRows")
	}
	sqliteRowsErr = origRowsErr
	sqliteQueryContext = origSqliteQuery

	// 9. DiscoverJoinCandidates: fk with len == 0 or len mismatch or empty constraint
	fkInvalid := ForeignKeySnapshot{
		Source: "s1",
		Keys: []ForeignKey{
			{ConstraintID: "", FromFields: []string{"a"}, ToFields: []string{"b"}},
			{ConstraintID: "fk2", FromFields: []string{}, ToFields: []string{}},
			{ConstraintID: "fk3", FromFields: []string{"a"}, ToFields: []string{"b", "c"}},
		},
	}
	candsEmpty, err := DiscoverJoinCandidates([]byte("from: {name: orders}\n"), fkInvalid)
	if err != nil || len(candsEmpty) != 0 {
		t.Fatalf("expected 0 candidates for invalid FKs, got %v, %v", candsEmpty, err)
	}

	// 10. deriveJoinDTQL:
	// DiscoverJoinCandidates error
	if _, _, err := deriveJoinDTQL([]byte("bad-yaml: ["), fkSnap, "cand1", dal.JoinInner); err == nil {
		t.Fatal("expected error on invalid DTQL in deriveJoinDTQL")
	}
	// dtqlDeserialize(parent) error
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		return nil, errors.New("deserialize fail")
	}
	if _, _, err := deriveJoinDTQL([]byte(rsOrders.DTQL), fkSnap, validCandID, dal.JoinInner); err == nil {
		t.Fatal("expected error from dtqlDeserialize in deriveJoinDTQL")
	}
	dtqlDeserialize = origDeserialize

	// Candidate with non-main target schema
	fkSnapNonMain := fkSnap
	fkSnapNonMain.Keys = []ForeignKey{
		{ConstraintID: "fk_nonmain", Schema: "main", FromRelation: "orders", FromFields: []string{"user_id"}, ToSchema: "other_schema", ToRelation: "users", ToFields: []string{"id"}},
	}
	fkSnapNonMain.Columns[relationKey("other_schema", "users")] = []string{"id", "name"}
	candsNonMain, _ := DiscoverJoinCandidates([]byte(rsOrders.DTQL), fkSnapNonMain)
	if len(candsNonMain) > 0 {
		if derived, _, err := deriveJoinDTQL([]byte(rsOrders.DTQL), fkSnapNonMain, candsNonMain[0].ID, dal.JoinInner); err != nil {
			t.Fatalf("expected success for non-main schema join, got %v: %s", err, string(derived))
		}
	}

	// dtqlSerialize error
	origSerialize := dtqlSerialize
	defer func() { dtqlSerialize = origSerialize }()
	dtqlSerialize = func(q dal.StructuredQuery) ([]byte, error) {
		return nil, errors.New("serialize fail")
	}
	if _, _, err := deriveJoinDTQL([]byte(rsOrders.DTQL), fkSnap, validCandID, dal.JoinInner); err == nil {
		t.Fatal("expected error from dtqlSerialize in deriveJoinDTQL")
	}
	dtqlSerialize = origSerialize

	// dtqlDeserialize(derived) error
	deserializeCount := 0
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		deserializeCount++
		if deserializeCount > 2 {
			return nil, errors.New("deserialize derived fail")
		}
		return origDeserialize(data)
	}
	if _, _, err := deriveJoinDTQL([]byte(rsOrders.DTQL), fkSnap, validCandID, dal.JoinInner); err == nil {
		t.Fatal("expected error from dtqlDeserialize(derived) in deriveJoinDTQL")
	}
	dtqlDeserialize = origDeserialize

	// 11. prepareJoinProjection:
	// Wildcard source not in query
	qWildBad := joinPreparedQuery{
		StructuredQuery: qOrdersBase(),
		columns:         []dal.Column{dal.AllColumnsExceptFrom("nonexistent")},
	}
	if _, err := prepareJoinProjection(qWildBad, fkSnap, candsValid[0], "users"); err == nil {
		t.Fatal("expected error when wildcard source not in query")
	}

	// Wildcard schema metadata unavailable
	fkSnapNoCols := fkSnap
	fkSnapNoCols.Columns = map[string][]string{}
	qWildOk := joinPreparedQuery{
		StructuredQuery: qOrdersBase(),
		columns:         []dal.Column{dal.AllColumnsExceptFrom("orders")},
	}
	if _, err := prepareJoinProjection(qWildOk, fkSnapNoCols, candsValid[0], "users"); err == nil {
		t.Fatal("expected error when wildcard column metadata empty")
	}

	// Multi-relation wildcard (!single) and alias collision loop
	candMulti := candsValid[0]
	qMultiJoin, err := dtql.Deserialize([]byte("from:\n  name: orders\n  alias: orders\n  joins:\n    - from: {name: other, alias: other}\n      on: [{left: {field: user_id, source: orders}, op: '==', right: {field: id, source: other}}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	qMultiWild := joinPreparedQuery{
		StructuredQuery: qMultiJoin,
		columns: []dal.Column{
			dal.AllColumnsExceptFrom("orders"),
			{Expression: dal.NewFieldRef("other", "id"), Alias: "users_id"},
		},
	}
	if prep, err := prepareJoinProjection(qMultiWild, fkSnap, candMulti, "users"); err != nil {
		t.Fatalf("expected success for multi-relation wildcard and alias collision, got %v", err)
	} else if len(prep.Columns()) == 0 {
		t.Fatal("expected columns")
	}

	// qualifyJoinExpression error on column
	qBadCol := joinPreparedQuery{
		StructuredQuery: qMultiJoin,
		columns: []dal.Column{
			{Expression: dal.NewFieldRef("", "unqualified_id")},
		},
	}
	if _, err := prepareJoinProjection(qBadCol, fkSnap, candMulti, "users"); err == nil {
		t.Fatal("expected error for unqualified field in multi-relation query")
	}

	// targetColumns unavailable
	fkSnapNoTargetCols := ForeignKeySnapshot{
		Source:  "s1",
		Columns: map[string][]string{relationKey("main", "orders"): {"id", "user_id"}},
	}
	if _, err := prepareJoinProjection(qOrdersBase(), fkSnapNoTargetCols, candMulti, "users"); err == nil {
		t.Fatal("expected error when target columns unavailable")
	}

	// qualifyJoinCondition error on filter
	qBadFilter := joinPreparedQuery{
		StructuredQuery: qMultiJoin,
		columns:         []dal.Column{{Expression: dal.NewFieldRef("orders", "id")}},
		where:           dal.NewComparison(dal.NewFieldRef("", "unqualified_id"), dal.Equal, dal.Constant{Value: 1}),
	}
	if _, err := prepareJoinProjection(qBadFilter, fkSnap, candMulti, "users"); err == nil {
		t.Fatal("expected error on filter with unqualified field")
	}

	// qualifyJoinExpression error on ordering
	qBadOrder := joinPreparedQuery{
		StructuredQuery: qMultiJoin,
		columns:         []dal.Column{{Expression: dal.NewFieldRef("orders", "id")}},
		order: []dal.OrderExpression{
			dal.Ascending(dal.NewFieldRef("", "unqualified_id")),
		},
	}
	if _, err := prepareJoinProjection(qBadOrder, fkSnap, candMulti, "users"); err == nil {
		t.Fatal("expected error on order_by with unqualified field")
	}

	// Valid ascending order_by
	qGoodOrder := joinPreparedQuery{
		StructuredQuery: qOrdersBase(),
		columns:         []dal.Column{{Expression: dal.NewFieldRef("orders", "id")}},
		order: []dal.OrderExpression{
			dal.Ascending(dal.NewFieldRef("orders", "id")),
		},
	}
	if prep, err := prepareJoinProjection(qGoodOrder, fkSnap, candMulti, "users"); err != nil {
		t.Fatalf("expected success for ascending order_by, got %v", err)
	} else if len(prep.OrderBy()) == 0 {
		t.Fatal("expected order_by expression")
	}

	// 12. qualifyJoinCondition: comparison right error
	compBadRight := dal.NewComparison(dal.NewFieldRef("orders", "id"), dal.Equal, dal.NewFieldRef("", "unqualified"))
	if _, err := qualifyJoinCondition(compBadRight, "orders", false); err == nil {
		t.Fatal("expected error when comparison right fails qualify")
	}

	// 13. qualifyJoinExpression: binary right error and aggregate arg error
	binBadRight := dal.Binary(dal.NewFieldRef("orders", "id"), dal.Add, dal.NewFieldRef("", "unqualified"))
	if _, err := qualifyJoinExpression(binBadRight, "orders", false); err == nil {
		t.Fatal("expected error when binary right fails qualify")
	}

	aggBadArg := dal.NewAggregate("COUNT", false, dal.NewFieldRef("", "unqualified"))
	if _, err := qualifyJoinExpression(aggBadArg, "orders", false); err == nil {
		t.Fatal("expected error when aggregate arg fails qualify")
	}

	// 14. fromAtPath: invalid index
	if node := fromAtPath(qOrdersBase().From(), "root/99"); node != nil {
		t.Fatal("expected nil for non-existent path")
	}

	// 15. activeJoinPairs: provenance[childPath] == true
	appliedEdge := AppliedJoinEdge{
		JoinPath: RelationInstanceID("root/0"),
	}
	qWithJoin, err := dtql.Deserialize([]byte("from:\n  name: orders\n  alias: orders\n  joins:\n    - from: {name: users, alias: users}\n      on: [{left: {field: user_id, source: orders}, op: '==', right: {field: id, source: users}}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	pairs := activeJoinPairs(qWithJoin.From(), []AppliedJoinEdge{appliedEdge})
	if len(pairs) != 0 {
		t.Fatal("expected 0 active join pairs when provenance matches")
	}

	// 16. deriveJoinDTQL error on group_by (line 405)
	if _, _, err := deriveJoinDTQL([]byte("from: {name: orders}\ngroup_by: [{field: user_id}]\n"), fkSnap, validCandID, dal.JoinInner); err == nil {
		t.Fatal("expected error on group_by in deriveJoinDTQL")
	}

	// 17. deriveJoinDTQL error when fromAtPath returns nil (line 440)
	qWithJoinBytes, err := dtqlSerialize(qWithJoin)
	if err != nil {
		t.Fatal(err)
	}
	fkSnapChild := fkSnap
	fkSnapChild.Keys = append(append([]ForeignKey(nil), fkSnap.Keys...), ForeignKey{
		ConstraintID: "fk_users_roles",
		Schema:       "main",
		FromRelation: "users",
		FromFields:   []string{"role_id"},
		ToSchema:     "main",
		ToRelation:   "roles",
		ToFields:     []string{"id"},
	})
	fkSnapChild.Columns = map[string][]string{}
	for k, v := range fkSnap.Columns {
		fkSnapChild.Columns[k] = v
	}
	fkSnapChild.Columns[relationKey("main", "roles")] = []string{"id", "role_name"}
	fkSnapChild.Columns["roles"] = []string{"id", "role_name"}

	candsWithJoin, err := DiscoverJoinCandidates(qWithJoinBytes, fkSnapChild)
	if err != nil {
		t.Fatal(err)
	}
	var childCandID JoinCandidateID
	for _, c := range candsWithJoin {
		if c.Source.ID == "root/0" {
			childCandID = c.ID
			break
		}
	}
	if childCandID == "" {
		t.Fatal("expected candidate with Source.ID == root/0")
	}
	deserCount := 0
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		deserCount++
		if deserCount == 2 {
			return qOrdersBase(), nil
		}
		return origDeserialize(data)
	}
	if _, _, err := deriveJoinDTQL(qWithJoinBytes, fkSnapChild, childCandID, dal.JoinInner); err == nil {
		t.Fatal("expected error when fromAtPath returns nil in deriveJoinDTQL")
	}
	dtqlDeserialize = origDeserialize

	// 18. fromAtPath when child == nil (line 692)
	fromWithNilChild := dal.From(dal.NewRootCollectionRef("p", "p")).Join(dal.JoinedSource{})
	if node := fromAtPath(fromWithNilChild, "root/0"); node != nil {
		t.Fatal("expected nil when child is nil in fromAtPath")
	}

	// 19. activeJoinPairs !targetKnown (line 800)
	fromWithRecordsetSource := dal.From(dal.NewRootCollectionRef("root", "root")).Join(dal.JoinedSource{
		RecordsetSource: dal.NewRootCollectionRef("rs_table", "rs_table"),
	})
	_ = activeJoinPairs(fromWithRecordsetSource, nil)

	// 20. deriveJoinDTQL dtqlDeserialize call 2 error (line 408)
	deserCall := 0
	dtqlDeserialize = func(data []byte) (dal.StructuredQuery, error) {
		deserCall++
		if deserCall == 2 {
			return nil, errors.New("second deserialize fail")
		}
		return origDeserialize(data)
	}
	if _, _, err := deriveJoinDTQL([]byte(rsOrders.DTQL), fkSnap, validCandID, dal.JoinInner); err == nil {
		t.Fatal("expected error on call 2 of dtqlDeserialize in deriveJoinDTQL")
	}
	dtqlDeserialize = origDeserialize

	// 21. fromAtPath with nil from (line 684)
	if node := fromAtPath(nil, "root"); node != nil {
		t.Fatal("expected nil from fromAtPath with nil from")
	}

	// 22. activeJoinPairs !targetKnown (lines 806-807)
	mockChild := &mockChangingFromSource{FromSource: dal.From(dal.NewRootCollectionRef("tbl", "alias1"))}
	rootChanging := dal.From(dal.NewRootCollectionRef("root", "root")).Join(dal.NewJoinedFrom(mockChild, dal.JoinInner))
	_ = activeJoinPairs(rootChanging, nil)
}

type mockChangingFromSource struct {
	dal.FromSource
	baseCallCount int
}

func (m *mockChangingFromSource) Base() dal.RecordsetSource {
	m.baseCallCount++
	if m.baseCallCount < 3 {
		return dal.NewRootCollectionRef("tbl", "alias1")
	}
	return dal.NewRootCollectionRef("tbl", "alias2")
}

func qOrdersBase() dal.StructuredQuery {
	q, _ := dtql.Deserialize([]byte("from: {name: orders}\ncolumns: [{field: id}]\n"))
	return q
}
