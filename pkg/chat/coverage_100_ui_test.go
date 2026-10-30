package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

type dummyExplorePanelWithInit struct {
	chatshell.SidePanel
}

func (d dummyExplorePanelWithInit) Init() tea.Cmd {
	return func() tea.Msg { return nil }
}

// 1. inspector_related_workspace.go coverage
func TestCoverage100_InspectorRelatedWorkspace(t *testing.T) {
	t.Parallel()

	u := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "users",
		TugQL:  "from: {name: users}\ncolumns: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	wp := u.workspace
	exp := u.explorer

	// CapturesEsc branches
	if wp.CapturesEsc() {
		t.Fatal("expected false initially")
	}
	exp.explorerSearch = "test"
	if !exp.CapturesEsc() {
		t.Fatal("expected true when explorerSearch non-empty")
	}
	exp.explorerSearch = ""
	wp.explorerSearchFocused = true
	if !wp.CapturesEsc() {
		t.Fatal("expected true when explorerSearchFocused")
	}
	wp.explorerSearchFocused = false
	wp.relatedFocused = true
	if !wp.CapturesEsc() {
		t.Fatal("expected true when relatedFocused")
	}
	wp.relatedFocused = false
	wp.bookmarkMode = "rename"
	if !wp.CapturesEsc() {
		t.Fatal("expected true when bookmarkMode non-empty")
	}
	wp.bookmarkMode = ""
	wp.bookmarkGridFocused = true
	if !wp.CapturesEsc() {
		t.Fatal("expected true when bookmarkGridFocused")
	}
	wp.bookmarkGridFocused = false
	wp.dockGridFocused = true
	if !wp.CapturesEsc() {
		t.Fatal("expected true when dockGridFocused")
	}
	wp.dockGridFocused = false

	// focusRelated:
	// 1. workspaceMutationPending
	u.workspaceMutationPending = true
	cmd := wp.focusRelated(0)
	if cmd != nil || u.workspaceFeedback == "" {
		t.Fatal("expected feedback on mutation pending")
	}
	u.workspaceMutationPending = false

	// 2. groupedRecord
	uGrouped := focusedGridStoreBackedTestUI(t, QueryResult{
		Title:  "grouped",
		TugQL:  "from: {name: users}\ngroupBy: [{field: id}]\n",
		Source: "sqlite:///fixture.db",
		Result: secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
	})
	cmd = uGrouped.workspace.focusRelated(0)
	if cmd != nil || !strings.Contains(uGrouped.workspaceFeedback, "Grouped output") {
		t.Fatal("expected grouped output feedback")
	}

	// 3. normal focusRelated
	cmd = wp.focusRelated(0)
	if cmd == nil || !wp.relatedLoading {
		t.Fatal("expected related loading")
	}
	// Execute the cmd
	msg := cmd()
	relMsg, ok := msg.(inspectorRelatedMsg)
	if !ok {
		t.Fatalf("expected inspectorRelatedMsg, got %T", msg)
	}

	// receiveRelated:
	// 1. finishOperation fails
	wp.receiveRelated(inspectorRelatedMsg{operationID: 999999})
	// 2. mismatch sequence
	badSeqMsg := relMsg
	badSeqMsg.sequence = wp.relatedSequence + 99
	wp.receiveRelated(badSeqMsg)
	// 3. !current (!operationContextCurrent)
	opID, _ := u.beginOperation("Related")
	staleMsg := inspectorRelatedMsg{
		operationID: opID,
		sequence:    wp.relatedSequence,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		recordID:    wp.relatedRecordID,
		rowKey:      wp.relatedRowKey,
	}
	wp.relatedOpen = 1
	// Changing composer text changes contextStamp, so operationContextCurrent is false, while finishOperation succeeds
	u.shell.SetComposerText("changed text")
	wp.receiveRelated(staleMsg)
	if wp.relatedOpen != -1 {
		t.Fatal("expected relatedOpen reset to -1 on stale message")
	}

	// 4. normal receiveRelated with relatedOpen >= 0
	opID, _ = u.beginOperation("Related")
	normalMsg := inspectorRelatedMsg{
		operationID: opID,
		sequence:    wp.relatedSequence,
		sessionID:   u.sessionID,
		generation:  u.workspaceGeneration,
		recordID:    wp.relatedRecordID,
		rowKey:      wp.relatedRowKey,
		related: []relatedRecord{
			{
				document: "SELECT 1",
				key:      ForeignKey{ConstraintID: "fk1", Schema: "main", FromRelation: "users", ToSchema: "main", ToRelation: "orders", FromFields: []string{"id"}, ToFields: []string{"user_id"}},
				result:   secureread.Result{Columns: []string{"id"}, Rows: []secureread.Row{{Data: map[string]any{"id": int64(1)}}}},
			},
			{
				document: "SELECT 2",
				key:      ForeignKey{ConstraintID: "fk2", Schema: "main", FromRelation: "orders", ToSchema: "main", ToRelation: "users"},
				incoming: true,
				err:      errors.New("rel item error"),
			},
		},
	}
	wp.relatedOpen = 0
	cmdOpen := wp.receiveRelated(normalMsg)
	if cmdOpen == nil {
		t.Fatal("expected openRelated cmd on receiveRelated with relatedOpen >= 0")
	}

	// relatedView:
	// 1. grouped record
	vGrouped := uGrouped.workspace.relatedView(80, 24)
	if !strings.Contains(vGrouped, "Related unavailable") {
		t.Fatalf("expected Related unavailable, got %s", vGrouped)
	}
	// 2. record mismatch
	wp.relatedRecordID = "wrong-rec-id"
	wp.relatedLoading = true
	vMismatch := wp.relatedView(80, 24)
	if !strings.Contains(vMismatch, "F10 loads") || wp.relatedLoading {
		t.Fatal("expected cancelRelated and prompt")
	}
	wp.relatedRecordID = normalMsg.recordID

	// 3. loading view
	wp.relatedLoading = true
	vLoading := wp.relatedView(80, 24)
	if !strings.Contains(vLoading, "Loading related") {
		t.Fatal("expected loading view")
	}
	wp.relatedLoading = false

	// 4. error view
	wp.relatedError = errors.New("preview fail")
	vErr := wp.relatedView(80, 24)
	if !strings.Contains(vErr, "Related records unavailable") {
		t.Fatal("expected error view")
	}
	wp.relatedError = nil

	// 5. empty records view
	wp.relatedRecords = nil
	vEmpty := wp.relatedView(80, 24)
	if !strings.Contains(vEmpty, "No authoritative relationships") {
		t.Fatal("expected empty view")
	}

	// 6. populated records view with details
	wp.relatedRecords = normalMsg.related
	wp.relatedFocused = true
	wp.relatedIndex = 0
	vPop := wp.relatedView(80, 10)
	if !strings.Contains(vPop, "orders") || !strings.Contains(vPop, "id → user_id") {
		t.Fatalf("expected orders and details line in relatedView, got %s", vPop)
	}

	// openRelated:
	// index out of range or loading
	if wp.openRelated(-1) != nil || wp.openRelated(999) != nil {
		t.Fatal("expected nil on out of bounds")
	}
	wp.relatedLoading = true
	if wp.openRelated(0) != nil {
		t.Fatal("expected nil when loading")
	}
	wp.relatedLoading = false
	// item has error
	if wp.openRelated(1) != nil || u.workspaceFeedback == "" {
		t.Fatal("expected feedback on item error")
	}
	// mutationPending
	u.workspaceMutationPending = true
	if wp.openRelated(0) != nil {
		t.Fatal("expected nil when mutation pending")
	}
	u.workspaceMutationPending = false
	// normal openRelated
	u.backends = nil
	u.operation = nil
	cmdAcc := wp.openRelated(0)
	if cmdAcc == nil {
		t.Fatal("expected cmd on valid openRelated")
	}

	// gridActionsOverlay:
	act := &gridActionsOverlay{ui: u}
	// labels with httpResponse
	g, _, _ := u.activeGrid()
	g.httpResponse = &HTTPResponse{StatusCode: 200}
	labelsHttp := act.labels()
	hasRaw := false
	for _, l := range labelsHttp {
		if l == "Raw response" {
			hasRaw = true
		}
	}
	if !hasRaw {
		t.Fatal("expected Raw response label")
	}
	g.httpResponse = nil

	_ = act.View(80, 24)
	// Update with non-key msg
	_, _, _ = act.Update(tea.MouseWheelMsg{})
	// Update with "up", "down", "esc"
	_, _, _ = act.Update(uitest.Key("up"))
	_, _, _ = act.Update(uitest.Key("down"))
	_, _, done := act.Update(uitest.Key("esc"))
	if !done {
		t.Fatal("expected done on esc")
	}

	// enter with visibleGridReason
	uNoGrid := focusedGridTestUI(t)
	backPanel := uNoGrid.shell.ShowTemporaryCenterPanel(&localGroupsPanel{ui: uNoGrid})
	actNoGrid := &gridActionsOverlay{ui: uNoGrid}
	_, _, _ = actNoGrid.Update(uitest.Key("enter"))
	backPanel()

	// enter on index < 3 (views)
	for i := 0; i < 3; i++ {
		act.index = i
		_, _, _ = act.Update(uitest.Key("enter"))
	}
	// enter on index == 14 (join picker)
	act.index = 14
	_, _, _ = act.Update(uitest.Key("enter"))

	// enter on extra labels
	labels := act.labels()
	for i := len(gridActionLabels); i < len(labels); i++ {
		act.index = i
		_, _, _ = act.Update(uitest.Key("enter"))
	}
	// enter on indices 3..13
	for i := 3; i < 14; i++ {
		act.index = i
		_, _, _ = act.Update(uitest.Key("enter"))
	}

	// candidateDigit:
	if candidateDigit(9) != "0" || candidateDigit(0) != "1" || candidateDigit(12) != "·" {
		t.Fatal("unexpected candidateDigit result")
	}

	// selectedListWindow:
	start, end := selectedListWindow(20, 15, 5)
	if start < 0 || end > 20 {
		t.Fatalf("unexpected window: %d..%d", start, end)
	}

	// joinPickerOverlay:
	// openJoinPicker with grouped record
	_ = uGrouped.openJoinPicker()
	// openJoinPicker normal
	_ = u.openJoinPicker()

	cands := []JoinCandidate{
		{
			ID:          "cand1",
			Source:      RelationInstance{Relation: "users"},
			Target:      RelationInstance{Relation: "orders"},
			Cardinality: "1:N",
			Fields:      []JoinFieldPair{{SourceField: "id", TargetField: "user_id"}},
		},
	}
	jp := &joinPickerOverlay{ui: u, candidates: cands, sessionID: u.sessionID, generation: u.workspaceGeneration}
	_ = jp.View(80, 24)
	jpEmpty := &joinPickerOverlay{ui: u, candidates: nil}
	_ = jpEmpty.View(80, 24)

	// Update on joinPickerOverlay
	_, _, _ = jp.Update(tea.MouseWheelMsg{})
	_, _, _ = jp.Update(uitest.Key("up"))
	_, _, _ = jp.Update(uitest.Key("down"))
	_, _, done = jp.Update(uitest.Key("esc"))
	if !done {
		t.Fatal("expected done on esc")
	}
	// enter with Mod != 0
	_, _, _ = jp.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	// enter normal
	_, _, _ = jp.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// digit '1' and digit '0'
	_, _, _ = jp.Update(uitest.Key("1"))
	_, _, _ = jp.Update(uitest.Key("0"))

	// apply out of range and session mismatch
	if jp.apply(-1) != nil || jp.apply(999) != nil {
		t.Fatal("expected nil on out of bounds apply")
	}
	jpWrong := &joinPickerOverlay{ui: u, candidates: cands, sessionID: "wrong"}
	if jpWrong.apply(0) != nil {
		t.Fatal("expected nil on session mismatch")
	}
}

