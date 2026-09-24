package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestWorkspacePanelTitleIsWorkspace covers Title(), a trivial one-liner
// with no other test hitting it directly.
func TestWorkspacePanelTitleIsWorkspace(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if got := u.workspace.Title(); got != "Workspace" {
		t.Fatalf("Title() = %q, want %q", got, "Workspace")
	}
}

// TestWorkspacePanelRefreshAndBookmarksNoOpWithoutSessions covers refresh's
// and refreshBookmarks' nil-ui.sessions early returns -- a ChatUI built
// without a durable session.
func TestWorkspacePanelRefreshAndBookmarksNoOpWithoutSessions(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "fake-model")
	if err := u.workspace.refresh(); err != nil {
		t.Fatalf("refresh() without sessions = %v, want nil", err)
	}
	if err := u.workspace.refreshBookmarks(); err != nil {
		t.Fatalf("refreshBookmarks() without sessions = %v, want nil", err)
	}
}

// TestWorkspacePanelRefreshBookmarksPropagatesFindError covers
// refreshBookmarks' error path: an invalid (empty, after trim) bookmark tag
// makes FindBookmarks/normalizedTags fail, and refreshBookmarks must return
// that error rather than swallow it.
func TestWorkspacePanelRefreshBookmarksPropagatesFindError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.bookmarkTags = []string{"   "}
	if err := u.workspace.refreshBookmarks(); err == nil {
		t.Fatal("expected refreshBookmarks to propagate the invalid-tag error")
	}
}

// TestWorkspacePanelSelectedBookmarkReferenceOutOfRange covers
// selectedBookmarkReference's empty-ContextReference fallback when the
// cursor sits outside the (empty) bookmark list.
func TestWorkspacePanelSelectedBookmarkReferenceOutOfRange(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.bookmarkIndex = 5
	if ref := u.workspace.selectedBookmarkReference(); ref != (ContextReference{}) {
		t.Fatalf("selectedBookmarkReference() = %+v, want zero value", ref)
	}
}

// TestWorkspacePanelPerformAndApplyWorkspaceActionReportErrorWithoutSessions
// covers performWorkspaceAction's/applyWorkspaceAction's error paths: a
// ChatUI without a durable session can't apply any WorkspaceAction, and
// performWorkspaceAction must surface that as a transcript error rather
// than panic.
func TestWorkspacePanelPerformAndApplyWorkspaceActionReportErrorWithoutSessions(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "fake-model")
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if err := u.workspace.applyWorkspaceAction(WorkspaceAction{Kind: "attach"}); err == nil {
		t.Fatal("expected applyWorkspaceAction to fail without a durable session")
	}
	u.workspace.performWorkspaceAction(WorkspaceAction{Kind: "attach"})
	if !strings.Contains(u.shell.View().Content, "durable chat session") {
		t.Fatalf("expected the transcript to show the workspace-action error:\n%s", u.shell.View().Content)
	}
}

// TestWorkspacePanelStartBookmarkInputSetsUpEditor covers startBookmarkInput
// directly -- mode, placeholder, cleared value and focus.
func TestWorkspacePanelStartBookmarkInputSetsUpEditor(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.bookmarkEditor.SetValue("stale")
	u.workspace.startBookmarkInput("rename", "Bookmark title")
	if u.workspace.bookmarkMode != "rename" {
		t.Fatalf("bookmarkMode = %q, want rename", u.workspace.bookmarkMode)
	}
	if u.workspace.bookmarkEditor.Placeholder != "Bookmark title" {
		t.Fatalf("placeholder = %q", u.workspace.bookmarkEditor.Placeholder)
	}
	if u.workspace.bookmarkEditor.Value() != "" {
		t.Fatalf("expected the editor value to be cleared, got %q", u.workspace.bookmarkEditor.Value())
	}
	if !u.workspace.bookmarkEditor.Focused() {
		t.Fatal("expected the bookmark editor to be focused")
	}
}

// TestWorkspacePanelEnsureBookmarkGridOutOfRange covers ensureBookmarkGrid's
// nil return when bookmarkIndex is out of range.
func TestWorkspacePanelEnsureBookmarkGridOutOfRange(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.bookmarkIndex = -1
	if g := u.workspace.ensureBookmarkGrid(); g != nil {
		t.Fatalf("ensureBookmarkGrid() = %v, want nil", g)
	}
}

// TestWorkspacePanelHandleBookmarkGridKeyNilGrid covers
// handleBookmarkGridKey's nil-bookmarkGrid early return.
func TestWorkspacePanelHandleBookmarkGridKeyNilGrid(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if _, handled := u.workspace.handleBookmarkGridKey(nil, tea.KeyPressMsg{Code: 'a', Text: "a"}); handled {
		t.Fatal("expected handleBookmarkGridKey to report unhandled with no grid")
	}
}

