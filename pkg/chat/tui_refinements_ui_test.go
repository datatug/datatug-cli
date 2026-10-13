package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tuigoff/tuigoff/pkg/grid"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// This uses the public SessionChat turn path. The provider sees the tools only
// when prepareTurn attaches the session-bound result-column callbacks.
func TestResultColumnsPrepareTurnProviderJourney(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "useful", true: "explicit"}[explicit], func(t *testing.T) {
			chat, _, executor, _ := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
			provider := &columnJourneyProvider{t: t, explicit: explicit}
			conversation, err := NewAIConversation(provider, executor, chat.source, "Invoice: InvoiceId, CustomerId\nCustomer: CustomerId, FirstName, LastName")
			if err != nil {
				t.Fatal(err)
			}
			chat.agent = conversation
			prompt := "customers with number of invoices per customer"
			if explicit {
				prompt = "Show only CustomerId and InvoiceCount"
			}
			turn, err := chat.Ask(context.Background(), prompt)
			if err != nil {
				t.Fatal(err)
			}
			wantQueries, wantCalls := 2, 2
			if explicit {
				wantQueries, wantCalls = 1, 1
			}
			if len(turn.Queries) != wantQueries || executor.calls != wantCalls {
				t.Fatalf("queries/calls %d/%d; want %d/%d", len(turn.Queries), executor.calls, wantQueries, wantCalls)
			}
		})
	}
}

func TestAddColumnsActionLoadsMetadataThroughShell(t *testing.T) {
	chat, _, executor, _ := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
	u, err := NewSessionChatUI(t.Context(), chat, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("result grid unavailable")
	}
	uxSearchAction(t, u, "Add columns")
	view := ansi.Strip(u.shell.View().Content)
	if strings.Contains(view, "Loading admitted schema metadata") || !strings.Contains(view, "Customer.FirstName") {
		t.Fatalf("Add columns did not receive its metadata message through the shell:\n%s", view)
	}
	if executor.calls != 0 {
		t.Fatal("opening Add columns executed a query")
	}
	u.shell.PopOverlay()
	cmd := u.openResultColumns()
	if cmd == nil {
		t.Fatal("second Add columns command unavailable")
	}
	loaded := cmd().(resultColumnsLoadedMsg)
	u.shell.PopOverlay()
	u.shell.Update(loaded)
	if !loaded.overlay.loading || len(loaded.overlay.rows) != 0 {
		t.Fatal("closed Add columns dialog accepted late metadata")
	}
}

func TestInspectFollowsExplorerThenReturnsToFocusedResult(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{Title: "Invoices", RecordSetID: "rs-invoices", DTQL: "from: {name: Invoice}\nlimit: 5", Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 7}}}}}}}
	u, _ := newTestChatUI(t, nil, turn)
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", SourceID: "main", ObjectID: "main.Invoice", Title: "Invoice"}, Columns: []string{"InvoiceId"}, ColumnTypes: map[string]string{"InvoiceId": "INTEGER"}}}}
	drainCmd(t, u, u.Submit("Show invoices"))
	u.shell.FocusLeftPanel()
	for i, node := range u.explorer.explorerNodes() {
		if node.objectIndex == 0 {
			u.explorer.explorerIndex = i
			break
		}
	}
	u.explorer.inspectExplorerSelection()
	if got := ansi.Strip(u.workspace.View(40, 24, false)); !strings.Contains(got, "Table: Invoice") {
		t.Fatalf("Explorer selection lacks object metadata (focus=%q target=%+v):\n%s", u.shell.WorkspaceFocus(), u.workspace.inspectObject, got)
	}
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("result grid unavailable")
	}
	if got := ansi.Strip(u.workspace.View(40, 24, false)); !strings.Contains(got, "Row 1 of 1") || strings.Contains(got, "Table: Invoice") {
		t.Fatalf("Inspect failed to return to result context:\n%s", got)
	}
	if !containsWorkspaceAction(u.workspaceActions(), "add-columns") {
		t.Fatal("result lacks Add columns action")
	}
}

func containsWorkspaceAction(actions []workspaceAction, id string) bool {
	for _, action := range actions {
		if action.id == id && action.disabled == "" {
			return true
		}
	}
	return false
}

func TestChatCloseChooserIsCompactAndTopbarKeepsProjectAccess(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog.Title = strings.Repeat("Long project title ", 12)
	_, context, right := u.topBarInfo(80)
	if !strings.Contains(context, "[F3] ¦ Chat") || !strings.Contains(context, "…") || len(right) != 2 {
		t.Fatalf("narrow topbar lost stable navigation: %q %+v", context, right)
	}
	chooser := &chatCloseOverlay{ui: u}
	if got := strings.Count(chooser.View(70, 30), "\n") + 1; got != 7 {
		t.Fatalf("close chooser height = %d, want seven content rows", got)
	}
}