// 2. unified_workspace.go coverage
func TestCoverage100_UnifiedWorkspace(t *testing.T) {
	t.Parallel()

	u := focusedGridTestUI(t)

	// apiTick
	tickCmd := apiTick()
	if tickCmd == nil {
		t.Fatal("expected tick cmd")
	}

	// Init
	u.explorePanel = dummyExplorePanelWithInit{SidePanel: u.explorer}
	initCmd := u.Init()
	if initCmd == nil {
		t.Fatal("expected init cmd")
	}

	// SetExplorePanel, SetExplorer, HasCatalogExplorer, Model
	u.SetExplorePanel(dummyExplorePanelWithInit{SidePanel: u.explorer})
	u.SetExplorer(dummyExplorePanelWithInit{SidePanel: u.explorer})
	_ = u.HasCatalogExplorer()
	if u.Model() == nil {
		t.Fatal("expected non-nil model")
	}
	u.SetExplorePanel(u.explorer)
	u.SetExplorer(u.explorer)

	// workspaceHints:
	// webLinkVisible
	u.webLinkVisible = true
	u.browserURL = "http://localhost:8080"
	hintsWeb := u.workspaceHints(80)
	if len(hintsWeb) == 0 || !strings.Contains(hintsWeb[0], "Open web") {
		t.Fatal("expected web link hints")
	}
	u.webLinkVisible = false

	// session-tabs
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF11})
	_ = u.workspaceHints(80)

	// explorer without / with catalog
	u.shell.FocusLeftPanel()
	u.catalog.Objects = nil
	_ = u.workspaceHints(80)
	u.catalog.Objects = []ProjectObject{{Reference: ContextReference{Title: "obj1"}}}
	_ = u.workspaceHints(80)

	// center:
	// apiScreen != nil
	u.shell.SetCenterPanel(dummyExplorePanelWithInit{}, true)
	u.apiScreen = &apiPanel{}
	_ = u.workspaceHints(80)
	u.apiScreen = nil
	// localGroupsPanel
	backLocal := u.shell.ShowTemporaryCenterPanel(&localGroupsPanel{ui: u})
	_ = u.workspaceHints(80)
	backLocal()
	// workspaceQueryEditor
	backEd := u.shell.ShowTemporaryCenterPanel(&workspaceQueryEditor{ui: u})
	_ = u.workspaceHints(80)
	backEd()
	_ = u.workspaceHints(80)

	// sidebar:
	u.shell.FocusRightPanel()
	u.workspace.inspectObject = &ProjectObject{}
	_ = u.workspaceHints(80)
	u.workspace.inspectObject = nil
	u.workspace.bookmarkMode = "rename"
	_ = u.workspaceHints(80)
	u.workspace.bookmarkMode = ""

	// relatedOrigins
	recID := u.activeRecordSetID()
	u.relatedOrigins[recID] = "origin-entry"
	u.shell.FocusEntry(u.lastGridEntryID)
	hintsOrigin := u.workspaceHints(80)
	hasAltLeft := false
	for _, h := range hintsOrigin {
		if strings.Contains(h, "Alt+←") {
			hasAltLeft = true
		}
	}
	if !hasAltLeft {
		t.Fatal("expected Alt+← origin hint")
	}

	// apiPanel
	p := &apiPanel{}
	if p.Title() != "API server" {
		t.Fatal("unexpected title")
	}
	_ = p.View(80, 24, false)
	// Keys
	for _, key := range []string{"up", "pgup", "down", "pgdown", "end"} {
		_, _ = p.Update(uitest.Key(key))
	}
	// Mouse wheel
	_, _ = p.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	_, _ = p.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})

	// hotkeysOverlay:
	h := &hotkeysOverlay{
		ui:           u,
		contentWidth: 80,
		scrollbar: helpScrollbar{
			visible:    true,
			x:          78,
			height:     20,
			thumbStart: 5,
			thumbSize:  5,
			total:      50,
		},
		actions: []helpActionTarget{
			{x: 10, y: 3, width: 10},
		},
	}
	// click on action target
	_, cmdAct, doneAct := h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 12, Y: 3})
	if !doneAct || cmdAct == nil {
		t.Fatal("expected action msg on click")
	}
	// click on scrollbar above thumb
	_, _, _ = h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 78, Y: 2})
	// click on scrollbar below thumb
	_, _, _ = h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 78, Y: 15})
	// click outside contentWidth
	_, _, _ = h.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 99, Y: 3})
	// Keys
	for _, key := range []string{"ctrl+p", "esc", "f1", "up", "down", "pgup", "pgdown", "home", "end"} {
		_, _, _ = h.Update(uitest.Key(key))
	}

	// closeAPIOverview:
	called := false
	u.apiScreen = &apiPanel{}
	u.apiReturn = func() { called = true }
	u.closeAPIOverview()
	if !called || u.apiScreen != nil {
		t.Fatal("expected restore called and apiScreen nil")
	}
}

