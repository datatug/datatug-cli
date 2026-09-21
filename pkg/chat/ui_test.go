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
	if !g.table.Focused() {
		t.Fatal("table lost focus after horizontal rebuild")
	}
	if _, handled := u.updateGrid(tea.KeyPressMsg{Code: tea.KeyDown}); !handled {
		t.Fatal("down was not handled")
	}
	if g.table.Cursor() != 1 {
		t.Fatalf("cursor = %d, want 1", g.table.Cursor())
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
	if u.activeGrid < 0 || !u.entries[u.activeGrid].grid.table.Focused() {
		t.Fatal("latest grid table is not focused")
	}

	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := u.entries[u.activeGrid].grid.table.Cursor(); got != 1 {
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
	if u.entries[newerGrid].grid.focused || strings.Contains(u.entries[newerGrid].grid.view(), "●") {
		t.Fatal("returning to input left the selected grid highlighted")
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
	if g.focused || strings.Contains(g.view(), "●") || !strings.Contains(g.view(), "○") {
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
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	_, _ = u.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	visible := ansi.Strip(u.history.View())
	if !strings.Contains(visible, "You: older question") || !strings.Contains(visible, "older") {
		t.Fatalf("focused grid lost its preceding message context:\n%s", visible)
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
	u.width = 80
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
	cueRow := u.historyHeight()
	lines := strings.Split(ansi.Strip(u.View().Content), "\n")
	if strings.TrimSpace(lines[cueRow]) != "" {
		t.Fatalf("composer spacer should be blank at the bottom: %q", lines[cueRow])
	}
	if !strings.Contains(lines[cueRow+1], "Ask about your data") {
		t.Fatalf("composer does not follow spacer: %q", lines[cueRow+1])
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
	_, _ = u.Update(tea.MouseClickMsg{X: u.width / 2, Y: u.historyHeight(), Button: tea.MouseLeft})
	if !u.history.AtBottom() || u.gridFocused || !u.input.Focused() {
		t.Fatal("scroll-down cue did not return to latest history and composer focus")
	}
	if u.entries[u.activeGrid].grid.focused {
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
	if len(topLines) < 3 || !strings.HasSuffix(topLines[1], "▐") || !strings.HasSuffix(topLines[2], "▐") {
		t.Fatalf("scrollbar does not use both table header lines:\n%s", g.view())
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if view := g.view(); !strings.Contains(view, "▐") {
		t.Fatalf("middle page has no scrollbar thumb:\n%s", view)
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if start, end := g.table.VisibleIndices(); start != 20 || end != 24 {
		t.Fatalf("final page range = %d-%d, want 20-24", start, end)
	}
	if view := g.view(); !strings.Contains(view, "▐") || !strings.Contains(view, "Rows 21–25 of 25 returned") {
		t.Fatalf("short final page has incorrect scrollbar/footer:\n%s", view)
	}
}

func TestGridColumnStylesAlignTextLeftAndNumbersRight(t *testing.T) {
	textColumn := GridColumn{Name: "Customer"}
	numberColumn := GridColumn{Name: "Total", Numeric: true}
	for _, selected := range []bool{false, true} {
		if got := gridColumnStyle(textColumn, selected).GetAlignHorizontal(); got != lipgloss.Left {
			t.Fatalf("text alignment selected=%v = %v, want left", selected, got)
		}
		if got := gridColumnStyle(numberColumn, selected).GetAlignHorizontal(); got != lipgloss.Right {
			t.Fatalf("numeric alignment selected=%v = %v, want right", selected, got)
		}
	}
}

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
	if got := g.model.Rows[0][0]; got != "Zulu" {
		t.Fatalf("Enter changed sort order to %q", got)
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Text: "s"})
	if got := g.model.Rows[0][0]; got != "Alpha" {
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
	if start, end := g.table.VisibleIndices(); start != 0 || end != 9 {
		t.Fatalf("initial visible range = %d-%d, want 0-9", start, end)
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyUp})
	if g.table.Cursor() != 0 {
		t.Fatalf("cursor wrapped at first row: %d", g.table.Cursor())
	}
	for i := 0; i < len(rows)-1; i++ {
		_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if g.table.Cursor() != len(rows)-1 {
		t.Fatalf("cursor = %d, want last row", g.table.Cursor())
	}
	if start, end := g.table.VisibleIndices(); start != 20 || end != 24 {
		t.Fatalf("last visible range = %d-%d, want 20-24", start, end)
	}
	if footer := g.footer(); !strings.Contains(footer, "Rows 21–25 of 25 returned") {
		t.Fatalf("last-page footer = %q", footer)
	}
	_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyDown})
	if g.table.Cursor() != len(rows)-1 {
		t.Fatalf("cursor wrapped at last row: %d", g.table.Cursor())
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
		first, last := g.visibleColumnRange()
		if g.selectedColumn+1 < first || g.selectedColumn+1 > last {
			t.Fatalf("step %d selected column %d outside visible range %d-%d", i+1, g.selectedColumn+1, first, last)
		}
	}
	if g.selectedColumn != 3 || g.table.ColumnOffset() == 0 {
		t.Fatalf("selected column/offset = %d/%d, want selected last column and horizontal offset", g.selectedColumn, g.table.ColumnOffset())
	}
	if g.table.ColumnOffset() != 1 {
		t.Fatalf("horizontal offset = %d, want exact-fit offset 1", g.table.ColumnOffset())
	}
	if first, last := g.visibleColumnRange(); first != 2 || last != 4 {
		t.Fatalf("visible columns = %d-%d, want exact rendered range 2-4", first, last)
	}
	plainTable := ansi.Strip(g.table.View())
	for _, visible := range []string{"Second", "Third", "Fourth", "two", "three", "four"} {
		if !strings.Contains(plainTable, visible) {
			t.Fatalf("reported visible value %q is absent from rendered table:\n%s", visible, plainTable)
		}
	}
	if footer := g.footer(); !strings.Contains(footer, "Cols 2–4 of 4") {
		t.Fatalf("footer does not match rendered columns: %q", footer)
	}
	for i := 0; i < 3; i++ {
		_, _ = u.updateGrid(tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	if g.selectedColumn != 0 || g.table.ColumnOffset() != 0 {
		t.Fatalf("left navigation selected column/offset = %d/%d", g.selectedColumn, g.table.ColumnOffset())
	}
}

func TestGridNarrowMarkerOnlyViewReportsNoVisibleColumns(t *testing.T) {
	grid := GridModel{
		Columns: []GridColumn{{Name: "First"}, {Name: "Second"}},
		Rows:    [][]string{{"one", "two"}},
	}
	g := newGridState(grid, "Narrow", 2)
	g.selectedColumn = 1
	g.ensureSelectedColumnVisible()
	if first, last := g.visibleColumnRange(); first != 0 || last != 0 {
		t.Fatalf("marker-only visible range = %d-%d, want 0-0", first, last)
	}
	if strings.Contains(g.footer(), "Cols ") {
		t.Fatalf("marker-only footer invented visible columns: %q", g.footer())
	}
}

func TestGridNarrowViewCanRevealSelectedFinalColumn(t *testing.T) {
	grid := GridModel{
		Columns: []GridColumn{{Name: "First"}, {Name: "Second"}},
		Rows:    [][]string{{"one", "two"}},
	}
	// The 8-cell table initially has room only for the right overflow marker:
	// a non-final six-cell column also needs its divider. At offset 1, however,
	// the left marker plus the divider-free final column fit exactly.
	g := newGridState(grid, "Narrow", 10)
	g.selectedColumn = 1
	g.ensureSelectedColumnVisible()
	if g.table.ColumnOffset() != 1 {
		t.Fatalf("horizontal offset = %d, want final-column offset 1", g.table.ColumnOffset())
	}
	if first, last := g.visibleColumnRange(); first != 2 || last != 2 {
		t.Fatalf("visible range = %d-%d, want selected final column 2-2", first, last)
	}
	plainTable := ansi.Strip(g.table.View())
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
	row := g.table.Cursor()
	u.width = 70
	u.rebuildHistory(false)
	if !g.table.Focused() || g.table.Cursor() != row {
		t.Fatalf("focus/cursor after resize = %v/%d, want true/%d", g.table.Focused(), g.table.Cursor(), row)
	}
	first, last := g.visibleColumnRange()
	if g.selectedColumn+1 < first || g.selectedColumn+1 > last {
		t.Fatalf("selected column %d is outside visible range %d-%d after resize", g.selectedColumn+1, first, last)
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
		"focused prompt":      styles.Focused.Prompt,
		"focused placeholder": styles.Focused.Placeholder,
		"focused text":        styles.Focused.Text,
		"blurred prompt":      styles.Blurred.Prompt,
		"blurred placeholder": styles.Blurred.Placeholder,
		"blurred text":        styles.Blurred.Text,
	} {
		if style.GetBackground() == nil {
			t.Errorf("%s has no composer background", name)
		}
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
	if !strings.Contains(gridView, "Shift+↑↓ to navigate") || !strings.Contains(gridView, "Enter reserved") || !strings.Contains(gridView, "s sort") {
		t.Fatalf("grid status is not contextual: %s", gridView)
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

func TestGridTitleAndSelectedColumnUseDifferentColors(t *testing.T) {
	if activeTitleStyle.Render("same") == selectedCellStyle.Render("same") {
		t.Fatal("table title and selected column use the same style")
	}
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
}
