package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/tugql"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/ai"
)

func TestCoverageAgentRunQueryTugQLFallback(t *testing.T) {
	t.Parallel()
	conv := &AIConversation{
		runDTQLOverride: func(ctx context.Context, exec DTQLExecutor, source string, args runDTQLArgs) (runDTQLResponse, error) {
			if args.DTQL != "SELECT 42" {
				t.Fatalf("expected DTQL to be 'SELECT 42', got %q", args.DTQL)
			}
			return runDTQLResponse{}, nil
		},
	}
	handlers := conv.handlers()
	handler := handlers[toolRunDTQL]
	call := ai.ToolCall{
		ID:        "call1",
		Arguments: json.RawMessage(`{"tugql":"SELECT 42"}`),
	}
	_, err := handler(context.Background(), call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCoverageRestoreRecordSetSortStateDockKindMismatch(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	u.snapshot.Workspace.Docks = []Dock{
		{
			ID: "dock1",
			Reference: ContextReference{
				Kind:     "table",
				ObjectID: "users",
			},
		},
	}
	// Calling syncRecordSetSort must hit the continue branch where dock.Reference.Kind != "recordset"
	u.syncRecordSetSort("rs1", 0, false)
}

func TestCoverageJoinBlockWorkspaceNoGridKey(t *testing.T) {
	t.Parallel()
	b := &JoinBlock{workspace: true, Grid: nil}
	updated, cmd := b.Update(tea.KeyPressMsg{Text: "x"})
	if updated != b || cmd != nil {
		t.Fatalf("expected b and nil cmd, got %v, %v", updated, cmd)
	}
}

func TestCoverageSessionChatSwitchRestoreArchiveError(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, testStorePath(t), testScope())

	ctx := context.Background()
	s1, err := store.Create(ctx, "Chat 1")
	if err != nil {
		t.Fatalf("create chat 1: %v", err)
	}
	s2, err := store.Create(ctx, "Chat 2")
	if err != nil {
		t.Fatalf("create chat 2: %v", err)
	}

	chat := &SessionChat{store: store, activeID: s1.ID}

	// Drop session_archives so RestoreArchive fails during Switch
	if _, err := store.db.Exec("DROP TABLE session_archives"); err != nil {
		t.Fatalf("drop session_archives: %v", err)
	}

	_, err = chat.Switch(ctx, s2.ID)
	if err == nil {
		t.Fatal("expected error from Switch when session_archives table is missing")
	}
}

func TestCoveragePhysicalRowIndexOutOfRange(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	g := newGridState(NewGridModel(secureread.Result{
		Columns: []string{"c1", "c2", "c3"},
	}), "rs1", "Title", 80)
	// record only has 1 column, but g has 3 columns -> i >= len(record.Result.Columns)
	record := &RecordSet{
		Result: secureread.Result{
			Columns: []string{"c1"},
		},
	}
	row := u.physicalRow(g, record)
	if row == nil {
		t.Fatal("expected non-nil row")
	}
}

func TestCoverageRunSavedQueryBackendBusy(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	u.savedQueryService = &savedQueryStub{}
	// Set an active operation so backendBusy() is true
	u.beginOperation("Busy Op")
	cmd := u.runSavedQuery(SavedQuery{ID: "q1", Title: "Query 1"}, nil)
	if cmd != nil {
		t.Fatalf("expected nil cmd, got %v", cmd)
	}
	if u.workspaceFeedback != "Cancel the running operation before starting another." {
		t.Fatalf("expected feedback about canceling running operation, got %q", u.workspaceFeedback)
	}
}

func TestCoverageGridStateProvenanceMissingAndHomeKey(t *testing.T) {
	t.Parallel()
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"A"}}), "rs1", "Title", 80)
	// record with unknown HTTPResponseID -> "HTTP request provenance unavailable."
	g.bindQuery(RecordSet{HTTPResponseID: "unknown-resp"}, ChatSession{})
	if !strings.Contains(g.queryText, "HTTP request provenance unavailable.") {
		t.Fatalf("expected provenance unavailable text, got %q", g.queryText)
	}

	// queryExtraView with empty text -> "Query provenance unavailable for this result."
	gEmpty := newGridState(NewGridModel(secureread.Result{Columns: []string{"A"}}), "rs2", "Title", 80)
	extra := gEmpty.queryExtraView()
	rendered := extra.Render(nil, 80, 24)
	if !strings.Contains(rendered, "Query provenance unavailable for this result.") {
		t.Fatalf("expected rendered text to contain unavailable message, got %q", rendered)
	}

	// Test viewportScrollUpdate "home" key
	vp := viewport.New(viewport.WithWidth(40), viewport.WithHeight(10))
	vp.SetContent("line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\nline 11\nline 12")
	vp.GotoBottom()
	fn := viewportScrollUpdate(&vp)
	cmd, handled := fn(nil, tea.KeyPressMsg{Code: tea.KeyHome})
	if !handled || cmd != nil {
		t.Fatalf("expected handled=true, cmd=nil for home key, got %v, %v", handled, cmd)
	}
	if vp.YOffset() != 0 {
		t.Fatalf("expected YOffset 0 after home, got %d", vp.YOffset())
	}
}

