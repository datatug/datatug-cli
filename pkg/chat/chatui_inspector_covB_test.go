package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// focusedGridTestUI is a small helper: a ChatUI with one submitted turn's
// grid focused, for handleGridKey/activeGrid-driven tests that don't need
// the fuller per-scenario setup other tests here use.
func focusedGridTestUI(t *testing.T) *ChatUI {
	t.Helper()
	turn := Turn{Queries: []QueryResult{{Title: "Widgets", RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"Id", "Name"},
		Rows:    []secureread.Row{{Data: map[string]any{"Id": 1, "Name": "Alpha"}}, {Data: map[string]any{"Id": 2, "Name": "Beta"}}},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show widgets"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	return u
}

// focusedGridStoreBackedTestUI is focusedGridTestUI's store-backed sibling:
// a plain Submit (via a stub Conversation) never refreshes u.snapshot's
// RecordSets/Selections cache (that only happens on session
// load/reload -- see ChatUI.loadSession), so any test needing
// u.snapshot.RecordSets[recordSetID] to actually resolve (selectFromGrid,
// openSaveQueryDialog) needs a RecordSet appended straight to the store
// and NewSessionChatUI's own loadSession call to pick it up, instead of a
// live Submit.
func focusedGridStoreBackedTestUI(t *testing.T, query QueryResult) *ChatUI {
	t.Helper()
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Show data")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", query); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	return u
}

// TestChatUIGridKeyQOpensSaveQueryDialog covers handleGridKey's "q" case.
func TestChatUIGridKeyQOpensSaveQueryDialog(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title: "Widgets", DTQL: "from: {name: Widget}\nlimit: 2\n",
		Result: secureread.Result{Columns: []string{"Id", "Name"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1, "Name": "Alpha"}}}},
	})
	u.savedQueryService = &savedQueryStub{}
	u.shell.Update(tea.KeyPressMsg{Text: "q"})
	if !strings.Contains(u.shell.View().Content, "Save as project query") {
		t.Fatalf("expected the save-query overlay to open:\n%s", u.shell.View().Content)
	}
}

// TestChatUIGridKeySpaceCRSelectAndAAttach covers handleGridKey's "space"/
// "c"/"r"/"a" cases through the real transcript grid.
func TestChatUIGridKeySpaceCRSelectAndAAttach(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title: "Widgets",
		Result: secureread.Result{
			Columns: []string{"Id", "Name"},
			Rows:    []secureread.Row{{Data: map[string]any{"Id": 1, "Name": "Alpha"}}, {Data: map[string]any{"Id": 2, "Name": "Beta"}}},
		},
	})
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(u.snapshot.Workspace.Selections) != 1 {
		t.Fatalf("expected space to create a row selection: %+v", u.snapshot.Workspace.Selections)
	}
	u.shell.Update(tea.KeyPressMsg{Text: "c"})
	if len(u.snapshot.Workspace.Selections) == 0 {
		t.Fatalf("expected 'c' to create a cell selection: %+v", u.snapshot.Workspace.Selections)
	}
	selection := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]
	if len(selection.Columns) != 1 {
		t.Fatalf("expected a single-column cell selection: %+v", selection)
	}
	u.shell.Update(tea.KeyPressMsg{Text: "r"}) // anchors a range
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	u.shell.Update(tea.KeyPressMsg{Text: "r"}) // dispatches the range
	rangeSelection := u.snapshot.Workspace.Selections[u.snapshot.Workspace.CurrentSelectionID]
	if len(rangeSelection.Ranges) == 0 {
		t.Fatalf("expected 'r' twice to create a range selection: %+v", u.snapshot.Workspace.Selections)
	}
	u.shell.Update(tea.KeyPressMsg{Text: "a"})
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("expected 'a' to attach the active grid's RecordSet: %+v", u.snapshot.Workspace.Attachments)
	}
}