func TestChatSwitchRetainsHTTPFocusAndViewMode(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	ctx, first := context.Background(), u.sessionID
	user, err := sessions.store.AppendUser(ctx, first, "fetch document")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.store.AppendHTTPResponse(ctx, first, user.ID, HTTPResponse{Method: "GET", URL: "https://example.test/doc", StatusCode: 200, ContentType: "text/plain", Body: []byte("retained response"), CreatedAt: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.store.Load(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	messageID, entryID := "", ""
	for id, entry := range u.transcriptMessageEntries {
		if u.transcriptEntryKinds[entry] == transcriptEntryKindHTTP {
			messageID, entryID = id, entry
		}
	}
	if entryID == "" || !u.shell.FocusEntry(entryID) {
		t.Fatal("persisted HTTP document is not focusable")
	}
	u.shell.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	if !u.httpBlocksByMessageID[messageID].showHeaders {
		t.Fatal("Headers control did not select the HTTP document view")
	}
	drainCmd(t, u, u.createChatTab())
	drainCmd(t, u, u.switchChat(first))
	if got := u.shell.FocusedEntryID(); got != u.transcriptMessageEntries[messageID] || u.transcriptEntryKinds[got] != transcriptEntryKindHTTP {
		t.Fatalf("HTTP focus changed after session restore: %q", got)
	}
	if !u.httpBlocksByMessageID[messageID].showHeaders {
		t.Fatal("HTTP Headers view was lost after session restore")
	}
	if !u.shell.FocusEntry(u.transcriptMessageEntries[user.ID]) {
		t.Fatal("persisted user message is not focusable")
	}
	drainCmd(t, u, u.createChatTab())
	drainCmd(t, u, u.switchChat(first))
	if got := u.shell.FocusedEntryID(); got != u.transcriptMessageEntries[user.ID] || u.transcriptEntryKinds[got] != transcriptEntryKindMessage {
		t.Fatalf("user message focus changed after session restore: %q", got)
	}
}

func TestChatSwitchRetainsComposerFocusOverRememberedGrid(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{Title: "Customers", DTQL: "from: {name: Customer}\nlimit: 5", Result: secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 1}}}}}}}
	u, _ := newTestChatUI(t, nil, turn)
	first := u.sessionID
	drainCmd(t, u, u.Submit("customers"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid fixture missing")
	}
	u.shell.FocusComposer()
	u.shell.SetComposerText("unfinished draft")
	drainCmd(t, u, u.createChatTab())
	drainCmd(t, u, u.switchChat(first))
	if u.shell.WorkspaceFocus() != "input" || u.shell.ComposerText() != "unfinished draft" {
		t.Fatalf("composer focus/draft not restored: %q %q", u.shell.WorkspaceFocus(), u.shell.ComposerText())
	}
}

func TestChatSwitchKeepsQueryEditorInItsOwnSession(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	first := u.sessionID
	object := ProjectObject{Reference: ContextReference{Kind: "table", ObjectID: "main.Customer", Title: "Customer"}, Columns: []string{"CustomerId"}}
	u.openWorkspaceQuery(object, false)
	firstEditor := u.queryEditor
	if firstEditor == nil {
		t.Fatal("query editor unavailable")
	}
	firstEditor.text.SetValue("session-specific draft")
	drainCmd(t, u, u.createChatTab())
	if u.shell.CenterPanel() != nil || u.queryEditor != nil {
		t.Fatal("new chat inherited another chat's query editor")
	}
	u.openWorkspaceQuery(object, false)
	secondEditor := u.queryEditor
	if secondEditor == firstEditor || secondEditor.text.Value() == "session-specific draft" {
		t.Fatal("new chat reused another chat's query draft")
	}
	drainCmd(t, u, u.switchChat(first))
	if u.shell.CenterPanel() != firstEditor || u.queryEditor != firstEditor || firstEditor.text.Value() != "session-specific draft" {
		t.Fatal("returning to chat did not restore its query editor")
	}
}

func TestDeletingChatPurgesItsQueryEditor(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	old := u.sessionID
	object := ProjectObject{Reference: ContextReference{Kind: "table", ObjectID: "main.Customer", Title: "Customer"}, Columns: []string{"CustomerId"}}
	u.openWorkspaceQuery(object, false)
	u.queryEditor.text.SetValue("delete with chat")
	drainCmd(t, u, u.closeChatTab(true))
	if u.shell.CenterPanel() != nil || u.queryEditor != nil {
		t.Fatalf("deleted chat left a center panel: %T", u.shell.CenterPanel())
	}
	for key := range u.queryEditors {
		if strings.HasPrefix(key, old+"\x00") {
			t.Fatalf("deleted chat retained query draft %q", key)
		}
	}
}

