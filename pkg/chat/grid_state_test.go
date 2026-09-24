package chat

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/grid"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// The tests in this file are ported from the legacy UI's ui_test.go: they
// exercise gridState/grid.Model rendering and navigation directly — no
// UI/ChatUI type needed even before this migration (ui_test.go's own
// versions only used NewUI/appendTurn as a roundabout way to build a grid).
// Ported by building the grid directly via newGridState and driving it with
// grid.Model.Update instead of NewUI/u.appendTurn/u.updateGrid; assertions
// are unchanged.

func TestGridKeepsFocusAndCursorAcrossHorizontalNavigation(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{
		Columns: []string{"First", "Second"},
		Rows: []secureread.Row{
			{Data: map[string]any{"First": "a", "Second": "1"}},
			{Data: map[string]any{"First": "b", "Second": "2"}},
		},
	}), "", "Rows", contentWidth(80))
	g.SetFocused(true)
	g.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !g.Focused() {
		t.Fatal("table lost focus after horizontal rebuild")
	}
	g.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if g.CurrentIndex() != 1 {
		t.Fatalf("cursor = %d, want 1", g.CurrentIndex())
	}
}

func TestGridTitleFooterAndScrollbarAreStructuredPresentation(t *testing.T) {
	rows := make([]secureread.Row, 20)
	for i := range rows {
		rows[i] = secureread.Row{Data: map[string]any{"ID": i + 1}}
	}
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID"}, Rows: rows}), "", "  Latest   orders\n", contentWidth(60))
	view := g.view()
	if !strings.Contains(view, "Latest orders") || !strings.Contains(view, "Rows 1–10 of 20 returned") || !strings.Contains(view, "▐") {
		t.Fatalf("grid presentation missing title/footer/scrollbar:\n%s", view)
	}
	if !strings.Contains(view, "○") {
		t.Fatalf("inactive grid marker missing:\n%s", view)
	}
	g.SetFocused(true)
	if !strings.Contains(g.view(), "●") {
		t.Fatalf("active grid marker missing:\n%s", g.view())
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
	rows := make([]secureread.Row, 25)
	for i := range rows {
		rows[i] = secureread.Row{Data: map[string]any{"ID": i + 1}}
	}
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID"}, Rows: rows}), "", "Rows", contentWidth(80))
	g.SetFocused(true)
	if view := g.view(); !strings.Contains(view, "▐") {
		t.Fatalf("top page has no scrollbar thumb:\n%s", view)
	}
	topLines := strings.Split(ansi.Strip(g.view()), "\n")
	if len(topLines) < 3 || !strings.HasSuffix(strings.TrimSpace(topLines[1]), "▐") || !strings.HasSuffix(strings.TrimSpace(topLines[2]), "▐") {
		t.Fatalf("scrollbar is not on the right card edge:\n%s", g.view())
	}
	g.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if view := g.view(); !strings.Contains(view, "▐") {
		t.Fatalf("middle page has no scrollbar thumb:\n%s", view)
	}
	g.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if start, end := g.VisibleIndices(); start != 20 || end != 24 {
		t.Fatalf("final page range = %d-%d, want 20-24", start, end)
	}
	if view := g.view(); !strings.Contains(view, "▐") || !strings.Contains(view, "Rows 21–25 of 25 returned") {
		t.Fatalf("short final page has incorrect scrollbar/footer:\n%s", view)
	}
}

func TestGridEnterIsReservedAndSSorts(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{
		Columns: []string{"Name"},
		Rows: []secureread.Row{
			{Data: map[string]any{"Name": "Zulu"}},
			{Data: map[string]any{"Name": "Alpha"}},
		},
	}), "", "Rows", contentWidth(80))
	g.SetFocused(true)
	g.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := g.Cell(0, 0); got != "Zulu" {
		t.Fatalf("Enter changed sort order to %q", got)
	}
	g.Update(tea.KeyPressMsg{Text: "s"})
	if got := g.Cell(0, 0); got != "Alpha" {
		t.Fatalf("s did not sort ascending, got %q", got)
	}
}

