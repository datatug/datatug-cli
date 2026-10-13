package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/resultops"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func TestUnifiedWorkspaceRelationshipsIncomingCompositeAndPolicySlots(t *testing.T) {
	snapshot := joinSnapshot()
	executor := &joinExecutorStub{}
	a := ForeignKeyJoinApplication{Source: snapshot.Source, Snapshot: snapshot, Executor: executor}
	record := RecordSet{Source: a.Source, DTQL: "from: {schema: main, name: Location}\nlimit: 5\n"}
	items, err := a.PreviewRelationships(context.Background(), record, map[string]any{"main.location.country": "NZ", "main.location.code": "AKL"})
	if err != nil || len(items) != 1 || !items[0].incoming || items[0].target().Relation != "Order" || items[0].err != nil {
		t.Fatalf("incoming preview: %+v, %v", items, err)
	}
	if !strings.Contains(executor.doc, "Country") || !strings.Contains(executor.doc, "Location") || !strings.Contains(executor.doc, "AKL") || !strings.Contains(executor.doc, "limit: 5") {
		t.Fatalf("composite bounded query: %s", executor.doc)
	}
	executor.doc = ""
	items, err = a.PreviewRelationships(context.Background(), record, map[string]any{"main.location.country": "NZ", "main.location.code": nil})
	if err != nil || len(items) != 1 || items[0].err == nil || executor.doc != "" {
		t.Fatal("null key must retain an unavailable slot without executing")
	}
	a.CanReadTarget = func(context.Context, RelationInstance) error { return errors.New("private credentials") }
	items, err = a.PreviewRelationships(context.Background(), record, map[string]any{"main.location.country": "NZ", "main.location.code": "AKL"})
	if err != nil || len(items) != 1 || items[0].err == nil || strings.Contains(items[0].err.Error(), "private") || executor.doc != "" {
		t.Fatal("denied relationship executed or leaked denial details")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.CanReadTarget = nil
	if _, err = a.PreviewRelationships(ctx, record, map[string]any{"main.location.country": "NZ", "main.location.code": "AKL"}); err == nil || executor.doc != "" {
		t.Fatal("cancelled preview executed")
	}
}

func TestUnifiedWorkspaceInspectionUsesDeclaredCompositeIdentity(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Widgets", DTQL: "from: {schema: main, name: Widgets}\nlimit: 5\n", Result: secureread.Result{Columns: []string{"Id", "Name"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1, "Name": "Alpha"}}}}})
	g, record, _ := u.activeGrid()
	record.DTQL = "from: {schema: main, name: Widgets}\nlimit: 5\n"
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Widgets", SourceID: record.Database, Title: "Widgets"}, Columns: []string{"Id", "Name"}, PrimaryKey: []string{"Id", "Name"}}}}
	if got := u.inspectionIdentity(g, record); got != "Widgets / Id=1 / Name=Alpha" {
		t.Fatalf("identity=%q", got)
	}
	u.catalog.Objects[0].PrimaryKey = nil
	if got := u.inspectionIdentity(g, record); !strings.Contains(got, "identity unavailable") {
		t.Fatalf("guessed Id identity: %q", got)
	}
	u.catalog.Objects[0].PrimaryKey = []string{"missing"}
	if got := u.inspectionIdentity(g, record); !strings.Contains(got, "identity unavailable") {
		t.Fatalf("missing key identity: %q", got)
	}
}

func TestUnifiedWorkspaceRelatedIgnoresLateGenerationAndCancelledRequests(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Widgets", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1}}}}})
	p := u.workspace
	g, r, _ := u.activeGrid()
	p.relatedSequence = 4
	p.relatedLoading = true
	msg := inspectorRelatedMsg{sequence: 4, sessionID: u.sessionID, generation: u.workspaceGeneration + 1, recordID: r.ID, rowKey: g.sourceRowKey(), related: []relatedRecord{{}}}
	p.receiveRelated(msg)
	if len(p.relatedRecords) != 0 || !p.relatedLoading {
		t.Fatal("late generation changed selection")
	}
	msg.generation = u.workspaceGeneration
	p.cancelRelated()
	p.receiveRelated(msg)
	if len(p.relatedRecords) != 0 {
		t.Fatal("cancelled request installed results")
	}
	if candidateDigit(9) != "0" || candidateDigit(0) != "1" {
		t.Fatal("candidate shortcuts must map zero to tenth")
	}
}