func TestChatDeletePrefixDoesNotFollowNewSession(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	old := u.sessionID
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF12})
	newX, newY := -1, -1
	for y, line := range strings.Split(ansi.Strip(u.shell.View().Content), "\n") {
		if at := strings.Index(line, "+ New"); at >= 0 {
			newX, newY = ansi.StringWidth(line[:at])+2, y
			break
		}
	}
	if newX < 0 {
		t.Fatal("New chat mouse control was not rendered")
	}
	_, cmd := u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: newX, Y: newY})
	drainCmd(t, u, cmd)
	next := u.sessionID
	if next == old {
		t.Fatal("mouse New did not create another chat")
	}
	_, cmd = u.shell.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	drainCmd(t, u, cmd)
	if _, err := sessions.store.Load(t.Context(), next); err != nil {
		t.Fatalf("ordinary d deleted the new chat after an old-chat prefix: %v", err)
	}
	if got := u.shell.ComposerText(); got != "d" {
		t.Fatalf("ordinary d did not reach the new chat's draft: %q", got)
	}
}

func TestChatDeletePrefixRequiresNextInputEvent(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	id := u.sessionID
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF12})
	u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 0})
	_, cmd := u.shell.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	drainCmd(t, u, cmd)
	if _, err := sessions.store.Load(t.Context(), id); err != nil {
		t.Fatalf("d after unrelated mouse input deleted chat: %v", err)
	}
	if got := u.shell.ComposerText(); got != "d" {
		t.Fatalf("d after unrelated mouse input was not literal: %q", got)
	}
}

func TestResultQueryTabKeepsExactResultProvenance(t *testing.T) {
	result := secureread.Result{Columns: []string{"CustomerId"}, Rows: []secureread.Row{{Data: map[string]any{"CustomerId": 7}}}}
	g := newGridState(NewGridModel(result), "first", "First", 90)
	g.SetFocused(true)
	first := RecordSet{ID: "first", QueryID: "execution-one", DTQL: "from: {name: Customer}\nlimit: 7", Parameters: map[string]any{"secret": "must-not-render"}}
	g.bindQuery(first, ChatSession{})
	if len(g.ExtraViews()) != 3 || g.ExtraViews()[2].Label != "Query" {
		t.Fatalf("result tabs = %+v, want Table/Charts/Current row/Query", g.ExtraViews())
	}
	g.ShowView(gridViewQuery)
	view := ansi.Strip(g.View(90, true))
	for _, want := range []string{"execution-one", "from: {name: Customer}", "limit: 7", "Bound parameters are hidden"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Query tab lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "must-not-render") {
		t.Fatalf("Query tab exposed a bound value:\n%s", view)
	}
	second := RecordSet{ID: "second", DTQL: "from: {name: Invoice}\nlimit: 2"}
	other := newGridState(NewGridModel(result), "second", "Second", 90)
	other.bindQuery(second, ChatSession{})
	other.ShowView(gridViewQuery)
	if got := ansi.Strip(other.View(90, true)); strings.Contains(got, "Customer") || !strings.Contains(got, "Invoice") {
		t.Fatalf("other result borrowed first query:\n%s", got)
	}
	g.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if g.ActiveView() != grid.ViewTable {
		t.Fatalf("Tab from Query = %d, want Table", g.ActiveView())
	}
	g.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if g.ActiveView() != gridViewQuery {
		t.Fatalf("Shift+Tab from Table = %d, want Query", g.ActiveView())
	}
}