func TestGridUsesBubbleTablePaginationAndDoesNotWrapAtBoundaries(t *testing.T) {
	rows := make([]secureread.Row, 25)
	for i := range rows {
		rows[i] = secureread.Row{Data: map[string]any{"ID": i + 1}}
	}
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID"}, Rows: rows}), "", "Rows", contentWidth(80))
	g.SetFocused(true)
	if start, end := g.VisibleIndices(); start != 0 || end != 9 {
		t.Fatalf("initial visible range = %d-%d, want 0-9", start, end)
	}
	g.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if g.CurrentIndex() != 0 {
		t.Fatalf("cursor wrapped at first row: %d", g.CurrentIndex())
	}
	for i := 0; i < len(rows)-1; i++ {
		g.Update(tea.KeyPressMsg{Code: tea.KeyDown})
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
	g.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if g.CurrentIndex() != len(rows)-1 {
		t.Fatalf("cursor wrapped at last row: %d", g.CurrentIndex())
	}
}

func TestGridHorizontalSelectionUsesBubbleTableOverflow(t *testing.T) {
	// contentWidth(26) is 24, giving bubble-table an exact 22-cell table:
	// after the left overflow marker, columns 2-4 fit only because the final
	// source column has no trailing divider.
	g := newGridState(NewGridModel(secureread.Result{
		Columns: []string{"First", "Second", "Third", "Fourth"},
		Rows:    []secureread.Row{{Data: map[string]any{"First": "one", "Second": "two", "Third": "three", "Fourth": "four"}}},
	}), "", "Rows", contentWidth(26))
	g.SetFocused(true)
	for i := 0; i < 3; i++ {
		g.Update(tea.KeyPressMsg{Code: tea.KeyRight})
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
		g.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
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
	g := newGridState(NewGridModel(secureread.Result{
		Columns: []string{"First", "Second", "Third", "Fourth"},
		Rows:    []secureread.Row{{Data: map[string]any{"First": "one", "Second": "two", "Third": "three", "Fourth": "four"}}, {Data: map[string]any{"First": "a", "Second": "b", "Third": "c", "Fourth": "d"}}},
	}), "", "Rows", contentWidth(26))
	g.SetFocused(true)
	g.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	g.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	g.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	row := g.CurrentIndex()
	g.SetWidth(contentWidth(70))
	if !g.Focused() || g.CurrentIndex() != row {
		t.Fatalf("focus/cursor after resize = %v/%d, want true/%d", g.Focused(), g.CurrentIndex(), row)
	}
	first, last := g.VisibleColumnRange()
	if g.SelectedColumn()+1 < first || g.SelectedColumn()+1 > last {
		t.Fatalf("selected column %d is outside visible range %d-%d after resize", g.SelectedColumn()+1, first, last)
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
	g := newGridState(NewGridModel(result), "", "Fields", contentWidth(180)*75/100)
	g.SetFocused(true)
	// CardView (grid.CardView, DataTug's "Current row" view) tracks its own
	// scroll offset internally, so this checks the rendered content instead
	// of an exported Y-offset: after scrolling down, Field00 is no longer
	// on screen; after the highlighted row changes, the next render resets
	// to the top (Field00 visible again).
	g.Update(tea.KeyPressMsg{Text: "3"})
	g.ToggleSecondaryFocusIfSplit()
	g.View(g.Width(), true)
	g.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if strings.Contains(g.ActiveViewContent(60, 10), "Field00") {
		t.Fatal("inspector did not scroll")
	}
	g.ToggleSecondaryFocusIfSplit()
	g.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !strings.Contains(g.ActiveViewContent(60, 10), "Field00") {
		t.Fatal("inspector offset after row change did not reset to top")
	}
}

func TestEmptyRecordsetCanBeFocusedAndShowsSecondaryEmptyStates(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{Columns: []string{"ID"}}), "", "Empty", contentWidth(80))
	g.SetFocused(true)
	g.Update(tea.KeyPressMsg{Text: "2"})
	if view := ansi.Strip(g.view()); !strings.Contains(view, "No chart candidates") {
		t.Fatalf("empty charts state missing:\n%s", view)
	}
	g.Update(tea.KeyPressMsg{Text: "3"})
	if view := ansi.Strip(g.view()); !strings.Contains(view, "No current row") {
		t.Fatalf("empty current-row state missing:\n%s", view)
	}
}

// TestGridSortRetainsSelectedSourceRowForInspector moved to
// chatui_inspector_test.go (TestChatUIGridSortRetainsSelectedSourceRowForInspector):
// preserving the highlighted row's identity across an "s" sort is
// DataTug's own handleMainGridKey/handleGridKey behavior (sourceRowKey +
// restoreByKey around grid.Model's own Sort, which does not preserve
// row identity by itself), not bare grid.Model behavior.

// TestGridTitleAndSelectedColumnUseDifferentColors is m12: this used to
// compare datatug's OWN now-dead style copies (selectedCellStyle,
// unreferenced by any real rendering since column-selection styling moved
// into the shared tui/grid package) rather than asserting anything about
// what's actually drawn on screen. It now renders a real grid and checks
// the title and the selected column's header carry visibly different
// ANSI styling.
func TestGridTitleAndSelectedColumnUseDifferentColors(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}), "", "Distinct Title", contentWidth(80))
	g.SetFocused(true)
	view := g.view()
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

