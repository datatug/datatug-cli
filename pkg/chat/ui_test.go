package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/grid"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestUIRendersStructuredResultAsBubbleTable(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 50
	u.appendTurn(Turn{Text: "Found one.", Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"CustomerId", "City"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1, "City": "Prague"}}},
	}}}})
	u.rebuildHistory(true)
	view := u.View().Content
	for _, want := range []string{"Found one.", "CustomerId", "City", "Prague", "Shift+↑↓ to navigate"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if !u.focusLatestGrid() || !u.gridFocused {
		t.Fatal("expected latest grid to become interactive")
	}
}

func TestUIInlineFKJoinNavigationAndApply(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Invoices", DTQL: "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}})
	u, err := NewSessionUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.width, u.height = 100, 40
	u.resizeChatPane()
	if !strings.Contains(ansi.Strip(u.View().Content), "You can JOIN") || !strings.Contains(u.View().Content, "Customer") {
		t.Fatalf("inline FK area missing:\n%s", u.View().Content)
	}
	if !u.focusLatestGrid() {
		t.Fatal("grid could not be focused")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "j"})
	if !u.joinFocused || u.entries[u.activeGrid].grid.Focused() {
		t.Fatal("JOIN area did not receive focus")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(u.View().Content, "Invoice.CustomerId") {
		t.Fatal("exact FK details not displayed")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if u.joinFocused || !u.entries[u.activeGrid].grid.Focused() {
		t.Fatal("Esc did not return to grid")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "j"})
	_, command := u.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if command == nil {
		t.Fatal("Space did not invoke JOIN application")
	}
	message := command()
	if _, ok := message.(joinMessage); !ok {
		t.Fatalf("JOIN command returned %T", message)
	}
	_, _ = u.Update(message)
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot.RecordSets) != 2 {
		t.Fatalf("JOIN result was not persisted: %d RecordSets, %v", len(snapshot.RecordSets), err)
	}
	if snapshot.RecordSets[base.RecordSetID].Lineage != nil {
		t.Fatal("base RecordSet was mutated")
	}
	if !strings.Contains(u.View().Content, "Invoices + Customer") {
		t.Fatal("joined grid not rendered")
	}
}

func TestUIShowsAppliedLimitationsIncludingEmptyResults(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"CustomerId"},
		Limitations: []secureread.Limitation{
			{Kind: secureread.LimitationPolicy, Note: `access: policy "support" restricted the query`},
			{Kind: secureread.LimitationRowsFiltered},
			{Kind: secureread.LimitationHiddenColumns, Columns: []string{"Email"}},
		},
	}}}})
	u.rebuildHistory(true)
	view := u.View().Content
	for _, want := range []string{"No rows returned.", `policy "support"`, "rows filtered by policy", "hidden columns: Email"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestGridKeepsFocusAndCursorAcrossHorizontalNavigation(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"First", "Second"},
		Rows: []secureread.Row{
			{Data: map[string]any{"First": "a", "Second": "1"}},
			{Data: map[string]any{"First": "b", "Second": "2"}},
		},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	if _, handled := u.updateGrid(tea.KeyPressMsg{Code: tea.KeyRight}); !handled {
		t.Fatal("right was not handled")
	}
	if !g.Focused() {
		t.Fatal("table lost focus after horizontal rebuild")
	}
	if _, handled := u.updateGrid(tea.KeyPressMsg{Code: tea.KeyDown}); !handled {
		t.Fatal("down was not handled")
	}
	if g.CurrentIndex() != 1 {
		t.Fatalf("cursor = %d, want 1", g.CurrentIndex())
	}
}

func TestShiftUpFromEmptyInputFocusesLatestGrid(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"InvoiceId"},
		Rows: []secureread.Row{
			{Data: map[string]any{"InvoiceId": 412}},
			{Data: map[string]any{"InvoiceId": 411}},
		},
	}}}})

	if u.gridFocused {
		t.Fatal("grid unexpectedly focused before pressing shift+up")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if u.gridFocused {
		t.Fatal("plain up unexpectedly focused the grid")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if !u.gridFocused {
		t.Fatal("shift+up from empty input did not focus the latest grid")
	}
	if u.activeGrid < 0 || !u.entries[u.activeGrid].grid.Focused() {
		t.Fatal("latest grid table is not focused")
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := u.entries[u.activeGrid].grid.CurrentIndex(); got != 1 {
		t.Fatalf("cursor after down = %d, want 1", got)
	}
}

func TestShiftArrowsNavigateBetweenGridsAndInput(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"First"},
		Rows:    []secureread.Row{{Data: map[string]any{"First": "older"}}},
	}}}})
	olderGrid := len(u.entries) - 1
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"Second"},
		Rows:    []secureread.Row{{Data: map[string]any{"Second": "newer"}}},
	}}}})
	newerGrid := len(u.entries) - 1

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if !u.gridFocused || u.activeGrid != newerGrid {
		t.Fatalf("first shift+up focused grid %d, want newest grid %d", u.activeGrid, newerGrid)
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if !u.gridFocused || u.activeGrid != olderGrid {
		t.Fatalf("second shift+up focused grid %d, want older grid %d", u.activeGrid, olderGrid)
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	if !u.gridFocused || u.activeGrid != newerGrid {
		t.Fatalf("shift+down focused grid %d, want newer grid %d", u.activeGrid, newerGrid)
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	if u.gridFocused || !u.input.Focused() {
		t.Fatal("shift+down from newest grid did not return focus to input")
	}
	if u.entries[newerGrid].grid.Focused() || strings.Contains(u.entries[newerGrid].grid.view(), "●") {
		t.Fatal("returning to input left the selected grid highlighted")
	}
}

func TestShiftRightFocusesWorkspacePaneAndShiftLeftReturnsToInput(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 120
	u.resizeChatPane()

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	if !u.workspaceFocused || u.input.Focused() {
		t.Fatal("shift+right did not move focus to the workspace pane")
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	if u.workspaceFocused || !u.input.Focused() {
		t.Fatal("shift+left did not return focus to the chat input")
	}
}

func TestShiftRightIsIgnoredWhenPanesAreNotSplit(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 80

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	if u.workspaceFocused {
		t.Fatal("shift+right focused the workspace pane without a split layout")
	}
}

func TestShiftRightFromGridFocusesWorkspacePane(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 120
	u.resizeChatPane()
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	if !u.workspaceFocused || u.gridFocused {
		t.Fatal("shift+right from a grid did not move focus to the workspace pane")
	}
}

func TestEscapeFocusesComposerAndClearsActiveGridHighlight(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if u.gridFocused || !u.input.Focused() {
		t.Fatal("Escape did not return keyboard focus to the composer")
	}
	if g.Focused() || strings.Contains(g.view(), "●") || !strings.Contains(g.view(), "○") {
		t.Fatalf("Escape left the selected grid highlighted:\n%s", g.view())
	}
}

func TestShiftNavigationScrollsFocusedGridIntoView(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 40
	u.history.SetWidth(40)
	u.history.SetHeight(5)
	for _, value := range []string{"oldest", "middle", "newest"} {
		u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
			Columns: []string{"Value"},
			Rows: []secureread.Row{
				{Data: map[string]any{"Value": value + " 1"}},
				{Data: map[string]any{"Value": value + " 2"}},
				{Data: map[string]any{"Value": value + " 3"}},
			},
		}}}})
	}
	u.rebuildHistory(true)

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	bottomOffset := u.history.YOffset()
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if got := u.history.YOffset(); got >= bottomOffset {
		t.Fatalf("viewport offset after focusing previous grid = %d, want less than bottom offset %d", got, bottomOffset)
	}
}