// 3. workspace_query_editor.go coverage
func TestCoverage100_WorkspaceQueryEditor(t *testing.T) {
	t.Parallel()

	u := focusedGridTestUI(t)
	obj := ProjectObject{
		Reference: ContextReference{Kind: "table", SourceID: "s1", ObjectID: "main.users", Title: "Users"},
		Columns:   []string{"id"},
	}

	// openWorkspaceQuery with invalid object kind
	objErr := ProjectObject{Reference: ContextReference{Kind: "invalid"}}
	cmd := u.openWorkspaceQuery(objErr, false)
	if cmd != nil || u.workspaceFeedback == "" {
		t.Fatal("expected feedback on object query document error")
	}

	// openWorkspaceQuery with run == true when sessions == nil
	savedSessions := u.sessions
	u.sessions = nil
	cmd = u.openWorkspaceQuery(obj, true)
	if cmd != nil || !strings.Contains(u.workspaceFeedback, "Choose a project") {
		t.Fatal("expected feedback on nil sessions")
	}
	u.sessions = savedSessions

	// openWorkspaceQuery when mutationPending
	u.workspaceMutationPending = true
	cmd = u.runWorkspaceQuery(obj.Reference, "SELECT 1")
	if cmd != nil || !strings.Contains(u.workspaceFeedback, "Cancel the running") {
		t.Fatal("expected feedback on running operation")
	}
	u.workspaceMutationPending = false

	// openWorkspaceQuery normal (editor created)
	cmd = u.openWorkspaceQuery(obj, false)
	if cmd == nil || u.queryEditor == nil {
		t.Fatal("expected editor created")
	}
	// openWorkspaceQuery second time (reuses existing editor)
	cmd2 := u.openWorkspaceQuery(obj, false)
	if cmd2 == nil {
		t.Fatal("expected cmd on reopen")
	}

	ed := u.queryEditor
	if !strings.Contains(ed.Title(), "Users") || !ed.CapturesEsc() {
		t.Fatal("expected title and captures esc")
	}

	// View:
	// focused with focus == 0
	ed.focus = 0
	_ = ed.View(80, 24, true)
	// focused with focus != 0
	ed.focus = 1
	_ = ed.View(80, 24, true)
	// unfocused
	_ = ed.View(80, 24, false)

	// Update:
	// Mouse click on control target (populated by View)
	if len(ed.controlSpans) > 0 {
		_, _ = ed.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: ed.controlSpans[0][0], Y: ed.controlY})
	}
	// Mouse click outside
	_, _ = ed.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 99})

	// esc key
	_, cmdEsc := ed.Update(uitest.Key("esc"))
	if cmdEsc == nil {
		t.Fatal("expected back cmd on esc")
	}

	// shift+tab and tab
	_, _ = ed.Update(uitest.Key("shift+tab"))
	_, _ = ed.Update(uitest.Key("tab"))

	// enter with Mod != 0
	_, _ = ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})

	// enter on focus == 1 (run query)
	ed.focus = 1
	_, _ = ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// enter on focus == 2 (select overlay)
	ed.focus = 2
	_, _ = ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// enter on focus == 3 (copy with and without selection)
	ed.focus = 3
	ed.selection = ""
	_, _ = ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	ed.selection = "copied query text"
	_, _ = ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// enter on focus == 4 (back)
	ed.focus = 4
	_, cmdBack := ed.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmdBack == nil {
		t.Fatal("expected back cmd on focus 4 enter")
	}

	// Non-zero focus non-key msg
	ed.focus = 1
	_, _ = ed.Update(tea.MouseWheelMsg{})

	// queryTextSelection:
	selText := []rune("line 1\nline 2\nline 3\n")
	acceptedText := ""
	qs := &queryTextSelection{
		editor:       ed,
		text:         selText,
		anchor:       0,
		cursor:       10,
		copyOnAccept: true,
		accept:       func(s string) { acceptedText = s },
	}
	if len(qs.HelpHints()) == 0 {
		t.Fatal("expected hints")
	}
	_ = qs.View(80, 24)

	// Click on controls (populated by View)
	if len(qs.controls) > 0 {
		_, _, _ = qs.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: qs.controls[0].start, Y: qs.controlsY})
	}
	// Click outside controls
	_, _, _ = qs.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 99})

	// Non-key msg
	_, _, _ = qs.Update(tea.MouseWheelMsg{})

	// Keys:
	for _, key := range []string{"esc", "ctrl+c", "a", "A", "left", "right", "home", "end"} {
		_, _, _ = qs.Update(uitest.Key(key))
	}
	// enter with accept
	_, _, done := qs.Update(uitest.Key("enter"))
	if !done || acceptedText == "" {
		t.Fatal("expected accepted text on enter")
	}

	// up when start == 0 and start > 0
	qs.cursor = 2
	_, _, _ = qs.Update(uitest.Key("up"))
	qs.cursor = 10
	_, _, _ = qs.Update(uitest.Key("up"))

	// down when end == len(text) and end < len(text)
	qs.cursor = len(selText)
	_, _, _ = qs.Update(uitest.Key("down"))
	qs.cursor = 2
	_, _, _ = qs.Update(uitest.Key("down"))

	// selectionControlKey
	_ = selectionControlKey("ctrl+c")
	_ = selectionControlKey("enter")
	_ = selectionControlKey("other")
}

