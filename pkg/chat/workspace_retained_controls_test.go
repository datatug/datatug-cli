package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/datatug/datatug-cli/pkg/queryops"
	"github.com/datatug/datatug-cli/pkg/resultops"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func TestUnifiedWorkspaceBrowserFallbackAndBusyVisibleAtBothWidths(t *testing.T) {
	t.Parallel()
	for _, width := range []int{80, 150} {
		u, _ := newTestChatUI(t, nil, Turn{})
		u.shell.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		u.browserURL = "https://example.test/chat?cap=local-test"
		u.openBrowser = nil
		u.shell.Update(uitest.Key("f5"))
		view := u.shell.View().Content
		if !u.webLinkVisible || !strings.Contains(view, u.browserURL) || !strings.Contains(ansi.Strip(view), "Open web chat") {
			t.Fatalf("width %d lost usable browser fallback: %s", width, view)
		}
		u.shell.Update(uitest.Key("esc"))
		if u.webLinkVisible {
			t.Fatal("Esc did not hide link")
		}
		u.shell.SetBusy(true)
		if view = ansi.Strip(u.shell.View().Content); !strings.Contains(view, "Thinking…") || !strings.Contains(view, "fake-model") {
			t.Fatalf("width %d lost busy/model indicator: %s", width, view)
		}
		u.shell.SetBusy(false)
		if strings.Contains(ansi.Strip(u.shell.View().Content), "Thinking…") {
			t.Fatal("busy indicator survived completed turn")
		}
	}
}