func TestShiftNavigationKeepsPrecedingMessageVisible(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 60
	u.height = 12
	for _, value := range []string{"older", "newer"} {
		u.entries = append(u.entries, historyEntry{role: "You", text: value + " question"})
		u.appendTurn(Turn{Queries: []QueryResult{{Title: value, Result: secureread.Result{
			Columns: []string{"Value"},
			Rows:    []secureread.Row{{Data: map[string]any{"Value": value}}},
		}}}})
	}
	u.rebuildHistory(true)
	// The newest stop is a grid, then its user message, then the older grid.
	for range 3 {
		_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	}
	visible := ansi.Strip(u.history.View())
	if !strings.Contains(visible, "You: older question") || !strings.Contains(visible, "older") {
		t.Fatalf("focused grid lost its preceding message context:\n%s", visible)
	}
}

func TestShiftArrowsSelectUserMessageAndEnterLoadsItIntoComposer(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.entries = append(u.entries, historyEntry{role: "You", text: "older question"})
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	u.entries = append(u.entries, historyEntry{role: "You", text: "newer question"})
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 2}}},
	}}}})
	newerMessage := len(u.entries) - 2

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if !u.gridFocused {
		t.Fatal("shift+up did not focus the newest grid")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if !u.messageFocused || u.selectedMessage != newerMessage {
		t.Fatalf("message focus = %v/%d, want true/%d", u.messageFocused, u.selectedMessage, newerMessage)
	}
	if !strings.Contains(ansi.Strip(u.View().Content), "message selected") {
		t.Fatal("status line does not announce the selected message")
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if u.messageFocused || !u.input.Focused() {
		t.Fatal("Enter did not return focus to the composer")
	}
	if got := u.input.Value(); got != "newer question" {
		t.Fatalf("composer value = %q, want %q", got, "newer question")
	}
	if got := u.input.Column(); got != len("newer question") {
		t.Fatalf("cursor position = %d, want %d", got, len("newer question"))
	}
}

func TestUserMessageCardHasBarPaddingAndSelectionState(t *testing.T) {
	normal := userMessageView("hello", 40, false)
	selected := userMessageView("hello", 40, true)
	lines := strings.Split(normal, "\n")
	if len(lines) != 3 {
		t.Fatalf("user message card has %d rows, want 3 (top pad, text, bottom pad)", len(lines))
	}
	for _, line := range lines {
		if got := ansi.StringWidth(line); got != 40 {
			t.Fatalf("user message line rendered at %d cells, want 40: %q", got, line)
		}
		if !strings.HasPrefix(ansi.Strip(line), "┃") {
			t.Fatalf("user message line lacks the left accent bar: %q", ansi.Strip(line))
		}
	}
	if !strings.Contains(ansi.Strip(normal), "You: hello") {
		t.Fatalf("user message card missing its label:\n%s", ansi.Strip(normal))
	}
	if normal == selected {
		t.Fatal("selected user message is indistinguishable from an unselected one")
	}
}

func TestShiftNavigationKeepsWrappedPrecedingMessageVisibleWhenItFits(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 32
	u.height = 10
	u.entries = append(u.entries, historyEntry{role: "You", text: "first prompt line that wraps onto another line"})
	u.appendTurn(Turn{Queries: []QueryResult{{Title: "result", Result: secureread.Result{
		Columns: []string{"Value"},
		Rows:    []secureread.Row{{Data: map[string]any{"Value": "answer"}}},
	}}}})
	u.rebuildHistory(true)

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	visible := ansi.Strip(u.history.View())
	for _, want := range []string{"first prompt line", "onto another line", "result", "answer"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("focused grid lost wrapped preceding context %q:\n%s", want, visible)
		}
	}
}

func TestGridFocusRestoresPrecedingContextWhenGridIsAlreadyVisible(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.history.SetWidth(40)
	u.history.SetHeight(6)
	blocks := []string{
		"prompt first line\nprompt second line",
		"grid title\nheader\nrow",
		"later one\nlater two\nlater three\nlater four",
	}
	u.history.SetContent(strings.Join(blocks, "\n\n"))
	u.history.SetYOffset(3)
	if got := u.history.YOffset(); got != 3 {
		t.Fatalf("test setup offset = %d, want 3", got)
	}

	u.ensureBlockVisible(blocks, 1)
	if got := u.history.YOffset(); got != 0 {
		t.Fatalf("contextual offset = %d, want 0 so the preceding message is visible", got)
	}
}

func TestShiftUpPreservesNonEmptyInput(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"InvoiceId"},
		Rows:    []secureread.Row{{Data: map[string]any{"InvoiceId": 412}}},
	}}}})
	u.input.SetValue("draft question")

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if u.gridFocused {
		t.Fatal("shift+up with non-empty input unexpectedly focused a grid")
	}
	if got := u.input.Value(); got != "draft question" {
		t.Fatalf("input value = %q, want draft preserved", got)
	}
}

func TestUIShowsShiftArrowNavigationHint(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	if view := u.View().Content; !strings.Contains(view, "Shift+↑↓ to navigate") {
		t.Fatalf("status line missing Shift+Arrow navigation hint:\n%s", view)
	}
}