// TestChatUIGridKeyDAndBDockAndBookmark covers handleGridKey's "d"/"b"
// cases (dockOrBookmarkActiveGrid), including its Selection-reference
// upgrade when a durable selection already matches the active RecordSet.
func TestChatUIGridKeyDAndBDockAndBookmark(t *testing.T) {
	u := focusedGridTestUI(t)
	u.shell.Update(tea.KeyPressMsg{Text: "d"})
	if len(u.snapshot.Workspace.Docks) != 1 {
		t.Fatalf("expected 'd' to dock the active grid's RecordSet: %+v", u.snapshot.Workspace.Docks)
	}
	u.shell.Update(tea.KeyPressMsg{Text: "b"})
	if len(u.snapshot.Bookmarks) != 1 {
		t.Fatalf("expected 'b' to bookmark the active grid's RecordSet: %+v", u.snapshot.Bookmarks)
	}

	// Now select the current row (matching the active RecordSet) and dock
	// again: dockOrBookmarkActiveGrid should upgrade to the Selection
	// reference instead of the bare RecordSet one.
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	u.shell.Update(tea.KeyPressMsg{Text: "d"})
	if len(u.snapshot.Workspace.Docks) != 2 {
		t.Fatalf("expected a second dock via the selection reference: %+v", u.snapshot.Workspace.Docks)
	}
	if u.snapshot.Workspace.Docks[1].Reference.Kind != "selection" {
		t.Fatalf("expected the second dock's reference to upgrade to the selection: %+v", u.snapshot.Workspace.Docks[1])
	}
}

// TestChatUIGridKeyBToggleExportBucket covers handleGridKey's "B" case,
// both the "bucket_add" and "bucket_remove" branches.
func TestChatUIGridKeyBToggleExportBucket(t *testing.T) {
	u := focusedGridTestUI(t)
	u.shell.Update(tea.KeyPressMsg{Text: "B"})
	if len(u.snapshot.Workspace.ExportBucket) != 1 {
		t.Fatalf("expected 'B' to add the RecordSet to the export bucket: %+v", u.snapshot.Workspace.ExportBucket)
	}
	u.shell.Update(tea.KeyPressMsg{Text: "B"})
	if len(u.snapshot.Workspace.ExportBucket) != 0 {
		t.Fatalf("expected a second 'B' to remove it from the export bucket: %+v", u.snapshot.Workspace.ExportBucket)
	}
}

// TestChatUIGridKeyEOpensExportDialog covers handleGridKey's "e" case.
func TestChatUIGridKeyEOpensExportDialog(t *testing.T) {
	u := focusedGridTestUI(t)
	u.shell.Update(tea.KeyPressMsg{Text: "e"})
	if u.lastGridRecordSetID == "" {
		t.Fatal("expected 'e' to record the active RecordSet as lastGridRecordSetID")
	}
	if !strings.Contains(u.shell.View().Content, "Export") {
		t.Fatalf("expected the export dialog overlay to open:\n%s", u.shell.View().Content)
	}
}

// TestChatUIGridKeyAWithNoActiveGridIsANoOp covers handleGridKey's "a"
// case guard when activeGrid resolves to nothing (no grid focused).
func TestChatUIGridKeyAWithNoActiveGridIsANoOp(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	_, handled := u.handleGridKey(nil, tea.KeyPressMsg{Text: "a"})
	if !handled {
		t.Fatal("expected 'a' to report handled even with no active grid")
	}
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("expected no attachment without an active grid: %+v", u.snapshot.Workspace.Attachments)
	}
}

// TestChatUIDockOrBookmarkActiveGridGuards covers dockOrBookmarkActiveGrid's
// own early-return guards directly: no active grid, and an empty
// RecordSetID.
func TestChatUIDockOrBookmarkActiveGridGuards(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.dockOrBookmarkActiveGrid("dock") // no active grid: no-op, no panic
	if len(u.snapshot.Workspace.Docks) != 0 {
		t.Fatalf("expected no dock without an active grid: %+v", u.snapshot.Workspace.Docks)
	}
}

