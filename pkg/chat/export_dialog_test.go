package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestExportDialogWritesCurrentRecordSet(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.snapshot.RecordSets = map[string]RecordSet{"record-1": exportFixture("Invoices")}
	u.entries = []historyEntry{{recordSetID: "record-1", grid: &gridState{title: "Invoices"}}}
	u.activeGrid = 0
	u.openExportDialog()
	if u.exportDialog == nil || u.exportDialog.name.Value() != "Invoices" {
		t.Fatal("dialog did not open for current RecordSet")
	}
	if view := u.View().Content; !strings.Contains(view, "Export RecordSet") || !strings.Contains(view, "Browse directories") {
		t.Fatal("export dialog is not visible")
	}
	u.exportDialog.dir.SetValue(t.TempDir())
	u.exportDialog.format = 1 // CSV
	u.exportDialog.focus = 5
	cmd := u.updateExportDialog(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || u.exportDialog != nil {
		t.Fatal("export did not start or dialog remained open")
	}
	message, ok := cmd().(exportMessage)
	if !ok || message.err != nil {
		t.Fatalf("export failed: %#v", message)
	}
	data, err := os.ReadFile(filepath.Join(message.path))
	if err != nil || !strings.Contains(string(data), "Czech Republic") {
		t.Fatalf("exported file unavailable: %v", err)
	}
}

func TestExportDialogValidationAndCancel(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.openExportDialog()
	d := u.exportDialog
	d.focus = 5
	d.name.SetValue("../outside")
	d.dir.SetValue(t.TempDir())
	if cmd := u.updateExportDialog(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || d.err == "" {
		t.Fatal("unsafe file name should be rejected in dialog")
	}
	if u.exportDialog == nil {
		t.Fatal("validation should keep dialog open")
	}
	u.updateExportDialog(tea.KeyPressMsg{Code: tea.KeyEscape})
	if u.exportDialog != nil {
		t.Fatal("escape should close dialog")
	}
}

func TestExportDialogDirectoryPicker(t *testing.T) {
	u := NewUI(context.Background(), nil, "test")
	u.openExportDialog()
	d := u.exportDialog
	d.dir.SetValue(t.TempDir())
	d.focus = 4
	if cmd := u.updateExportDialog(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd == nil || d.picker == nil {
		t.Fatal("directory picker did not open")
	}
	u.updateExportDialog(tea.KeyPressMsg{Code: tea.KeySpace})
	if d.picker != nil || d.dir.Value() == "" {
		t.Fatal("current directory was not selected")
	}
}
