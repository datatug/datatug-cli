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

// TestExportDialogOverlayDefaultsToBucketScopeWhenNoGridFocused covers
// newExportDialogOverlay's ExportBucket-default-name branch.
func TestExportDialogOverlayDefaultsToBucketScopeWhenNoGridFocused(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.snapshot.Workspace.ExportBucket = []string{"rs1"}
	d := newExportDialogOverlay(u)
	if d.scope != 1 || d.name.Value() != "bucket" {
		t.Fatalf("scope=%d name=%q, want bucket scope defaulted", d.scope, d.name.Value())
	}
}

// TestExportDialogOverlayTabCyclesFocusAndFocusesTextInputs covers
// Tab/Shift+Tab's focus cycling (including wraparound) and focusInput's
// dir/name Focus() calls.
func TestExportDialogOverlayTabCyclesFocusAndFocusesTextInputs(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	next, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if done || next.(*exportDialogOverlay).focus != 1 {
		t.Fatalf("expected Tab to advance focus to 1, got %d", next.(*exportDialogOverlay).focus)
	}
	d.focus = 0
	next, _, _ = d.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if next.(*exportDialogOverlay).focus != 5 {
		t.Fatalf("expected Shift+Tab to wrap focus to 5, got %d", next.(*exportDialogOverlay).focus)
	}
	d.focus = 2
	d.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if !d.dir.Focused() && d.focus == 3 && !d.name.Focused() {
		t.Fatalf("expected the newly-focused text input to receive Focus(): dir=%v name=%v focus=%d", d.dir.Focused(), d.name.Focused(), d.focus)
	}
}

// TestExportDialogOverlayCtrlCQuits covers the Ctrl+C branches in both the
// normal and directory-picker Update paths.
func TestExportDialogOverlayCtrlCQuits(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	_, cmd, done := d.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if done || cmd == nil {
		t.Fatal("expected Ctrl+C to return tea.Quit without closing")
	}

	d.dir.SetValue(t.TempDir())
	d.focus = 4
	next, openCmd, _ := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	d = next.(*exportDialogOverlay)
	if openCmd == nil || d.picker == nil {
		t.Fatal("directory picker did not open")
	}
	_, pickerCmd, done := d.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if done || pickerCmd == nil {
		t.Fatal("expected Ctrl+C to return tea.Quit while the picker is open")
	}
}

// TestExportDialogOverlayLeftRightCycleScopeAndFormat covers left/right on
// the scope (focus 0) and format (focus 1) fields, including wraparound.
func TestExportDialogOverlayLeftRightCycleScopeAndFormat(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	d.focus = 0
	d.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if d.scope != 1 {
		t.Fatalf("expected left to wrap scope to 1, got %d", d.scope)
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if d.scope != 0 {
		t.Fatalf("expected right to wrap scope back to 0, got %d", d.scope)
	}
	d.focus = 1
	before := d.format
	d.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if d.format == before {
		t.Fatal("expected right to advance the format index")
	}
	for range len(exportFormats) - 1 {
		d.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	}
	if d.format != before {
		t.Fatalf("expected format to wrap back to %d after a full cycle, got %d", before, d.format)
	}
}

// TestExportDialogOverlayEnterStepsThroughLeadingFields covers Enter's
// case 0/1/2/3 focus-advance branch (distinct from the format/dir/name
// typing paths).
func TestExportDialogOverlayEnterStepsThroughLeadingFields(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	for want := 1; want <= 3; want++ {
		next, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		d = next.(*exportDialogOverlay)
		if done || cmd != nil || d.focus != want {
			t.Fatalf("Enter step %d: focus=%d done=%v cmd=%v, want focus=%d", want, d.focus, done, cmd, want)
		}
	}
}

// TestExportDialogOverlaySaveValidationErrors covers save's remaining
// validation branches: an empty file name, a directory that fails to
// stat/isn't a directory, and an empty directory value.
func TestExportDialogOverlaySaveValidationErrors(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})

	d := newExportDialogOverlay(u)
	d.focus = 5
	d.name.SetValue("")
	if _, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); done || d.err == "" {
		t.Fatalf("expected an empty file name to be rejected, err=%q", d.err)
	}

	d = newExportDialogOverlay(u)
	d.focus = 5
	d.name.SetValue("out")
	d.dir.SetValue("")
	if _, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); done || d.err != "Choose a directory." {
		t.Fatalf("expected an empty directory to be rejected, err=%q", d.err)
	}

	d = newExportDialogOverlay(u)
	d.focus = 5
	d.name.SetValue("out")
	d.dir.SetValue(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); done || d.err != "Choose an existing directory." {
		t.Fatalf("expected a nonexistent directory to be rejected, err=%q", d.err)
	}

	// The same "existing directory" check gates opening the picker itself
	// (focus 4), not just Export (focus 5).
	d = newExportDialogOverlay(u)
	d.focus = 4
	d.dir.SetValue(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); done || d.err != "Choose an existing directory." {
		t.Fatalf("expected the directory picker to reject a nonexistent directory, err=%q", d.err)
	}
}