func TestCoverageStoreSessionArchivesViewConflictAndLatestOrCreate(t *testing.T) {
	// Not parallel: temporarily mutates package-level dbExec seam
	origDBExec := dbExec
	t.Cleanup(func() { dbExec = origDBExec })

	dbExec = func(db *sql.DB, query string) (sql.Result, error) {
		if strings.Contains(query, "session_archives") {
			return nil, errors.New("simulated session_archives init error")
		}
		return origDBExec(db, query)
	}

	validDir := testStorePath(t)
	_, err := OpenSessionStore(validDir, testScope())
	if err == nil || !strings.Contains(err.Error(), "initialize session archives") {
		t.Fatalf("expected initialize session archives error, got %v", err)
	}
	dbExec = origDBExec

	// Test LatestOrCreate when ArchivedSessionIDs fails
	validDir2 := testStorePath(t)
	store := openTestStore(t, validDir2, testScope())

	if _, err := store.db.Exec("DROP TABLE session_archives"); err != nil {
		t.Fatalf("drop session_archives: %v", err)
	}
	_, err = store.LatestOrCreate(context.Background())
	if err == nil {
		t.Fatal("expected error from LatestOrCreate when session_archives table is missing")
	}
}

func TestCoverageDecodeExactJSONTrailing(t *testing.T) {
	t.Parallel()
	var v map[string]any
	// Multiple JSON values -> unexpected trailing JSON value
	err := decodeExactJSON([]byte(`{"a": 1} {"b": 2}`), &v)
	if err == nil || !strings.Contains(err.Error(), "unexpected trailing JSON value") {
		t.Fatalf("expected unexpected trailing JSON value error, got %v", err)
	}

	// Malformed trailing data -> syntax error
	err = decodeExactJSON([]byte(`{"a": 1} not_json`), &v)
	if err == nil {
		t.Fatal("expected error from decodeExactJSON with trailing garbage")
	}
}