func TestUIViewEnablesMouseWheelHistoryScrolling(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 50
	u.height = 8
	for i := 0; i < 20; i++ {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "history line"})
	}
	u.rebuildHistory(true)
	if got := u.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want MouseModeCellMotion for wheel events", got)
	}
	bottom := u.history.YOffset()
	if bottom == 0 {
		t.Fatal("test history did not overflow")
	}
	_, _ = u.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if got := u.history.YOffset(); got >= bottom {
		t.Fatalf("mouse wheel did not scroll history up: offset %d, bottom %d", got, bottom)
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if got := u.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("mouse mode after F2 = %v, want MouseModeNone for terminal selection", got)
	}
	if view := u.View().Content; !strings.Contains(view, "F2 wheel") {
		t.Fatalf("selection mode status does not advertise restoring wheel capture:\n%s", view)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if got := u.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode after second F2 = %v, want MouseModeCellMotion", got)
	}
}

func TestUIComposerSpacerShowsScrollDownCueAndClickJumpsToLatest(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 70
	u.height = 12
	for i := 0; i < 20; i++ {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "history line"})
	}
	u.history.SetHeight(u.historyHeight())
	u.rebuildHistory(true)
	u.View()
	if !u.history.AtBottom() {
		t.Fatal("test history did not start at the bottom")
	}
	cueRow := u.historyHeight() + 1 // application top bar
	lines := strings.Split(ansi.Strip(u.View().Content), "\n")
	if strings.TrimSpace(lines[cueRow]) != "" {
		t.Fatalf("composer spacer should be blank at the bottom: %q", lines[cueRow])
	}
	if !strings.Contains(lines[cueRow+3], "Ask about your data") {
		t.Fatalf("composer does not follow attachment row: %q", lines[cueRow+3])
	}

	_, _ = u.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if u.history.AtBottom() {
		t.Fatal("mouse wheel did not scroll away from bottom")
	}
	lines = strings.Split(ansi.Strip(u.View().Content), "\n")
	if !strings.Contains(lines[cueRow], "▼ scroll to see more ▼") {
		t.Fatalf("missing scroll-down cue above composer: %q", lines[cueRow])
	}
	_, _ = u.Update(tea.MouseClickMsg{X: u.width / 2, Y: cueRow, Button: tea.MouseLeft})
	if !u.history.AtBottom() {
		t.Fatal("clicking scroll-down cue did not jump to latest message")
	}
	lines = strings.Split(ansi.Strip(u.View().Content), "\n")
	if strings.TrimSpace(lines[cueRow]) != "" {
		t.Fatalf("scroll-down cue remained after jumping to bottom: %q", lines[cueRow])
	}
}

func TestClickingScrollDownCueClearsGridFocus(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 80
	u.height = 12
	for i := 0; i < 20; i++ {
		u.entries = append(u.entries, historyEntry{role: "DataTug", text: "history line"})
	}
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	u.rebuildHistory(true)
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	u.history.GotoTop()
	_, _ = u.Update(tea.MouseClickMsg{X: u.width / 2, Y: u.historyHeight() + 1, Button: tea.MouseLeft})
	if !u.history.AtBottom() || u.gridFocused || !u.input.Focused() {
		t.Fatal("scroll-down cue did not return to latest history and composer focus")
	}
	if u.entries[u.activeGrid].grid.Focused() {
		t.Fatal("grid remained highlighted after clicking scroll-down cue")
	}
}

func TestGridTitleFooterAndScrollbarAreStructuredPresentation(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 60
	rows := make([]secureread.Row, 20)
	for i := range rows {
		rows[i] = secureread.Row{Data: map[string]any{"ID": i + 1}}
	}
	u.appendTurn(Turn{Queries: []QueryResult{{Title: "  Latest   orders\n", Result: secureread.Result{
		Columns: []string{"ID"}, Rows: rows,
	}}}})
	u.rebuildHistory(true)
	view := u.View().Content
	if !strings.Contains(view, "Latest orders") || !strings.Contains(view, "Rows 1–10 of 20 returned") || !strings.Contains(view, "▐") {
		t.Fatalf("grid presentation missing title/footer/scrollbar:\n%s", view)
	}
	if !strings.Contains(view, "○") {
		t.Fatalf("inactive grid marker missing:\n%s", view)
	}
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	u.rebuildHistory(false)
	if !strings.Contains(u.View().Content, "●") {
		t.Fatalf("active grid marker missing:\n%s", u.View().Content)
	}
}

func TestResultTitleAppearsOnceInCardBorder(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}}}), "", "Customers", 80)
	view := ansi.Strip(g.View(80, g.Focused()))
	if strings.Count(strings.Split(view, "\n")[0], "Customers") != 1 || strings.Count(view, "Customers") != 1 {
		t.Fatalf("title repeated in result card:\n%s", view)
	}
	for _, tab := range []string{"1 Table", "2 Charts", "3 Current row"} {
		if !strings.Contains(strings.Split(view, "\n")[0], tab) {
			t.Fatalf("top border does not expose %s:\n%s", tab, view)
		}
	}
}

func TestFocusedRecordSetTitleAndFooterAreReadable(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}}}), "", "Invoices", 80)
	g.SetFocused(true)
	lines := strings.Split(g.View(80, g.Focused()), "\n")
	if !strings.Contains(lines[0], activeTitleStyle.Render("Invoices")) {
		t.Fatalf("focused title has no active color: %q", lines[0])
	}
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, "38;5;252") || !strings.Contains(footer, "Rows 1–1") {
		t.Fatalf("footer label is not bright enough: %q", footer)
	}
}

func TestGridScrollbarTracksCursorBeforePageScrolls(t *testing.T) {
	rows := make([]secureread.Row, 20)
	for i := range rows {
		rows[i] = secureread.Row{Data: map[string]any{"ID": i}}
	}
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID"}, Rows: rows}), "", "Rows", 60)
	g.SetFocused(true)
	start, _ := g.VisibleIndices()
	before := g.View(60, true)
	g.SelectRow(5)
	after := g.View(60, true)
	still, _ := g.VisibleIndices()
	if start != still || before == after {
		t.Fatalf("scrollbar did not follow cursor within page: page %d→%d, view unchanged=%v", start, still, before == after)
	}
}

func TestWorkspaceReturnsToPreviousGrid(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 150
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}}}}}})
	if !u.focusLatestGrid() {
		t.Fatal("grid missing")
	}
	gridIndex := u.activeGrid
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift})
	if !u.gridFocused || u.activeGrid != gridIndex || !u.entries[gridIndex].grid.Focused() {
		t.Fatal("Shift+Left did not restore previous grid focus")
	}
}