func TestUnifiedWorkspaceBookmarkEscCancelsOneLayerWithoutMutation(t *testing.T) {
	t.Parallel()
	u, sessions := newTestChatUI(t, nil, Turn{})
	session, ref := bookmarkableSelection(t, sessions.store)
	if _, err := sessions.store.CreateBookmark(u.ctx, session.ID, ref, "Saved invoice selection"); err != nil {
		t.Fatal(err)
	}
	if err := u.workspace.refreshBookmarks(); err != nil {
		t.Fatal(err)
	}
	before, err := sessions.FindBookmarks(u.ctx, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	u.workspace.setTab(3)
	u.shell.FocusRightPanel()
	for _, mode := range []string{"rename", "tag_add", "tag_remove", "search", "tags", "delete"} {
		u.workspace.startBookmarkInput(mode, "Value")
		u.workspace.bookmarkEditor.SetValue("delete")
		if hint := u.statusBar(80); !strings.Contains(hint, "Esc cancel") || !strings.Contains(hint, mode) {
			t.Fatalf("mode %s misleading hint: %s", mode, hint)
		}
		u.shell.Update(uitest.Key("esc"))
		if u.workspace.bookmarkMode != "" || u.workspace.bookmarkEditor.Focused() || u.shell.WorkspaceFocus() != "sidebar" {
			t.Fatalf("mode %s did not cancel one layer", mode)
		}
	}
	after, err := sessions.FindBookmarks(u.ctx, "", nil)
	if err != nil || !reflect.DeepEqual(before, after) || u.workspace.bookmarkSearch != "" || len(u.workspace.bookmarkTags) != 0 {
		t.Fatalf("cancel mutated durable bookmarks or filters: %v", err)
	}
}

func TestUnifiedWorkspaceMixedSQLiteNumbersSurviveStoreReloadAndLocalOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mixed.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("CREATE TABLE Mixed (Id INTEGER PRIMARY KEY, Amount REAL); INSERT INTO Mixed VALUES (9007199254740993,19.8),(9007199254740994,2.5)"); err != nil {
		t.Fatal(err)
	}
	source := "sqlite://" + path
	executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
	document := "from: {schema: main, name: Mixed}\ncolumns: [{field: Id}, {field: Amount}]\norderBy: [{field: Id}]\nlimit: 10\n"
	result, err := executor.RunTugQL(ctx, source, []byte(document), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := result.Rows[0].Data["Id"].(int64); !ok || id != 9007199254740993 {
		t.Fatalf("secure reader lost exact integer: %#v", result.Rows[0].Data["Id"])
	}
	if amount, ok := result.Rows[0].Data["Amount"].(float64); !ok || amount != 19.8 {
		t.Fatalf("secure reader lost native REAL: %#v", result.Rows[0].Data["Amount"])
	}
	scope := testScope()
	scope.Sources[scope.Database] = source
	store := openTestStore(t, testStorePath(t), scope)
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, source, ProjectCatalog{ID: scope.ProjectID, Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Mixed", SourceID: scope.Database, Title: "Mixed"}, Columns: []string{"Id", "Amount"}, ColumnTypes: map[string]string{"Id": "INTEGER", "Amount": "REAL"}, PrimaryKey: []string{"Id"}}}})
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Load mixed values")
	if err != nil {
		t.Fatal(err)
	}
	query, err := store.AppendQuery(ctx, sessions.activeID, user.ID, source, QueryResult{Title: "Mixed", TugQL: document, SourceID: scope.Database, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, sessions.activeID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Rows, loaded.RecordSets[query.RecordSetID].Result.Rows) {
		t.Fatal("typed numeric result changed during durable reload")
	}
	u, err := NewSessionChatUI(ctx, sessions, "none")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	u.shell.FocusEntry(u.lastGridEntryID)
	g, record, ok := u.activeGrid()
	if !ok {
		t.Fatal("missing reloaded grid")
	}
	captured, err := u.captureLoaded(g, record)
	if err != nil {
		t.Fatal(err)
	}

	sessions.ConfigureQueryExecutor(executor)
	parent := loaded.RecordSets[query.RecordSetID]
	operation := derivedOperation{Group: true, Columns: []string{"Id"}, Count: true, Scope: queryops.OriginalOutput}
	preview, previewErr := deriveResultPreview(sessions.catalog, parent, operation)
	if previewErr != nil {
		t.Fatal(previewErr)
	}
	_, _, executionErr := sessions.executeDerivedResult(ctx, sessions.activeID, parent.ID, operation, preview.TugQL)
	if executionErr == nil {
		t.Fatal("generic OriginalOutput grouping collapsed adjacent BIGINT values instead of refusing")
	}
	after, loadErr := sessions.Snapshot(ctx)
	if loadErr != nil || len(after.RecordSets) != len(loaded.RecordSets) {
		t.Fatal("unsafe aggregation appended a child result")
	}
	operation.Scope = queryops.MatchingSource
	preview, previewErr = deriveResultPreview(sessions.catalog, parent, operation)
	if previewErr != nil {
		t.Fatal(previewErr)
	}
	_, nativeGroup, executionErr := sessions.executeDerivedResult(ctx, sessions.activeID, parent.ID, operation, preview.TugQL)
	if executionErr != nil || len(nativeGroup.Result.Rows) != 2 {
		t.Fatalf("native SQL grouping failed exact adjacent BIGINT: %v %+v", executionErr, nativeGroup.Result.Rows)
	}
	groups, err := captured.snapshot.GroupBy([]string{"Id"})
	if err != nil || len(groups) != 2 {
		t.Fatalf("exact durable grouping failed: %+v %v", groups, err)
	}
	order, err := captured.snapshot.Sort([]resultops.SortKey{{Column: "Amount"}})
	if err != nil || !reflect.DeepEqual(order, []int{1, 0}) {
		t.Fatalf("native REAL durable sorting failed: %v %v", order, err)
	}
}

func TestUnifiedWorkspaceUnsafePersistedNumericParametersRefuseWithoutAppending(t *testing.T) {
	t.Parallel()
	sessions, parent := derivedFixture(t)
	for _, value := range []any{json.Number("9007199254740993"), int64(9007199254740993), json.Number("0.10000000000000000001")} {
		parent.Parameters = map[string]any{"minimum": value}
		operation := derivedOperation{Columns: []string{"Id"}, Sort: []queryops.SortTerm{{Column: "Id"}}, Scope: queryops.MatchingSource}
		preview, err := deriveResultPreview(sessions.catalog, parent, operation)
		if err != nil {
			t.Fatal(err)
		}
		// Persist the exact parameter lexeme, then re-read through the real store.
		user, err := sessions.store.AppendUser(context.Background(), sessions.activeID, "Unsafe numeric binding")
		if err != nil {
			t.Fatal(err)
		}
		query, err := sessions.store.AppendQuery(context.Background(), sessions.activeID, user.ID, parent.Source, QueryResult{Title: parent.Title, TugQL: parent.TugQL, Parameters: parent.Parameters, SourceID: parent.Database, Result: parent.Result})
		if err != nil {
			t.Fatal(err)
		}
		before, err := sessions.Snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = sessions.executeDerivedResult(context.Background(), sessions.activeID, query.RecordSetID, operation, preview.TugQL)
		if err == nil {
			t.Fatalf("unsafe numeric parameter %T executed successfully", value)
		}
		after, loadErr := sessions.Snapshot(context.Background())
		if loadErr != nil || len(after.RecordSets) != len(before.RecordSets) {
			t.Fatal("refused parameter appended a derived result")
		}
	}
}

func TestUnifiedWorkspaceExplorerDetailsScrollAndResetThroughShell(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	query := "first-query-line\n" + strings.Repeat("middle-query-line\n", 35) + "last-query-line"
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "query", ObjectID: "q", Title: "Saved report"}, QueryType: "TugQL", QueryText: query}}}
	u.shell.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	u.shell.FocusLeftPanel()
	for i, node := range u.explorer.explorerNodes() {
		if node.label == "Saved report" {
			u.explorer.explorerIndex = i
			break
		}
	}
	u.explorer.inspectExplorerSelection()
	u.shell.FocusRightPanel()
	view := ansi.Strip(u.workspace.View(26, 20, true))
	if !strings.Contains(view, "first-query-line") || !strings.Contains(view, "Query: Saved report") {
		t.Fatalf("details not visible: %s", view)
	}
	u.shell.Update(uitest.Key("end"))
	if view = ansi.Strip(u.workspace.View(26, 20, true)); !strings.Contains(view, "last-query-line") {
		t.Fatalf("details cannot scroll to end: %s", view)
	}
	u.explorer.inspectExplorerSelection()
	if view = ansi.Strip(u.workspace.View(26, 20, true)); !strings.Contains(view, "first-query-line") || strings.Contains(view, "last-query-line") {
		t.Fatalf("reselected details retained stale offset: %s", view)
	}
}