func TestCoverageStoreArchiveAllBranches(t *testing.T) {
	t.Parallel()
	validDir := testStorePath(t)
	store := openTestStore(t, validDir, testScope())

	ctx := context.Background()
	s1, err := store.Create(ctx, "Chat 1")
	if err != nil {
		t.Fatalf("create chat 1: %v", err)
	}

	// RestoreArchive with nonexistent id returns error from Load
	if err := store.RestoreArchive(ctx, "nonexistent"); err == nil {
		t.Fatal("expected error restoring nonexistent archive")
	}

	chat := &SessionChat{store: store, activeID: s1.ID}

	// ArchiveActive with expectedID mismatch
	_, err = chat.ArchiveActive(ctx, "wrong-id")
	if err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}

	// DeleteActive with expectedID mismatch
	_, err = chat.DeleteActive(ctx, "wrong-id")
	if err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}

	// ArchiveActive when it is the ONLY session: next.ID == "" -> creates "New chat"
	next, err := chat.ArchiveActive(ctx, s1.ID)
	if err != nil {
		t.Fatalf("unexpected error archiving only session: %v", err)
	}
	if next.Title != "New chat" {
		t.Fatalf("expected new chat to be created, got title %q", next.Title)
	}

	// Drop session_archives to trigger Archive error in ArchiveActive
	if _, err := store.db.Exec("DROP TABLE session_archives"); err != nil {
		t.Fatalf("drop session_archives: %v", err)
	}
	_, err = chat.ArchiveActive(ctx, next.ID)
	if err == nil {
		t.Fatal("expected error from ArchiveActive when session_archives is missing")
	}

	// DeleteActive when LatestOrCreate fails (due to missing session_archives)
	_, err = chat.DeleteActive(ctx, next.ID)
	if err == nil {
		t.Fatal("expected error from DeleteActive when session_archives is missing")
	}

	// Archive on store with missing session_archives
	if err := store.Archive(ctx, s1.ID); err == nil {
		t.Fatal("expected error from store.Archive when session_archives is missing")
	}
}

func TestCoverageResultAggregateControlsBranches(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = ProjectCatalog{
		Objects: []ProjectObject{
			{
				Reference: ContextReference{SourceID: "db1", ObjectID: "users"},
			},
		},
	}

	o := &resultOperationOverlay{
		ui:      u,
		capture: &loadedResultCapture{columnNames: []string{"id", "val"}},
		record:  RecordSet{Database: "db1", Result: secureread.Result{Columns: []string{"id", "val"}}},
	}

	// defaultSumEligible with object == nil
	if o.defaultSumEligible(0) {
		t.Fatal("expected false when object is not matched")
	}

	// aggregateFunction out of range
	if fn := o.aggregateFunction(-1); fn != "" {
		t.Fatalf("expected empty for negative column, got %q", fn)
	}
	if fn := o.aggregateFunction(10); fn != "" {
		t.Fatalf("expected empty for out of range column, got %q", fn)
	}

	// setAggregate with column < 0
	o.setAggregate(-1, "SUM")

	// setAggregate on grouping key
	o.selected = []int{0}
	o.setAggregate(0, "SUM")
	if o.err != "This is a grouping key. Remove its key selection before aggregating it." {
		t.Fatalf("expected grouping key error, got %q", o.err)
	}

	// setAggregate with nil aggregates map initializes map
	o.selected = nil
	o.aggregates = nil
	o.setAggregate(1, "COUNT")
	if o.aggregates[1] != "COUNT" {
		t.Fatalf("expected COUNT aggregate set, got %v", o.aggregates)
	}

	// formatAggregateValue for *big.Rat with non-terminating denominator
	rat := big.NewRat(1, 3)
	if s := formatAggregateValue(rat); s != "1/3" {
		t.Fatalf("expected '1/3', got %q", s)
	}

	// aggregateExplanation
	exp := o.aggregateExplanation(1)
	if exp == "" {
		t.Fatal("expected non-empty explanation")
	}
}