func TestWorkspaceInspectorsUseCatalogTypes(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 150
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Customer", Title: "Customer"}, Columns: []string{"CustomerId", "City"}, ColumnTypes: map[string]string{"CustomerId": "INTEGER", "City": "TEXT"}}}}
	u.appendTurn(Turn{Queries: []QueryResult{{
		Title: "Customers",
		Result: secureread.Result{
			Columns: []string{"CustomerId", "City"},
			Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 42, "City": "Prague"}}},
		},
	}}})
	if !u.focusLatestGrid() {
		t.Fatal("grid missing")
	}
	u.focusWorkspace()
	u.workspaceTab = 1
	for _, tc := range []struct{ key, want string }{{"1", "INTEGER"}, {"2", "main.Customer.CustomerId"}, {"3", "main.Customer.City"}} {
		_, _ = u.Update(tea.KeyPressMsg{Text: tc.key})
		if view := ansi.Strip(u.workspaceView(65, 22)); !strings.Contains(view, tc.want) {
			t.Fatalf("inspector %s lacks %q:\n%s", tc.key, tc.want, view)
		}
	}
}

func TestInspectorDoesNotAttributeDerivedOutputByDisplayName(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Customer", SourceID: "chinook"}, Columns: []string{"CustomerId", "City"}, ColumnTypes: map[string]string{"CustomerId": "INTEGER", "City": "TEXT"}}}}
	record := RecordSet{Database: "chinook", DTQL: "from: {name: Customer}\ncolumns:\n  - aggregate: {function: COUNT, args: [{star: true}]}\n    as: CustomerId\nlimit: 10\n"}
	if got := u.columnMeta(&record, "CustomerId"); got.qualified != "" || got.dbType != "" {
		t.Fatalf("aggregate was falsely attributed to a physical column: %+v", got)
	}
	record.DTQL = "from: {name: Customer}\ncolumns:\n  - field: City\n    as: CustomerId\nlimit: 10\n"
	if got := u.columnMeta(&record, "CustomerId"); got.qualified != "main.Customer.City" || got.dbType != "TEXT" {
		t.Fatalf("aliased field provenance = %+v", got)
	}
}

func TestAltSCyclesAllTablesAndRestoresSessionStyle(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	result := secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}}}
	u.appendTurn(Turn{Queries: []QueryResult{{Result: result}, {Result: result}}})
	_, command := u.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModAlt})
	if command == nil || u.tableStyle.Name != grid.StyleSoft.Name || u.styleNotice != "Table style: Soft" {
		t.Fatalf("Alt+S style/notice = %s/%q", u.tableStyle.Name, u.styleNotice)
	}
	if status := strings.Join(u.statusLines(), " "); !strings.Contains(status, "Table style: Soft") || strings.Contains(status, "Alt+S style") {
		t.Fatalf("style notice did not temporarily replace shortcut hint: %q", status)
	}
	for _, entry := range u.entries {
		if entry.grid != nil && entry.grid.Style().Name != grid.StyleSoft.Name {
			t.Fatal("existing result did not adopt the style")
		}
	}
	u.appendTurn(Turn{Queries: []QueryResult{{Result: result}}})
	if got := u.entries[len(u.entries)-1].grid.Style(); got.Name != grid.StyleSoft.Name {
		t.Fatalf("new result style = %s", got.Name)
	}
	storedStyle, err := sessions.TableStyle(ctx)
	if err != nil || storedStyle != "Soft" {
		t.Fatalf("persisted style = %q, %v", storedStyle, err)
	}
	newSession, err := sessions.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(newSession)
	if u.tableStyle.Name != grid.StyleSoft.Name {
		t.Fatal("new session lost the shared table style")
	}
	reopened, err := NewSessionUI(ctx, sessions, "fake-model")
	if err != nil || reopened.tableStyle.Name != grid.StyleSoft.Name {
		t.Fatalf("restored style = %v, %v", reopened.tableStyle, err)
	}
	_, _ = u.Update(tableStyleNoticeExpired{id: u.styleNoticeID})
	if u.styleNotice != "" {
		t.Fatal("style name notice did not clear")
	}
	u.appendTurn(Turn{Queries: []QueryResult{{Result: result}}})
	u.focusLatestGrid()
	if status := strings.Join(u.statusLines(), " "); !strings.Contains(status, "Alt+S style") || strings.Contains(status, "Table style: Soft") {
		t.Fatalf("shortcut hint did not return after notice: %q", status)
	}
}

func TestGridWithoutScrollingUsesPlainRightBorder(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}}}), "", "Rows", 60)
	view := ansi.Strip(g.view())
	if strings.ContainsAny(view, "▏▐") {
		t.Fatalf("non-scrolling grid has a scrollbar placeholder: %q", view)
	}
	for _, line := range strings.Split(view, "\n")[1 : len(strings.Split(view, "\n"))-1] {
		if !strings.HasSuffix(line, "│") {
			t.Fatalf("grid right edge is not a plain border: %q", line)
		}
	}
}

func TestMacOptionSCyclesTableStyle(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.Update(tea.KeyPressMsg{Code: 'ß', Text: "ß"})
	if u.tableStyle.Name != grid.StyleSoft.Name || u.styleNotice != "Table style: Soft" {
		t.Fatalf("Option+S style/notice = %s/%q", u.tableStyle.Name, u.styleNotice)
	}
}

func TestTableStylePresetsChangeHeaderAndDividerColors(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID", "Name"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1, "Name": "Alex"}}}}), "", "Rows", 60)
	if g.Style().Name != grid.StyleLines.Name {
		t.Fatal("new table did not default to Lines")
	}
	for _, tc := range []struct {
		style grid.Style
		color string
	}{
		{grid.StyleLines, "241"},
		{grid.StyleSoft, "235"},
		{grid.StyleMinimal, "232"},
	} {
		g.SetStyle(tc.style)
		view := g.TableView()
		if !strings.Contains(view, "38;5;"+tc.color+"m┃") {
			t.Fatalf("%s column divider lacks preset color: %q", tc.style.Name, view)
		}
		header := strings.Split(view, "\n")[0]
		if !strings.Contains(header, "\x1b[1;") {
			t.Fatalf("%s header is not bold: %q", tc.style.Name, header)
		}
		card := strings.Split(g.view(), "\n")
		if len(card) != 4 || strings.Contains(ansi.Strip(card[2]), "━") {
			t.Fatalf("%s card still has a header/data border: %q", tc.style.Name, card)
		}
	}
}

