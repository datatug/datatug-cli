package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestChatUISlashExportNoArgsOpensDialog(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/export"))
	view := u.shell.View().Content
	if !strings.Contains(view, "Export RecordSet") {
		t.Fatalf("expected export dialog overlay in view:\n%s", view)
	}
}

func TestExportDialogOverlayEscCloses(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	_, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done {
		t.Fatal("expected Esc to close the export dialog")
	}
}

func TestExportDialogOverlayWritesCurrentRecordSet(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	// exportCommand only reads ChatUI's own bookkeeping (lastGridRecordSetID,
	// snapshot.RecordSets), set directly here rather than round-tripping
	// through a stub conversation that (like contextualStub) never calls the
	// store-persisting query observer a live agent turn goes through.
	u.lastGridRecordSetID = "rs1"
	u.snapshot.RecordSets = map[string]RecordSet{"rs1": {ID: "rs1", Title: "Customers", Result: secureread.Result{
		Columns: []string{"CustomerId", "City"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1, "City": "Prague"}}},
	}}}

	dir := t.TempDir()
	d := newExportDialogOverlay(u)
	d.dir.SetValue(dir)
	d.name.SetValue("out")
	d.format = indexOf(exportFormats, "csv")
	d.focus = 5
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatalf("expected Enter on Export to close the dialog (matching ui.go's exportDialog = nil on success); dialog error: %q", d.err)
	}
	if cmd == nil {
		t.Fatalf("expected Enter on Export to return a write command; dialog error: %q", d.err)
	}
	msg := cmd()
	done2, ok := msg.(chatExportDoneMsg)
	if !ok {
		t.Fatalf("expected chatExportDoneMsg, got %T", msg)
	}
	if done2.err != nil {
		t.Fatalf("export failed: %v", done2.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.csv")); err != nil {
		t.Fatalf("expected exported file: %v", err)
	}
}

// Ported from the legacy UI's export_dialog_test.go
// (TestExportDialogValidationAndCancel): an unsafe file name is rejected in
// place, and Esc still closes the (still-open) dialog.
func TestExportDialogOverlayValidationAndCancel(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	d.focus = 5
	d.name.SetValue("../outside")
	d.dir.SetValue(t.TempDir())
	next, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || done || next.(*exportDialogOverlay).err == "" {
		t.Fatal("unsafe file name should be rejected in dialog")
	}
	_, _, done = next.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done {
		t.Fatal("escape should close dialog")
	}
}

// Ported from the legacy UI's export_dialog_test.go
// (TestExportDialogDirectoryPicker).
func TestExportDialogOverlayDirectoryPicker(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	d.dir.SetValue(t.TempDir())
	d.focus = 4
	next, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	overlay := next.(*exportDialogOverlay)
	if cmd == nil || done || overlay.picker == nil {
		t.Fatal("directory picker did not open")
	}
	next, _, done = overlay.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	overlay = next.(*exportDialogOverlay)
	if done || overlay.picker != nil || overlay.dir.Value() == "" {
		t.Fatal("current directory was not selected")
	}
}

// TestExportDialogOverlayDefaultNameExcludesVersionBadge is the regression
// test for M2, ported from the legacy UI's export_dialog_test.go: a version
// badge ("changed · "/"unchanged · ") prefixed onto the grid's visible
// header title (setVersionBadge/SetTitle) must never leak into an export's
// default filename — newExportDialogOverlay reads RecordSet.Title, not the
// grid's badge-decorated Title().
func TestExportDialogOverlayDefaultNameExcludesVersionBadge(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.lastGridRecordSetID = "record-1"
	u.snapshot.RecordSets = map[string]RecordSet{"record-1": exportFixture("Invoices")}
	g := newGridState(GridModel{}, "", "Invoices", 80)
	g.setVersionBadge("changed")
	if title := g.Title(); !strings.Contains(title, "changed") {
		t.Fatalf("setup: grid Title() = %q, want it to contain the badge", title)
	}
	d := newExportDialogOverlay(u)
	if d.name.Value() != "Invoices" {
		t.Fatalf("export default filename = %q, want %q (no version badge)", d.name.Value(), "Invoices")
	}
}

func indexOf(values []string, target string) int {
	for i, v := range values {
		if v == target {
			return i
		}
	}
	return 0
}