// TestExportDialogOverlayTextInputsAcceptTyping covers the dir/name
// textinput.Update passthrough (focus 2/3, not matched by any of the
// special-cased keys above).
func TestExportDialogOverlayTextInputsAcceptTyping(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	d.focus = 2
	d.dir.SetValue("")
	d.dir.Focus()
	d.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if d.dir.Value() != "x" {
		t.Fatalf("directory field did not accept typing: %q", d.dir.Value())
	}
	d.focus = 3
	d.name.SetValue("")
	d.name.Focus()
	d.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if d.name.Value() != "y" {
		t.Fatalf("name field did not accept typing: %q", d.name.Value())
	}
}

// TestExportDialogOverlayPickerEnterOnFileDoesNotSelect covers the
// directory-picker branch where Enter lands on a non-directory path (the
// os.Stat/IsDir guard inside the picker's Update case): it must not close
// the picker or change d.dir.
func TestExportDialogOverlayPickerEnterOnFileDoesNotSelect(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newExportDialogOverlay(u)
	d.dir.SetValue(dir)
	d.focus = 4
	next, cmd, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	d = next.(*exportDialogOverlay)
	if done || cmd == nil || d.picker == nil {
		t.Fatal("directory picker did not open")
	}
	drainCmd(t, u, cmd)
	before := d.dir.Value()
	d.picker.Path = file
	next, _, done = d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	d = next.(*exportDialogOverlay)
	if done || d.picker == nil || d.dir.Value() != before {
		t.Fatalf("Enter on a file should not select it: picker=%v dir=%q, want unchanged %q", d.picker, d.dir.Value(), before)
	}
}

// TestExportDialogOverlayPickerEscCloses covers the directory picker's own
// Esc branch (distinct from the outer dialog's Esc, which closes the whole
// overlay).
func TestExportDialogOverlayPickerEscCloses(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	d.dir.SetValue(t.TempDir())
	d.focus = 4
	next, cmd, _ := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	d = next.(*exportDialogOverlay)
	if cmd == nil || d.picker == nil {
		t.Fatal("directory picker did not open")
	}
	next, _, done := d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	d = next.(*exportDialogOverlay)
	if done || d.picker != nil {
		t.Fatal("expected Esc to close the picker without closing the dialog")
	}
}

// TestExportDialogOverlayViewRendersPickerAndError covers View's picker and
// error-message rendering branches.
func TestExportDialogOverlayViewRendersPickerAndError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	d := newExportDialogOverlay(u)
	d.err = "boom"
	view := d.View(70, 20)
	if !strings.Contains(view, "boom") {
		t.Fatalf("expected the error message in the view:\n%s", view)
	}
	d.dir.SetValue(t.TempDir())
	d.focus = 4
	_, cmd, _ := d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	drainCmd(t, u, cmd)
	view = d.View(70, 20)
	if !strings.Contains(view, "Directory: ") || !strings.Contains(view, "Space current") {
		t.Fatalf("expected the directory picker in the view:\n%s", view)
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