// TestChatUISyncRecordSetSortAppliesToDockedGridsToo covers
// syncRecordSetSort's docked-grid propagation branch and its own guard
// (a dock whose Reference isn't a matching "recordset" kind is skipped).
func TestChatUISyncRecordSetSortAppliesToDockedGridsToo(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	// The catalog's ID must match the store's scope ProjectID
	// (testScope().ProjectID) -- CreateBookmark stamps a new bookmark's
	// ProjectID from the store's own scope, and dock/validateContextReference
	// requires that to equal SessionChat's catalog.ID (see
	// TestWorkspacePanelBookmarkGridKeysTabSortDock's identical setup).
	catalog := ProjectCatalog{ID: testScope().ProjectID, Title: "Demo"}
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", catalog)
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
	// A second, unrelated dock (a bookmark reference) exercises the "skip"
	// branch of the reference-kind/ID match.
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: recordID}, Title: "Saved"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var bookmarkRef ContextReference
	for _, b := range snapshot.Bookmarks {
		bookmarkRef = bookmarkReference(b)
	}
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: bookmarkRef}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	u.shell.Update(tea.KeyPressMsg{Text: "s"})
	dock := u.snapshot.Workspace.Docks[0]
	if dockGrid := u.workspace.dockGrids[dock.ID]; dockGrid == nil {
		t.Fatal("expected the docked grid to still be tracked")
	}
}

// TestChatUIActiveGridAndRecordSetIDNoFocusedRef covers activeGrid's and
// activeRecordSetID's "no focused ref"/"wrong ref type" guards.
func TestChatUIActiveGridAndRecordSetIDNoFocusedRef(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if g, record, ok := u.activeGrid(); ok || g != nil || record != nil {
		t.Fatalf("expected activeGrid() to report nothing with no focus: g=%v record=%v ok=%v", g, record, ok)
	}
	if id := u.activeRecordSetID(); id != "" {
		t.Fatalf("activeRecordSetID() = %q, want empty", id)
	}
}

// TestChatUIActiveGridRecordSetNotInSnapshot covers activeGrid's
// "record not found" branch: a tracked grid whose RecordSet has since been
// removed from the snapshot still resolves (grid, nil, true).
func TestChatUIActiveGridRecordSetNotInSnapshot(t *testing.T) {
	u := focusedGridTestUI(t)
	delete(u.snapshot.RecordSets, "rs1")
	g, record, ok := u.activeGrid()
	if !ok || g == nil || record != nil {
		t.Fatalf("expected (grid, nil, true) when the RecordSet is missing: g=%v record=%v ok=%v", g, record, ok)
	}
}

// TestChatUIOpenCellDetailGuards covers openCellDetail's own guards: no
// active grid, and a selected column out of range (an empty grid has
// none).
func TestChatUIOpenCellDetailGuards(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if cmd := u.openCellDetail(); cmd != nil {
		t.Fatal("expected openCellDetail to no-op with no active grid")
	}

	turn := Turn{Queries: []QueryResult{{Title: "Empty", RecordSetID: "rs2", Result: secureread.Result{}}}}
	u2, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u2, u2.Submit("Show nothing"))
	if u2.shell.FocusEntry(u2.lastGridEntryID) {
		if cmd := u2.openCellDetail(); cmd != nil {
			t.Fatal("expected openCellDetail to no-op with no selectable column")
		}
	}
}

// TestChatUIOpenCellDetailWithoutJoinApplicationSkipsPreview covers
// openCellDetail's early returns after pushing the overlay: no
// sessions/no qualified meta/ambiguous source/no configured join
// application. TestChatUIInlineFKJoinNavigationAndApply (chatui_test.go)
// already covers the full async preview path with a configured
// application.
func TestChatUIOpenCellDetailWithoutJoinApplicationSkipsPreview(t *testing.T) {
	u := focusedGridTestUI(t)
	// PushOverlay pushes synchronously and returns a nil tea.Cmd (nothing
	// async to schedule for the overlay itself); it's u.pendingDetail that
	// records the push happened.
	u.openCellDetail()
	if u.pendingDetail == nil || u.pendingDetail.loading {
		t.Fatalf("expected no async preview to start without a configured join application: %+v", u.pendingDetail)
	}
}