func TestChatTabsArchiveRestoreDeleteAndKeepDrafts(t *testing.T) {
	u, sessions := newTestChatUI(t, nil)
	first := u.sessionID
	u.shell.SetComposerText("draft for first chat")
	drainCmd(t, u, u.createChatTab())
	second := u.sessionID
	if first == second {
		t.Fatal("New did not create another chat")
	}
	u.shell.SetComposerText("draft for second chat")
	drainCmd(t, u, u.switchChat(first))
	if got := u.shell.ComposerText(); got != "draft for first chat" {
		t.Fatalf("first draft = %q", got)
	}
	drainCmd(t, u, u.closeChatTab(false))
	if u.sessionID != second {
		t.Fatalf("archive selected %q, want second %q", u.sessionID, second)
	}
	for _, item := range u.chatSessions {
		if item.ID == first {
			t.Fatal("archived chat remains an open tab")
		}
	}
	archived, err := sessions.store.ArchivedSessionIDs(u.ctx)
	if err != nil || !archived[first] {
		t.Fatalf("archive metadata = %v, %v", archived, err)
	}
	if _, err := sessions.store.Load(u.ctx, first); err != nil {
		t.Fatalf("archive lost durable chat: %v", err)
	}
	drainCmd(t, u, u.switchChat(first))
	if got := u.shell.ComposerText(); got != "draft for first chat" {
		t.Fatalf("restored draft = %q", got)
	}
	archived, _ = sessions.store.ArchivedSessionIDs(u.ctx)
	if archived[first] {
		t.Fatal("restored chat remains archived")
	}
	drainCmd(t, u, u.closeChatTab(true))
	if _, err := sessions.store.Load(u.ctx, first); err == nil {
		t.Fatal("deleted chat is still durable")
	}
	if _, ok := u.chatViews[first]; ok {
		t.Fatal("deleted chat retained its UI cache")
	}
	if u.sessionID != second || u.shell.ComposerText() != "draft for second chat" {
		t.Fatalf("surviving chat lost its draft: active %q draft %q", u.sessionID, u.shell.ComposerText())
	}
}

func TestChatTabOrderStaysStableAcrossMouseKeysAndRestore(t *testing.T) {
	u, sessions := newTestChatUI(t, nil)
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	first := u.sessionID
	u.shell.SetComposerText("first draft")
	clickTabLabel := func(label string) {
		t.Helper()
		for y, line := range strings.Split(ansi.Strip(u.shell.View().Content), "\n") {
			if !strings.Contains(line, "+ New") || !strings.Contains(line, label) {
				continue
			}
			x := ansi.StringWidth(line[:strings.Index(line, label)]) + 1
			_, cmd := u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			drainCmd(t, u, cmd)
			return
		}
		t.Fatalf("chat tab %q missing from the visible strip", label)
	}
	clickTabLabel("+ New")
	second := u.sessionID
	u.shell.SetComposerText("second draft")
	clickTabLabel("+ New")
	third := u.sessionID
	u.shell.SetComposerText("third draft")
	if first == second || second == third || first == third {
		t.Fatal("New did not create three distinct chats")
	}
	assertOrder := func(want ...string) {
		t.Helper()
		tabs, active := u.chatTabs(100)
		if len(tabs) != len(want) {
			t.Fatalf("tab count %d, want %d", len(tabs), len(want))
		}
		for i, id := range want {
			if tabs[i].ID != id {
				t.Fatalf("tab %d = %q, want %q", i, tabs[i].ID, id)
			}
			if id == u.sessionID && active != i {
				t.Fatalf("active tab index %d, want %d", active, i)
			}
		}
	}
	assertOrder(first, second, third)
	// Cold loading has no in-memory order. It must use creation order even
	// though the store lists the most recently activated chat first.
	cold, err := NewSessionChatUI(t.Context(), sessions, "test")
	if err != nil {
		t.Fatal(err)
	}
	coldTabs, _ := cold.chatTabs(100)
	for i, id := range []string{first, second, third} {
		if coldTabs[i].ID != id {
			t.Fatalf("cold tab %d = %q, want creation-order %q", i, coldTabs[i].ID, id)
		}
	}
	for id, title := range map[string]string{first: "First", second: "Second", third: "Third"} {
		if err := sessions.store.Rename(t.Context(), id, title); err != nil {
			t.Fatal(err)
		}
	}
	u.refreshChatTabs()
	clickTabLabel("Second")
	if u.sessionID != second || u.shell.ComposerText() != "second draft" {
		t.Fatalf("mouse selected %q with draft %q; want Second", u.sessionID, u.shell.ComposerText())
	}
	assertOrder(first, second, third)
	clickTabLabel("Third")
	if u.sessionID != third || u.shell.ComposerText() != "third draft" {
		t.Fatalf("mouse selected %q with draft %q; want Third", u.sessionID, u.shell.ComposerText())
	}
	assertOrder(first, second, third)
	clickTabLabel("Second") // Same screen coordinate still means Second.
	if u.sessionID != second {
		t.Fatalf("repeat mouse selection reached %q, want Second", u.sessionID)
	}
	_, cmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModCtrl})
	drainCmd(t, u, cmd)
	if u.sessionID != third {
		t.Fatalf("Ctrl+Tab reached %q, want Third", u.sessionID)
	}
	assertOrder(first, second, third)
	_, cmd = u.shell.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModCtrl | tea.ModShift})
	drainCmd(t, u, cmd)
	if u.sessionID != second || u.shell.ComposerText() != "second draft" {
		t.Fatalf("reverse tab reached %q with draft %q, want Second", u.sessionID, u.shell.ComposerText())
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF12})
	_, cmd = u.shell.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	drainCmd(t, u, cmd)
	assertOrder(first, third)
	archived, err := sessions.store.ArchivedSessionIDs(t.Context())
	if err != nil || !archived[second] {
		t.Fatalf("Second archive state = %v, %v", archived, err)
	}
	list, err := sessions.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	currentIndex, restoreIndex := -1, -1
	for i, item := range list {
		if item.ID == u.sessionID {
			currentIndex = i
		}
		if item.ID == second {
			restoreIndex = i
		}
	}
	if currentIndex < 0 || restoreIndex < 0 {
		t.Fatal("picker list lost the archived or active chat")
	}
	drainCmd(t, u, u.openSessionPicker())
	for currentIndex != restoreIndex {
		key := tea.KeyDown
		if currentIndex > restoreIndex {
			key = tea.KeyUp
			currentIndex--
		} else {
			currentIndex++
		}
		u.shell.Update(tea.KeyPressMsg{Code: key})
	}
	_, cmd = u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainCmd(t, u, cmd)
	if u.sessionID != second || u.shell.ComposerText() != "second draft" {
		t.Fatalf("restored %q with draft %q, want Second", u.sessionID, u.shell.ComposerText())
	}
	assertOrder(first, third, second)
}