func TestCoverageWorkspaceQueryBranches(t *testing.T) {
	t.Parallel()
	// objectQueryDocument validation errors
	_, err := objectQueryDocument(ProjectObject{Reference: ContextReference{Kind: "invalid"}})
	if err == nil || !strings.Contains(err.Error(), "select a table or view") {
		t.Fatalf("expected select a table or view, got %v", err)
	}

	// Single part name -> "main", parts[0]
	doc, err := objectQueryDocument(ProjectObject{Reference: ContextReference{Kind: "table", ObjectID: "users"}})
	if err != nil {
		t.Fatalf("unexpected error for single part: %v", err)
	}
	if !strings.Contains(doc, "name: users") {
		t.Fatalf("expected doc to contain name: users, got %q", doc)
	}

	// Incomplete parts (empty)
	_, err = objectQueryDocument(ProjectObject{Reference: ContextReference{Kind: "table", ObjectID: "main."}})
	if err == nil || !strings.Contains(err.Error(), "relation metadata is incomplete") {
		t.Fatalf("expected relation metadata is incomplete, got %v", err)
	}

	// executeWorkspaceQuery branches
	validDir := testStorePath(t)
	scope := testScope()
	scope.Sources = map[string]string{"chinook": "sqlite:///test.db", "src1": "sqlite:///test.db"}
	store := openTestStore(t, validDir, scope)

	ctx := context.Background()
	s1, err := store.Create(ctx, "Query test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	chat := &SessionChat{store: store, activeID: s1.ID}

	ref := ContextReference{Kind: "table", SourceID: "src1", ObjectID: "main.users", Title: "Users"}

	// activeID mismatch
	_, err = chat.executeWorkspaceQuery(ctx, "wrong-id", ref, "SELECT 1")
	if err != ErrActiveSessionChanged {
		t.Fatalf("expected ErrActiveSessionChanged, got %v", err)
	}

	// relation is unavailable (no catalog or empty objects)
	_, err = chat.executeWorkspaceQuery(ctx, s1.ID, ref, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "selected relation is unavailable") {
		t.Fatalf("expected selected relation is unavailable, got %v", err)
	}

	// relation known, but source cannot execute queries (nil queryExecutor)
	chat.catalog = ProjectCatalog{
		Objects: []ProjectObject{
			{Reference: ref},
		},
	}
	_, err = chat.executeWorkspaceQuery(ctx, s1.ID, ref, "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "this source cannot execute queries") {
		t.Fatalf("expected this source cannot execute queries, got %v", err)
	}

	// Invalid DTQL query or invalid limit
	fakeExec := &mockExecutorRunner{}
	chat.queryExecutor = fakeExec
	_, err = chat.executeWorkspaceQuery(ctx, s1.ID, ref, "INVALID DTQL")
	if err == nil || !strings.Contains(err.Error(), "use a valid DTQL query") {
		t.Fatalf("expected use a valid DTQL query, got %v", err)
	}

	// Query execution failure
	validQuery := dal.From(dal.NewQualifiedRootCollectionRef("main", "users", "")).NewQuery().Limit(5).SelectColumns()
	validDoc, _ := tugql.Serialize(validQuery)
	fakeExec.err = errors.New("simulated execution error")
	_, err = chat.executeWorkspaceQuery(ctx, s1.ID, ref, string(validDoc))
	if err == nil || !strings.Contains(err.Error(), "query failed") {
		t.Fatalf("expected query failed, got %v", err)
	}
}

type mockExecutorRunner struct {
	err error
	res secureread.Result
}

func (m *mockExecutorRunner) RunDTQL(ctx context.Context, source string, query []byte, parameters map[string]any) (secureread.Result, error) {
	return m.res, m.err
}

func TestCoverageWorkspaceCatalogDetailsAllMethods(t *testing.T) {
	t.Parallel()
	obj := ProjectObject{
		Reference: ContextReference{
			Title: "Test Catalog Object",
		},
		QueryText: "Detailed catalog description",
	}
	overlay := newCatalogDetailsOverlay(obj)
	hints := overlay.HelpHints()
	if len(hints) == 0 {
		t.Fatal("expected non-empty hints")
	}

	view := overlay.View(80, 24)
	if !strings.Contains(view, "Test Catalog Object") {
		t.Fatalf("expected view to contain title, got %q", view)
	}

	// Esc returns true to close
	_, _, closed := overlay.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !closed {
		t.Fatal("expected Esc to close overlay")
	}

	// Home key
	_, _, closed = overlay.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if closed {
		t.Fatal("expected home not to close")
	}

	// End key
	_, _, closed = overlay.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if closed {
		t.Fatal("expected end not to close")
	}

	// Default viewport update
	_, cmd, closed := overlay.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if closed || cmd != nil {
		t.Fatalf("expected default update to not close, got cmd=%v", cmd)
	}
}

func TestCoverageWorkspaceActionsDispatchAndKeyboardOverlay(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})

	// Test dispatchWorkspaceAction cases
	actions := []string{
		"loaded-up", "server-up", "server-down",
		"back", "forward", "result-actions",
		"sessions", "new-chat", "close-chat",
		"archive-chat", "delete-chat",
		"inspect-section", "docked-section", "bookmarks-section",
		"related", "expand", "composer", "explorer", "inspect", "api", "help",
		"unknown-action",
	}
	for _, act := range actions {
		_ = u.dispatchWorkspaceAction(act)
	}

	// Test keyboardCheckOverlay
	kOverlay := &keyboardCheckOverlay{closeY: 5}
	if !kOverlay.LocalMouseCoordinates() {
		t.Fatal("expected LocalMouseCoordinates true")
	}
	if len(kOverlay.HelpHints()) == 0 {
		t.Fatal("expected non-empty HelpHints")
	}

	// Click on closeY closes overlay
	_, _, closed := kOverlay.Update(tea.MouseClickMsg{Button: tea.MouseLeft, Y: 5})
	if !closed {
		t.Fatal("expected click on closeY to close overlay")
	}

	// Type keys until len(o.keys) > 32 triggers trimming
	for i := 0; i < 35; i++ {
		_, _, _ = kOverlay.Update(tea.KeyPressMsg{Text: "a"})
	}
	if len(kOverlay.keys) > 32 {
		t.Fatalf("expected keys trimmed to at most 32, got %d", len(kOverlay.keys))
	}
}