func TestGridScrollbarUsesTopMiddleAndShortFinalPages(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	rows := make([]secureread.Row, 25)
	for i := range rows {
		rows[i] = secureread.Row{Data: map[string]any{"ID": i + 1}}
	}
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{Columns: []string{"ID"}, Rows: rows}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	if view := g.view(); !strings.Contains(view, "▐") {
		t.Fatalf("top page has no scrollbar thumb:\n%s", view)
	}
	topLines := strings.Split(ansi.Strip(g.view()), "\n")
	if len(topLines) < 3 || !strings.HasSuffix(strings.TrimSpace(topLines[1]), "▐") || !strings.HasSuffix(strings.TrimSpace(topLines[2]), "▐") {
		t.Fatalf("scrollbar is not on the right card edge:\n%s", g.view())
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if view := g.view(); !strings.Contains(view, "▐") {
		t.Fatalf("middle page has no scrollbar thumb:\n%s", view)
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if start, end := g.VisibleIndices(); start != 20 || end != 24 {
		t.Fatalf("final page range = %d-%d, want 20-24", start, end)
	}
	if view := g.view(); !strings.Contains(view, "▐") || !strings.Contains(view, "Rows 21–25 of 25 returned") {
		t.Fatalf("short final page has incorrect scrollbar/footer:\n%s", view)
	}
}

// TestGridColumnStylesAlignTextLeftAndNumbersRight: per-column alignment
// (gridColumnStyle) moved into strongo/aichat's tui/grid as an unexported
// policy (grid.Column.Numeric drives right-alignment); its coverage moved
// with it (TestColumnAlignment in tui/grid's test suite). What remains
// DataTug's own is that GridColumn.Numeric is set correctly by
// columnIsNumeric, already covered by TestNewGridModelPreservesStructureAndFormatsValues.

func TestGridEnterIsReservedAndSSorts(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"Name"},
		Rows: []secureread.Row{
			{Data: map[string]any{"Name": "Zulu"}},
			{Data: map[string]any{"Name": "Alpha"}},
		},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := g.Cell(0, 0); got != "Zulu" {
		t.Fatalf("Enter changed sort order to %q", got)
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Text: "s"})
	if got := g.Cell(0, 0); got != "Alpha" {
		t.Fatalf("s did not sort ascending, got %q", got)
	}
}

func TestGridUsesBubbleTablePaginationAndDoesNotWrapAtBoundaries(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	rows := make([]secureread.Row, 25)
	for i := range rows {
		rows[i] = secureread.Row{Data: map[string]any{"ID": i + 1}}
	}
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{Columns: []string{"ID"}, Rows: rows}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	if start, end := g.VisibleIndices(); start != 0 || end != 9 {
		t.Fatalf("initial visible range = %d-%d, want 0-9", start, end)
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyUp})
	if g.CurrentIndex() != 0 {
		t.Fatalf("cursor wrapped at first row: %d", g.CurrentIndex())
	}
	for i := 0; i < len(rows)-1; i++ {
		_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if g.CurrentIndex() != len(rows)-1 {
		t.Fatalf("cursor = %d, want last row", g.CurrentIndex())
	}
	if start, end := g.VisibleIndices(); start != 20 || end != 24 {
		t.Fatalf("last visible range = %d-%d, want 20-24", start, end)
	}
	if footer := g.Footer(); !strings.Contains(footer, "Rows 21–25 of 25 returned") {
		t.Fatalf("last-page footer = %q", footer)
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyDown})
	if g.CurrentIndex() != len(rows)-1 {
		t.Fatalf("cursor wrapped at last row: %d", g.CurrentIndex())
	}
}

func TestGridHorizontalSelectionUsesBubbleTableOverflow(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	// contentWidth(26) is 24, giving bubble-table an exact 22-cell table:
	// after the left overflow marker, columns 2-4 fit only because the final
	// source column has no trailing divider.
	u.width = 26
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"First", "Second", "Third", "Fourth"},
		Rows:    []secureread.Row{{Data: map[string]any{"First": "one", "Second": "two", "Third": "three", "Fourth": "four"}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	for i := 0; i < 3; i++ {
		_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyRight})
		first, last := g.VisibleColumnRange()
		if g.SelectedColumn()+1 < first || g.SelectedColumn()+1 > last {
			t.Fatalf("step %d selected column %d outside visible range %d-%d", i+1, g.SelectedColumn()+1, first, last)
		}
	}
	if g.SelectedColumn() != 3 || g.ColumnOffset() == 0 {
		t.Fatalf("selected column/offset = %d/%d, want selected last column and horizontal offset", g.SelectedColumn(), g.ColumnOffset())
	}
	if g.ColumnOffset() != 1 {
		t.Fatalf("horizontal offset = %d, want exact-fit offset 1", g.ColumnOffset())
	}
	if first, last := g.VisibleColumnRange(); first != 2 || last != 4 {
		t.Fatalf("visible columns = %d-%d, want exact rendered range 2-4", first, last)
	}
	plainTable := ansi.Strip(g.TableView())
	for _, visible := range []string{"Second", "Third", "Fourth", "two", "three", "four"} {
		if !strings.Contains(plainTable, visible) {
			t.Fatalf("reported visible value %q is absent from rendered table:\n%s", visible, plainTable)
		}
	}
	if footer := g.Footer(); !strings.Contains(footer, "Cols 2–4 of 4") {
		t.Fatalf("footer does not match rendered columns: %q", footer)
	}
	for i := 0; i < 3; i++ {
		_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	if g.SelectedColumn() != 0 || g.ColumnOffset() != 0 {
		t.Fatalf("left navigation selected column/offset = %d/%d", g.SelectedColumn(), g.ColumnOffset())
	}
}

func TestGridNarrowMarkerOnlyViewReportsNoVisibleColumns(t *testing.T) {
	model := GridModel{
		Columns: []GridColumn{{Name: "First"}, {Name: "Second"}},
		Rows:    [][]string{{"one", "two"}},
	}
	g := newGridState(model, "", "Narrow", 2)
	g.SelectColumn(1)
	if first, last := g.VisibleColumnRange(); first != 0 || last != 0 {
		t.Fatalf("marker-only visible range = %d-%d, want 0-0", first, last)
	}
	if strings.Contains(g.Footer(), "Cols ") {
		t.Fatalf("marker-only footer invented visible columns: %q", g.Footer())
	}
}