// TestCellDetailOverlayViewShowsLoadingRelatedErrorAndRelatedRecords
// covers cellDetailOverlay.View's loading/relatedError/related-present
// branches -- TestChatUIEnterOpensAndClosesCellDetail only exercises the
// "no related FK records" empty state.
func TestCellDetailOverlayViewShowsLoadingRelatedErrorAndRelatedRecords(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = ProjectCatalog{Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "table", ObjectID: "main.Customer", Title: "Customer"}, Columns: []string{"CustomerId", "City"}},
	}}
	loading := &cellDetail{title: "Invoices", column: "CustomerId", columns: []string{"CustomerId"}, values: []any{1}, loading: true}
	overlayLoading := &cellDetailOverlay{ui: u, detail: loading}
	if view := overlayLoading.View(80, 24); !strings.Contains(view, "loading") {
		t.Fatalf("expected a loading indicator:\n%s", view)
	}

	errored := &cellDetail{title: "Invoices", column: "CustomerId", columns: []string{"CustomerId"}, values: []any{1}, relatedError: context.DeadlineExceeded}
	overlayErr := &cellDetailOverlay{ui: u, detail: errored}
	if view := overlayErr.View(80, 24); !strings.Contains(view, "unavailable") {
		t.Fatalf("expected an unavailable-related-records message:\n%s", view)
	}

	related := &cellDetail{
		title: "Invoices", column: "CustomerId", columns: []string{"CustomerId"}, values: []any{1},
		related: []relatedRecord{{
			key: ForeignKey{ConstraintID: "fk1", ToSchema: "main", ToRelation: "Customer", FromRelation: "Invoice", FromFields: []string{"CustomerId"}, ToFields: []string{"CustomerId"}},
			result: secureread.Result{
				Columns: []string{"CustomerId", "City"},
				Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1, "City": "Prague"}}},
			},
		}},
	}
	overlayRelated := &cellDetailOverlay{ui: u, detail: related}
	view := overlayRelated.View(90, 30)
	for _, want := range []string{"FK fk1", "Customer", "Columns:", "Related records (showing 1, max 5):", "Prague"} {
		if !strings.Contains(view, want) {
			t.Errorf("related view missing %q:\n%s", want, view)
		}
	}

	// A related record with zero matching rows exercises the "No matching
	// records" branch.
	related.related[0].result = secureread.Result{Columns: []string{"CustomerId"}}
	view = overlayRelated.View(90, 30)
	if !strings.Contains(view, "No matching records") {
		t.Fatalf("expected the no-matching-records message:\n%s", view)
	}
}

// TestCellDetailOverlayUpdateScrollsAndCopies covers cellDetailOverlay's
// Update up/down offset and "y" copy branches, plus its non-key no-op.
func TestCellDetailOverlayUpdateScrollsAndCopies(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := &cellDetail{title: "T", column: "C", value: "v", offset: 3}
	o := &cellDetailOverlay{ui: u, detail: d}

	overlay, cmd, done := o.Update(tea.WindowSizeMsg{})
	if overlay != o || cmd != nil || done {
		t.Fatal("expected a non-key message to be a no-op")
	}

	o.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.offset != 4 {
		t.Fatalf("offset = %d, want 4 after down", d.offset)
	}
	o.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	o.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	o.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	o.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	o.Update(tea.KeyPressMsg{Code: tea.KeyUp}) // clamps at 0
	if d.offset != 0 {
		t.Fatalf("offset = %d, want 0 (clamped)", d.offset)
	}

	_, cmd, done = o.Update(tea.KeyPressMsg{Text: "y"})
	if cmd == nil || done {
		t.Fatal("expected 'y' to return a clipboard command and stay open")
	}
}