func TestCoverageChatUIHTTPOverlayBranches(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})

	// sendHTTPRequest when backendBusy returns error
	u.beginOperation("Busy Op")
	_, err := u.sendHTTPRequest(httpRequestSpec{Method: "GET", URL: "https://example.test"})
	if err == nil || !strings.Contains(err.Error(), "cancel the running operation") {
		t.Fatalf("expected cancel the running operation error, got %v", err)
	}

	// httpRequestOverlay with operation active
	overlay := newHTTPRequestOverlay(u, httpRequestSpec{})
	// View with operation active shows operation label
	view := overlay.View(80, 24)
	if !strings.Contains(view, "Busy Op") {
		t.Fatalf("expected view to contain Busy Op, got %q", view)
	}

	// Esc / Ctrl+X cancels operation
	_, _, closed := overlay.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if closed {
		t.Fatal("expected esc during operation not to close overlay but cancel operation")
	}
	if overlay.err != "Request canceled; draft retained." {
		t.Fatalf("expected canceled draft retained error, got %q", overlay.err)
	}

	// Mouse click on Cancel [Ctrl+X] header
	u.beginOperation("Op 2")
	overlay.err = ""
	_, _, _ = overlay.Update(tea.MouseClickMsg{Button: tea.MouseLeft, Y: 0, X: 5})
}

func TestCoverageChatUIPickersGlobalKeysAndArchivedView(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})

	// chatTransition == true -> feedback "Closing or switching chat…"
	u.chatTransition = true
	cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3})
	if !handled || cmd != nil || u.workspaceFeedback != "Closing or switching chat…" {
		t.Fatalf("expected handled true and transition feedback, got %v, %v, %q", handled, cmd, u.workspaceFeedback)
	}
	u.chatTransition = false

	// Alt+0 with sidebar focus -> index 9
	u.shell.FocusRightPanel()
	u.workspace.relatedRecords = make([]relatedRecord, 10)
	_, handled = u.globalKeys(tea.KeyPressMsg{Code: '0', Mod: tea.ModAlt})
	if !handled {
		t.Fatal("expected Alt+0 to be handled")
	}

	// F1 pushes hotkeys overlay
	_, handled = u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF1})
	if !handled {
		t.Fatal("expected F1 to be handled")
	}

	// F3 when sessionFence is true returns nil, true
	u.beginOperation("Fence Op")
	cmd, handled = u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3})
	if !handled || cmd != nil {
		t.Fatalf("expected F3 with sessionFence handled with nil cmd, got %v, %v", handled, cmd)
	}
	u.cancelOperation(true)

	// openSessionPicker when sessions is nil
	u.sessions = nil
	if cmd := u.openSessionPicker(); cmd != nil {
		t.Fatal("expected nil cmd from openSessionPicker when sessions is nil")
	}

	// sessionPickerOverlay View with archived session and height limit
	sPicker := &sessionPickerOverlay{
		sessions: []ChatSession{
			{ID: "s1", Title: "Session 1"},
			{ID: "s2", Title: "Session 2"},
		},
		archived: map[string]bool{"s1": true},
	}
	pView := sPicker.View(80, 5)
	if !strings.Contains(pView, "[archived]") {
		t.Fatalf("expected view to contain [archived], got %q", pView)
	}
}