func TestUnifiedWorkspaceQuickLoadedSortChordsAndModalOwnership(t *testing.T) {
	t.Parallel()
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Items", TugQL: "from: {schema: main, name: Item}\nlimit: 2\n", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 2}}, {Data: map[string]any{"Id": 1}}}}})
	g, record, _ := u.activeGrid()
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Item", SourceID: record.Database}, Columns: []string{"Id"}, ColumnTypes: map[string]string{"Id": "INTEGER"}}}}
	selected := g.sourceRowKey()
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt | tea.ModShift})
	if g.Cell(0, 0) != "1" || g.sourceRowKey() != selected {
		t.Fatal("AltShiftUp did not sort ascending and preserve selected source row")
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModAlt | tea.ModShift})
	if g.Cell(0, 0) != "2" || g.sourceRowKey() != selected {
		t.Fatal("AltShiftDown did not sort descending and preserve selected source row")
	}
	uxSearchAction(t, u, "Sort loaded")
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModAlt | tea.ModShift})
	if g.Cell(0, 0) != "2" {
		t.Fatal("modal arrow escaped into quick sort")
	}
}

func TestUnifiedWorkspaceModalHelpReturnsToRetainedGroupChoices(t *testing.T) {
	t.Parallel()
	sessions, _ := derivedFixture(t)
	u, err := NewSessionChatUI(context.Background(), sessions, "none")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	u.shell.FocusEntry(u.lastGridEntryID)
	operation, err := u.newResultOperation(true)
	if err != nil {
		t.Fatal(err)
	}
	operation.selected = []int{0}
	operation.count = true
	u.shell.PushOverlay(operation)
	u.shell.Update(uitest.Key("f1"))
	view := strings.Join(strings.Fields(ansi.Strip(u.shell.View().Content)), " ")
	if !strings.Contains(view, "Group / Sort result") || !strings.Contains(view, "Shift+Enter review server") {
		t.Fatalf("modal help described underlying grid: %s", view)
	}
	u.shell.Update(uitest.Key("esc"))
	if !operation.count || len(operation.selected) != 1 || !strings.Contains(ansi.Strip(u.shell.View().Content), "Count rows") {
		t.Fatal("help return lost group choices")
	}
}