func TestGridNarrowViewCanRevealSelectedFinalColumn(t *testing.T) {
	model := GridModel{
		Columns: []GridColumn{{Name: "First"}, {Name: "Second"}},
		Rows:    [][]string{{"one", "two"}},
	}
	// The 8-cell table initially has room only for the right overflow marker:
	// a non-final six-cell column also needs its divider. At offset 1, however,
	// the left marker plus the divider-free final column fit exactly.
	g := newGridState(model, "", "Narrow", 10)
	g.SelectColumn(1)
	if g.ColumnOffset() != 1 {
		t.Fatalf("horizontal offset = %d, want final-column offset 1", g.ColumnOffset())
	}
	if first, last := g.VisibleColumnRange(); first != 2 || last != 2 {
		t.Fatalf("visible range = %d-%d, want selected final column 2-2", first, last)
	}
	plainTable := ansi.Strip(g.TableView())
	for _, visible := range []string{"Second", "two"} {
		if !strings.Contains(plainTable, visible) {
			t.Fatalf("selected final-column value %q is absent from rendered table:\n%s", visible, plainTable)
		}
	}
}

func TestGridResizePreservesFocusRowAndHorizontalWindow(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 26
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"First", "Second", "Third", "Fourth"},
		Rows:    []secureread.Row{{Data: map[string]any{"First": "one", "Second": "two", "Third": "three", "Fourth": "four"}}, {Data: map[string]any{"First": "a", "Second": "b", "Third": "c", "Fourth": "d"}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyDown})
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyRight})
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyRight})
	row := g.CurrentIndex()
	u.width = 70
	u.rebuildHistory(false)
	if !g.Focused() || g.CurrentIndex() != row {
		t.Fatalf("focus/cursor after resize = %v/%d, want true/%d", g.Focused(), g.CurrentIndex(), row)
	}
	first, last := g.VisibleColumnRange()
	if g.SelectedColumn()+1 < first || g.SelectedColumn()+1 > last {
		t.Fatalf("selected column %d is outside visible range %d-%d after resize", g.SelectedColumn()+1, first, last)
	}
}

func TestUIUsesRootGutterAndPlacesStatusBelowInput(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 60
	u.appendTurn(Turn{Text: "hello"})
	u.rebuildHistory(true)
	view := u.View().Content
	firstLine := strings.Split(view, "\n")[0]
	if !strings.HasPrefix(firstLine, "  ") {
		t.Fatalf("root gutter missing from first line: %q", firstLine)
	}
	plain := ansi.Strip(view)
	inputIndex := strings.Index(plain, "Ask about your data")
	statusIndex := strings.LastIndex(plain, "model: fake-model")
	if inputIndex < 0 || statusIndex < 0 || statusIndex <= inputIndex {
		t.Fatalf("status is not below input:\n%s", view)
	}
	if ansi.Strip(view) == view {
		t.Fatal("expected subtle styled surfaces")
	}
}

func TestUIComposerPromptUsesComposerBackground(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	styles := u.input.Styles()
	for name, style := range map[string]lipgloss.Style{
		"focused placeholder": styles.Focused.Placeholder,
		"focused text":        styles.Focused.Text,
		"blurred placeholder": styles.Blurred.Placeholder,
		"blurred text":        styles.Blurred.Text,
	} {
		if style.GetBackground() == nil {
			t.Errorf("%s has no composer background", name)
		}
	}
}

func TestShiftEnterAddsComposerLineWithoutSending(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.input.SetValue("first")
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if got := u.input.Value(); got != "first\n" {
		t.Fatalf("composer text = %q", got)
	}
	if u.input.Height() != 2 || u.busy {
		t.Fatalf("composer height/busy = %d/%v", u.input.Height(), u.busy)
	}
}

func TestComposerAccentBarTracksFocus(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	focused := u.composerView(40)
	u.input.Blur()
	if u.input.Focused() {
		t.Fatal("input did not blur")
	}
	blurred := u.composerView(40)
	for _, line := range strings.Split(focused, "\n") {
		if got := ansi.StringWidth(line); got != 40 {
			t.Fatalf("composer line rendered at %d cells, want 40: %q", got, line)
		}
	}
	if !strings.Contains(ansi.Strip(focused), "┃") || !strings.Contains(ansi.Strip(focused), "▀") {
		t.Fatalf("composer lacks its accent bar or fade edge:\n%s", focused)
	}
	if focused == blurred {
		t.Fatal("composer accent bar did not change with focus")
	}
}

func TestUIStatusHintsFollowFocus(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	inputView := ansi.Strip(u.View().Content)
	if !strings.Contains(inputView, "Shift+↑↓ to navigate") || !strings.Contains(inputView, "Enter send") {
		t.Fatalf("input status is not contextual: %s", inputView)
	}
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	u.rebuildHistory(false)
	gridView := ansi.Strip(u.View().Content)
	if !strings.Contains(gridView, "1 Table") || !strings.Contains(gridView, "2 Charts") || !strings.Contains(gridView, "3 Current row") || !strings.Contains(gridView, "↑↓ rows") {
		t.Fatalf("grid status is not contextual: %s", gridView)
	}
}