// 4. chatui_refinements.go coverage
func TestCoverage100_ChatUIRefinements(t *testing.T) {
	t.Parallel()

	u := focusedGridTestUI(t)

	// WorkspaceHeader
	hTitle, hKey := u.explorer.WorkspaceHeader()
	if hTitle != "Explorer" || hKey != "F6" {
		t.Fatalf("unexpected explorer header: %s %s", hTitle, hKey)
	}
	hTitle, hKey = u.workspace.WorkspaceHeader()
	if hTitle != "Inspect" || hKey != "F7" {
		t.Fatalf("unexpected inspect header: %s %s", hTitle, hKey)
	}

	// inspectExplorerSelection:
	// p != explorer
	u.workspace.inspectExplorerSelection()
	// explorer with index out of range
	u.explorer.explorerIndex = -1
	u.explorer.inspectExplorerSelection()
	if u.workspace.inspectGroup != "No matching objects" {
		t.Fatal("expected No matching objects")
	}
	// explorer with catalog object
	u.catalog.Objects = []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "my_table", Title: "my_table"}}}
	nodes := u.explorer.explorerNodes()
	for i, n := range nodes {
		if n.objectIndex >= 0 {
			u.explorer.explorerIndex = i
			break
		}
	}
	u.explorer.inspectExplorerSelection()
	if u.workspace.inspectObject == nil || u.workspace.inspectObject.Reference.Title != "my_table" {
		t.Fatal("expected inspected object my_table")
	}
	// explorer with issue node
	u.catalog.Objects = []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "bad_table", Title: "bad_table"}, Issue: "corrupted"}}
	nodes = u.explorer.explorerNodes()
	for i, n := range nodes {
		if n.issue {
			u.explorer.explorerIndex = i
			break
		}
	}
	u.explorer.inspectExplorerSelection()
	if u.workspace.inspectObject == nil {
		t.Fatal("expected inspected object from issue node")
	}

	// Available to add rendering:
	_, rec, _ := u.activeGrid()
	// cache.err != nil
	u.resultColumnsCache = resultColumnsCache{recordID: rec.ID, loading: false, err: errors.New("cannot load")}
	linesErr := u.workspace.compactResultColumns(80)
	if !strings.Contains(strings.Join(linesErr, "\n"), "Column metadata unavailable") {
		t.Fatal("expected metadata unavailable line")
	}

	// cache with rows
	u.resultColumnsCache = resultColumnsCache{
		recordID: rec.ID,
		loading:  false,
		rows: []ResultColumnRow{
			{Name: "ret_col", Returned: true},
			{Name: "added_col", Source: "orders", Addable: true, Type: "INTEGER"},
			{Name: "unavail_col", Addable: false, Reason: "no relation"},
		},
	}
	linesRows := u.workspace.compactResultColumns(80)
	strRows := strings.Join(linesRows, "\n")
	if !strings.Contains(strRows, "orders.added_col") || !strings.Contains(strRows, "unavail_col") {
		t.Fatalf("expected formatted rows, got %s", strRows)
	}

	// cache with 0 rows
	u.resultColumnsCache = resultColumnsCache{recordID: rec.ID, loading: false, rows: nil}
	linesZero := u.workspace.compactResultColumns(80)
	if !strings.Contains(strings.Join(linesZero, "\n"), "No omitted fields") {
		t.Fatal("expected no omitted fields line")
	}

	// panelContextActions & panelContextAction:
	// focus "explorer"
	u.shell.FocusLeftPanel()
	_ = u.panelContextActions()
	for _, act := range []string{"attach", "details", "query"} {
		_ = u.panelContextAction(act)
	}

	// focus "sidebar" tabs 1, 2, 3
	u.shell.FocusRightPanel()
	for tab := 1; tab <= 3; tab++ {
		u.workspace.tab = tab
		_ = u.panelContextActions()
	}
	// panelContextAction sidebar actions
	for _, act := range []string{"rename", "tags-add", "tags-remove", "filter", "search", "attach", "dock", "bookmark", "remove"} {
		_ = u.panelContextAction(act)
	}

	// loadInspectResultColumns branches
	// workspace.tab != 1
	u.workspace.tab = 2
	if u.loadInspectResultColumns() != nil {
		t.Fatal("expected nil when tab != 1")
	}
	u.workspace.tab = 1
	u.workspace.inspectorSubTab = 0
	if u.loadInspectResultColumns() != nil {
		t.Fatal("expected nil when inspectorSubTab != 2")
	}
	u.workspace.inspectorSubTab = 2
	u.resultColumnsCache.recordID = rec.ID
	if u.loadInspectResultColumns() != nil {
		t.Fatal("expected nil when already cached")
	}
}