// TestWorkspacePanelBookmarkGridKeysTabSortDock drives the bookmark grid's
// remaining key.String() cases -- "tab" (unfocus), "s" (sort) and "d"
// (dock via the shared WorkspaceAction path) -- that
// TestChatUIBookmarkWorkspaceTabOpensStructuredGrid didn't reach ("a"
// attach and Enter-focus are already covered there).
func TestWorkspacePanelBookmarkGridKeysTabSortDock(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	catalog := ProjectCatalog{ID: testScope().ProjectID, Title: "Demo"}
	chatSessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", catalog)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := chatSessions.Snapshot(ctx)
	recordID := workspaceTestRecord(t, store, session.ID)
	if _, err := chatSessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: recordID}, Title: "Saved customers"}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, chatSessions, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.workspace.setTab(3)
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if u.workspace.bookmarkGrid == nil {
		t.Fatal("expected the bookmark grid to open")
	}

	// "s" sorts the bookmark grid.
	u.workspace.updateKey(tea.KeyPressMsg{Code: 's', Text: "s"})
	if _, desc := u.workspace.bookmarkGrid.SortState(); desc {
		// Either direction is fine; we only need the Sort call to have run
		// without panicking. Re-press to exercise the descending toggle too.
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: 's', Text: "s"})

	// "d" docks the bookmark via the shared WorkspaceAction path. Docking
	// switches the session's ActiveTab to "Docked" as a side effect, which
	// the ensuing refresh() picks up (p.tab becomes 2) -- drive the final
	// "tab" press straight through handleBookmarkGridKey instead of
	// updateKey, so it isn't routed by that now-stale tab instead.
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	snapshot, err := chatSessions.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.Docks) != 1 {
		t.Fatalf("expected the bookmark to be docked: %+v, %v", snapshot.Workspace.Docks, err)
	}

	// "tab" unfocuses the bookmark grid.
	u.workspace.handleBookmarkGridKey(u.workspace.bookmarkGrid.Model, tea.KeyPressMsg{Code: tea.KeyTab})
	if u.workspace.bookmarkGridFocused {
		t.Fatal("expected tab to unfocus the bookmark grid")
	}
}

// TestWorkspacePanelRebuildDockGridsInitializesNilMap covers
// rebuildDockGrids' nil-dockGrids-map initialization branch.
func TestWorkspacePanelRebuildDockGridsInitializesNilMap(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.dockGrids = nil
	u.workspace.rebuildDockGrids()
	if u.workspace.dockGrids == nil {
		t.Fatal("expected rebuildDockGrids to initialize a non-nil map")
	}
}

// TestWorkspacePanelHandleDockGridKeyGuardsAndCases drives
// handleDockGridKey's remaining branches: out-of-range dockIndex, a missing
// grid for the current dock, and the "tab"/"enter"/"r"/"a"/"s"(no ViewID)
// cases a plain (non-Selection) recordset dock exercises --
// TestChatUIDockedGridSortAndCellSelectionUseSourceCoordinates and
// TestChatUIDockGridHasNoViewSwitcher already cover the Selection-backed
// dock's "c" and view-sorted "s" paths.
func TestWorkspacePanelHandleDockGridKeyGuardsAndCases(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ContextReference{Kind: "recordset", ObjectID: recordID}}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	u.workspace.tab = 2

	// Guard: out-of-range dockIndex.
	u.workspace.dockIndex = -1
	if _, handled := u.workspace.handleDockGridKey(nil, tea.KeyPressMsg{Code: 'a', Text: "a"}); handled {
		t.Fatal("expected handleDockGridKey to report unhandled with dockIndex out of range")
	}

	// Guard: no grid tracked for the current dock.
	u.workspace.dockIndex = 0
	dock := u.snapshot.Workspace.Docks[0]
	saved := u.workspace.dockGrids[dock.ID]
	delete(u.workspace.dockGrids, dock.ID)
	if _, handled := u.workspace.handleDockGridKey(nil, tea.KeyPressMsg{Code: 'a', Text: "a"}); handled {
		t.Fatal("expected handleDockGridKey to report unhandled with no tracked grid")
	}
	u.workspace.dockGrids[dock.ID] = saved

	// setTab (rather than a raw tab assignment) persists ActiveTab, so the
	// refresh() every performWorkspaceAction below triggers keeps p.tab at
	// 2 instead of resetting it to the default (0) once ActiveTab is read
	// back -- setTab itself clears dockGridFocused, so focus is set after.
	u.workspace.setTab(2)
	dockGrid := u.workspace.dockGrids[dock.ID]
	dockGrid.SetFocused(true)
	u.workspace.dockGridFocused = true

	// Drive handleDockGridKey directly rather than through updateKey's
	// dockGridFocused-forwarding branch: selectFromDockGrid's own "row"/
	// "cell" modes clear p.dockGridFocused once they dispatch (matching
	// ui.go: selecting returns focus to workspace navigation), which would
	// otherwise stop updateKey from routing the very next key press here.

	// "enter"/"space" selects the current row.
	u.workspace.handleDockGridKey(dockGrid.Model, tea.KeyPressMsg{Code: tea.KeyEnter})
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || snapshot.Workspace.CurrentSelectionID == "" {
		t.Fatalf("expected enter to select the current row: %+v, %v", snapshot.Workspace, err)
	}

	// "r" starts a range selection (sets the anchor, no WorkspaceAction yet).
	u.workspace.handleDockGridKey(dockGrid.Model, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if u.workspace.rangeAnchor < 0 {
		t.Fatal("expected 'r' to set the range anchor")
	}
	u.workspace.rangeAnchor = -1 // reset so 'r' below doesn't require a second press

	// "a" toggles attachment on the dock's reference.
	u.workspace.handleDockGridKey(dockGrid.Model, tea.KeyPressMsg{Code: 'a', Text: "a"})
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("expected 'a' to attach the dock: %+v, %v", snapshot.Workspace.Attachments, err)
	}

	// "s" without a ViewID (a plain recordset dock) sorts the grid directly.
	dockGrid.SelectColumn(0)
	u.workspace.handleDockGridKey(dockGrid.Model, tea.KeyPressMsg{Code: 's', Text: "s"})

	// "tab" unfocuses the dock grid.
	u.workspace.dockGridFocused = true
	u.workspace.handleDockGridKey(dockGrid.Model, tea.KeyPressMsg{Code: tea.KeyTab})
	if u.workspace.dockGridFocused {
		t.Fatal("expected tab to unfocus the dock grid")
	}
}