func TestRecordsetViewsRouteFocusAcrossSplitAndNarrowLayouts(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 180
	u.chatPanePercent = 75
	u.appendTurn(Turn{Queries: []QueryResult{{Title: "Customers", Result: secureread.Result{
		Columns: []string{"ID", "Country"},
		Rows:    []secureread.Row{{Data: map[string]any{"ID": 1, "Country": "Ireland"}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	g.charts = []ChartCandidate{
		{Spec: ChartSpec{Kind: ChartBar, Title: "By country", Points: []ChartPoint{{Label: "Ireland", Value: 1}}}},
		{Spec: ChartSpec{Kind: ChartBar, Title: "By ID", Points: []ChartPoint{{Label: "1", Value: 1}}}},
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "2"})
	if g.CurrentView() != gridViewCharts || !chooseGridLayout(u.chatPaneWidth(), g.NaturalWidth(), g.CurrentView()).Split || g.SecondaryFocus() {
		t.Fatalf("wide chart state = view:%v layout:%+v secondary:%v", g.CurrentView(), chooseGridLayout(u.chatPaneWidth(), g.NaturalWidth(), g.CurrentView()), g.SecondaryFocus())
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !g.SecondaryFocus() || g.chartIndex != 1 {
		t.Fatalf("chart focus/index = %v/%d, want true/1", g.SecondaryFocus(), g.chartIndex)
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "1"})
	if g.CurrentView() != grid.ViewTable || g.SecondaryFocus() {
		t.Fatalf("table return = view:%v secondary:%v", g.CurrentView(), g.SecondaryFocus())
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "3"})
	if g.CurrentView() != gridViewCurrentRow {
		t.Fatalf("current-row view = %v", g.CurrentView())
	}
	u.width = 70
	u.rebuildHistory(false)
	if chooseGridLayout(u.chatPaneWidth(), g.NaturalWidth(), g.CurrentView()).Split || !g.SecondaryFocus() {
		t.Fatalf("narrow inspector layout/focus = %+v/%v", chooseGridLayout(u.chatPaneWidth(), g.NaturalWidth(), g.CurrentView()), g.SecondaryFocus())
	}
	if view := ansi.Strip(u.View().Content); !strings.Contains(view, "3 Current row") || !strings.Contains(view, "Ireland") {
		t.Fatalf("narrow inspector did not render current row:\n%s", view)
	}
}

func TestRecordsetTabReturnsToComposerOutsideWideSplit(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}}}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if u.gridFocused || !u.input.Focused() {
		t.Fatal("Tab in Table did not return to the composer")
	}
	if !u.focusLatestGrid() {
		t.Fatal("expected grid refocus")
	}
	u.width = 70
	_, _ = u.Update(tea.KeyPressMsg{Text: "3"})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if u.gridFocused || !u.input.Focused() {
		t.Fatal("Tab in narrow secondary view did not return to the composer")
	}
}

func TestEscapeClearsSecondaryPaneHighlight(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 70
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "3"})
	u.rebuildHistory(false)
	if view := ansi.Strip(u.View().Content); !strings.Contains(view, "● Query result") {
		t.Fatalf("focused secondary pane lacks active marker:\n%s", view)
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if u.gridFocused || !u.input.Focused() {
		t.Fatal("Escape did not return focus to composer")
	}
	if view := ansi.Strip(u.View().Content); strings.Contains(view, "● Query result") {
		t.Fatalf("Escape left secondary pane highlighted:\n%s", view)
	}
}

func TestRecordsetHeaderRetainsViewControlsForLongTitles(t *testing.T) {
	g := newGridState(GridModel{}, "", strings.Repeat("very long generated title ", 8), 70)
	header := ansi.Strip(g.HeaderLine(70))
	for _, want := range []string{"1 Table", "2 Charts", "3 Current row"} {
		if !strings.Contains(header, want) {
			t.Fatalf("long-title header hid %q: %q", want, header)
		}
	}
	if got := ansi.StringWidth(header); got != 70 {
		t.Fatalf("header width = %d, want 70: %q", got, header)
	}
}

func TestRecordsetHeaderRetainsAllControlsAtTwentyTwoCells(t *testing.T) {
	g := newGridState(GridModel{}, "", strings.Repeat("generated title ", 8), 22)
	header := ansi.Strip(g.HeaderLine(22))
	for _, want := range []string{"1", "2", "3"} {
		if !strings.Contains(header, want) {
			t.Fatalf("22-cell header hid %q: %q", want, header)
		}
	}
	if got := ansi.StringWidth(header); got != 22 {
		t.Fatalf("header width = %d, want 22: %q", got, header)
	}
	top := strings.Split(ansi.Strip(g.View(22, g.Focused())), "\n")[0]
	if !strings.Contains(top, "1 2 3") {
		t.Fatalf("rendered border clips a tab: %q", top)
	}
}

func TestCurrentRowInspectorScrollsAndResetsForAnotherSourceRow(t *testing.T) {
	result := secureread.Result{}
	for index := 0; index < 18; index++ {
		name := fmt.Sprintf("Field%02d", index)
		result.Columns = append(result.Columns, name)
	}
	result.Rows = []secureread.Row{
		{Data: map[string]any{}},
		{Data: map[string]any{}},
	}
	for index, name := range result.Columns {
		result.Rows[0].Data[name] = fmt.Sprintf("first value %02d with enough words to wrap", index)
		result.Rows[1].Data[name] = fmt.Sprintf("second value %02d", index)
	}
	u := NewUI(context.Background(), nil, "fake-model")
	u.width = 180
	u.chatPanePercent = 75
	u.appendTurn(Turn{Queries: []QueryResult{{Result: result}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	// CardView (grid.CardView, DataTug's "Current row" view) tracks its own
	// scroll offset internally, so this checks the rendered content instead
	// of an exported Y-offset: after scrolling down, Field00 is no longer
	// on screen; after the highlighted row changes, the next render resets
	// to the top (Field00 visible again). It reads g.ActiveViewContent
	// (the card's own body) rather than the full u.View().Content: at this
	// width the recordset is split table+card side by side, and the table
	// pane's own header legitimately still shows "Field00" regardless of
	// the card's scroll position.
	_, _ = u.Update(tea.KeyPressMsg{Text: "3"})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	u.rebuildHistory(false)
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	u.rebuildHistory(false)
	if strings.Contains(g.ActiveViewContent(60, 10), "Field00") {
		t.Fatal("inspector did not scroll")
	}
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	u.rebuildHistory(false)
	if !strings.Contains(g.ActiveViewContent(60, 10), "Field00") {
		t.Fatal("inspector offset after row change did not reset to top")
	}
}

func TestEmptyRecordsetCanBeFocusedAndShowsSecondaryEmptyStates(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Title: "Empty", Result: secureread.Result{Columns: []string{"ID"}}}}})
	if !u.focusLatestGrid() {
		t.Fatal("empty grid was not focusable")
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "2"})
	u.rebuildHistory(false)
	if view := ansi.Strip(u.View().Content); !strings.Contains(view, "No chart candidates") {
		t.Fatalf("empty charts state missing:\n%s", view)
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "3"})
	u.rebuildHistory(false)
	if view := ansi.Strip(u.View().Content); !strings.Contains(view, "No current row") {
		t.Fatalf("empty current-row state missing:\n%s", view)
	}
}