// 5. chatui_sessions.go coverage
func TestCoverage100_ChatUISessions(t *testing.T) {
	t.Parallel()

	u := focusedGridTestUI(t)

	// activateChatTab
	if u.activateChatTab(-1) != nil || u.activateChatTab(99) != nil {
		t.Fatal("expected nil on invalid index")
	}

	// closeChatTabAt:
	if u.closeChatTabAt(-1) != nil || u.closeChatTabAt(99) != nil {
		t.Fatal("expected nil on invalid index")
	}
	u.chatSessions = []ChatSession{
		{ID: u.sessionID, Title: "current"},
		{ID: "other-id", Title: "other"},
	}
	// close active session tab -> opens close chooser
	u.closeChatTabAt(0)
	if !strings.Contains(u.shell.View().Content, "Archive keeps this chat") {
		t.Fatal("expected chatCloseOverlay pushed")
	}
	u.shell.PopOverlay()

	// close other session tab -> switchChat and set chatCloseAfterSwitch
	cmdSwitch := u.closeChatTabAt(1)
	if cmdSwitch == nil || !u.chatCloseAfterSwitch {
		t.Fatal("expected switchChat cmd and chatCloseAfterSwitch true")
	}
	u.chatCloseAfterSwitch = false

	// switchChat branches:
	// id == u.sessionID
	if u.switchChat(u.sessionID) != nil {
		t.Fatal("expected nil on same session")
	}
	// chatTransition == true
	u.chatTransition = true
	if u.switchChat("other-id") != nil {
		t.Fatal("expected nil on chatTransition")
	}
	u.chatTransition = false

	// createChatTab when chatTransition == true
	u.chatTransition = true
	if u.createChatTab() != nil {
		t.Fatal("expected nil on chatTransition")
	}
	u.chatTransition = false

	// chatCloseOverlay:
	co := &chatCloseOverlay{ui: u}
	if !co.LocalMouseCoordinates() || !co.BoundedMouseInput() || len(co.HelpHints()) == 0 {
		t.Fatal("expected overlay methods")
	}
	_ = co.View(80, 24)

	// Mouse click
	co.rowY = 3
	_, _, _ = co.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 3})
	_, _, _ = co.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 4})
	_, _, _ = co.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 5, Y: 5})

	// Keys
	for _, key := range []string{"up", "down", "tab", "shift+tab"} {
		_, _, _ = co.Update(uitest.Key(key))
	}
	_, _, done := co.Update(uitest.Key("esc"))
	if !done {
		t.Fatal("expected done on esc")
	}

	// activate cases: 0 (archive), 1 (delete), 2 (cancel)
	co.index = 0
	_, _, _ = co.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	co.index = 1
	_, _, _ = co.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	co.index = 2
	_, _, _ = co.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// sessionPrefixKey:
	// f12 key
	_, done = u.sessionPrefixKey(tea.KeyPressMsg{Code: tea.KeyF12, Text: "f12"})
	if !done || u.chatPrefixUntil.IsZero() {
		t.Fatal("expected prefix active")
	}
	// ctrl+w
	_, _ = u.sessionPrefixKey(uitest.Key("ctrl+w"))

	// active prefix keys: "a", "d", "esc"
	for _, k := range []string{"a", "d", "esc"} {
		u.chatPrefixUntil = time.Now().Add(5 * time.Second)
		u.chatPrefixSessionID = u.sessionID
		u.chatPrefixGeneration = u.workspaceGeneration
		u.chatPrefixFocus = u.shell.WorkspaceFocus()
		u.chatPrefixInputEpoch = u.shell.InputEpoch() - 1
		_, _ = u.sessionPrefixKey(uitest.Key(k))
	}
}