// TestWorkspacePanelSelectFromDockGridGuards covers selectFromDockGrid's
// early returns: dockIndex out of range, and gridDataForReference failing
// (a dock whose Reference no longer resolves).
func TestWorkspacePanelSelectFromDockGridGuards(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.dockIndex = -1
	u.workspace.selectFromDockGrid("row") // no panic, no-op

	u.ui2Dock(t)
}

// ui2Dock is a small helper that gives selectFromDockGrid a dock whose
// reference cannot resolve (so gridDataForReference fails), exercising its
// "!ok || g == nil" guard without a full session round trip.
func (u *ChatUI) ui2Dock(t *testing.T) {
	t.Helper()
	u.snapshot.Workspace.Docks = []Dock{{ID: "ghost", Reference: ContextReference{Kind: "recordset", ObjectID: "missing"}}}
	u.workspace.dockGrids = map[string]*gridState{"ghost": gridStateTestFixture(t)}
	u.workspace.dockIndex = 0
	u.workspace.selectFromDockGrid("row")
}

// TestWorkspacePanelSelectFromGridStateRangeModeAndGuards covers
// selectFromGridState's guards (an out-of-range source row, an unresolved
// RecordSet, an out-of-range selected column) and its "range" mode branch
// (anchor set on the first press, then a full range computed and
// dispatched on the second) -- the existing docked-grid tests only reach
// "row" and "cell" mode.
func TestWorkspacePanelSelectFromGridStateRangeModeAndGuards(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})

	record := u.snapshot.RecordSets[recordID]
	g := newMinimalGridState(NewGridModel(record.Result), recordID, "Customers", 80)

	// Guard: unresolved RecordSet.
	u.workspace.selectFromGridState(g, "missing-recordset", "", "row")

	// "range" mode: first press anchors, does not dispatch.
	g.SelectColumn(0)
	u.workspace.selectFromGridState(g, recordID, "", "range")
	if u.workspace.rangeAnchor < 0 {
		t.Fatal("expected the first range press to set the anchor")
	}

	// Second press with the cursor moved computes the range and dispatches.
	g.SelectRow(2)
	g.SelectColumn(1)
	u.workspace.selectFromGridState(g, recordID, "", "range")
	if u.workspace.rangeAnchor != -1 {
		t.Fatal("expected the range anchor to reset after dispatch")
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || snapshot.Workspace.CurrentSelectionID == "" {
		t.Fatalf("expected a range selection to be recorded: %+v, %v", snapshot.Workspace, err)
	}
	selection := snapshot.Workspace.Selections[snapshot.Workspace.CurrentSelectionID]
	if len(selection.Ranges) == 0 {
		t.Fatalf("expected non-empty cell ranges for a multi-row/column range: %+v", selection)
	}
}

// TestWorkspacePanelProjectExplorerCollapsedRoot covers explorerNodes' early
// return once the project root itself is collapsed.
func TestWorkspacePanelProjectExplorerCollapsedRoot(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	rootID := "project:" + u.catalog.ID
	u.workspace.explorerCollapsed[rootID] = true
	nodes := u.workspace.explorerNodes()
	if len(nodes) != 1 || nodes[0].id != rootID {
		t.Fatalf("expected only the collapsed root node, got %+v", nodes)
	}
}

// TestWorkspacePanelExplorerNodesViewScrollsWithCursor covers
// explorerNodesView's offset-follow-the-cursor branches (moving past the
// bottom, then above the top of the viewport).
func TestWorkspacePanelExplorerNodesViewScrollsWithCursor(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	for i := 0; i < 30; i++ {
		u.catalog.Objects = append(u.catalog.Objects, ProjectObject{Reference: ContextReference{
			Kind: "source", SourceID: "s" + string(rune('a'+i%26)) + string(rune('0'+i/26)), ObjectID: "s" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Title: "Extra",
		}})
	}
	nodes := u.workspace.explorerNodes()
	if len(nodes) < 6 {
		t.Fatalf("setup: expected several explorer nodes, got %d", len(nodes))
	}
	u.workspace.focused = true
	u.workspace.explorerIndex = len(nodes) - 1
	view := u.workspace.explorerNodesView(60, 3)
	if view == "" {
		t.Fatal("expected a non-empty explorer view scrolled to the bottom")
	}
	u.workspace.explorerIndex = 0
	view = u.workspace.explorerNodesView(60, 3)
	if view == "" {
		t.Fatal("expected a non-empty explorer view scrolled to the top")
	}
}