func TestUnifiedWorkspaceQuickServerSortOpensExactPreviewWithoutExecution(t *testing.T) {
	t.Parallel()
	sessions, _ := derivedFixture(t)
	u, err := NewSessionChatUI(context.Background(), sessions, "none")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	u.shell.FocusEntry(u.lastGridEntryID)
	before := len(u.snapshot.RecordSets)
	for _, code := range []rune{tea.KeyUp, tea.KeyDown} {
		u.shell.Update(tea.KeyPressMsg{Code: code, Mod: tea.ModCtrl | tea.ModShift})
		view := ansi.Strip(u.shell.View().Content)
		if !strings.Contains(view, "Review exact server query") || !strings.Contains(view, "field: Id") || len(u.snapshot.RecordSets) != before {
			t.Fatalf("quick server sort did not open safe preview: %s", view)
		}
		if code == tea.KeyDown && !strings.Contains(view, "desc: true") {
			t.Fatalf("descending shortcut preview lost direction: %s", view)
		}
		_, cmd := u.shell.Update(uitest.Key("esc"))
		drainCmd(t, u, cmd)
		u.shell.Update(uitest.Key("esc"))
	}
}

func clickWorkspaceText(t *testing.T, u *ChatUI, label string) tea.Cmd {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(u.shell.View().Content), "\n") {
		if at := strings.Index(line, label); at >= 0 {
			_, cmd := u.shell.Update(tea.MouseClickMsg{X: ansi.StringWidth(line[:at]) + 1, Y: y, Button: tea.MouseLeft})
			return cmd
		}
	}
	t.Fatalf("missing clickable control %q: %s", label, ansi.Strip(u.shell.View().Content))
	return nil
}

func TestUnifiedWorkspaceToolbarTogglePreservesContextFeedbackAndModalPriority(t *testing.T) {
	t.Parallel()
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Items", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1}}}}})
	u.shell.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	u.shell.FocusComposer()
	u.shell.SetComposerText("keep draft")
	u.shell.Update(uitest.Key("esc"))
	selected := u.activeRecordSetID()
	u.shell.Update(uitest.Key("f9"))
	for _, key := range []string{"alt+f1", "ctrl+f1"} {
		u.shell.Update(uitest.Key(key))
		if !u.toolbarHidden || !strings.Contains(ansi.Strip(u.shell.View().Content), "keep draft") || !u.shell.Expanded() || u.activeRecordSetID() != selected {
			t.Fatal("toggle lost retained state")
		}
		first := strings.Split(ansi.Strip(u.shell.View().Content), "\n")[0]
		if !strings.Contains(first, "F1 Help") || !strings.Contains(first, "F8 Server ") {
			t.Fatalf("compact header lost recovery/status: %s", first)
		}
		u.shell.Update(uitest.Key("f1"))
		u.shell.Update(uitest.Key(key))
		if !u.toolbarHidden {
			t.Fatal("toolbar toggle escaped modal")
		}
		u.shell.Update(uitest.Key("esc"))
		u.browserURL = "https://example.test/local-chat"
		u.openBrowser = nil
		u.shell.Update(uitest.Key("f5"))
		if !strings.Contains(ansi.Strip(u.shell.View().Content), u.browserURL) {
			t.Fatal("hidden toolbar hid operational URL")
		}
		u.shell.Update(uitest.Key("esc"))
		u.shell.SetBusy(true)
		if !strings.Contains(ansi.Strip(u.shell.View().Content), "Thinking…") {
			t.Fatal("hidden toolbar hid busy status")
		}
		u.shell.SetBusy(false)
		u.shell.Update(uitest.Key(key))
		if u.toolbarHidden {
			t.Fatal("toggle did not restore toolbar")
		}
	}
}