func TestGridSortRetainsSelectedSourceRowForInspector(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{RecordSetID: "recordset", Result: secureread.Result{
		Columns: []string{"Name"},
		Rows: []secureread.Row{
			{Data: map[string]any{"Name": "Zulu"}},
			{Data: map[string]any{"Name": "Alpha"}},
			{Data: map[string]any{"Name": "Zulu"}},
		},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	g := u.entries[u.activeGrid].grid
	if before := g.selectedSourceRow(); before != 0 {
		t.Fatalf("source row before sort = %d, want 0", before)
	}
	_, _ = u.Update(tea.KeyPressMsg{Text: "s"})
	if got := g.selectedSourceRow(); got != 0 || g.CurrentIndex() != 1 || g.sourceIndexAt(2) != 2 {
		t.Fatalf("sorted selection = source:%d index:%d, want source:0 index:1", got, g.CurrentIndex())
	}
}

func TestSessionHelpDocumentsRecordsetAndExistingGridControls(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.runSessionCommand("/help")
	if len(u.entries) == 0 {
		t.Fatal("help did not add a response")
	}
	help := u.entries[len(u.entries)-1].text
	for _, want := range []string{
		"1 Table", "2 Charts", "3 Current row", "Tab panes", "Shift+↑↓ select",
		"j JOINs", "Space row", "c cell", "r range", "a attach", "d dock", "b bookmark", "s sort", "Enter details", "Esc composer",
		"F2", "F6", "Ctrl+C",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q: %s", want, help)
		}
	}
}

func TestUIStatusUsesOneLineWhenItFits(t *testing.T) {
	u := NewUI(context.Background(), nil, "deepseek-flash")
	u.width = 160
	if lines := u.statusLines(); len(lines) != 1 {
		t.Fatalf("wide input status uses %d lines, want 1: %#v", len(lines), lines)
	}
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	if lines := u.statusLines(); len(lines) != 1 {
		t.Fatalf("wide grid status uses %d lines, want 1: %#v", len(lines), lines)
	}
}

// TestGridTitleAndSelectedColumnUseDifferentColors is m12: this used to
// compare datatug's OWN now-dead style copies (selectedCellStyle,
// unreferenced by any real rendering since column-selection styling moved
// into the shared tui/grid package) rather than asserting anything about
// what's actually drawn on screen. It now renders a real grid and checks
// the title and the selected column's header carry visibly different
// ANSI styling.
func TestGridTitleAndSelectedColumnUseDifferentColors(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Title: "Distinct Title", Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	view := u.entries[u.activeGrid].grid.view()
	lines := strings.Split(view, "\n")
	titleLine, headerLine := lines[0], lines[1]
	titleStart := strings.Index(titleLine, "Distinct")
	headerStart := strings.Index(headerLine, "ID")
	if titleStart < 0 || headerStart < 0 {
		t.Fatalf("could not locate title/header in rendered grid:\n%s", view)
	}
	// Compare the ANSI escape sequence immediately preceding each label —
	// the styling actually applied to it — rather than the label text
	// itself, which carries no color information once printed.
	titleStyle := lastAnsiEscape(titleLine[:titleStart])
	headerStyle := lastAnsiEscape(headerLine[:headerStart])
	if titleStyle == "" || headerStyle == "" {
		t.Fatalf("expected ANSI styling before both labels: title=%q header=%q", titleStyle, headerStyle)
	}
	if titleStyle == headerStyle {
		t.Fatalf("table title and selected column header use the same style: %q", titleStyle)
	}
}

// lastAnsiEscape returns the last ANSI CSI escape sequence in s (e.g.
// "\x1b[1;38;5;255;48;5;237m"), or "" if none.
func lastAnsiEscape(s string) string {
	last := ""
	for i := 0; i < len(s); i++ {
		if s[i] != '\x1b' {
			continue
		}
		end := strings.IndexByte(s[i:], 'm')
		if end < 0 {
			break
		}
		last = s[i : i+end+1]
		i += end
	}
	return last
}

func TestUIWidthSweepKeepsRenderedLinesWithinTerminal(t *testing.T) {
	for width := 1; width <= 80; width++ {
		u := NewUI(context.Background(), nil, "fake-model")
		u.width = width
		u.appendTurn(Turn{Queries: []QueryResult{{Title: "A result title", Result: secureread.Result{
			Columns: []string{"First", "Second", "Third"},
			Rows:    []secureread.Row{{Data: map[string]any{"First": "one", "Second": "two", "Third": "three"}}},
		}}}})
		u.rebuildHistory(true)
		for lineIndex, line := range strings.Split(ansi.Strip(u.View().Content), "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width %d line %d rendered at %d cells: %q", width, lineIndex, got, line)
			}
		}
	}
}

func TestGridBorderChangesWithFocus(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	u.appendTurn(Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}}}})
	g := u.entries[len(u.entries)-1].grid
	inactive := strings.Split(g.view(), "\n")[0]
	if !u.focusLatestGrid() {
		t.Fatal("expected grid focus")
	}
	active := strings.Split(g.view(), "\n")[0]
	activeShape := strings.ReplaceAll(strings.ReplaceAll(ansi.Strip(active), "●", "○"), "○", "○")
	if activeShape != ansi.Strip(inactive) || active == inactive {
		t.Fatalf("grid border did not change with focus:\ninactive %q\nactive %q", inactive, active)
	}
	activeLines := strings.Split(g.view(), "\n")
	// The right-edge scrollbar/border uses the shared grid's own
	// selectedOutlineStyle (color 250) when focused — datatug no longer
	// keeps its own copy of that style to compare Render() output against
	// (m12); check for its ANSI color code directly instead.
	if !strings.Contains(activeLines[1], activeBorderStyle.Render("│")) || !strings.HasSuffix(ansi.Strip(activeLines[1]), "│") || !strings.Contains(activeLines[1], "38;5;250m") {
		t.Fatalf("focused grid side colors are wrong: %q", activeLines[1])
	}
	if !strings.Contains(activeLines[0], "38;5;250m╭") || !strings.Contains(activeLines[len(activeLines)-1], "38;5;250m╰") || strings.Contains(activeLines[len(activeLines)-1], "38;5;51") {
		t.Fatalf("focused grid top/bottom border colors differ: %q / %q", activeLines[0], activeLines[len(activeLines)-1])
	}
}

// TestCurrentRowShowsAbsentMarkerNotBlankForSparseCells is the regression
// test for m5: a sparse cell (never present in the underlying row's data —
// GridModel.Rows leaves its display string at "") must show as "—" in the
// Current row view, via grid.Absent, not an ambiguous blank line.
func TestCurrentRowShowsAbsentMarkerNotBlankForSparseCells(t *testing.T) {
	model := GridModel{
		Columns: []GridColumn{{Name: "First"}, {Name: "Second"}},
		Rows:    [][]string{{"one", ""}}, // Second is sparse/absent for this row
	}
	g := newGridState(model, "", "Sparse", 60)
	g.SetView(gridViewCurrentRow)
	content := ansi.Strip(g.ActiveViewContent(60, 10))
	if !strings.Contains(content, "—") {
		t.Fatalf("current row view missing the absent marker: %q", content)
	}
	if strings.Contains(content, "NULL") {
		t.Fatalf("current row view showed NULL for a sparse (never-present) cell: %q", content)
	}
}