// TestWorkspacePanelSelectedExplorerObjectExcludesProjectRoot covers
// selectedExplorerObject's "project" kind exclusion: the cursor sitting on
// the root node itself reports no selectable object.
func TestWorkspacePanelSelectedExplorerObjectExcludesProjectRoot(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	u.workspace.explorerIndex = 0 // the project root node
	if obj := u.workspace.selectedExplorerObject(); obj != nil {
		t.Fatalf("selectedExplorerObject() on the project root = %+v, want nil", obj)
	}
}

// TestProjectObjectDetailsQueryAndSourceAndDefaultKinds covers
// projectObjectDetails' remaining switch branches: a query with no
// SourceID and no QueryText, a "source" kind, and the default
// (unrecognized kind) branch.
func TestProjectObjectDetailsQueryAndSourceAndDefaultKinds(t *testing.T) {
	title, detail := projectObjectDetails(ProjectObject{
		Reference: ContextReference{Kind: "query", Title: "Unbound query"},
		QueryType: "DTQL",
	}, 60)
	if title != "Query: Unbound query" || !strings.Contains(detail, "Query text unavailable.") || strings.Contains(detail, "Database:") {
		t.Fatalf("unexpected query-without-source details: %q / %q", title, detail)
	}

	title, detail = projectObjectDetails(ProjectObject{
		Reference: ContextReference{Kind: "source", Title: "chinook"},
	}, 60)
	if title != "Database: chinook" || !strings.Contains(detail, "Tables and views are listed above.") {
		t.Fatalf("unexpected source details: %q / %q", title, detail)
	}

	title, _ = projectObjectDetails(ProjectObject{Reference: ContextReference{Kind: "project", Title: "Demo"}}, 60)
	if title != "Demo" {
		t.Fatalf("unexpected default-kind title: %q", title)
	}
}

// TestProjectObjectDetailsTableWithNoColumns covers the table/view branch's
// "No column metadata available." fallback.
func TestProjectObjectDetailsTableWithNoColumns(t *testing.T) {
	_, detail := projectObjectDetails(ProjectObject{Reference: ContextReference{Kind: "table", Title: "Empty"}}, 60)
	if !strings.Contains(detail, "No column metadata available.") {
		t.Fatalf("expected the no-columns fallback: %q", detail)
	}
}

// TestWorkspacePanelCurrentRowDetailsNullAndUnknownType covers
// currentRowDetails' NULL-cell rendering (rawRow[i] == nil) and its "?"
// fallback when columnMeta reports no dbType.
func TestWorkspacePanelCurrentRowDetailsNullAndUnknownType(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{Title: "Widgets", RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"Name"},
		Rows:    []secureread.Row{{Data: map[string]any{"Name": nil}}},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show widgets"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	u.workspace.tab = 1
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "NULL") {
		t.Fatalf("expected NULL rendered for a nil cell:\n%s", view)
	}
}

// TestWorkspacePanelCurrentColumnDetailsUnavailableSourceAndType covers
// currentColumnDetails' "Source: unavailable"/"Type: not available in
// catalog" fallbacks for a column with no catalog attribution.
func TestWorkspacePanelCurrentColumnDetailsUnavailableSourceAndType(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{Title: "Widgets", RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"Mystery"},
		Rows:    []secureread.Row{{Data: map[string]any{"Mystery": 1}}},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show widgets"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	u.workspace.tab, u.workspace.inspectorSubTab = 1, 1
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "Source: unavailable") || !strings.Contains(view, "Type: not available in catalog") {
		t.Fatalf("expected unavailable source/type fallbacks:\n%s", view)
	}
}

// TestWorkspacePanelCurrentColumnDetailsMultipleSourceTables covers
// currentColumnDetails' "Possible source tables:" branch (meta.objects with
// more than one candidate).
func TestWorkspacePanelCurrentColumnDetailsMultipleSourceTables(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{
		Title:       "Ambiguous",
		DTQL:        "from: {name: Widget}\ncolumns: [{field: Id}]\nlimit: 5\n",
		RecordSetID: "rs1",
		Result: secureread.Result{
			Columns: []string{"Id"},
			Rows:    []secureread.Row{{Data: map[string]any{"Id": 1}}},
		},
	}}}
	u, _ := newTestChatUI(t, nil, turn)
	u.catalog = ProjectCatalog{Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "table", ObjectID: "main.WidgetA", Title: "WidgetA"}, Columns: []string{"Id"}},
		{Reference: ContextReference{Kind: "table", ObjectID: "main.WidgetB", Title: "WidgetB"}, Columns: []string{"Id"}},
	}}
	drainCmd(t, u, u.Submit("Show widgets"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	u.workspace.tab, u.workspace.inspectorSubTab = 1, 1
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "Possible source tables:") {
		t.Fatalf("expected the ambiguous-source hint:\n%s", view)
	}
}

// TestWorkspacePanelCurrentColumnDetailsRelatedJoinCandidate covers
// currentColumnDetails' related-tables/FK-constraint branch: a configured
// join application whose candidates match the focused column.
func TestWorkspacePanelCurrentColumnDetailsRelatedJoinCandidate(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	query, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Invoices", DTQL: "from: {name: Invoice}\ncolumns: [{field: CustomerId}]\nlimit: 5\n",
		Result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 1}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}})
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	u.workspace.tab, u.workspace.inspectorSubTab = 1, 1
	view := u.workspace.View(90, 25, true)
	if !strings.Contains(view, "Related tables:") {
		t.Fatalf("expected a related-tables section:\n%s\n(query recordset=%s)", view, query.RecordSetID)
	}
}

