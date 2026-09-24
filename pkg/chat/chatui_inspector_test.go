package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestChatUIEnterOpensAndClosesCellDetail is ported from the legacy UI's
// cell_detail_test.go (TestEnterOpensAndClosesCellDetailWithoutChangingSelection):
// same scenario (Enter on a focused grid opens cell detail showing the
// selected cell and its row, Esc returns to the grid), driven through
// ChatUI's real wiring (Submit -> transcript grid -> FocusEntry ->
// handleGridKey's "enter" case -> cellDetailOverlay) instead of
// u.updateGrid/u.detail/u.gridFocused.
func TestChatUIEnterOpensAndClosesCellDetail(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{Title: "Invoices", RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"InvoiceId", "CustomerId"},
		Rows:    []secureread.Row{{Data: map[string]any{"InvoiceId": 7, "CustomerId": 1}}},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show invoices"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}

	_, cmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	view := u.shell.View().Content
	if !strings.Contains(view, "Cell · InvoiceId") || !strings.Contains(view, "CustomerId: 1") {
		t.Fatalf("dialog missing row or cell:\n%s", view)
	}

	_, escCmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if escCmd != nil {
		t.Fatalf("expected no command from closing the cell detail overlay, got %v", escCmd)
	}
	if strings.Contains(u.shell.View().Content, "Cell · InvoiceId") {
		t.Fatal("Esc did not close the cell detail overlay")
	}
}

// TestChatUIGridSortRetainsSelectedSourceRowForInspector is ported from
// ui_test.go's TestGridSortRetainsSelectedSourceRowForInspector: pressing
// "s" on a focused transcript grid sorts by the selected column while
// keeping the SAME underlying row highlighted (handleGridKey's "s" case,
// chatui_inspector.go) — grid.Model.Sort alone does not do this (it always
// leaves the display cursor at position 0), so this is real, DataTug-owned
// behavior, not native grid mechanics.
func TestChatUIGridSortRetainsSelectedSourceRowForInspector(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{RecordSetID: "recordset", Result: secureread.Result{
		Columns: []string{"Name"},
		Rows: []secureread.Row{
			{Data: map[string]any{"Name": "Zulu"}},
			{Data: map[string]any{"Name": "Alpha"}},
			{Data: map[string]any{"Name": "Zulu"}},
		},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show names"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("expected grid focus")
	}
	g := u.gridsByRecordSetID[u.lastGridRecordSetID]
	if g == nil {
		t.Fatal("grid not tracked by recordset ID")
	}
	if before := g.selectedSourceRow(); before != 0 {
		t.Fatalf("source row before sort = %d, want 0", before)
	}
	u.shell.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	if got := g.selectedSourceRow(); got != 0 || g.CurrentIndex() != 1 || g.sourceIndexAt(2) != 2 {
		t.Fatalf("sorted selection = source:%d index:%d, want source:0 index:1", got, g.CurrentIndex())
	}
}