func TestUnifiedWorkspaceExplorerActionsAndEditableQueryPreserveDraft(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	catalog := workspaceTestCatalog()
	catalog.Objects[2].Reference.ProjectID = testScope().ProjectID
	catalog.ID = testScope().ProjectID
	for i := range catalog.Objects {
		catalog.Objects[i].Reference.ProjectID = catalog.ID
		if catalog.Objects[i].Reference.SourceID != "" {
			catalog.Objects[i].Reference.SourceID = testScope().Database
		}
		if catalog.Objects[i].Reference.Kind == "project" {
			catalog.Objects[i].Reference.ObjectID = catalog.ID
		}
		if catalog.Objects[i].Reference.Kind == "source" {
			catalog.Objects[i].Reference.ObjectID = testScope().Database
		}
	}
	catalog.Objects[2].Reference.SourceID = testScope().Database
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, testScope().Sources[testScope().Database], catalog)
	if err != nil {
		t.Fatal(err)
	}
	executor := &joinExecutorStub{}
	sessions.ConfigureQueryExecutor(executor)
	u, err := NewSessionChatUI(ctx, sessions, "none")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.SetComposerText("retain composer draft")
	u.shell.FocusLeftPanel()
	for i, n := range u.explorer.explorerNodes() {
		if n.objectIndex == 2 {
			u.explorer.explorerIndex = i
		}
	}
	_, cmd := u.shell.Update(uitest.Key("ctrl+n"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	if u.queryEditor == nil || executor.doc != "" || u.shell.WorkspaceFocus() != "center" {
		t.Fatalf("New query must open editable document without execution: editor=%v focus=%s nodes=%+v index=%d feedback=%s", u.queryEditor, u.shell.WorkspaceFocus(), u.explorer.explorerNodes(), u.explorer.explorerIndex, u.workspaceFeedback)
	}
	draft := u.queryEditor.text.Value()
	u.shell.Update(uitest.Text("/9"))
	if !strings.Contains(u.queryEditor.text.Value(), "/9") {
		t.Fatal("slash/digits did not edit query")
	}
	before := u.queryEditor.text.Value()
	workspaceRoutingKey(u, "shift+left")
	if u.shell.WorkspaceFocus() != "explorer" || u.queryEditor.text.Value() != before {
		t.Fatal("editor Shift arrows lost draft or panel navigation")
	}
	workspaceRoutingKey(u, "shift+right")
	// Explicit Select text is the second Tab-reachable action.
	u.queryEditor.focus = 0
	u.shell.Update(uitest.Key("tab"))
	u.shell.Update(uitest.Key("tab"))
	_, cmd = u.shell.Update(uitest.Key("enter"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	u.shell.Update(uitest.Key("left"))
	u.shell.Update(uitest.Key("enter"))
	if u.queryEditor.selection == "" || u.queryEditor.text.Value() != before {
		t.Fatal("selection modal mutated draft or failed to select")
	}
	_, cmd = u.shell.Update(uitest.Key("esc"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	u.shell.FocusLeftPanel()
	u.shell.Update(uitest.Key("alt+a"))
	if len(u.snapshot.Workspace.Attachments) != 1 || executor.doc != "" || !strings.Contains(u.shell.View().Content, "retain composer draft") {
		t.Fatal("Attach must retain composer and execute nothing")
	}
	u.shell.FocusLeftPanel()
	// Restore a valid initial query before opening data through the real shell.
	u.queryEditor.text.SetValue(draft)
	_, cmd = u.shell.Update(uitest.Key("enter"))
	if cmd == nil {
		t.Fatal("Open data produced no secure execution")
	}
	drainCmd(t, u, cmd)
	if executor.doc == "" || len(u.snapshot.RecordSets) != 1 {
		t.Fatal("Open data failed to append result")
	}
}

func TestUnifiedWorkspaceJoinCtrlJAndStalePickerCannotExecute(t *testing.T) {
	u := focusedGridTestUI(t)
	o := &joinPickerOverlay{ui: u, sessionID: u.sessionID, generation: u.workspaceGeneration, candidates: []JoinCandidate{{ID: "candidate"}}}
	_, cmd, done := o.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl})
	if cmd != nil || done {
		t.Fatal("Ctrl+J/LF invoked JOIN")
	}
	o.generation--
	if cmd = o.apply(0); cmd != nil {
		t.Fatal("stale picker invoked JOIN")
	}
}

func TestUnifiedWorkspaceRelatedViewNeverShowsPreviousRowRelations(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Invoice", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1}}, {Data: map[string]any{"Id": 2}}}}})
	g, record, _ := u.activeGrid()
	p := u.workspace
	p.relatedRecordID = record.ID
	p.relatedRowKey = g.sourceRowKey()
	p.relatedSessionID = u.sessionID
	p.relatedGeneration = u.workspaceGeneration
	p.relatedRecords = []relatedRecord{{key: ForeignKey{ToRelation: "Previous relation"}}}
	if !strings.Contains(p.relatedView(80), "Previous relation") {
		t.Fatal("current relationships absent")
	}
	g.Update(uitest.Key("down"))
	if strings.Contains(p.relatedView(80), "Previous relation") || !strings.Contains(p.relatedView(80), "F10 loads") {
		t.Fatal("stale relationships displayed under a new row")
	}
}

func TestUnifiedWorkspaceTextSelectionAnchorCopyAndLineMovement(t *testing.T) {
	u := focusedGridTestUI(t)
	p := &workspaceQueryEditor{ui: u}
	s := &queryTextSelection{editor: p, text: []rune("abc\ndefgh\nijk"), anchor: 1, cursor: 1}
	s.Update(uitest.Key("down"))
	if s.cursor != 5 {
		t.Fatalf("down should preserve column: %d", s.cursor)
	}
	s.Update(uitest.Key("a"))
	s.Update(uitest.Key("right"))
	s.Update(uitest.Key("right"))
	_, cmd, done := s.Update(uitest.Key("ctrl+c"))
	if cmd == nil || done || s.selected() != "ef" {
		t.Fatalf("non-prefix selection/copy failed: %q", s.selected())
	}
	s.Update(uitest.Key("enter"))
	if p.selection != "ef" {
		t.Fatal("accepted selection unavailable for Copy selected")
	}
}

// gridActionJourney dispatches the advertised menu through the real shell.
func gridActionJourney(t *testing.T, u *ChatUI, label string) {
	t.Helper()
	index := -1
	for i, action := range (&gridActionsOverlay{ui: u}).labels() {
		if action == label {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatalf("unknown action %s", label)
	}
	_, cmd := u.shell.Update(uitest.Key("alt+a"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	for range index {
		u.shell.Update(uitest.Key("down"))
	}
	_, cmd = u.shell.Update(uitest.Key("enter"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
}
func TestUnifiedWorkspaceGoldTitleAndHelpFitActualWidth(t *testing.T) {
	u := focusedGridTestUI(t)
	title, _, _ := u.topBarInfo(150)
	if !strings.Contains(title, "38;2;255;215;0") || !strings.Contains(title, "DataTug") {
		t.Fatalf("title lost gold span: %q", title)
	}
	o := &hotkeysOverlay{ui: u}
	for _, width := range []int{24, 45, 90} {
		for _, line := range strings.Split(o.View(width, 100), "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("help overflow at %d: %q", width, line)
			}
		}
	}
}

func TestUnifiedWorkspaceLocalOperationsStableSelectionFilteringAndReset(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Widgets", DTQL: "from: {schema: main, name: Widgets}\nlimit: 2\n", Result: secureread.Result{Columns: []string{"Id", "Name"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 2, "Name": "same"}}, {Data: map[string]any{"Id": 1, "Name": "same"}}}}})
	g, record, _ := u.activeGrid()
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Widgets", SourceID: record.Database}, Columns: []string{"Id", "Name"}, ColumnTypes: map[string]string{"Id": "INTEGER", "Name": "TEXT"}}}}
	executor := &joinExecutorStub{}
	u.sessions.ConfigureQueryExecutor(executor)
	g.SelectRow(1)
	originalKey := g.sourceRowKey()
	_, cmd := u.shell.Update(uitest.Key("alt+s"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	u.shell.Update(uitest.Text("1"))
	_, cmd = u.shell.Update(uitest.Key("enter"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	if g.Cell(0, 0) != "1" || g.sourceRowKey() != originalKey || executor.doc != "" {
		t.Fatal("local typed sort lost source selection or executed")
	}
	_, cmd = u.shell.Update(uitest.Key("alt+s"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	_, cmd = u.shell.Update(uitest.Key("ctrl+r"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	if g.Cell(0, 0) != "2" || g.sourceRowKey() != originalKey {
		t.Fatal("reset failed to restore captured order/selection")
	}
	capture, err := u.captureLoaded(g, record)
	if err != nil {
		t.Fatal(err)
	}
	o := &resultOperationOverlay{ui: u, grid: g, record: *record, capture: capture, sessionID: u.sessionID, generation: u.workspaceGeneration, group: true, descending: map[int]bool{}, filter: textinput.New()}
	o.Update(uitest.Text("2"))
	o.Update(uitest.Key("tab"))
	o.Update(uitest.Text("Id1"))
	if len(o.selected) != 1 || o.selected[0] != 1 || o.filter.Value() != "Id1" || len(o.visibleColumns()) != 0 || !strings.Contains(o.View(70, 30), "Selected: Name") {
		t.Fatal("filter renumbered or erased a hidden selection/literal digit")
	}
	o.Update(uitest.Key("esc"))
	if len(u.snapshot.RecordSets) != 1 || executor.doc != "" {
		t.Fatal("cancel changed source")
	}
	_, cmd = u.shell.Update(uitest.Key("alt+g"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	u.shell.Update(uitest.Text("2"))
	u.shell.Update(uitest.Text("c"))
	_, cmd = u.shell.Update(uitest.Key("enter"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	view := ansi.Strip(u.shell.View().Content)
	if !strings.Contains(view, "2 loaded rows") || !strings.Contains(view, "Count") || executor.doc != "" || len(u.snapshot.RecordSets) != 1 {
		t.Fatalf("local group: %s", view)
	}
	_, cmd = u.shell.Update(uitest.Key("esc"))
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	if u.shell.WorkspaceFocus() != "transcript" || g.sourceRowKey() != originalKey {
		t.Fatal("group Back failed to restore origin")
	}
	// Explicit unsupported type is an error, never string coercion.
	bad, _ := resultops.Capture([]resultops.Column{{Name: "Id", Type: "unknown"}}, []resultops.Row{{Cells: []resultops.Cell{{Present: true, Value: 1}}}})
	o.capture = &loadedResultCapture{snapshot: bad, columns: capture.columns, columnNames: capture.columnNames, rows: capture.rows}
	o.selected = []int{0}
	o.group = false
	o.applyLoaded()
	if o.err == "" || executor.doc != "" {
		t.Fatal("unsupported local type did not refuse")
	}
}

func TestUnifiedWorkspaceRelatedOpenPersistsOnceAndReturnsExactOrigin(t *testing.T) {
	u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Parent", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1}}, {Data: map[string]any{"Id": 2}}}}})
	g, record, _ := u.activeGrid()
	g.SelectRow(1)
	sourceKey, originEntry := g.sourceRowKey(), g.entryID
	executor := &joinExecutorStub{}
	u.sessions.ConfigureQueryExecutor(executor)
	p := u.workspace
	// This fixture's appended source must be the currently configured source.
	u.sessions.store.info.Sources[record.Database] = record.Source
	p.relatedRecordID = record.ID
	p.relatedRowKey = sourceKey
	p.relatedSessionID = u.sessionID
	p.relatedGeneration = u.workspaceGeneration
	p.relatedRecords = []relatedRecord{{key: ForeignKey{ToRelation: "Child"}, document: "from: {schema: main, name: Child}\nlimit: 5\n", result: secureread.Result{Columns: []string{"ChildId"}, Rows: []secureread.Row{{Data: map[string]any{"ChildId": 8}}}}}}
	cmd := p.openRelated(0)
	if cmd == nil {
		t.Fatal("open produced no durable operation")
	}
	drainCmd(t, u, cmd)
	child, childRecord, ok := u.activeGrid()
	if !ok || child == g || childRecord == nil || childRecord.Title != "Child" || len(u.snapshot.RecordSets) != 2 || executor.doc != "" {
		t.Fatalf("preview was rerun or child not durable: ok=%v same=%v record=%+v count=%d executed=%q feedback=%q parent=%+v", ok, child == g, childRecord, len(u.snapshot.RecordSets), executor.doc, u.workspaceFeedback, record)
	}
	stored, err := u.sessions.Snapshot(u.ctx)
	if err != nil || len(stored.RecordSets) != 2 {
		t.Fatalf("store child: %v", err)
	}
	workspaceRoutingKey(u, "alt+left")
	if u.shell.FocusedEntryID() != originEntry || g.sourceRowKey() != sourceKey {
		t.Fatal("return lost exact origin row")
	}
}