// TestWorkspacePanelCurrentRecordsetDetailsFallbackLabels covers
// currentRecordsetDetails' "type unavailable" fallback for a column with no
// catalog type, alongside its qualified-name fallback (column.Name).
func TestWorkspacePanelCurrentRecordsetDetailsFallbackLabels(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{Title: "Widgets", RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"Mystery"},
		Rows:    []secureread.Row{{Data: map[string]any{"Mystery": 1}}},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show widgets"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	u.workspace.tab, u.workspace.inspectorSubTab = 1, 2
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "type unavailable") || !strings.Contains(view, "Mystery") {
		t.Fatalf("expected fallback labels for an unattributed column:\n%s", view)
	}
}

// TestWorkspacePanelSelectedDetailsMissingRecordSet covers selectedDetails'
// "no longer available" branch: a Selection whose View points at a
// RecordSet that has since been removed from the snapshot.
func TestWorkspacePanelSelectedDetailsMissingRecordSet(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.snapshot.Workspace.CurrentSelectionID = "sel1"
	u.snapshot.Workspace.Selections = map[string]Selection{"sel1": {ID: "sel1", ViewID: "view1", Title: "Gone"}}
	u.snapshot.Workspace.Views = map[string]RecordSetView{"view1": {ID: "view1", RecordSetID: "missing"}}
	if got := u.workspace.selectedDetails(60); got != "The selected RecordSet is no longer available." {
		t.Fatalf("selectedDetails() = %q", got)
	}
}

// TestWorkspacePanelSelectedDetailsSingleRowShowsFKIDColumnsAndValues
// covers selectedDetails' single-row branch: its FK-looking "*id" column
// listing (excluding the selected column itself) followed by the selected
// columns' own values.
func TestWorkspacePanelSelectedDetailsSingleRowShowsFKIDColumnsAndValues(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.snapshot.Workspace.CurrentSelectionID = "sel1"
	u.snapshot.Workspace.Selections = map[string]Selection{"sel1": {
		ID: "sel1", ViewID: "view1", Title: "One row", Rows: []int{0}, Columns: []string{"City"},
	}}
	u.snapshot.Workspace.Views = map[string]RecordSetView{"view1": {ID: "view1", RecordSetID: "rs1"}}
	u.snapshot.RecordSets = map[string]RecordSet{"rs1": {
		ID: "rs1", Title: "Customers", Result: secureread.Result{
			Columns: []string{"CustomerId", "City"},
			Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 42, "City": "Prague"}}},
		},
	}}
	got := u.workspace.selectedDetails(60)
	if !strings.Contains(got, "CustomerId") || !strings.Contains(got, "42") || !strings.Contains(got, "Prague") {
		t.Fatalf("selectedDetails() = %q, want the FK id column and selected column value", got)
	}
}

// TestWorkspacePanelSelectedDetailsMultiRowElides covers selectedDetails'
// multi-row branch, including its "…" elision past 8 rows.
func TestWorkspacePanelSelectedDetailsMultiRowElides(t *testing.T) {
	rows := make([]secureread.Row, 10)
	selRows := make([]int, 10)
	for i := range rows {
		rows[i] = secureread.Row{Data: map[string]any{"City": i}}
		selRows[i] = i
	}
	u, _ := newTestChatUI(t, nil, Turn{})
	u.snapshot.Workspace.CurrentSelectionID = "sel1"
	u.snapshot.Workspace.Selections = map[string]Selection{"sel1": {
		ID: "sel1", ViewID: "view1", Title: "Many rows", Rows: selRows, Columns: []string{"City"},
	}}
	u.snapshot.Workspace.Views = map[string]RecordSetView{"view1": {ID: "view1", RecordSetID: "rs1"}}
	u.snapshot.RecordSets = map[string]RecordSet{"rs1": {
		ID: "rs1", Title: "Cities", Result: secureread.Result{Columns: []string{"City"}, Rows: rows},
	}}
	got := u.workspace.selectedDetails(60)
	if !strings.Contains(got, "…") {
		t.Fatalf("selectedDetails() = %q, want an elision past 8 rows", got)
	}
}

// TestWorkspacePanelDockedViewIndexOutOfRange covers dockedView's
// out-of-range dockIndex branch (the listing renders without a detail
// grid below it).
func TestWorkspacePanelDockedViewIndexOutOfRange(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.snapshot.Workspace.Docks = []Dock{{ID: "d1", Title: "First"}}
	u.workspace.dockIndex = 5
	got := u.workspace.dockedView(60)
	if !strings.Contains(got, "First") {
		t.Fatalf("dockedView() = %q, want the dock listed", got)
	}
}

