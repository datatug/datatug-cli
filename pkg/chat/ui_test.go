package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
