package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
	"github.com/strongo/aichat/ai/cloudproto"
	"github.com/strongo/aichat/tui/grid"
)

func TestTelemetryFullCoverage(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}

	origUUID := newUUID
	defer func() { newUUID = origUUID }()
	base := ai.ClientContext{InstallationID: "id1"}
	sessions.ConfigureTelemetry(&recordingInteractionReporter{}, base)

	// 1. turnContext error when newUUID fails
	newUUID = func() (string, error) { return "", errors.New("uuid error") }
	_, id, cc := sessions.turnContext(ctx)
	if id != "" || cc != nil {
		t.Errorf("expected empty id and nil clientContext on uuid error")
	}

	// 2. NewCommandInteractionID error when newUUID fails
	cmdID := sessions.NewCommandInteractionID()
	if cmdID != "" {
		t.Errorf("expected empty cmdID on uuid error")
	}

	newUUID = origUUID

	// 3. reportTurn branches:
	// a) turn.Queries with error and without error
	// b) turn.Actions with error and without error
	// c) len(turn.Queries) > 0 with turnErr == nil -> outcome == "action_attempted"
	rep := &recordingInteractionReporter{}
	sessions.ConfigureTelemetry(rep, base)
	tCtx, interactionID, client := sessions.turnContext(ctx)

	turnWithActionsAndQueries := Turn{
		Queries: []QueryResult{
			{DTQL: "SELECT 1", Err: nil},
			{DTQL: "SELECT 2", Err: errors.New("query failed")},
		},
		Actions: []WorkspaceActionResult{
			{Summary: "set_tab", Err: nil},
			{Summary: "dock", Err: errors.New("dock failed")},
		},
	}
	sessions.reportTurn(tCtx, interactionID, client, "test query and actions", turnWithActionsAndQueries, nil, true)
	sessions.WaitForTelemetry()

	// 4. enqueueReport hitting default when telemetrySlots is full (channel size 32)
	for i := 0; i < 32; i++ {
		sessions.telemetrySlots <- struct{}{}
	}
	sessions.enqueueReport(cloudproto.InteractionReport{InteractionID: "overflow"})
	for i := 0; i < 32; i++ {
		<-sessions.telemetrySlots
	}
}