// TestWorkspacePanelBookmarksViewSearchAndTagFilters covers bookmarksView's
// search/tag-filter header line and its no-match empty states.
func TestWorkspacePanelBookmarksViewSearchAndTagFilters(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 3
	u.workspace.bookmarkSearch = "widgets"
	u.workspace.bookmarkTags = []string{"reporting"}
	view := u.workspace.View(60, 15, true)
	if !strings.Contains(view, "search: widgets") || !strings.Contains(view, "tags: reporting") {
		t.Fatalf("expected filter summary in header:\n%s", view)
	}
	if !strings.Contains(view, "No bookmarks match these tags.") {
		t.Fatalf("expected the tags-only empty state (tags take priority):\n%s", view)
	}
	u.workspace.bookmarkTags = nil
	view = u.workspace.View(60, 15, true)
	if !strings.Contains(view, "No bookmarks match this search.") {
		t.Fatalf("expected the search-only empty state:\n%s", view)
	}
}

// TestWorkspacePanelBookmarksViewShowsAttachedAndDockedFlags covers
// bookmarksView's per-row "attached"/"docked" flag rendering.
func TestWorkspacePanelBookmarksViewShowsAttachedAndDockedFlags(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	catalog := ProjectCatalog{ID: testScope().ProjectID, Title: "Demo"}
	chatSessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", catalog)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := chatSessions.Snapshot(ctx)
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := chatSessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: recordID}, Title: "Saved customers"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chatSessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	if _, err := chatSessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, chatSessions, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.workspace.setTab(3)
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "attached") || !strings.Contains(view, "docked") {
		t.Fatalf("expected attached/docked flags in bookmark row:\n%s", view)
	}
}

// TestWorkspacePanelViewTruncatesOverflowLines covers View's tail
// truncation when the rendered body produces more lines than the given
// height.
func TestWorkspacePanelViewTruncatesOverflowLines(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	view := u.workspace.View(40, 3, true)
	if got := strings.Count(view, "\n"); got != 2 {
		t.Fatalf("expected exactly 3 lines (2 newlines) at height 3, got %d newlines:\n%s", got, view)
	}
}

// TestWorkspacePanelUpdateIgnoresNonKeyMessages covers Update's non-key
// message no-op branch.
func TestWorkspacePanelUpdateIgnoresNonKeyMessages(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	tab := u.workspace.tab
	panel, cmd := u.workspace.Update(tea.WindowSizeMsg{Width: 10, Height: 10})
	if panel != u.workspace || cmd != nil {
		t.Fatalf("expected a non-key message to be a no-op: panel=%v cmd=%v", panel, cmd)
	}
	if u.workspace.tab != tab {
		t.Fatal("non-key message should not change tab state")
	}
}

// TestWorkspacePanelUpdateKeyBookmarkGridFocusedNilGridRecovers covers
// updateKey's bookmarkGridFocused branch when ensureBookmarkGrid can no
// longer resolve a grid (bookmark list emptied out from under it): it must
// clear bookmarkGridFocused instead of panicking on a nil grid.
func TestWorkspacePanelUpdateKeyBookmarkGridFocusedNilGridRecovers(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 3
	u.workspace.bookmarkGridFocused = true
	u.workspace.bookmarkItems = nil
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if u.workspace.bookmarkGridFocused {
		t.Fatal("expected updateKey to clear bookmarkGridFocused when the grid can't resolve")
	}
}

// TestWorkspacePanelUpdateKeyDockGridFocusedGuards covers updateKey's
// dockGridFocused branch guards: an out-of-range dockIndex, and a dock with
// no tracked grid -- both must return without a command instead of
// panicking.
func TestWorkspacePanelUpdateKeyDockGridFocusedGuards(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 2
	u.workspace.dockGridFocused = true
	u.workspace.dockIndex = -1
	if cmd := u.workspace.updateKey(tea.KeyPressMsg{Code: 'a', Text: "a"}); cmd != nil {
		t.Fatalf("expected nil cmd with dockIndex out of range, got %v", cmd)
	}

	u.snapshot.Workspace.Docks = []Dock{{ID: "d1", Title: "First"}}
	u.workspace.dockIndex = 0
	u.workspace.dockGrids = map[string]*gridState{}
	if cmd := u.workspace.updateKey(tea.KeyPressMsg{Code: 'a', Text: "a"}); cmd != nil {
		t.Fatalf("expected nil cmd with no tracked dock grid, got %v", cmd)
	}
}