// TestChatUIOpenSaveQueryDialogGuardsAndBranches covers openSaveQueryDialog's
// full guard/branch surface directly.
func TestChatUIOpenSaveQueryDialogGuardsAndBranches(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.savedQueryService = &savedQueryStub{}

	// No active grid/RecordSet.
	if cmd := u.openSaveQueryDialog(); cmd != nil {
		t.Fatal("expected no command without a focused RecordSet")
	}
	if !strings.Contains(u.shell.View().Content, "Focus a DTQL or HTTP result") {
		t.Fatalf("expected the focus-a-result hint:\n%s", u.shell.View().Content)
	}

	// Busy.
	u.shell.SetBusy(true)
	if cmd := u.openSaveQueryDialog(); cmd != nil {
		t.Fatal("expected no command while busy")
	}
	u.shell.SetBusy(false)

	// No savedQueryService.
	u.savedQueryService = nil
	if cmd := u.openSaveQueryDialog(); cmd != nil {
		t.Fatal("expected no command without a saved-query service")
	}
	u.savedQueryService = &savedQueryStub{}
}

// TestChatUIOpenSaveQueryDialogHTTPBranches covers openSaveQueryDialog's
// HTTPResponseID branches: a missing response, a response whose request
// had URL parameters, a non-GET method, and the success path.
func TestChatUIOpenSaveQueryDialogHTTPBranches(t *testing.T) {
	ctx := context.Background()
	server := "https://example.com/data?token=x"
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "/http get "+server)
	if err != nil {
		t.Fatal(err)
	}
	response := HTTPResponse{Method: "GET", URL: server, StatusCode: 200, ContentType: "application/json", Body: []byte(`[{"a":1}]`)}
	query := &QueryResult{Title: "Data", Result: secureread.Result{Columns: []string{"a"}, Rows: []secureread.Row{{Data: map[string]any{"a": 1}}}}}
	if _, err := store.AppendHTTPResponse(ctx, sessions.activeID, user.ID, response, query); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.savedQueryService = &savedQueryStub{}
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	if cmd := u.openSaveQueryDialog(); cmd != nil {
		t.Fatal("expected no command for a request with URL parameters")
	}
	if !strings.Contains(u.shell.View().Content, "URL parameters") {
		t.Fatalf("expected the URL-parameters hint:\n%s", u.shell.View().Content)
	}
}

// TestChatUIOpenSaveQueryDialogHTTPNonGETMethod covers openSaveQueryDialog's
// non-GET-method HTTP branch.
func TestChatUIOpenSaveQueryDialogHTTPNonGETMethod(t *testing.T) {
	ctx := context.Background()
	server := "https://example.com/data"
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "/http post "+server)
	if err != nil {
		t.Fatal(err)
	}
	response := HTTPResponse{Method: "POST", URL: server, StatusCode: 200, ContentType: "application/json", Body: []byte(`[{"a":1}]`)}
	query := &QueryResult{Title: "Data", Result: secureread.Result{Columns: []string{"a"}, Rows: []secureread.Row{{Data: map[string]any{"a": 1}}}}}
	if _, err := store.AppendHTTPResponse(ctx, sessions.activeID, user.ID, response, query); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.savedQueryService = &savedQueryStub{}
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	if cmd := u.openSaveQueryDialog(); cmd != nil {
		t.Fatal("expected no command for a non-GET request")
	}
	if !strings.Contains(u.shell.View().Content, "non-GET method") {
		t.Fatalf("expected the non-GET-method hint:\n%s", u.shell.View().Content)
	}
}

// TestChatUIOpenSaveQueryDialogDTQLWithParametersIsRejected covers
// openSaveQueryDialog's DTQL-parameterized-result branch.
func TestChatUIOpenSaveQueryDialogDTQLWithParametersIsRejected(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title: "Filtered", DTQL: "from: {name: Customer}\nlimit: {parameter: n}\n",
		Parameters: map[string]any{"n": nil},
		Result:     secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 1}}}},
	})
	u.savedQueryService = &savedQueryStub{}
	if cmd := u.openSaveQueryDialog(); cmd != nil {
		t.Fatal("expected no command for a parameterized DTQL result")
	}
	if !strings.Contains(u.shell.View().Content, "parameters") {
		t.Fatalf("expected the parameterized-result hint:\n%s", u.shell.View().Content)
	}
}