func TestWorkspaceFullCoverage(t *testing.T) {
	catalog := workspaceTestCatalog()
	session := ChatSession{
		Workspace: WorkspaceState{
			Views:      map[string]RecordSetView{},
			Selections: map[string]Selection{},
		},
		RecordSets: map[string]RecordSet{
			"rec1": {
				ID:    "rec1",
				Title: "Customers",
				Result: secureread.Result{
					Columns: []string{"id", "name"},
					Rows: []secureread.Row{
						{Data: map[string]any{"id": int64(1), "name": "Alice"}},
						{Data: map[string]any{"id": int64(2), "name": "Bob"}},
					},
				},
			},
		},
		Bookmarks: map[string]Bookmark{},
	}
	w := session.Workspace

	// bucket_remove with item not in bucket
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "bucket_remove", RecordSetID: "none"}); err == nil {
		t.Errorf("expected error")
	}

	// sort_view with unknown view
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "sort_view", ViewID: "none"}); err == nil {
		t.Errorf("expected error")
	}

	// sort_view with unknown recordset
	w.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "missing"}
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "sort_view", ViewID: "v1"}); err == nil {
		t.Errorf("expected error")
	}

	// sort_view with missing column
	w.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "rec1"}
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "sort_view", ViewID: "v1", OrderBy: "nonexistent"}); err == nil {
		t.Errorf("expected error")
	}

	// sort_view with selection from another view (hits line 159 continue)
	w.Selections["s_other"] = Selection{ID: "s_other", ViewID: "other_view"}
	w.Selections["s_v1"] = Selection{ID: "s_v1", ViewID: "v1", Rows: []int{0}}
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "sort_view", ViewID: "v1", OrderBy: "id"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// clear_selection
	w.CurrentSelectionID = "s_v1"
	w2, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "clear_selection"})
	if err != nil || w2.CurrentSelectionID != "" {
		t.Errorf("expected selection cleared")
	}

	// set_tab unknown tab
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "set_tab", Title: "UnknownTab"}); err == nil {
		t.Errorf("expected error")
	}

	// attach already attached
	ref := ContextReference{Kind: "recordset", ObjectID: "rec1"}
	w.Attachments = []ContextReference{ref}
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "attach", Reference: ref}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// detach_all
	w3, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "detach_all"})
	if err != nil || len(w3.Attachments) != 0 {
		t.Errorf("expected attachments empty")
	}

	// dock unsupported kind
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "dock", Reference: ContextReference{Kind: "project", ObjectID: "chinook", ProjectID: "chinook"}}); err == nil {
		t.Errorf("expected error")
	}

	// dock already docked
	dockRef := ContextReference{Kind: "recordset", ObjectID: "rec1"}
	w.Docks = []Dock{{Reference: dockRef}}
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "dock", Reference: dockRef}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// undock not found
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "undock", DockID: "none"}); err == nil {
		t.Errorf("expected error")
	}

	// default unknown action
	if _, _, err := w.apply(session, catalog, WorkspaceAction{Kind: "bogus"}); err == nil {
		t.Errorf("expected error")
	}

	// selectRows error branches:
	// recordset not available
	if _, _, err := w.selectRows(session, WorkspaceAction{RecordSetID: "missing"}); err == nil {
		t.Errorf("expected error")
	}

	// selected view not available or recordset mismatch
	w.Views["v_bad"] = RecordSetView{ID: "v_bad", RecordSetID: "other"}
	if _, _, err := w.selectRows(session, WorkspaceAction{ViewID: "v_bad", RecordSetID: "rec1"}); err == nil {
		t.Errorf("expected error")
	}

	// view with len(parentColumns) == 0
	w.Views["v_empty_cols"] = RecordSetView{ID: "v_empty_cols", RecordSetID: "rec1", RowIndices: []int{0}}
	if _, _, err := w.selectRows(session, WorkspaceAction{ViewID: "v_empty_cols", RecordSetID: "rec1"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// row outside recordset
	if _, _, err := w.selectRows(session, WorkspaceAction{RecordSetID: "rec1", Rows: []int{999}}); err == nil {
		t.Errorf("expected error")
	}

	// row positions negative
	if _, _, err := w.selectRows(session, WorkspaceAction{RecordSetID: "rec1", RowStart: -1}); err == nil {
		t.Errorf("expected error")
	}

	// filter Contains does not match
	if _, _, err := w.selectRows(session, WorkspaceAction{RecordSetID: "rec1", Column: "name", Contains: "nomatch"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// end < start (need at least 5 rows in recordset)
	recManyRows := RecordSet{
		ID:    "rec_many",
		Title: "Many",
		Result: secureread.Result{
			Columns: []string{"id", "name"},
			Rows: []secureread.Row{
				{Data: map[string]any{"id": int64(1), "name": "A"}},
				{Data: map[string]any{"id": int64(2), "name": "B"}},
				{Data: map[string]any{"id": int64(3), "name": "C"}},
				{Data: map[string]any{"id": int64(4), "name": "D"}},
				{Data: map[string]any{"id": int64(5), "name": "E"}},
			},
		},
	}
	session.RecordSets["rec_many"] = recManyRows
	if _, _, err := w.selectRows(session, WorkspaceAction{RecordSetID: "rec_many", OrderBy: "name", RowStart: 4, RowEnd: 1}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// selectRows with validateSelectionRangeProjection error (line 390)
	if _, _, err := w.selectRows(session, WorkspaceAction{
		RecordSetID: "rec1",
		Columns:     []string{"id", "name"},
		Ranges:      []CellRange{{FirstRow: 0, LastRow: 0, FirstCol: 0, LastCol: 0}},
	}); err == nil {
		t.Errorf("expected validateSelectionRangeProjection error")
	}

	// select with ranges and validateSelectionRangeProjection error
	// column out of bounds
	if err := validateSelectionRangeProjection([]string{"id"}, []int{0}, []string{"id"}, []CellRange{{FirstRow: 0, LastRow: 0, FirstCol: 5, LastCol: 5}}); err == nil {
		t.Errorf("expected error")
	}

	// range row not in selected rows
	if err := validateSelectionRangeProjection([]string{"id"}, []int{0}, []string{"id"}, []CellRange{{FirstRow: 1, LastRow: 1, FirstCol: 0, LastCol: 0}}); err == nil {
		t.Errorf("expected error")
	}

	// range column not in selected columns
	if err := validateSelectionRangeProjection([]string{"id", "name"}, []int{0}, []string{"name"}, []CellRange{{FirstRow: 0, LastRow: 0, FirstCol: 0, LastCol: 0}}); err == nil {
		t.Errorf("expected error")
	}

	// selectionParameters missing references
	session.Workspace.Attachments = []ContextReference{
		{Kind: "selection", ObjectID: "missing_sel"},
		{Kind: "selection", ObjectID: "sel_missing_view"},
		{Kind: "selection", ObjectID: "sel_missing_rec"},
		{Kind: "bookmark", ObjectID: "missing_bm"},
		{Kind: "unknown", ObjectID: "unk"},
	}
	session.Workspace.Selections["sel_missing_view"] = Selection{ID: "sel_missing_view", ViewID: "no_view"}
	session.Workspace.Selections["sel_missing_rec"] = Selection{ID: "sel_missing_rec", ViewID: "v_has_no_rec"}
	session.Workspace.Views["v_has_no_rec"] = RecordSetView{ID: "v_has_no_rec", RecordSetID: "no_rec"}
	params := selectionParameters(session)
	if len(params) != 0 {
		t.Errorf("expected empty params")
	}

	// validateContextReference branches
	// empty object ID
	if err := validateContextReference(session, catalog, w, ContextReference{Kind: "view", ObjectID: ""}); err == nil {
		t.Errorf("expected error")
	}

	// missing view
	if err := validateContextReference(session, catalog, w, ContextReference{Kind: "view", ObjectID: "none"}); err == nil {
		t.Errorf("expected error")
	}

	// missing selection
	if err := validateContextReference(session, catalog, w, ContextReference{Kind: "selection", ObjectID: "none"}); err == nil {
		t.Errorf("expected error")
	}

	// missing bookmark
	if err := validateContextReference(session, catalog, w, ContextReference{Kind: "bookmark", ObjectID: "none"}); err == nil {
		t.Errorf("expected error")
	}

	// unsupported kind
	if err := validateContextReference(session, catalog, w, ContextReference{Kind: "bogus", ObjectID: "id"}); err == nil {
		t.Errorf("expected error")
	}
}

func TestStoreFullCoverage(t *testing.T) {
	ctx := context.Background()

	// 1. DefaultChatStorePath error branches
	origHome := userHomeDir
	defer func() { userHomeDir = origHome }()
	userHomeDir = func() (string, error) { return "", errors.New("home error") }
	if _, err := DefaultChatStorePath("/tmp"); err == nil {
		t.Errorf("expected home error")
	}
	userHomeDir = origHome
	if _, err := DefaultChatStorePath("/dev/null/impossible"); err == nil {
		t.Errorf("expected canonical path error")
	}

	// 2. OpenSessionStore scope validation
	scope := testScope()
	scope.ProjectID = ""
	if _, err := OpenSessionStore(testStorePath(t), scope); err == nil {
		t.Errorf("expected scope error")
	}
	scope = testScope()
	scope.Environment = ""
	if _, err := OpenSessionStore(testStorePath(t), scope); err == nil {
		t.Errorf("expected scope error")
	}
	scope = testScope()
	scope.Database = ""
	if _, err := OpenSessionStore(testStorePath(t), scope); err == nil {
		t.Errorf("expected scope error")
	}
	scope = testScope()
	scope.AccessFingerprint = ""
	if _, err := OpenSessionStore(testStorePath(t), scope); err == nil {
		t.Errorf("expected scope error")
	}
	scope = testScope()
	scope.Sources = map[string]string{}
	if _, err := OpenSessionStore(testStorePath(t), scope); err == nil {
		t.Errorf("expected scope error")
	}
	if _, err := OpenSessionStore("", testScope()); err == nil {
		t.Errorf("expected empty path error")
	}

	// 3. OpenSessionStore filesystem errors
	tmp := t.TempDir()
	fileAsDir := filepath.Join(tmp, "a_file")
	if err := os.WriteFile(fileAsDir, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSessionStore(filepath.Join(fileAsDir, "sub", "store.sqlite"), testScope()); err == nil {
		t.Errorf("expected mkdir error")
	}

	// Directory permissions != 0700
	badPermDir := filepath.Join(tmp, "bad_perm_dir")
	if err := os.Mkdir(badPermDir, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(badPermDir, 0o777)
	if _, err := OpenSessionStore(filepath.Join(badPermDir, "store.sqlite"), testScope()); err == nil {
		t.Errorf("expected dir permission error")
	}

	// DB file permissions != 0600
	goodDir := filepath.Join(tmp, "good_dir")
	if err := os.Mkdir(goodDir, 0o700); err != nil {
		t.Fatal(err)
	}
	badFile := filepath.Join(goodDir, "bad.sqlite")
	if err := os.WriteFile(badFile, []byte{}, 0o666); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(badFile, 0o666)
	if _, err := OpenSessionStore(badFile, testScope()); err == nil {
		t.Errorf("expected file permission error")
	}

	// DB path is a directory
	badDirFile := filepath.Join(goodDir, "dir_as_file")
	if err := os.Mkdir(badDirFile, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSessionStore(badDirFile, testScope()); err == nil {
		t.Errorf("expected regular file error")
	}

	// 4. initChatSchema version branches
	memDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = memDB.Close() }()

	if _, err := memDB.Exec("PRAGMA user_version = 10"); err != nil {
		t.Fatal(err)
	}
	if err := initChatSchema(memDB); err == nil {
		t.Errorf("expected unsupported version error")
	}
	if _, err := memDB.Exec("PRAGMA user_version = -1"); err != nil {
		t.Fatal(err)
	}
	if err := initChatSchema(memDB); err == nil {
		t.Errorf("expected unsupported version error")
	}

	// Version >= 1 missing tables
	if _, err := memDB.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := initChatSchema(memDB); err == nil {
		t.Errorf("expected missing table error")
	}

	// Migrations from older versions
	for _, v := range []int{1, 2, 3, 6, 8} {
		mDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		// Create minimal tables required for version >= 1 so ALTER TABLE branches run
		for _, s := range []string{
			`CREATE TABLE sessions (id TEXT PRIMARY KEY, scope TEXT, title TEXT, created_at TEXT, updated_at TEXT)`,
			`CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, kind TEXT, text TEXT, created_at TEXT)`,
			`CREATE TABLE queries (id TEXT PRIMARY KEY, session_id TEXT, origin_message_id TEXT, title TEXT, dtql TEXT, source TEXT, parameters_json TEXT, executed_at TEXT, error TEXT)`,
			`CREATE TABLE recordsets (id TEXT PRIMARY KEY, session_id TEXT, query_id TEXT, origin_message_id TEXT, title TEXT, dtql TEXT, source TEXT, environment TEXT, database_id TEXT, parameters_json TEXT, created_at TEXT, result_json BLOB)`,
			`CREATE TABLE session_workspace (session_id TEXT PRIMARY KEY, state_json TEXT)`,
			`CREATE TABLE bookmarks (id TEXT PRIMARY KEY, project_id TEXT, scope TEXT, title TEXT, tags_json TEXT, target_kind TEXT, created_at TEXT, updated_at TEXT, snapshot_json BLOB)`,
			`CREATE TABLE chat_preferences (scope TEXT PRIMARY KEY, table_style TEXT)`,
			`CREATE TABLE http_responses (id TEXT PRIMARY KEY, session_id TEXT, origin_message_id TEXT, url TEXT, status_code INTEGER, content_type TEXT, request_has_query INTEGER, body BLOB, created_at TEXT)`,
		} {
			_, _ = mDB.Exec(s)
		}
		_, _ = mDB.Exec("PRAGMA user_version = ?", v)
		_ = initChatSchema(mDB)
		_ = mDB.Close()
	}

	// 5. migrateLegacyChatScopes
	// same scope
	if err := migrateLegacyChatScopes(memDB, "scopeA", "scopeA", "url"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// 6. encodeValue / decodeValue / decodeResult
	if val, err := encodeValue(uint(42)); err != nil || val.Type != "uint" {
		t.Errorf("expected uint encoding: %v", err)
	}
	if _, err := decodeResult([]byte(`{"Rows": null}`)); err == nil {
		t.Errorf("expected missing rows error")
	}
	if _, err := decodeResult([]byte(`{"Rows": [{"Data": {"c": {"Type": "unknown"}}}]}`)); err == nil {
		t.Errorf("expected row decode error")
	}
	if _, err := decodeValue(storedValue{Type: "int", Value: []byte("not_json")}); err == nil {
		t.Errorf("expected int decode error")
	}
	if _, err := decodeValue(storedValue{Type: "uint", Value: []byte("not_uint")}); err == nil {
		t.Errorf("expected uint decode error")
	}
	if _, err := decodeValue(storedValue{Type: "json", Value: []byte("bad json")}); err == nil {
		t.Errorf("expected json decode error")
	}
	if _, err := decodeValue(storedValue{Type: "unknown_type"}); err == nil {
		t.Errorf("expected unknown cell type error")
	}

	// 7. normalizeSessionTitle
	if res := normalizeSessionTitle("   "); res != "New chat" {
		t.Errorf("expected New chat, got %q", res)
	}
	longTitle := strings.Repeat("A", 100)
	if res := normalizeSessionTitle(longTitle); len(res) != 80 {
		t.Errorf("expected 80 chars, got %d", len(res))
	}

	// 9. checkOrigin error branches
	store2 := openTestStore(t, testStorePath(t), testScope())
	sess, err := store2.Create(ctx, "Test")
	if err != nil {
		t.Fatal(err)
	}
	// origin message not found
	if _, err := store2.AppendQuery(ctx, sess.ID, "nonexistent_origin", "sqlite:///chinook.db", QueryResult{}); err == nil {
		t.Errorf("expected origin not found error")
	}

	// 10. validateLoadedWorkspace branches
	baseSession := ChatSession{
		ID: sess.ID,
		RecordSets: map[string]RecordSet{
			"r1": {
				ID: "r1",
				Result: secureread.Result{
					Columns: []string{"col1"},
					Rows:    []secureread.Row{{Data: map[string]any{"col1": "val1"}}},
				},
			},
		},
		Workspace: WorkspaceState{
			Views:      map[string]RecordSetView{},
			Selections: map[string]Selection{},
		},
	}
	// missing bucket record
	sCopy := baseSession
	sCopy.Workspace.ExportBucket = []string{"missing"}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// duplicate bucket record
	sCopy = baseSession
	sCopy.Workspace.ExportBucket = []string{"r1", "r1"}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// view ID mismatch
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v2"}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// view references missing record
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "missing"}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// view has invalid column
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "r1", Columns: []string{"bad_col"}}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// view has invalid row index
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "r1", RowIndices: []int{99}}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// selection ID mismatch
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "r1"}
	sCopy.Workspace.Selections["s1"] = Selection{ID: "s2"}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// selection references missing view
	sCopy = baseSession
	sCopy.Workspace.Selections["s1"] = Selection{ID: "s1", ViewID: "missing"}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// selection has row outside view
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "r1", RowIndices: []int{0}}
	sCopy.Workspace.Selections["s1"] = Selection{ID: "s1", ViewID: "v1", Rows: []int{1}}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// selection has column outside view
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "r1", Columns: []string{"col1"}}
	sCopy.Workspace.Selections["s1"] = Selection{ID: "s1", ViewID: "v1", Columns: []string{"other"}}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// selection has invalid cell range
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "r1"}
	sCopy.Workspace.Selections["s1"] = Selection{ID: "s1", ViewID: "v1", Ranges: []CellRange{{FirstRow: -1}}}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// selection has cell range outside view
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "r1", RowIndices: []int{0}}
	sCopy.Workspace.Selections["s1"] = Selection{ID: "s1", ViewID: "v1", Ranges: []CellRange{{FirstRow: 1, LastRow: 1, FirstCol: 0, LastCol: 0}}}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// selection validateSelectionRangeProjection error
	sCopy = baseSession
	sCopy.Workspace.Views["v1"] = RecordSetView{ID: "v1", RecordSetID: "r1"}
	sCopy.Workspace.Selections["s1"] = Selection{ID: "s1", ViewID: "v1", Ranges: []CellRange{{FirstRow: 0, LastRow: 0, FirstCol: 0, LastCol: 0}}, Columns: []string{"col1", "extra"}}
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}
	// current selection missing
	sCopy = baseSession
	sCopy.Workspace.CurrentSelectionID = "missing"
	if err := validateLoadedWorkspace(sCopy); err == nil {
		t.Errorf("expected error")
	}

	// 11. Seams for Close, canonicalProjectPath, OpenSessionStore
	origAbs := filepathAbs
	origOpen := sqlOpenStore
	origDBClose := dbClose
	origSettingsClose := settingsDBClose
	origDBExec := dbExec
	origInitSchema := initChatSchemaFn
	origMigrateScopes := migrateLegacyChatScopesFn
	origOpenHTTPSettings := openHTTPSettingsDBFn
	defer func() {
		filepathAbs = origAbs
		sqlOpenStore = origOpen
		dbClose = origDBClose
		settingsDBClose = origSettingsClose
		dbExec = origDBExec
		initChatSchemaFn = origInitSchema
		migrateLegacyChatScopesFn = origMigrateScopes
		openHTTPSettingsDBFn = origOpenHTTPSettings
	}()

	// DefaultChatStorePath success
	if p, err := DefaultChatStorePath(t.TempDir()); err != nil || p == "" {
		t.Errorf("expected store path, got %q, err %v", p, err)
	}

	filepathAbs = func(p string) (string, error) { return "", errors.New("abs error") }
	if _, err := canonicalProjectPath("foo"); err == nil {
		t.Errorf("expected abs error")
	}
	if _, err := OpenSessionStore("foo", testScope()); err == nil {
		t.Errorf("expected abs error")
	}
	filepathAbs = origAbs

	sqlOpenStore = func(driver, dsn string) (*sql.DB, error) { return nil, errors.New("open error") }
	if _, err := OpenSessionStore(testStorePath(t), testScope()); err == nil {
		t.Errorf("expected open error")
	}
	sqlOpenStore = origOpen

	// dbExec error in OpenSessionStore
	dbExec = func(db *sql.DB, q string) (sql.Result, error) { return nil, errors.New("exec error") }
	if _, err := OpenSessionStore(testStorePath(t), testScope()); err == nil {
		t.Errorf("expected exec error")
	}
	dbExec = origDBExec

	// initChatSchema error in OpenSessionStore
	initChatSchemaFn = func(db *sql.DB) error { return errors.New("init schema error") }
	if _, err := OpenSessionStore(testStorePath(t), testScope()); err == nil {
		t.Errorf("expected init schema error")
	}
	initChatSchemaFn = origInitSchema

	// migrateLegacyChatScopes error in OpenSessionStore
	migrateLegacyChatScopesFn = func(db *sql.DB, o, n, u string) error { return errors.New("migrate scopes error") }
	if _, err := OpenSessionStore(testStorePath(t), testScope()); err == nil {
		t.Errorf("expected migrate scopes error")
	}
	migrateLegacyChatScopesFn = origMigrateScopes

	// openHTTPSettingsDB error in OpenSessionStore
	openHTTPSettingsDBFn = func(path string) (*sql.DB, error) { return nil, errors.New("open http settings error") }
	if _, err := OpenSessionStore(testStorePath(t), testScope()); err == nil {
		t.Errorf("expected open http settings error")
	}
	openHTTPSettingsDBFn = origOpenHTTPSettings

	// Close error branches
	store3 := openTestStore(t, testStorePath(t), testScope())
	dbClose = func(db *sql.DB) error { return errors.New("db close error") }
	if err := store3.Close(); err == nil {
		t.Errorf("expected db close error")
	}
	dbClose = origDBClose

	store4 := openTestStore(t, testStorePath(t), testScope())
	settingsDBClose = func(db *sql.DB) error { return errors.New("settings close error") }
	if err := store4.Close(); err == nil {
		t.Errorf("expected settings close error")
	}
	settingsDBClose = origSettingsClose

	// migrateLegacyChatScopes full logic test
	migDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migDB.Close() }()
	_, _ = migDB.Exec("CREATE TABLE sessions (id TEXT, scope TEXT)")
	_, _ = migDB.Exec("CREATE TABLE queries (session_id TEXT, source TEXT)")
	_, _ = migDB.Exec("CREATE TABLE recordsets (session_id TEXT, source TEXT)")
	_, _ = migDB.Exec("INSERT INTO sessions VALUES ('s1', 'oldScope'), ('s2', 'oldScope')")
	_, _ = migDB.Exec("INSERT INTO queries VALUES ('s1', 'matching_url'), ('s2', 'different_url')")
	if err := migrateLegacyChatScopes(migDB, "oldScope", "newScope", "matching_url"); err != nil {
		t.Errorf("unexpected migration error: %v", err)
	}
	// missing table queries in migrateLegacyChatScopes
	_, _ = migDB.Exec("DROP TABLE queries")
	_, _ = migDB.Exec("DROP TABLE recordsets")
	_, _ = migDB.Exec("INSERT INTO sessions VALUES ('s3', 'oldScope2')")
	if err := migrateLegacyChatScopes(migDB, "oldScope2", "newScope2", "matching_url"); err == nil {
		t.Errorf("expected query error in legacy migration")
	}
	// missing table sessions in migrateLegacyChatScopes
	_, _ = migDB.Exec("DROP TABLE sessions")
	if err := migrateLegacyChatScopes(migDB, "oldScope3", "newScope3", "matching_url"); err == nil {
		t.Errorf("expected sessions query error in legacy migration")
	}

	// addColumnIfMissing error branches
	txTest, err := migDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// ALTER on missing table
	if err := addColumnIfMissing(txTest, "missing_table", "col", "TEXT"); err == nil {
		t.Errorf("expected alter missing table error")
	}
	_ = txTest.Rollback()
	// rolled back tx Query error
	if err := addColumnIfMissing(txTest, "any_table", "col", "TEXT"); err == nil {
		t.Errorf("expected rolled back tx error")
	}

	// 12. Corrupt rows in store tables
	store5 := openTestStore(t, testStorePath(t), testScope())
	s5, err := store5.Create(ctx, "CorruptTest")
	if err != nil {
		t.Fatal(err)
	}

	// Corrupt message timestamp
	_, _ = store5.db.Exec(`INSERT INTO messages (id, session_id, role, kind, text, created_at) VALUES ('bad_msg', ?, 'You', 'text', 'hi', 'bad_time')`, s5.ID)
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected corrupt message timestamp error")
	}
	_, _ = store5.db.Exec(`DELETE FROM messages WHERE id = 'bad_msg'`)

	// Corrupt query parameters
	_, _ = store5.db.Exec(`INSERT INTO queries (id, session_id, origin_message_id, title, dtql, source, parameters_json, executed_at, error) VALUES ('bad_q1', ?, 'm', 't', 'd', 's', 'bad_json', '2026-01-01T00:00:00Z', '')`, s5.ID)
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected corrupt query params error")
	}
	_, _ = store5.db.Exec(`DELETE FROM queries WHERE id = 'bad_q1'`)

	// Corrupt query timestamp
	_, _ = store5.db.Exec(`INSERT INTO queries (id, session_id, origin_message_id, title, dtql, source, parameters_json, executed_at, error) VALUES ('bad_q2', ?, 'm', 't', 'd', 's', '{}', 'bad_time', '')`, s5.ID)
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected corrupt query timestamp error")
	}
	_, _ = store5.db.Exec(`DELETE FROM queries WHERE id = 'bad_q2'`)

	// Corrupt recordset params
	_, _ = store5.db.Exec(`INSERT INTO queries (id, session_id, origin_message_id, title, dtql, source, parameters_json, executed_at, error) VALUES ('valid_q', ?, 'm', 't', 'd', 's', '{}', '2026-01-01T00:00:00Z', '')`, s5.ID)
	_, _ = store5.db.Exec(`INSERT INTO recordsets (id, session_id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json, parent_recordset_id, join_candidate_id, join_applied_edges_json) VALUES ('bad_r1', ?, 'valid_q', 'm', 't', 'd', 's', 'e', 'db', 'bad_json', '2026-01-01T00:00:00Z', '{}', '', '', '[]')`, s5.ID)
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected corrupt recordset params error")
	}
	_, _ = store5.db.Exec(`DELETE FROM recordsets WHERE id = 'bad_r1'`)

	// Corrupt recordset timestamp
	_, _ = store5.db.Exec(`INSERT INTO recordsets (id, session_id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json, parent_recordset_id, join_candidate_id, join_applied_edges_json) VALUES ('bad_r2', ?, 'valid_q', 'm', 't', 'd', 's', 'e', 'db', '{}', 'bad_time', '{}', '', '', '[]')`, s5.ID)
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected corrupt recordset timestamp error")
	}
	_, _ = store5.db.Exec(`DELETE FROM recordsets WHERE id = 'bad_r2'`)

	// Corrupt recordset result_json
	_, _ = store5.db.Exec(`INSERT INTO recordsets (id, session_id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json, parent_recordset_id, join_candidate_id, join_applied_edges_json) VALUES ('bad_r3', ?, 'valid_q', 'm', 't', 'd', 's', 'e', 'db', '{}', '2026-01-01T00:00:00Z', 'bad_json', '', '', '[]')`, s5.ID)
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected corrupt recordset result error")
	}
	_, _ = store5.db.Exec(`DELETE FROM recordsets WHERE id = 'bad_r3'`)

	// Corrupt recordset JOIN lineage
	_, _ = store5.db.Exec(`INSERT INTO recordsets (id, session_id, query_id, origin_message_id, title, dtql, source, environment, database_id, parameters_json, created_at, result_json, parent_recordset_id, join_candidate_id, join_applied_edges_json) VALUES ('bad_r4', ?, 'valid_q', 'm', 't', 'd', 's', 'e', 'db', '{}', '2026-01-01T00:00:00Z', '{"Rows":[]}', 'p1', 'c1', 'bad_json')`, s5.ID)
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected corrupt recordset lineage error")
	}
	_, _ = store5.db.Exec(`DELETE FROM recordsets WHERE id = 'bad_r4'`)

	// Corrupt session_workspace
	_, _ = store5.db.Exec(`INSERT INTO session_workspace (session_id, state_json) VALUES (?, 'bad_json')`, s5.ID)
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected corrupt workspace error")
	}
	_, _ = store5.db.Exec(`DELETE FROM session_workspace WHERE session_id = ?`, s5.ID)

	// 13. updateSession rowsAffected == 0
	if err := store5.Rename(ctx, "nonexistent", "Title"); err == nil {
		t.Errorf("expected not found error")
	}
	if err := store5.Activate(ctx, "nonexistent"); err == nil {
		t.Errorf("expected not found error")
	}
	if err := store5.Delete(ctx, "nonexistent"); err == nil {
		t.Errorf("expected not found error")
	}
	if err := store5.Clear(ctx, "nonexistent"); err == nil {
		t.Errorf("expected not found error")
	}
	if err := store5.SaveWorkspace(ctx, "nonexistent", WorkspaceState{}); err == nil {
		t.Errorf("expected not found error")
	}

	// 14. AppendTurn with markdown and pre-existing QueryID
	uMsg, err := store5.AppendUser(ctx, s5.ID, "question")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store5.AppendTurn(ctx, s5.ID, uMsg.ID, "sqlite:///chinook.db", Turn{
		Text:       "# Markdown title",
		TextFormat: "markdown",
		Queries: []QueryResult{
			{QueryID: "pre-existing-id"},
			{Title: "Q1", DTQL: "SELECT 1", Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}}},
		},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// 15. Closed DB operations
	_ = store5.db.Close()
	if _, err := store5.List(ctx); err == nil {
		t.Errorf("expected error on closed db")
	}
	if _, err := store5.Load(ctx, s5.ID); err == nil {
		t.Errorf("expected error on closed db")
	}
	if err := store5.SaveWorkspace(ctx, s5.ID, WorkspaceState{}); err == nil {
		t.Errorf("expected error on closed db")
	}
	if _, err := store5.ResultVersionsToKeep(ctx); err == nil {
		t.Errorf("expected error on closed db")
	}
	if err := store5.SetResultVersionsToKeep(ctx, 3); err == nil {
		t.Errorf("expected error on closed db")
	}
	if err := store5.Clear(ctx, s5.ID); err == nil {
		t.Errorf("expected error on closed db")
	}
	if err := store5.Rename(ctx, s5.ID, "new"); err == nil {
		t.Errorf("expected error on closed db")
	}
	if err := store5.Activate(ctx, s5.ID); err == nil {
		t.Errorf("expected error on closed db")
	}
	if err := store5.Delete(ctx, s5.ID); err == nil {
		t.Errorf("expected error on closed db")
	}
	if _, err := store5.AppendUser(ctx, s5.ID, "hi"); err == nil {
		t.Errorf("expected error on closed db")
	}
	if _, err := store5.AppendQuery(ctx, s5.ID, "orig", "src", QueryResult{}); err == nil {
		t.Errorf("expected error on closed db")
	}
	if _, err := store5.AppendTurn(ctx, s5.ID, "orig", "prompt", Turn{}); err == nil {
		t.Errorf("expected error on closed db")
	}
}