func TestCoverageWorkspaceTrailBranches(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})

	// rememberResult with record.ID == "" returns early
	u.rememberResult(RecordSet{})

	// trailPath with len(labels) >= 32
	u.trailNodes = map[string]*explorationNode{}
	curr := "node0"
	for i := 0; i < 35; i++ {
		parent := fmt.Sprintf("node%d", i+1)
		u.trailNodes[curr] = &explorationNode{id: curr, parent: parent, label: fmt.Sprintf("N%d", i)}
		curr = parent
	}
	path := u.trailPath("node0")
	if !strings.Contains(path, "[earlier ancestors…]") {
		t.Fatalf("expected path to contain '[earlier ancestors…]', got %q", path)
	}

	// showTrailNode with missing node
	if u.showTrailNode("nonexistent") {
		t.Fatal("expected false for nonexistent trail node")
	}
	if u.workspaceFeedback != "This trail result is unavailable." {
		t.Fatalf("expected unavailable feedback, got %q", u.workspaceFeedback)
	}

	// showTrailNode with node that has missing grid
	u.trailNodes["n_missing_g"] = &explorationNode{id: "n_missing_g", recordID: "rs_missing"}
	if u.showTrailNode("n_missing_g") {
		t.Fatal("expected false for trail node with missing grid")
	}
	if u.workspaceFeedback != "This retained result is unavailable." {
		t.Fatalf("expected unavailable feedback, got %q", u.workspaceFeedback)
	}

	// visitTrail with failed showTrailNode
	u.visitTrail("nonexistent")

	// trailMove out of range
	u.trailHistory = []string{"n1"}
	u.trailPosition = 0
	u.trailMove(-1)
	if u.workspaceFeedback != "No retained result in this direction. Ctrl+T opens the trail." {
		t.Fatalf("expected trail boundary feedback, got %q", u.workspaceFeedback)
	}

	// explorationTrailOverlay Esc key
	tOverlay := &explorationTrailOverlay{}
	_, _, closed := tOverlay.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !closed {
		t.Fatal("expected Esc to close trail overlay")
	}

	// rememberLocal with nil trailNodes
	u.trailNodes = nil
	p := &localGroupsPanel{trailID: "t1"}
	u.rememberLocal(p, "parent", "label")
	if u.trailNodes[p.trailID] == nil {
		t.Fatal("expected trail node registered")
	}

	// trailNavigationReason
	u.shell.SetCenterPanel(&localGroupsPanel{trailID: "t1"}, true)
	u.trailNodes = map[string]*explorationNode{"t1": {id: "t1"}}
	u.trailHistory = []string{"t1", "t2"}
	u.trailPosition = 0
	if r := u.trailNavigationReason(1); r != "" {
		t.Fatalf("expected empty reason when next is in range, got %q", r)
	}

	// reconcileResults with mismatched session ID returns early
	u.sessionID = "current_session"
	u.reconcileResults(ChatSession{ID: "other_session"})

	// reconcileResults with HTTP response and markdown message
	u.transcriptMessageEntries = map[string]string{}
	u.httpBlocksByMessageID = map[string]*httpDocumentBlock{}
	u.reconcileResults(ChatSession{
		ID: "current_session",
		Messages: []ChatMessage{
			{ID: "m1", HTTPResponseID: "resp1"},
			{ID: "m2", Kind: "markdown", Text: "markdown text"},
		},
		HTTPResponses: map[string]HTTPResponse{
			"resp1": {StatusCode: 200, Body: []byte("ok")},
		},
	})
}