func TestUnifiedWorkspaceMouseGroupChoicesAndQueryEditorControls(t *testing.T) {
	t.Parallel()
	sessions, _ := derivedFixture(t)
	u, err := NewSessionChatUI(context.Background(), sessions, "none")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	u.shell.FocusEntry(u.lastGridEntryID)
	before := len(u.snapshot.RecordSets)
	uxSearchAction(t, u, "Group loaded")
	clickWorkspaceText(t, u, "[ ] Category")
	clickWorkspaceText(t, u, "Count rows")
	clickWorkspaceText(t, u, "Group loaded")
	if _, ok := u.shell.CenterPanel().(*localGroupsPanel); !ok || len(u.snapshot.RecordSets) != before {
		t.Fatalf("mouse local group did not preserve immutable result: %s", ansi.Strip(u.shell.View().Content))
	}
	_, cmd := u.shell.Update(uitest.Key("esc"))
	drainCmd(t, u, cmd)
	object := u.catalog.Objects[0]
	u.openWorkspaceQuery(object, false)
	editor := u.queryEditor
	editor.text.SetValue("from: {schema: main, name: Item}\ncolumns: [{field: Id}]\nlimit: 1\n")
	clickWorkspaceText(t, u, "Select text")
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Select text · arrows extend") {
		t.Fatalf("mouse selection did not open explicit mode: %s", ansi.Strip(u.shell.View().Content))
	}
	u.shell.Update(uitest.Key("esc"))
	draft := editor.text.Value()
	drainCmd(t, u, clickWorkspaceText(t, u, "Back"))
	u.openWorkspaceQuery(object, false)
	if u.queryEditor != editor || editor.text.Value() != draft {
		t.Fatal("mouse Back lost query draft")
	}
	drainCmd(t, u, clickWorkspaceText(t, u, "Run"))
	if len(u.snapshot.RecordSets) != before+1 || len(u.snapshot.RecordSets[u.lastGridRecordSetID].Result.Rows) != 1 {
		t.Fatalf("mouse Run did not use secure editor query: %s", ansi.Strip(u.shell.View().Content))
	}
}

func TestUnifiedWorkspaceOriginalOutputRefusesInexactArithmeticBeforeChildPersistence(t *testing.T) {
	t.Parallel()
	sessions, _ := derivedFixture(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite", strings.TrimPrefix(sessions.source, "sqlite://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("DELETE FROM Item; INSERT INTO Item VALUES (1,'A',9007199254740991),(2,'A',2)"); err != nil {
		t.Fatal(err)
	}
	for _, function := range []string{"sum", "avg"} {
		document := "from: {schema: main, name: Item}\ncolumns: [{aggregate: {function: " + function + ", args: [{field: Value}]}, as: total}]\nlimit: 10\n"
		executor := secureread.NewExecutor(secureread.Session{Unrestricted: true})
		native, err := executor.RunTugQL(ctx, sessions.source, []byte(document), nil)
		if err != nil {
			t.Fatal(err)
		}
		origin, err := sessions.store.AppendUser(ctx, sessions.activeID, "Native "+function)
		if err != nil {
			t.Fatal(err)
		}
		query, err := sessions.store.AppendQuery(ctx, sessions.activeID, origin.ID, sessions.source, QueryResult{Title: function, SourceID: sessions.catalog.Objects[0].Reference.SourceID, TugQL: document, Result: native})
		if err != nil {
			t.Fatal(err)
		}
		before, err := sessions.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		operation := derivedOperation{Scope: queryops.OriginalOutput, Sort: []queryops.SortTerm{{Column: "total"}}}
		preview, err := deriveResultPreview(sessions.catalog, before.RecordSets[query.RecordSetID], operation)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = sessions.executeDerivedResult(ctx, sessions.activeID, query.RecordSetID, operation, preview.TugQL); err == nil || !strings.Contains(err.Error(), "SUM/AVG") {
			t.Fatalf("%s arithmetic did not refuse explicitly: %v", function, err)
		}
		after, err := sessions.Snapshot(ctx)
		if err != nil || len(after.RecordSets) != len(before.RecordSets) {
			t.Fatal("unsupported generic arithmetic persisted a corrupt child")
		}
	}
}

func TestUnifiedWorkspaceHeaderSessionPickerAndExpandAreClickable(t *testing.T) {
	t.Parallel()
	sessions, _ := derivedFixture(t)
	u, err := NewSessionChatUI(context.Background(), sessions, "none")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 42})
	u.shell.FocusEntry(u.lastGridEntryID)
	u.shell.Update(uitest.Key("f9"))
	if !u.shell.Expanded() {
		t.Fatal("F9 did not expand selected result")
	}
	clickWorkspaceText(t, u, "…")
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Chats  ↑↓ choose") {
		t.Fatalf("chat-tab overflow click did not open session picker: %s", ansi.Strip(u.shell.View().Content))
	}
	u.shell.Update(uitest.Key("esc"))
	if !u.shell.Expanded() || u.shell.FocusedEntryID() != u.lastGridEntryID {
		t.Fatal("session picker cancel lost result expansion")
	}
}