type mockRowsAffectedErrorResult struct{}

func (m mockRowsAffectedErrorResult) LastInsertId() (int64, error) { return 0, nil }
func (m mockRowsAffectedErrorResult) RowsAffected() (int64, error) {
	return 0, errors.New("simulated rows affected error")
}

func TestStoreRemaining100Coverage(t *testing.T) {
	ctx := context.Background()

	// 1. OpenSessionStore osLstat & osOpenFile error branches
	origLstat := osLstat
	origOpenFile := osOpenFile
	defer func() {
		osLstat = origLstat
		osOpenFile = origOpenFile
	}()

	dir := t.TempDir()
	_ = os.Chmod(dir, 0o700)
	dbPath := filepath.Join(dir, "chat.db")

	osLstat = func(name string) (os.FileInfo, error) {
		return nil, errors.New("lstat disk error")
	}
	if _, err := OpenSessionStore(dbPath, testScope()); err == nil || !strings.Contains(err.Error(), "lstat disk error") {
		t.Errorf("expected lstat error: %v", err)
	}

	osLstat = origLstat
	osOpenFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		return nil, errors.New("open file error")
	}
	if _, err := OpenSessionStore(dbPath, testScope()); err == nil || !strings.Contains(err.Error(), "open file error") {
		t.Errorf("expected open file error: %v", err)
	}
	osOpenFile = origOpenFile

	// 2. migrateLegacyChatScopes error branches
	origDBBegin := dbBeginFn
	origTxQuery := txQueryFn
	origTxExec := txExecFn
	defer func() {
		dbBeginFn = origDBBegin
		txQueryFn = origTxQuery
		txExecFn = origTxExec
	}()

	memDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = memDB.Close() }()

	// dbBeginFn error
	dbBeginFn = func(db *sql.DB) (*sql.Tx, error) {
		return nil, errors.New("begin error")
	}
	if err := migrateLegacyChatScopes(memDB, "old", "new", "url"); err == nil {
		t.Errorf("expected begin error")
	}
	dbBeginFn = origDBBegin

	// sessions query error
	txQueryFn = func(tx *sql.Tx, query string, args ...any) (*sql.Rows, error) {
		return nil, errors.New("query error")
	}
	if err := migrateLegacyChatScopes(memDB, "old", "new", "url"); err == nil {
		t.Errorf("expected query error")
	}

	for _, s := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, scope TEXT)`,
		`INSERT INTO sessions VALUES ('s1', 'old')`,
		`CREATE TABLE queries (session_id TEXT, source TEXT)`,
		`CREATE TABLE recordsets (session_id TEXT, source TEXT)`,
	} {
		if _, err := memDB.Exec(s); err != nil {
			t.Fatal(err)
		}
	}

	// sessions scan error (return 2 columns instead of 1)
	txQueryFn = func(tx *sql.Tx, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "SELECT id FROM sessions") {
			return tx.Query(`SELECT 1, 2`)
		}
		return origTxQuery(tx, query, args...)
	}
	if err := migrateLegacyChatScopes(memDB, "old", "new", "url"); err == nil {
		t.Errorf("expected scan error")
	}

	// sources query error
	txQueryFn = func(tx *sql.Tx, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "SELECT source FROM queries") {
			return nil, errors.New("sources query error")
		}
		return origTxQuery(tx, query, args...)
	}
	if err := migrateLegacyChatScopes(memDB, "old", "new", "url"); err == nil {
		t.Errorf("expected sources query error")
	}

	// sources scan error (return 2 columns)
	txQueryFn = func(tx *sql.Tx, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "SELECT source FROM queries") {
			return tx.Query(`SELECT 1, 2`)
		}
		return origTxQuery(tx, query, args...)
	}
	if err := migrateLegacyChatScopes(memDB, "old", "new", "url"); err == nil {
		t.Errorf("expected sources scan error")
	}

	// txExecFn error during UPDATE sessions
	txQueryFn = origTxQuery
	txExecFn = func(tx *sql.Tx, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE sessions") {
			return nil, errors.New("update exec error")
		}
		return origTxExec(tx, query, args...)
	}
	if err := migrateLegacyChatScopes(memDB, "old", "new", "url"); err == nil {
		t.Errorf("expected update exec error")
	}
	txExecFn = origTxExec

	// rowsErrFn error on sessions and rows in migrateLegacyChatScopes
	origRowsErr := rowsErrFn
	defer func() { rowsErrFn = origRowsErr }()
	rowsErrFn = func(rows *sql.Rows) error {
		return errors.New("rows err fail")
	}
	if err := migrateLegacyChatScopes(memDB, "old", "new", "url"); err == nil {
		t.Errorf("expected rows err fail in migrateLegacyChatScopes")
	}

	// Make sessions iterate at least one row, then fail rowsErr on sources
	txQueryFn = func(tx *sql.Tx, query string, args ...any) (*sql.Rows, error) {
		return origTxQuery(tx, query, args...)
	}
	rowsErrFn = func(rows *sql.Rows) error {
		// allow first check, fail second
		return errors.New("sources rows err fail")
	}
	// To fail rowsErrFn on sources: allow sessions.Err() to return nil, but rows.Err() to return error:
	callCount := 0
	rowsErrFn = func(rows *sql.Rows) error {
		callCount++
		if callCount > 1 {
			return errors.New("sources rows err fail")
		}
		return nil
	}
	if err := migrateLegacyChatScopes(memDB, "old", "new", "url"); err == nil {
		t.Errorf("expected sources rows err fail")
	}
	rowsErrFn = origRowsErr

	// 3. initChatSchema error branches
	origQuickCheck := quickCheckFn
	origTxExecSchema := txExecSchemaFn
	origAddCol := addColumnIfMissingFn
	origAddRecCol := addRecordsetColumnFn
	origTableInfo := tableInfoQueryFn
	defer func() {
		quickCheckFn = origQuickCheck
		txExecSchemaFn = origTxExecSchema
		addColumnIfMissingFn = origAddCol
		addRecordsetColumnFn = origAddRecCol
		tableInfoQueryFn = origTableInfo
	}()

	// QueryRow PRAGMA user_version error (closed db)
	closedDB, _ := sql.Open("sqlite", ":memory:")
	_ = closedDB.Close()
	if err := initChatSchema(closedDB); err == nil {
		t.Errorf("expected closed db error")
	}

	// quickCheckFn error & non-ok (requires version >= 1 and tables created)
	qcDB, _ := sql.Open("sqlite", ":memory:")
	for _, s := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, scope TEXT, title TEXT, created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, kind TEXT, text TEXT, created_at TEXT)`,
		`CREATE TABLE queries (id TEXT PRIMARY KEY, session_id TEXT, origin_message_id TEXT, title TEXT, dtql TEXT, source TEXT, parameters_json TEXT, executed_at TEXT, error TEXT)`,
		`CREATE TABLE recordsets (id TEXT PRIMARY KEY, session_id TEXT, query_id TEXT, origin_message_id TEXT, title TEXT, dtql TEXT, source TEXT, environment TEXT, database_id TEXT, parameters_json TEXT, created_at TEXT, result_json BLOB)`,
	} {
		_, _ = qcDB.Exec(s)
	}
	_, _ = qcDB.Exec("PRAGMA user_version = 1")
	quickCheckFn = func(db *sql.DB) (string, error) {
		return "", errors.New("quick check error")
	}
	if err := initChatSchema(qcDB); err == nil {
		t.Errorf("expected quick check error")
	}
	quickCheckFn = func(db *sql.DB) (string, error) {
		return "corrupt", nil
	}
	if err := initChatSchema(qcDB); err == nil {
		t.Errorf("expected corrupt quick check error")
	}
	quickCheckFn = origQuickCheck
	_ = qcDB.Close()

	// version >= 2 missing session_workspace, bookmarks, chat_preferences
	for _, tableToDrop := range []string{"session_workspace", "bookmarks", "chat_preferences"} {
		testDB, _ := sql.Open("sqlite", ":memory:")
		if err := initChatSchema(testDB); err != nil {
			t.Fatal(err)
		}
		_, _ = testDB.Exec("DROP TABLE " + tableToDrop)
		version := 2
		switch tableToDrop {
		case "bookmarks":
			version = 3
		case "chat_preferences":
			version = 8
		}
		_, _ = testDB.Exec("PRAGMA user_version = ?", version)
		if err := initChatSchema(testDB); err == nil {
			t.Errorf("expected missing %s error", tableToDrop)
		}
		_ = testDB.Close()
	}

	// dbBeginFn error in initChatSchema
	initDB, _ := sql.Open("sqlite", ":memory:")
	defer func() { _ = initDB.Close() }()
	dbBeginFn = func(db *sql.DB) (*sql.Tx, error) {
		return nil, errors.New("begin tx error")
	}
	if err := initChatSchema(initDB); err == nil {
		t.Errorf("expected begin tx error")
	}
	dbBeginFn = origDBBegin

	// txExecSchemaFn error in statement execution
	txExecSchemaFn = func(tx *sql.Tx, query string) (sql.Result, error) {
		return nil, errors.New("schema exec error")
	}
	if err := initChatSchema(initDB); err == nil {
		t.Errorf("expected schema exec error")
	}
	txExecSchemaFn = origTxExecSchema

	// addRecordsetColumnFn and addColumnIfMissingFn errors across versions 1..8
	setupDBForVersion := func(v int) *sql.DB {
		d, _ := sql.Open("sqlite", ":memory:")
		for _, s := range []string{
			`CREATE TABLE sessions (id TEXT PRIMARY KEY, scope TEXT, title TEXT, created_at TEXT, updated_at TEXT)`,
			`CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, kind TEXT, text TEXT, created_at TEXT)`,
			`CREATE TABLE queries (id TEXT PRIMARY KEY, session_id TEXT, origin_message_id TEXT, title TEXT, dtql TEXT, source TEXT, parameters_json TEXT, executed_at TEXT, error TEXT)`,
			`CREATE TABLE recordsets (id TEXT PRIMARY KEY, session_id TEXT, query_id TEXT, origin_message_id TEXT, title TEXT, dtql TEXT, source TEXT, environment TEXT, database_id TEXT, parameters_json TEXT, created_at TEXT, result_json BLOB)`,
			`CREATE TABLE session_workspace (session_id TEXT PRIMARY KEY, state_json TEXT)`,
			`CREATE TABLE bookmarks (id TEXT PRIMARY KEY, project_id TEXT, scope TEXT, title TEXT, tags_json TEXT, target_kind TEXT, created_at TEXT, updated_at TEXT, snapshot_json BLOB)`,
			`CREATE TABLE chat_preferences (scope TEXT PRIMARY KEY, table_style TEXT)`,
			`CREATE TABLE http_responses (id TEXT PRIMARY KEY, session_id TEXT, origin_message_id TEXT, url TEXT, status_code INTEGER, content_type TEXT, request_has_query INTEGER, body BLOB, created_at TEXT)`,
		} {
			_, _ = d.Exec(s)
		}
		_, _ = d.Exec(fmt.Sprintf("PRAGMA user_version = %d", v))
		return d
	}

	recColsToFail := []struct {
		version int
		column  string
	}{
		{1, "parent_recordset_id"},
		{1, "join_candidate_id"},
		{4, "join_applied_edges_json"},
		{6, "http_response_id"},
		{6, "refresh_parent_id"},
	}
	for _, tc := range recColsToFail {
		d := setupDBForVersion(tc.version)
		addRecordsetColumnFn = func(tx *sql.Tx, name, def string) error {
			if name == tc.column {
				return errors.New("rec col error: " + name)
			}
			return origAddRecCol(tx, name, def)
		}
		if err := initChatSchema(d); err == nil {
			t.Errorf("expected error for recordset column %s", tc.column)
		}
		_ = d.Close()
	}
	addRecordsetColumnFn = origAddRecCol

	colIfMissingToFail := []struct {
		version int
		column  string
	}{
		{6, "http_response_id"},
		{6, "headers_json"},
		{6, "response_nanos"},
		{6, "final_url"},
		{6, "refresh_parent_id"},
		{8, "method"},
		{8, "request_headers_json"},
	}
	for _, tc := range colIfMissingToFail {
		d := setupDBForVersion(tc.version)
		addColumnIfMissingFn = func(tx *sql.Tx, table, name, def string) error {
			if name == tc.column {
				return errors.New("col error: " + name)
			}
			return origAddCol(tx, table, name, def)
		}
		if err := initChatSchema(d); err == nil {
			t.Errorf("expected error for column %s", tc.column)
		}
		_ = d.Close()
	}
	addColumnIfMissingFn = origAddCol

	// version 6 fail ALTER TABLE chat_preferences ADD COLUMN result_versions_to_keep
	{
		d, _ := sql.Open("sqlite", ":memory:")
		for _, s := range []string{
			`CREATE TABLE sessions (id TEXT PRIMARY KEY, scope TEXT, title TEXT, created_at TEXT, updated_at TEXT)`,
			`CREATE TABLE messages (id TEXT PRIMARY KEY, session_id TEXT, role TEXT, kind TEXT, text TEXT, created_at TEXT, http_response_id TEXT)`,
			`CREATE TABLE queries (id TEXT PRIMARY KEY, session_id TEXT, origin_message_id TEXT, title TEXT, dtql TEXT, source TEXT, parameters_json TEXT, executed_at TEXT, error TEXT)`,
			`CREATE TABLE recordsets (id TEXT PRIMARY KEY, session_id TEXT, query_id TEXT, origin_message_id TEXT, title TEXT, dtql TEXT, source TEXT, environment TEXT, database_id TEXT, parameters_json TEXT, created_at TEXT, result_json BLOB, parent_recordset_id TEXT, join_candidate_id TEXT, join_applied_edges_json TEXT, http_response_id TEXT, refresh_parent_id TEXT)`,
			`CREATE TABLE session_workspace (session_id TEXT PRIMARY KEY, state_json TEXT)`,
			`CREATE TABLE bookmarks (id TEXT PRIMARY KEY, project_id TEXT, scope TEXT, title TEXT, tags_json TEXT, target_kind TEXT, created_at TEXT, updated_at TEXT, snapshot_json BLOB)`,
			`CREATE TABLE chat_preferences (scope TEXT PRIMARY KEY, table_style TEXT, result_versions_to_keep INTEGER NOT NULL DEFAULT 2)`,
			`CREATE TABLE http_responses (id TEXT PRIMARY KEY, session_id TEXT, origin_message_id TEXT, url TEXT, status_code INTEGER, content_type TEXT, headers_json TEXT, request_headers_json TEXT, response_nanos INTEGER, download_nanos INTEGER, final_url TEXT, redirects_json TEXT, request_has_query INTEGER, body BLOB, refresh_parent_id TEXT, method TEXT, created_at TEXT)`,
		} {
			_, _ = d.Exec(s)
		}
		_, _ = d.Exec("PRAGMA user_version = 6")
		if err := initChatSchema(d); err == nil {
			t.Errorf("expected duplicate column error")
		}
		_ = d.Close()
	}

	// 4. addColumnIfMissing error branches
	{
		testDB, _ := sql.Open("sqlite", ":memory:")
		defer func() { _ = testDB.Close() }()
		tx, _ := testDB.Begin()
		defer func() { _ = tx.Rollback() }()

		tableInfoQueryFn = func(tx *sql.Tx, table string) (*sql.Rows, error) {
			return nil, errors.New("table info fail")
		}
		if err := addColumnIfMissing(tx, "dummy", "col", "TEXT"); err == nil {
			t.Errorf("expected table info error")
		}

		tableInfoQueryFn = func(tx *sql.Tx, table string) (*sql.Rows, error) {
			return tx.Query("SELECT 1")
		}
		if err := addColumnIfMissing(tx, "dummy", "col", "TEXT"); err == nil {
			t.Errorf("expected scan error")
		}
		tableInfoQueryFn = origTableInfo

		// rowsErrFn error in addColumnIfMissing
		rowsErrFn = func(rows *sql.Rows) error {
			return errors.New("rows err in add col")
		}
		_, _ = tx.Exec("CREATE TABLE t_err (id TEXT)")
		if err := addColumnIfMissing(tx, "t_err", "col2", "TEXT"); err == nil {
			t.Errorf("expected rows err in addColumnIfMissing")
		}
		rowsErrFn = origRowsErr

		txExecSchemaFn = func(tx *sql.Tx, query string) (sql.Result, error) {
			return nil, errors.New("alter table fail")
		}
		_, _ = tx.Exec("CREATE TABLE t1 (id TEXT)")
		if err := addColumnIfMissing(tx, "t1", "new_col", "TEXT"); err == nil {
			t.Errorf("expected alter table fail")
		}
		txExecSchemaFn = origTxExecSchema
	}

	// 5. Store operations error branches with active store
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	s, err := store.Create(ctx, "Test Store Remainder")
	if err != nil {
		t.Fatal(err)
	}

	// List error branches
	origDBQuery := dbQueryContextFn
	defer func() { dbQueryContextFn = origDBQuery }()
	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		return nil, errors.New("list query fail")
	}
	if _, err := store.List(ctx); err == nil {
		t.Errorf("expected list query error")
	}

	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		return db.QueryContext(ctx, "SELECT 1")
	}
	if _, err := store.List(ctx); err == nil {
		t.Errorf("expected list scan error")
	}
	dbQueryContextFn = origDBQuery

	// Corrupt created_at and updated_at in sessions for List
	_, _ = store.db.Exec(`UPDATE sessions SET created_at = 'bad' WHERE id = ?`, s.ID)
	if _, err := store.List(ctx); err == nil {
		t.Errorf("expected corrupt created_at error in List")
	}
	_, _ = store.db.Exec(`UPDATE sessions SET created_at = ?, updated_at = 'bad' WHERE id = ?`, stamp(s.CreatedAt), s.ID)
	if _, err := store.List(ctx); err == nil {
		t.Errorf("expected corrupt updated_at error in List")
	}
	_, _ = store.db.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, stamp(s.UpdatedAt), s.ID)

	// Load error branches
	_, _ = store.db.Exec(`UPDATE sessions SET updated_at = 'bad' WHERE id = ?`, s.ID)
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected corrupt updated_at error in Load")
	}
	_, _ = store.db.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, stamp(s.UpdatedAt), s.ID)

	origLoadHTTP := loadHTTPResponsesFn
	origLoadBM := loadBookmarksFn
	origLoadWS := loadWorkspaceFn
	origValBM := validateBookmarkRefsFn
	defer func() {
		loadHTTPResponsesFn = origLoadHTTP
		loadBookmarksFn = origLoadBM
		loadWorkspaceFn = origLoadWS
		validateBookmarkRefsFn = origValBM
	}()

	loadHTTPResponsesFn = func(s *SessionStore, ctx context.Context, item *ChatSession) error {
		return errors.New("load http fail")
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load http error")
	}
	loadHTTPResponsesFn = origLoadHTTP

	loadBookmarksFn = func(s *SessionStore, ctx context.Context, item *ChatSession) error {
		return errors.New("load bm fail")
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load bm error")
	}
	loadBookmarksFn = origLoadBM

	loadWorkspaceFn = func(s *SessionStore, ctx context.Context, item *ChatSession) error {
		return errors.New("load ws fail")
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load ws error")
	}
	loadWorkspaceFn = origLoadWS

	validateBookmarkRefsFn = func(s *SessionStore, ctx context.Context, db bookmarkSQL, state WorkspaceState) error {
		return errors.New("val bm fail")
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected val bm error in Load")
	}
	validateBookmarkRefsFn = origValBM

	// allowedRows[row] is false in validateWorkspace during Load
	uMsg, err := store.AppendUser(ctx, s.ID, "show users")
	if err != nil {
		t.Fatal(err)
	}
	qRes, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{
		Title: "Two rows",
		DTQL:  "SELECT * FROM t",
		Result: secureread.Result{
			Columns: []string{"id", "val"},
			Rows: []secureread.Row{
				{Data: map[string]any{"id": int64(1), "val": "a"}},
				{Data: map[string]any{"id": int64(2), "val": "b"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	wsOutside := WorkspaceState{
		Views: map[string]RecordSetView{
			"v1": {ID: "v1", RecordSetID: qRes.RecordSetID, RowIndices: []int{0}}, // only row 0 allowed
		},
		Selections: map[string]Selection{
			"sel1": {
				ID: "sel1", ViewID: "v1",
				Ranges:  []CellRange{{FirstRow: 0, LastRow: 1, FirstCol: 0, LastCol: 0}}, // spans row 1 which is not in view
				Rows:    []int{0},
				Columns: []string{"id"},
			},
		},
	}
	wsBytes, _ := json.Marshal(wsOutside)
	_, _ = store.db.Exec(`INSERT INTO session_workspace (session_id, state_json) VALUES (?, ?) ON CONFLICT(session_id) DO UPDATE SET state_json = excluded.state_json`, s.ID, string(wsBytes))
	if _, err := store.Load(ctx, s.ID); err == nil || !strings.Contains(err.Error(), "cell range outside its view") {
		t.Errorf("expected cell range outside its view error: %v", err)
	}

	// validateSelectionRangeProjection error in validateWorkspace during Load
	wsProjMismatch := WorkspaceState{
		Views: map[string]RecordSetView{
			"v1": {ID: "v1", RecordSetID: qRes.RecordSetID, RowIndices: []int{0, 1}},
		},
		Selections: map[string]Selection{
			"sel1": {
				ID: "sel1", ViewID: "v1",
				Ranges:  []CellRange{{FirstRow: 0, LastRow: 0, FirstCol: 0, LastCol: 0}},
				Rows:    []int{0, 1}, // projection length 2 != span length 1
				Columns: []string{"id"},
			},
		},
	}
	wsBytes2, _ := json.Marshal(wsProjMismatch)
	_, _ = store.db.Exec(`INSERT INTO session_workspace (session_id, state_json) VALUES (?, ?) ON CONFLICT(session_id) DO UPDATE SET state_json = excluded.state_json`, s.ID, string(wsBytes2))
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected projection validation error in Load")
	}

	// Reset workspace
	_, _ = store.db.Exec(`DELETE FROM session_workspace WHERE session_id = ?`, s.ID)

	// SaveWorkspace error branches
	origMarshalWS := jsonMarshalWorkspace
	origSessionExists := sessionExistsFn
	origTxExecCtx := txExecContextFn
	origTxCommit := txCommitFn
	defer func() {
		jsonMarshalWorkspace = origMarshalWS
		sessionExistsFn = origSessionExists
		txExecContextFn = origTxExecCtx
		txCommitFn = origTxCommit
	}()

	jsonMarshalWorkspace = func(v any) ([]byte, error) {
		return nil, errors.New("marshal ws fail")
	}
	if err := store.SaveWorkspace(ctx, s.ID, WorkspaceState{}); err == nil {
		t.Errorf("expected marshal ws error")
	}
	jsonMarshalWorkspace = origMarshalWS

	sessionExistsFn = func(ctx context.Context, tx *sql.Tx, id, scope string) error {
		return errors.New("session exists fail")
	}
	if err := store.SaveWorkspace(ctx, s.ID, WorkspaceState{}); err == nil {
		t.Errorf("expected session exists error in SaveWorkspace")
	}
	sessionExistsFn = origSessionExists

	validateBookmarkRefsFn = func(s *SessionStore, ctx context.Context, db bookmarkSQL, state WorkspaceState) error {
		return errors.New("val bm fail in save")
	}
	if err := store.SaveWorkspace(ctx, s.ID, WorkspaceState{}); err == nil {
		t.Errorf("expected val bm error in SaveWorkspace")
	}
	validateBookmarkRefsFn = origValBM

	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO session_workspace") {
			return nil, errors.New("insert ws fail")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if err := store.SaveWorkspace(ctx, s.ID, WorkspaceState{}); err == nil {
		t.Errorf("expected insert ws error")
	}

	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE sessions SET updated_at") {
			return nil, errors.New("update session updated_at fail")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if err := store.SaveWorkspace(ctx, s.ID, WorkspaceState{}); err == nil {
		t.Errorf("expected update session error in SaveWorkspace")
	}
	txExecContextFn = origTxExecCtx

	// ResultVersionsToKeep < 1 error
	_, _ = store.db.Exec(`INSERT INTO chat_preferences (scope, table_style, result_versions_to_keep) VALUES (?, ?, 0) ON CONFLICT(scope) DO UPDATE SET result_versions_to_keep = 0`, store.scope, grid.StyleLines.Name)
	if _, err := store.ResultVersionsToKeep(ctx); err == nil {
		t.Errorf("expected invalid result version limit error")
	}
	_, _ = store.db.Exec(`DELETE FROM chat_preferences WHERE scope = ?`, store.scope)

	// loadMessages query and scan errors
	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "FROM messages WHERE session_id") {
			return nil, errors.New("load messages query fail")
		}
		return origDBQuery(db, ctx, query, args...)
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load messages query error")
	}

	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "FROM messages WHERE session_id") {
			return db.QueryContext(ctx, "SELECT 1")
		}
		return origDBQuery(db, ctx, query, args...)
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load messages scan error")
	}

	// loadQueries query and scan errors
	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "FROM queries WHERE session_id") {
			return nil, errors.New("load queries query fail")
		}
		return origDBQuery(db, ctx, query, args...)
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load queries query error")
	}

	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "FROM queries WHERE session_id") {
			return db.QueryContext(ctx, "SELECT 1")
		}
		return origDBQuery(db, ctx, query, args...)
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load queries scan error")
	}

	// loadRecordSets query and scan errors
	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "FROM recordsets WHERE session_id") {
			return nil, errors.New("load recordsets query fail")
		}
		return origDBQuery(db, ctx, query, args...)
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load recordsets query error")
	}

	dbQueryContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (*sql.Rows, error) {
		if strings.Contains(query, "FROM recordsets WHERE session_id") {
			return db.QueryContext(ctx, "SELECT 1")
		}
		return origDBQuery(db, ctx, query, args...)
	}
	if _, err := store.Load(ctx, s.ID); err == nil {
		t.Errorf("expected load recordsets scan error")
	}
	dbQueryContextFn = origDBQuery

	// Clear error branches
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "DELETE FROM") {
			return nil, errors.New("delete fail")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if err := store.Clear(ctx, s.ID); err == nil {
		t.Errorf("expected delete fail in Clear")
	}

	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE sessions SET title = 'New chat'") {
			return nil, errors.New("update clear fail")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if err := store.Clear(ctx, s.ID); err == nil {
		t.Errorf("expected update clear fail in Clear")
	}
	txExecContextFn = origTxExecCtx

	// updateSession RowsAffected error branch
	origExecCtx := execContextFn
	defer func() { execContextFn = origExecCtx }()
	execContextFn = func(db *sql.DB, ctx context.Context, query string, args ...any) (sql.Result, error) {
		return mockRowsAffectedErrorResult{}, nil
	}
	if err := store.Rename(ctx, s.ID, "New Name"); err == nil {
		t.Errorf("expected rows affected error")
	}
	execContextFn = origExecCtx

	// sessionExists query error (not ErrNoRows)
	origTxQueryRow := txQueryRowContextFn
	defer func() { txQueryRowContextFn = origTxQueryRow }()
	txQueryRowContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) *sql.Row {
		if strings.Contains(query, "SELECT 1 FROM sessions WHERE id = ?") {
			// Query that fails scan
			return tx.QueryRowContext(ctx, "SELECT 'not_an_int_when_empty' WHERE 1=0") // ErrNoRows is handled, so let's force real scan error
		}
		return origTxQueryRow(tx, ctx, query, args...)
	}
	// To force scan error other than ErrNoRows: select a string into an int
	txQueryRowContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) *sql.Row {
		if strings.Contains(query, "SELECT 1 FROM sessions WHERE id = ?") {
			return tx.QueryRowContext(ctx, "SELECT 'not_an_integer'")
		}
		return origTxQueryRow(tx, ctx, query, args...)
	}
	if _, err := store.AppendUser(ctx, s.ID, "hi"); err == nil {
		t.Errorf("expected scan error in sessionExists")
	}
	txQueryRowContextFn = origTxQueryRow

	// AppendUser error branches
	sessionExistsFn = func(ctx context.Context, tx *sql.Tx, id, scope string) error {
		return errors.New("session exists fail")
	}
	if _, err := store.AppendUser(ctx, s.ID, "hi"); err == nil {
		t.Errorf("expected session exists fail in AppendUser")
	}
	sessionExistsFn = origSessionExists

	origInsertMsg := insertMessageFn
	defer func() { insertMessageFn = origInsertMsg }()
	insertMessageFn = func(ctx context.Context, tx *sql.Tx, sessionID string, message ChatMessage) error {
		return errors.New("insert msg fail")
	}
	if _, err := store.AppendUser(ctx, s.ID, "hi"); err == nil {
		t.Errorf("expected insert msg fail in AppendUser")
	}
	insertMessageFn = origInsertMsg

	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE sessions SET updated_at") {
			return nil, errors.New("update session updated_at fail in AppendUser")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if _, err := store.AppendUser(ctx, s.ID, "hi"); err == nil {
		t.Errorf("expected update session fail in AppendUser")
	}

	txExecContextFn = origTxExecCtx
	txCommitFn = func(tx *sql.Tx) error {
		return errors.New("tx commit fail in AppendUser")
	}
	if _, err := store.AppendUser(ctx, s.ID, "hi"); err == nil {
		t.Errorf("expected tx commit fail in AppendUser")
	}
	txCommitFn = origTxCommit

	// checkOrigin scan error other than ErrNoRows
	txQueryRowContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) *sql.Row {
		if strings.Contains(query, "SELECT 1 FROM messages WHERE id = ?") {
			return tx.QueryRowContext(ctx, "SELECT 'not_an_int'")
		}
		return origTxQueryRow(tx, ctx, query, args...)
	}
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{}); err == nil {
		t.Errorf("expected checkOrigin scan error")
	}
	txQueryRowContextFn = origTxQueryRow

	// appendQueryTx error branches
	// parameters marshal error
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{
		Parameters: map[string]any{"bad": make(chan int)},
	}); err == nil {
		t.Errorf("expected params marshal error")
	}

	// queries insert error
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO queries") {
			return nil, errors.New("insert queries fail")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{}); err == nil {
		t.Errorf("expected insert queries fail")
	}
	txExecContextFn = origTxExecCtx

	// encodeResultFn error
	origEncodeResult := encodeResultFn
	defer func() { encodeResultFn = origEncodeResult }()
	encodeResultFn = func(result secureread.Result) ([]byte, error) {
		return nil, errors.New("encode result fail")
	}
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{}); err == nil {
		t.Errorf("expected encode result fail")
	}
	encodeResultFn = origEncodeResult

	// jsonMarshalLineage error
	origMarshalLineage := jsonMarshalLineage
	defer func() { jsonMarshalLineage = origMarshalLineage }()
	jsonMarshalLineage = func(v any) ([]byte, error) {
		return nil, errors.New("marshal lineage fail")
	}
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{
		Lineage: &JoinLineage{ParentRecordSetID: "p1", CandidateID: "c1"},
	}); err == nil {
		t.Errorf("expected marshal lineage fail")
	}
	jsonMarshalLineage = origMarshalLineage

	// recordsets insert error
	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "INSERT INTO recordsets") {
			return nil, errors.New("insert recordsets fail")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{}); err == nil {
		t.Errorf("expected insert recordsets fail")
	}
	txExecContextFn = origTxExecCtx

	// AppendQuery update & commit errors
	origAppendQueryTx := appendQueryTxFn
	defer func() { appendQueryTxFn = origAppendQueryTx }()
	appendQueryTxFn = func(s *SessionStore, ctx context.Context, tx *sql.Tx, sessionID, originID, source string, query *QueryResult, now time.Time) error {
		return errors.New("append query tx fail")
	}
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{}); err == nil {
		t.Errorf("expected append query tx fail")
	}
	appendQueryTxFn = origAppendQueryTx

	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE sessions SET updated_at") {
			return nil, errors.New("update session updated_at fail in AppendQuery")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{}); err == nil {
		t.Errorf("expected update session fail in AppendQuery")
	}

	txExecContextFn = origTxExecCtx
	txCommitFn = func(tx *sql.Tx) error {
		return errors.New("tx commit fail in AppendQuery")
	}
	if _, err := store.AppendQuery(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", QueryResult{}); err == nil {
		t.Errorf("expected tx commit fail in AppendQuery")
	}
	txCommitFn = origTxCommit

	// AppendTurn error branches
	sessionExistsFn = func(ctx context.Context, tx *sql.Tx, id, scope string) error {
		return errors.New("checkOrigin fail in AppendTurn")
	}
	if _, err := store.AppendTurn(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", Turn{Text: "turn"}); err == nil {
		t.Errorf("expected checkOrigin fail in AppendTurn")
	}
	sessionExistsFn = origSessionExists

	insertMessageFn = func(ctx context.Context, tx *sql.Tx, sessionID string, message ChatMessage) error {
		return errors.New("insert msg fail in AppendTurn")
	}
	if _, err := store.AppendTurn(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", Turn{Text: "turn text"}); err == nil {
		t.Errorf("expected insert msg fail in AppendTurn")
	}
	insertMessageFn = origInsertMsg

	appendQueryTxFn = func(s *SessionStore, ctx context.Context, tx *sql.Tx, sessionID, originID, source string, query *QueryResult, now time.Time) error {
		return errors.New("append query tx fail in AppendTurn")
	}
	if _, err := store.AppendTurn(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", Turn{
		Queries: []QueryResult{{Title: "q1"}},
	}); err == nil {
		t.Errorf("expected append query tx fail in AppendTurn")
	}
	appendQueryTxFn = origAppendQueryTx

	txExecContextFn = func(tx *sql.Tx, ctx context.Context, query string, args ...any) (sql.Result, error) {
		if strings.Contains(query, "UPDATE sessions SET updated_at") {
			return nil, errors.New("update session updated_at fail in AppendTurn")
		}
		return origTxExecCtx(tx, ctx, query, args...)
	}
	if _, err := store.AppendTurn(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", Turn{}); err == nil {
		t.Errorf("expected update session fail in AppendTurn")
	}

	txExecContextFn = origTxExecCtx
	txCommitFn = func(tx *sql.Tx) error {
		return errors.New("tx commit fail in AppendTurn")
	}
	if _, err := store.AppendTurn(ctx, s.ID, uMsg.ID, "sqlite:///chinook.db", Turn{}); err == nil {
		t.Errorf("expected tx commit fail in AppendTurn")
	}
	txCommitFn = origTxCommit
}