func TestGridBorderChangesWithFocus(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{
		Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}},
	}), "", "Rows", contentWidth(80))
	inactive := strings.Split(g.view(), "\n")[0]
	g.SetFocused(true)
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

func TestRecordsetViewsRouteFocusAcrossSplitAndNarrowLayouts(t *testing.T) {
	g := newGridState(NewGridModel(secureread.Result{
		Columns: []string{"ID", "Country"},
		Rows:    []secureread.Row{{Data: map[string]any{"ID": 1, "Country": "Ireland"}}},
	}), "", "Customers", contentWidth(180)*75/100)
	g.SetFocused(true)
	g.charts = []ChartCandidate{
		{Spec: ChartSpec{Kind: ChartBar, Title: "By country", Points: []ChartPoint{{Label: "Ireland", Value: 1}}}},
		{Spec: ChartSpec{Kind: ChartBar, Title: "By ID", Points: []ChartPoint{{Label: "1", Value: 1}}}},
	}
	g.Update(tea.KeyPressMsg{Text: "2"})
	wideWidth := contentWidth(180) * 75 / 100
	if g.CurrentView() != gridViewCharts || !chooseGridLayout(wideWidth, g.NaturalWidth(), g.CurrentView()).Split || g.SecondaryFocus() {
		t.Fatalf("wide chart state = view:%v layout:%+v secondary:%v", g.CurrentView(), chooseGridLayout(wideWidth, g.NaturalWidth(), g.CurrentView()), g.SecondaryFocus())
	}
	g.ToggleSecondaryFocusIfSplit()
	g.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if !g.SecondaryFocus() || g.chartIndex != 1 {
		t.Fatalf("chart focus/index = %v/%d, want true/1", g.SecondaryFocus(), g.chartIndex)
	}
	g.Update(tea.KeyPressMsg{Text: "1"})
	if g.CurrentView() != grid.ViewTable || g.SecondaryFocus() {
		t.Fatalf("table return = view:%v secondary:%v", g.CurrentView(), g.SecondaryFocus())
	}
	g.Update(tea.KeyPressMsg{Text: "3"})
	if g.CurrentView() != gridViewCurrentRow {
		t.Fatalf("current-row view = %v", g.CurrentView())
	}
	narrowWidth := contentWidth(70)
	g.SetWidth(narrowWidth)
	if chooseGridLayout(narrowWidth, g.NaturalWidth(), g.CurrentView()).Split {
		t.Fatalf("narrow inspector layout = %+v, want unsplit", chooseGridLayout(narrowWidth, g.NaturalWidth(), g.CurrentView()))
	}
	if view := ansi.Strip(g.View(narrowWidth, true)); !strings.Contains(view, "3 Current row") || !strings.Contains(view, "Ireland") {
		t.Fatalf("narrow inspector did not render current row:\n%s", view)
	}
}

func TestUIWidthSweepKeepsRenderedLinesWithinTerminal(t *testing.T) {
	for width := 1; width <= 80; width++ {
		g := newGridState(NewGridModel(secureread.Result{
			Columns: []string{"First", "Second", "Third"},
			Rows:    []secureread.Row{{Data: map[string]any{"First": "one", "Second": "two", "Third": "three"}}},
		}), "", "A result title", contentWidth(width))
		for lineIndex, line := range strings.Split(ansi.Strip(g.View(contentWidth(width), false)), "\n") {
			if got := ansi.StringWidth(line); got > contentWidth(width) {
				t.Fatalf("width %d line %d rendered at %d cells: %q", width, lineIndex, got, line)
			}
		}
	}
}