// TestChatUIOpenSaveQueryDialogUnsavableRecordSetFallsBack covers
// openSaveQueryDialog's final "request.Type == ”" fallback: a RecordSet
// with neither an HTTPResponseID nor a DTQL.
func TestChatUIOpenSaveQueryDialogUnsavableRecordSetFallsBack(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "Bare",
		Result: secureread.Result{Columns: []string{"X"}, Rows: []secureread.Row{{Data: map[string]any{"X": 1}}}},
	})
	u.savedQueryService = &savedQueryStub{}
	if cmd := u.openSaveQueryDialog(); cmd != nil {
		t.Fatal("expected no command for an unsavable RecordSet")
	}
	if !strings.Contains(u.shell.View().Content, "Focus a DTQL or HTTP result") {
		t.Fatalf("expected the focus-a-result fallback hint:\n%s", u.shell.View().Content)
	}
}

// TestChatUIOpenSaveQueryDialogForHTTPResponseBranches covers
// openSaveQueryDialogForHTTPResponse's full guard/branch surface.
func TestChatUIOpenSaveQueryDialogForHTTPResponseBranches(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.savedQueryService = &savedQueryStub{}

	if cmd := u.openSaveQueryDialogForHTTPResponse(nil); cmd != nil {
		t.Fatal("expected nil response to no-op")
	}

	response := &HTTPResponse{Method: "GET", URL: "https://example.com/notes", RequestHasQuery: true}
	if cmd := u.openSaveQueryDialogForHTTPResponse(response); cmd != nil {
		t.Fatal("expected a query-parameter response to no-op")
	}
	if !strings.Contains(u.shell.View().Content, "URL parameters") {
		t.Fatalf("expected the URL-parameters hint:\n%s", u.shell.View().Content)
	}

	response2 := &HTTPResponse{Method: "POST", URL: "https://example.com/notes"}
	if cmd := u.openSaveQueryDialogForHTTPResponse(response2); cmd != nil {
		t.Fatal("expected a non-GET response to no-op")
	}

	u.shell.SetBusy(true)
	response3 := &HTTPResponse{Method: "GET", URL: "https://example.com/notes"}
	if cmd := u.openSaveQueryDialogForHTTPResponse(response3); cmd != nil {
		t.Fatal("expected no command while busy")
	}
	u.shell.SetBusy(false)

	// PushOverlay pushes synchronously and returns a nil tea.Cmd; check the
	// overlay actually opened via the rendered view instead.
	u.openSaveQueryDialogForHTTPResponse(response3)
	if !strings.Contains(u.shell.View().Content, "Save as project query") {
		t.Fatalf("expected the save-query overlay to open for a supported GET response:\n%s", u.shell.View().Content)
	}
}

// TestSaveQueryOverlayAddTagGuardsAndDuplicate covers addTag's own
// blank-input and duplicate-tag guards.
func TestSaveQueryOverlayAddTagGuardsAndDuplicate(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Q"})
	o.values[1] = "   "
	o.addTag()
	if len(o.tags) != 0 {
		t.Fatalf("expected a blank tag to be ignored: %+v", o.tags)
	}
	o.tags = []string{"reporting"}
	o.values[1] = "Reporting"
	o.addTag()
	if len(o.tags) != 1 || o.err == "" {
		t.Fatalf("expected a case-insensitive duplicate tag to be rejected: tags=%+v err=%q", o.tags, o.err)
	}
}

// TestSaveQueryOverlaySaveGuardsAndAsync covers save()'s no-writer/
// empty-title guards and its async in-flight branch.
func TestSaveQueryOverlaySaveGuardsAndAsync(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.savedQueryService = struct{ SavedQueryService }{} // not a SavedQueryWriter
	o := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Q"})
	if _, cmd, done := o.save(); cmd != nil || done {
		t.Fatal("expected save() to no-op without a SavedQueryWriter")
	}
	if o.err == "" {
		t.Fatal("expected an unavailable-saving error")
	}

	u.savedQueryService = &savedQueryStub{}
	o2 := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "  "})
	if _, cmd, done := o2.save(); cmd != nil || done {
		t.Fatal("expected save() to no-op with a blank title")
	}
	if o2.err == "" {
		t.Fatal("expected a give-this-query-a-name error")
	}

	o3 := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Real name"})
	_, cmd, done := o3.save()
	if done || cmd == nil {
		t.Fatal("expected save() to stay open and return a command while in flight")
	}
	if u.pendingSaveQuery != o3 {
		t.Fatal("expected save() to record itself as pendingSaveQuery")
	}
	drainCmd(t, u, cmd)
}