// TestWorkspacePanelExplorerPgUpPgDownAdjustDetailOffset covers updateKey's
// pgdown/pgup handling of explorerDetailOffset while a details card is
// showing.
func TestWorkspacePanelExplorerPgUpPgDownAdjustDetailOffset(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	u.workspace.height = 40
	for i, node := range u.workspace.explorerNodes() {
		if node.label == "Customer" {
			u.workspace.explorerIndex = i
			break
		}
	}
	if u.workspace.selectedExplorerObject() == nil {
		t.Fatal("setup: expected the cursor to sit on a selectable object")
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if u.workspace.explorerDetailOffset == 0 {
		t.Fatal("expected pgdown to advance explorerDetailOffset")
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if u.workspace.explorerDetailOffset != 0 {
		t.Fatalf("expected pgup to return explorerDetailOffset to 0, got %d", u.workspace.explorerDetailOffset)
	}
}

// TestWorkspacePanelExplorerHLFoldAndJumpToAncestor covers updateKey's
// "h"/"left" branch: folding a branch node under the cursor, and (on a
// leaf) jumping the cursor up to its nearest shallower branch ancestor.
// "l"/"right" unfolds a branch node again.
func TestWorkspacePanelExplorerHLFoldAndJumpToAncestor(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	nodes := u.workspace.explorerNodes()
	var sourceIndex, customerIndex int
	for i, node := range nodes {
		if node.label == "Chinook local" {
			sourceIndex = i
		}
		if node.label == "Customer" {
			customerIndex = i
		}
	}
	// h on a leaf jumps to the nearest shallower branch ancestor.
	u.workspace.explorerIndex = customerIndex
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if u.workspace.explorerIndex >= customerIndex {
		t.Fatalf("expected h on a leaf to jump up to an ancestor, index=%d", u.workspace.explorerIndex)
	}
	// h on a branch node folds it.
	u.workspace.explorerIndex = sourceIndex
	sourceID := nodes[sourceIndex].id
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if !u.workspace.explorerCollapsed[sourceID] {
		t.Fatal("expected h on a branch node to collapse it")
	}
	// l unfolds it again.
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if u.workspace.explorerCollapsed[sourceID] {
		t.Fatal("expected l to unfold the branch node")
	}
}

// TestWorkspacePanelUpDownAcrossAllTabs covers updateKey's "up"/"down"
// switch across the Docked and Bookmarks tabs (case 2/3) --
// TestWorkspacePanelExplorerHLFoldAndJumpToAncestor and
// TestChatUISelectedTabUpDownScrollsInspectorOffset already cover tabs 0/1.
func TestWorkspacePanelUpDownAcrossAllTabs(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.snapshot.Workspace.Docks = []Dock{{ID: "d1"}, {ID: "d2"}}
	u.workspace.tab = 2
	u.workspace.dockIndex = 0
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if u.workspace.dockIndex != 1 {
		t.Fatalf("expected down to advance dockIndex to 1, got %d", u.workspace.dockIndex)
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if u.workspace.dockIndex != 0 {
		t.Fatalf("expected up to return dockIndex to 0, got %d", u.workspace.dockIndex)
	}

	u.workspace.tab = 3
	u.workspace.bookmarkItems = []Bookmark{{ID: "b1"}, {ID: "b2"}}
	u.workspace.bookmarkIndex = 0
	u.workspace.bookmarkGrid = gridStateTestFixture(t)
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if u.workspace.bookmarkIndex != 1 || u.workspace.bookmarkGrid != nil {
		t.Fatalf("expected down to advance bookmarkIndex and clear the cached grid: index=%d grid=%v", u.workspace.bookmarkIndex, u.workspace.bookmarkGrid)
	}
	u.workspace.bookmarkGrid = gridStateTestFixture(t)
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if u.workspace.bookmarkIndex != 0 || u.workspace.bookmarkGrid != nil {
		t.Fatalf("expected up to return bookmarkIndex and clear the cached grid: index=%d grid=%v", u.workspace.bookmarkIndex, u.workspace.bookmarkGrid)
	}
}

// TestWorkspacePanelSpaceTogglesAttachmentOnSelectedAndDockedTabs covers
// updateKey's "space"/"a" branches for tab 1 (Selected) and tab 2 (Docked)
// -- tab 0 (Project) and tab 3 (Bookmarks) are already covered by
// TestChatUIWorkspaceSplitAndKeyboardSelection and
// TestChatUIBookmarkWorkspaceTabOpensStructuredGrid.
func TestWorkspacePanelSpaceTogglesAttachmentOnSelectedAndDockedTabs(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0}, Title: "Row"}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.workspace.tab = 1
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeySpace})
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("expected space on Selected tab to attach the current selection: %+v, %v", snapshot.Workspace.Attachments, err)
	}

	// Dock a DIFFERENT reference (the plain RecordSet, not the Selection
	// already attached above) so the Docked-tab space press below performs
	// a genuine second attach instead of toggling the same reference off.
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ContextReference{Kind: "recordset", ObjectID: recordID}}); err != nil {
		t.Fatal(err)
	}
	u2, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u2.workspace.tab = 2
	u2.workspace.dockIndex = 0
	u2.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeySpace})
	snapshot2, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot2.Workspace.Attachments) != 2 {
		t.Fatalf("expected space on Docked tab to attach the dock: %+v, %v", snapshot2.Workspace.Attachments, err)
	}
}

