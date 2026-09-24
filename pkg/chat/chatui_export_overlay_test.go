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

func indexOf(values []string, target string) int {
	for i, v := range values {
		if v == target {
			return i
		}
	}
	return 0
}