// 6. chatui.go & chatui_sidepanel.go coverage
func TestCoverage100_ChatUIAndSidepanel(t *testing.T) {
	t.Parallel()

	u := focusedGridTestUI(t)

	// cycleTableStyle
	for i := 0; i < 5; i++ {
		u.cycleTableStyle()
	}

	// syncChips with nil sessions and with record sets
	u.syncChips()
	u.snapshot.RecordSets = nil
	u.syncChips()

	// activeRecordSetID when no active grid
	uEmpty, _ := newTestChatUI(t, nil)
	uEmpty.gridsByRecordSetID = nil
	uEmpty.retainedRecordSetID = ""
	if id := uEmpty.activeRecordSetID(); id != "" {
		t.Fatalf("expected empty activeRecordSetID, got %q", id)
	}

	// workspacePanel methods in chatui_sidepanel.go
	wp := u.workspace
	// bookmark search and tag input
	wp.startBookmarkInput("search", "Search")
	wp.startBookmarkInput("tags", "Tags")
	wp.startBookmarkInput("rename", "Rename")
	wp.startBookmarkInput("tag_add", "Add tag")
	wp.startBookmarkInput("tag_remove", "Remove tag")
	wp.updateBookmarkInput(tea.KeyPressMsg{Code: tea.KeyEscape})

	// subtab navigation
	wp.tab = 1
	for st := 0; st <= 2; st++ {
		wp.inspectorSubTab = st
		_ = wp.View(80, 24, false)
	}

	// dock navigation
	wp.tab = 2
	_ = wp.View(80, 24, false)

	// bookmarks tab
	wp.tab = 3
	_ = wp.View(80, 24, false)
}
