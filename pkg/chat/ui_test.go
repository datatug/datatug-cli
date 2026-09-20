package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestUIRendersStructuredResultAsBubblesTable(t *testing.T) {
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

func TestUIViewLeavesMouseAvailableForTerminalSelection(t *testing.T) {
	u := NewUI(context.Background(), nil, "fake-model")
	if got := u.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("mouse mode = %v, want MouseModeNone so the terminal can select text", got)
	}
}

func TestVisibleColumnsWindowsWideResults(t *testing.T) {
	grid := GridModel{Columns: []GridColumn{{Name: "First"}, {Name: "Second"}, {Name: "Third"}}, Rows: [][]string{{"aaaaaaaa", "bbbbbbbb", "cccccccc"}}}
	columns, indexes := visibleColumns(grid, 1, 14)
	if len(columns) != 1 || len(indexes) != 1 || indexes[0] != 1 {
		t.Fatalf("visible columns = %+v, indexes = %+v", columns, indexes)
	}
}