// TestWorkspacePanelBookmarkKeysBDockRenameTagsSearch covers updateKey's
// "b"/"d"/"x"/"r"/"t"/"T"/"/"/"f" branches on tab 1 (Selected -> b/d) and
// tab 3 (Bookmarks -> the rest), plus the tab-2 dock "b"/"x" branches.
func TestWorkspacePanelBookmarkKeysBDockRenameTagsSearch(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0}, Title: "Row"}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}

	// tab 1: "b" bookmarks the current selection. setTab (not a raw tab
	// assignment) persists ActiveTab, so refresh() -- triggered by every
	// performWorkspaceAction below -- recomputes p.tab back to the same
	// tab instead of resetting it to the default.
	u.workspace.setTab(1)
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'b', Text: "b"})
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot.Bookmarks) != 1 {
		t.Fatalf("expected 'b' on Selected tab to create a bookmark: %+v, %v", snapshot.Bookmarks, err)
	}
	// tab 1: "d" docks the current selection.
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.Docks) != 1 {
		t.Fatalf("expected 'd' on Selected tab to dock the current selection: %+v, %v", snapshot.Workspace.Docks, err)
	}

	// tab 2: "b" bookmarks the dock's own reference (non-bookmark kind).
	u.workspace.setTab(2)
	u.workspace.dockIndex = 0
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'b', Text: "b"})
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil || len(snapshot.Bookmarks) != 2 {
		t.Fatalf("expected 'b' on Docked tab to create another bookmark: %+v, %v", snapshot.Bookmarks, err)
	}
	// tab 2: "x" undocks.
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.Docks) != 0 {
		t.Fatalf("expected 'x' on Docked tab to undock: %+v, %v", snapshot.Workspace.Docks, err)
	}

	// tab 3: bookmark editor round trips for rename/tag_add/tag_remove/
	// search/tags/delete, driven through updateKey's start + updateKey's
	// enter-commit path (updateBookmarkInput).
	u.workspace.setTab(3)
	if err := u.workspace.refreshBookmarks(); err != nil {
		t.Fatal(err)
	}
	if len(u.workspace.bookmarkItems) != 2 {
		t.Fatalf("expected 2 bookmarks, got %d", len(u.workspace.bookmarkItems))
	}
	u.workspace.bookmarkIndex = 0

	u.workspace.updateKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if u.workspace.bookmarkMode != "rename" {
		t.Fatalf("expected 'r' to start rename mode, got %q", u.workspace.bookmarkMode)
	}
	u.workspace.bookmarkEditor.SetValue("Renamed")
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var renamed bool
	for _, b := range snapshot.Bookmarks {
		if b.Title == "Renamed" {
			renamed = true
		}
	}
	if !renamed {
		t.Fatalf("expected the bookmark to be renamed: %+v", snapshot.Bookmarks)
	}

	u.workspace.updateKey(tea.KeyPressMsg{Code: 't', Text: "t"})
	if u.workspace.bookmarkMode != "tag_add" {
		t.Fatalf("expected 't' to start tag_add mode, got %q", u.workspace.bookmarkMode)
	}
	u.workspace.bookmarkEditor.SetValue("reporting")
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	u.workspace.updateKey(tea.KeyPressMsg{Code: 'T', Text: "T"})
	if u.workspace.bookmarkMode != "tag_remove" {
		t.Fatalf("expected 'T' to start tag_remove mode, got %q", u.workspace.bookmarkMode)
	}
	u.workspace.bookmarkEditor.SetValue("reporting")
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range snapshot.Bookmarks {
		if b.Title == "Renamed" && len(b.Tags) != 0 {
			t.Fatalf("expected the tag to be removed: %+v", b.Tags)
		}
	}

	u.workspace.updateKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	if u.workspace.bookmarkMode != "search" {
		t.Fatalf("expected '/' to start search mode, got %q", u.workspace.bookmarkMode)
	}
	u.workspace.bookmarkEditor.SetValue("no-such-bookmark-title")
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(u.workspace.bookmarkItems) != 0 {
		t.Fatalf("expected the search to filter out every bookmark: %+v", u.workspace.bookmarkItems)
	}
	u.workspace.bookmarkSearch = ""
	if err := u.workspace.refreshBookmarks(); err != nil {
		t.Fatal(err)
	}

	u.workspace.updateKey(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if u.workspace.bookmarkMode != "tags" {
		t.Fatalf("expected 'f' to start tags-filter mode, got %q", u.workspace.bookmarkMode)
	}
	u.workspace.bookmarkEditor.SetValue("no-such-tag")
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(u.workspace.bookmarkItems) != 0 {
		t.Fatalf("expected the tag filter to exclude every bookmark: %+v", u.workspace.bookmarkItems)
	}
	u.workspace.bookmarkTags = nil
	if err := u.workspace.refreshBookmarks(); err != nil {
		t.Fatal(err)
	}

	u.workspace.updateKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if u.workspace.bookmarkMode != "delete" {
		t.Fatalf("expected 'x' to start delete-confirm mode, got %q", u.workspace.bookmarkMode)
	}
	u.workspace.bookmarkEditor.SetValue("delete")
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	snapshot, err = sessions.Snapshot(ctx)
	if err != nil || len(snapshot.Bookmarks) != 1 {
		t.Fatalf("expected the confirmed delete to remove one bookmark: %+v, %v", snapshot.Bookmarks, err)
	}
}

// TestWorkspacePanelXDetachesLastAttachmentWhenNoOtherTargetApplies covers
// updateKey's "x" fallback branch: outside tab 2/3 with an existing
// attachment, it detaches the last one.
func TestWorkspacePanelXDetachesLastAttachmentWhenNoOtherTargetApplies(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	ref := workspaceTestCatalog().Objects[1].Reference
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.workspace.tab = 0
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("expected 'x' to detach the last attachment: %+v, %v", snapshot.Workspace.Attachments, err)
	}
}

// TestWorkspacePanelUpdateBookmarkInputTypesIntoEditor covers
// updateBookmarkInput's non-Enter branch: ordinary key input is forwarded
// to the textinput.Model.
func TestWorkspacePanelUpdateBookmarkInputTypesIntoEditor(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.startBookmarkInput("rename", "Bookmark title")
	u.workspace.updateBookmarkInput(tea.KeyPressMsg{Text: "x"})
	if u.workspace.bookmarkEditor.Value() != "x" {
		t.Fatalf("expected the keystroke to reach the editor, got %q", u.workspace.bookmarkEditor.Value())
	}
}