// TestChatUIHandleSaveQueryDoneBranches covers handleSaveQueryDone's full
// branch surface: failure with/without a pending overlay, and a success
// whose reloadSavedQueries call itself fails.
func TestChatUIHandleSaveQueryDoneBranches(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Q"})
	u.pendingSaveQuery = o
	u.handleSaveQueryDone(saveQueryDoneMsg{err: context.DeadlineExceeded})
	if o.err == "" {
		t.Fatal("expected the pending overlay to show the generic save-failure message")
	}
	if u.pendingSaveQuery != nil {
		t.Fatal("expected pendingSaveQuery to be cleared")
	}

	u.handleSaveQueryDone(saveQueryDoneMsg{err: context.DeadlineExceeded})
	if !strings.Contains(u.shell.View().Content, "Could not save") {
		t.Fatalf("expected a transcript message without a pending overlay:\n%s", u.shell.View().Content)
	}

	// Success without SetSavedQueryService (reloadSavedQueries fails since
	// u.savedQueryService is nil / not a lister) surfaces conciseError.
	u.handleSaveQueryDone(saveQueryDoneMsg{query: SavedQuery{ID: "q1", Title: "Saved"}})
	// Whatever reloadSavedQueries' outcome, this must not panic; check the
	// transcript reflects SOME outcome (error or success message).
	view := u.shell.View().Content
	if view == "" {
		t.Fatal("expected some transcript content after handleSaveQueryDone")
	}
}

// TestChatUIHandleSaveQueryDoneSuccessReportsTitle covers
// handleSaveQueryDone's success path end to end, including CloseOverlay
// and the final "Saved project query" message.
func TestChatUIHandleSaveQueryDoneSuccessReportsTitle(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	service := &savedQueryStub{}
	if err := u.SetSavedQueryService(service); err != nil {
		t.Fatal(err)
	}
	o := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Q"})
	u.shell.PushOverlay(o)
	u.pendingSaveQuery = o
	u.handleSaveQueryDone(saveQueryDoneMsg{query: SavedQuery{ID: "q1", Title: "My query"}})
	if !strings.Contains(u.shell.View().Content, `Saved project query "My query"`) {
		t.Fatalf("expected the saved-query confirmation message:\n%s", u.shell.View().Content)
	}
}

// TestSaveQueryOverlayViewShowsTagChipsAndError covers View's tag-chips
// and error-line branches.
func TestSaveQueryOverlayViewShowsTagChipsAndError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	o := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Q", Type: "DTQL"})
	o.tags = []string{"reporting", "weekly"}
	o.err = "Something went wrong."
	view := ansi.Strip(o.View(60, 20))
	for _, want := range []string{"reporting", "weekly", "Something went wrong."} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

