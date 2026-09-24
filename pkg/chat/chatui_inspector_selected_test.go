package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestChatUISelectedTabInspectsFocusedGridRow/Column/RecordSet are ported
// from the legacy UI's inspector_ui.go (inspectorWorkspaceView,
// currentRowDetails/currentColumnDetails/currentRecordsetDetails,
// columnMeta): the "Selected" workspace tab (workspacePanel.tab == 1) is
// itself the whole Inspector — old ui.go never had a separate top-level
// "Inspector" tab, just this one with its own "1 Current row / 2 Current
// column / 3 Current recordset" sub-tabs (inspectorSubTab), switched with
// the 1/2/3 keys exactly like here. These drive it through ChatUI's real
// wiring (Submit -> transcript grid -> FocusEntry -> workspacePanel) instead
// of u.updateWorkspaceKey/u.workspaceTab.
func TestChatUISelectedTabInspectsFocusedGridRowColumnRecordSet(t *testing.T) {
	dtql := "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n"
	turn := Turn{Queries: []QueryResult{{
		Title:       "Invoices",
		DTQL:        dtql,
		RecordSetID: "rs1",
		Result: secureread.Result{
			Columns: []string{"InvoiceId"},
			Rows:    []secureread.Row{{Data: map[string]any{"InvoiceId": 7}}},
		},
	}}}
	u, _ := newTestChatUI(t, nil, turn)
	u.catalog = ProjectCatalog{ID: "proj1", Title: "Demo", Objects: []ProjectObject{
		{
			Reference:   ContextReference{Kind: "table", ObjectID: "main.invoice", Title: "Invoice"},
			Columns:     []string{"InvoiceId"},
			ColumnTypes: map[string]string{"InvoiceId": "INTEGER"},
		},
	}}
	drainCmd(t, u, u.Submit("Show invoices"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid unavailable to focus")
	}
	u.workspace.tab = 1

	// Sub-tab 0 (default): current row.
	if u.workspace.inspectorSubTab != 0 {
		t.Fatalf("expected default inspectorSubTab 0, got %d", u.workspace.inspectorSubTab)
	}
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "Row 1 of 1") || !strings.Contains(view, "InvoiceId") || !strings.Contains(view, "INTEGER") {
		t.Fatalf("expected row details with catalog type in view:\n%s", view)
	}
	if !strings.Contains(view, "● 1 Current row") {
		t.Fatalf("expected sub-tab header to mark row selected:\n%s", view)
	}

	// "2" switches to current column.
	u.workspace.updateKey(tea.KeyPressMsg{Code: '2', Text: "2"})
	if u.workspace.inspectorSubTab != 1 {
		t.Fatalf("expected inspectorSubTab 1 after pressing 2, got %d", u.workspace.inspectorSubTab)
	}
	view = u.workspace.View(80, 20, true)
	if !strings.Contains(view, "Column: InvoiceId") || !strings.Contains(view, "Source: main.invoice.InvoiceId") || !strings.Contains(view, "Type: INTEGER") {
		t.Fatalf("expected column details in view:\n%s", view)
	}
	if !strings.Contains(view, "No available JOIN candidate for this column.") {
		t.Fatalf("expected no-FK-candidate message in view:\n%s", view)
	}

	// "3" switches to current recordset.
	u.workspace.updateKey(tea.KeyPressMsg{Code: '3', Text: "3"})
	if u.workspace.inspectorSubTab != 2 {
		t.Fatalf("expected inspectorSubTab 2 after pressing 3, got %d", u.workspace.inspectorSubTab)
	}
	view = u.workspace.View(80, 20, true)
	if !strings.Contains(view, "1 rows") || !strings.Contains(view, "1 columns") || !strings.Contains(view, "main.invoice.InvoiceId") || !strings.Contains(view, "INTEGER") {
		t.Fatalf("expected recordset details in view:\n%s", view)
	}
	if !strings.Contains(view, "Constraints require full schema metadata.") {
		t.Fatalf("expected recordset constraints footer in view:\n%s", view)
	}

	// "1" switches back to current row.
	u.workspace.updateKey(tea.KeyPressMsg{Code: '1', Text: "1"})
	if u.workspace.inspectorSubTab != 0 {
		t.Fatalf("expected inspectorSubTab 0 after pressing 1, got %d", u.workspace.inspectorSubTab)
	}
}

// TestChatUISelectedTabColumnDetailsWithoutFocusedGrid is the empty-state
// half of currentColumnDetails, ui.go's "Focus a result grid..." message.
func TestChatUISelectedTabColumnDetailsWithoutFocusedGrid(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 1
	u.workspace.inspectorSubTab = 1
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "Focus a result grid to inspect its current column.") {
		t.Fatalf("expected empty-state column message:\n%s", view)
	}
}

// TestChatUISelectedTabRecordSetDetailsWithoutFocusedGrid is the empty-state
// half of currentRecordsetDetails.
func TestChatUISelectedTabRecordSetDetailsWithoutFocusedGrid(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 1
	u.workspace.inspectorSubTab = 2
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "Focus a result grid to inspect its RecordSet.") {
		t.Fatalf("expected empty-state recordset message:\n%s", view)
	}
}

// TestChatUISelectedTabUpDownScrollsInspectorOffset is ui.go's
// u.inspectorOffset up/down handling (updateWorkspaceKey's "up"/"down"
// case 1), ported onto workspacePanel.inspectorOffset.
func TestChatUISelectedTabUpDownScrollsInspectorOffset(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 1
	u.workspace.inspectorOffset = 3
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if u.workspace.inspectorOffset != 2 {
		t.Fatalf("expected up/k to decrement inspectorOffset to 2, got %d", u.workspace.inspectorOffset)
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if u.workspace.inspectorOffset != 3 {
		t.Fatalf("expected down/j to increment inspectorOffset to 3, got %d", u.workspace.inspectorOffset)
	}
}

// TestChatUISelectedTabRowDetailsFallsBackToSelectedDetails is
// currentRowDetails' fallback when no grid is focused (or its cursor is out
// of range): ui.go returned u.selectedDetails instead.
func TestChatUISelectedTabRowDetailsFallsBackToSelectedDetails(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 1
	view := u.workspace.View(80, 20, true)
	if !strings.Contains(view, "No durable selection") {
		t.Fatalf("expected selectedDetails fallback in row sub-tab view:\n%s", view)
	}
}