func TestLateTurnCannotPopulateAnotherChat(t *testing.T) {
	u, _ := newTestChatUI(t, nil)
	first, generation := u.sessionID, u.workspaceGeneration
	u.pendingTurns["old-turn"] = turnOutcome{turn: Turn{Text: "stale answer"}, sessionID: first, generation: generation}
	drainCmd(t, u, u.createChatTab())
	u.OnStreamDone("old-turn", nil)
	if strings.Contains(ansi.Strip(u.shell.View().Content), "stale answer") {
		t.Fatal("late stream populated the newly selected chat")
	}
	if _, retained := u.pendingTurns["old-turn"]; retained {
		t.Fatal("late turn remained in memory")
	}
}

func TestExplorerFilterShowsQueriesDescendantsAndClearsFromTree(t *testing.T) {
	u, sessions := newTestChatUI(t, nil)
	executor := &fakeExecutor{}
	sessions.ConfigureQueryExecutor(executor)
	u.catalog = workspaceTestCatalog()
	u.catalog.Objects = append(u.catalog.Objects, ProjectObject{Reference: ContextReference{Kind: "query", ProjectID: "chinook", ObjectID: "queries/customer-count", Title: "Customer counts"}})
	p := u.explorer
	p.explorerCollapsed["group:*:query"] = true
	p.explorerSearch = "Queries"
	nodes := p.explorerNodes()
	group := -1
	for i, node := range nodes {
		if strings.HasPrefix(node.id, "group:*") {
			group = i
			break
		}
	}
	if group < 0 {
		t.Fatalf("Queries group missing: %+v", nodes)
	}
	if group+1 >= len(nodes) || nodes[group+1].label != "Customer counts" {
		t.Fatalf("filtered collapsed Queries lost child: %+v", nodes)
	}
	p.explorerIndex = group
	p.inspectExplorerSelection()
	if u.workspace.inspectGroup == "" {
		t.Fatal("group selection did not update Inspect")
	}
	p.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if u.workspace.inspectObject == nil || u.workspace.inspectObject.Reference.Title != "Customer counts" {
		t.Fatalf("query metadata did not follow selection: %+v", u.workspace.inspectObject)
	}
	if executor.calls != 0 {
		t.Fatalf("metadata selection executed %d data queries", executor.calls)
	}
	p.explorerIndex = group
	p.explorerSearchFocused = false
	p.updateKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if p.explorerSearch != "" {
		t.Fatalf("Esc left filter %q", p.explorerSearch)
	}
	if selected := p.explorerNodes()[p.explorerIndex]; !strings.HasPrefix(selected.id, "group:*") {
		t.Fatalf("Esc lost selected group: %+v", selected)
	}
	// macOS Option+A may arrive as a printable å; it belongs to search.
	p.updateKey(tea.KeyPressMsg{Code: 'å', Text: "å"})
	if p.explorerSearch != "å" {
		t.Fatalf("literal Option text = %q", p.explorerSearch)
	}
	if plain := ansi.Strip(p.View(30, 12, true)); !strings.Contains(plain, "No matches") || !strings.Contains(plain, "Esc clears") {
		t.Fatalf("no-match recovery missing:\n%s", plain)
	}
}