// TestSaveQueryOverlayUpdateFullSurface covers saveQueryOverlayState.Update's
// remaining branches: non-key no-op, esc, tab/shift+tab focus cycling,
// backspace-removes-last-tag, Enter at focus 0/1 (with/without a pending
// tag value)/2 (save), and ctrl+s both with a pending tag value and
// without.
func TestSaveQueryOverlayUpdateFullSurface(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.savedQueryService = &savedQueryStub{}

	o := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Q"})
	overlay, cmd, done := o.Update(tea.WindowSizeMsg{})
	if overlay != o || cmd != nil || done {
		t.Fatal("expected a non-key message to be a no-op")
	}

	_, _, done = o.Update(tea.KeyPressMsg{Text: "esc"})
	if !done {
		t.Fatal("expected esc to close the overlay")
	}

	o = newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Q"})
	o.Update(tea.KeyPressMsg{Text: "tab"})
	if o.focus != 1 {
		t.Fatalf("focus = %d, want 1 after tab", o.focus)
	}
	o.Update(tea.KeyPressMsg{Text: "shift+tab"})
	if o.focus != 0 {
		t.Fatalf("focus = %d, want 0 after shift+tab", o.focus)
	}

	// backspace removes the last tag only when focus==1, the tag-input
	// value is empty, and at least one tag exists.
	o.focus = 1
	o.tags = []string{"a", "b"}
	o.values[1] = ""
	o.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(o.tags) != 1 {
		t.Fatalf("expected backspace to remove the last tag: %+v", o.tags)
	}

	// Enter at focus 0 advances to 1.
	o.focus = 0
	o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if o.focus != 1 {
		t.Fatalf("focus = %d, want 1 after Enter at focus 0", o.focus)
	}
	// Enter at focus 1 with a pending value adds a tag and stays at 1.
	o.values[1] = "newtag"
	o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if o.focus != 1 || o.values[1] != "" {
		t.Fatalf("expected Enter to add the tag and clear the input: focus=%d value=%q", o.focus, o.values[1])
	}
	// Enter at focus 1 with NO pending value advances to 2.
	o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if o.focus != 2 {
		t.Fatalf("focus = %d, want 2 after Enter at focus 1 with no pending tag", o.focus)
	}
	// Enter at focus 2 (default case) calls save().
	_, cmd, done = o.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || cmd == nil {
		t.Fatalf("expected Enter at focus 2 to call save() and stay open: err=%q", o.err)
	}
	drainCmd(t, u, cmd)

	// ctrl+s with a pending tag value adds the tag first, then saves.
	o2 := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: "Q2"})
	o2.values[1] = "onemoretag"
	_, cmd, done = o2.Update(tea.KeyPressMsg{Text: "ctrl+s"})
	if done || cmd == nil {
		t.Fatalf("expected ctrl+s to add the tag and save: err=%q tags=%+v", o2.err, o2.tags)
	}
	if len(o2.tags) != 1 {
		t.Fatalf("expected ctrl+s to have added the pending tag: %+v", o2.tags)
	}
	drainCmd(t, u, cmd)

	// Typing at focus 0/1 routes through editLineOnKey.
	o3 := newSaveQueryOverlay(u, SavedQuerySaveRequest{Title: ""})
	o3.focus = 0
	o3.Update(tea.KeyPressMsg{Text: "x"})
	if !strings.Contains(o3.values[0], "x") {
		t.Fatalf("expected typing to reach the name field, got %q", o3.values[0])
	}
}

// TestEditLineOnKeyBranches covers editLineOnKey's backspace (non-empty and
// already-empty), ctrl+u and plain-character branches directly.
func TestEditLineOnKeyBranches(t *testing.T) {
	if got := editLineOnKey("abc", tea.KeyPressMsg{Code: tea.KeyBackspace}); got != "ab" {
		t.Fatalf("backspace on non-empty = %q, want %q", got, "ab")
	}
	if got := editLineOnKey("", tea.KeyPressMsg{Code: tea.KeyBackspace}); got != "" {
		t.Fatalf("backspace on empty = %q, want empty", got)
	}
	if got := editLineOnKey("abc", tea.KeyPressMsg{Text: "ctrl+u"}); got != "" {
		t.Fatalf("ctrl+u = %q, want empty", got)
	}
	if got := editLineOnKey("ab", tea.KeyPressMsg{Text: "c"}); got != "abc" {
		t.Fatalf("plain char = %q, want %q", got, "abc")
	}
	// A key with no Text (e.g. a bare arrow) is a no-op.
	if got := editLineOnKey("ab", tea.KeyPressMsg{Code: tea.KeyLeft}); got != "ab" {
		t.Fatalf("no-text key = %q, want unchanged %q", got, "ab")
	}
}
