package chat

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func resultColumnsUIFixture(t *testing.T) (*ChatUI, *resultColumnsOverlay, *columnCountingExecutor) {
	t.Helper()
	c, parent, e, _ := resultColumnsFixture(t, resultColumnsSchema, resultColumnsDocument)
	u, err := NewSessionChatUI(context.Background(), c, "test")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 35})
	u.shell.FocusEntry(u.lastGridEntryID)
	g, r, ok := u.activeGrid()
	if !ok || r.ID != parent.ID {
		t.Fatal("no active grid")
	}
	o := &resultColumnsOverlay{ui: u, grid: g, record: *r, sessionID: u.sessionID, generation: u.workspaceGeneration, selected: map[string]bool{}}
	rows, err := c.ResultColumns(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = o.Update(resultColumnsLoadedMsg{overlay: o, rows: rows})
	return u, o, e
}

func TestResultColumnsCheckboxApplyReviewRunAndCancel(t *testing.T) {
	t.Parallel()
	u, o, e := resultColumnsUIFixture(t)
	for i, r := range o.rows {
		if r.Name == "Customer.FirstName" || r.Name == "Customer.LastName" {
			o.index = i
			o.Update(uitest.Key("space"))
		}
	}
	if len(o.ids()) != 2 || e.calls != 0 {
		t.Fatal("checkbox executed")
	}
	o.Update(uitest.Key("enter"))
	if o.preview == nil || e.calls != 0 {
		t.Fatal("Apply executed instead of review")
	}
	document := o.preview.DTQL
	view := ansi.Strip(o.View(70, 20))
	if !strings.Contains(view, "Review column query") || !strings.Contains(view, "Run on server") {
		t.Fatal(view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 70 {
			t.Fatal("dialog overflow")
		}
	}
	o.Update(uitest.Key("esc"))
	if o.preview != nil || len(o.ids()) != 2 || e.calls != 0 {
		t.Fatal("review cancel lost choices or executed")
	}
	o.Update(uitest.Key("enter"))
	_, cmd, done := o.Update(uitest.Key("shift+enter"))
	if !done || cmd == nil {
		t.Fatal("Run did not launch")
	}
	msg, ok := cmd().(workspaceRelatedOpenedMsg)
	if !ok || msg.err != nil || msg.query.DTQL != document || e.calls != 1 {
		t.Fatalf("execution %+v calls=%d", msg, e.calls)
	}
	u.shell.Update(msg)
}

func TestResultColumnsReviewViewPreservesExactDTQLLayout(t *testing.T) {
	t.Parallel()
	_, overlay, executor := resultColumnsUIFixture(t)
	for i, row := range overlay.rows {
		if row.Name == "Customer.FirstName" || row.Name == "Customer.LastName" {
			overlay.index = i
			overlay.Update(uitest.Key("space"))
		}
	}
	overlay.Update(uitest.Key("enter"))
	if overlay.preview == nil || executor.calls != 0 {
		t.Fatal("Apply did not open an execution-free review")
	}
	document := overlay.preview.DTQL
	view := ansi.Strip(overlay.View(100, 80))
	visibleLines := strings.Split(view, "\n")
	for i, line := range visibleLines {
		visibleLines[i] = strings.TrimRight(line, " ")
	}
	content := strings.Join(visibleLines, "\n")
	for _, fragment := range []string{
		"Add Customer.FirstName, Customer.LastName\n\n",
		"aggregate output.\n\nSecure source-bound execution",
		"Exact DTQL submitted:\nfrom:\n  name: Invoice\n  alias: i\n  joins:",
		"\ncolumns:\n  - field: CustomerId\n    source: i",
	} {
		if !strings.Contains(content, fragment) {
			t.Fatalf("review lost line breaks or indentation around %q:\n%s", fragment, view)
		}
	}
	if overlay.preview.DTQL != document || executor.calls != 0 {
		t.Fatal("rendering changed the submitted document or executed it")
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 100 {
			t.Fatalf("review overflow: %q", line)
		}
	}
}

func TestResultColumnsOverlayHelpFollowsCurrentStep(t *testing.T) {
	t.Parallel()
	_, overlay, _ := resultColumnsUIFixture(t)
	choices := overlay.HelpHints()
	for _, hint := range []string{"↑↓ choose", "Space checkbox", "Enter Apply and review", "Esc cancel"} {
		if !slices.Contains(choices, hint) {
			t.Fatalf("choice help missing %q: %v", hint, choices)
		}
	}
	for i, row := range overlay.rows {
		if row.Addable {
			overlay.index = i
			overlay.Update(uitest.Key("space"))
			break
		}
	}
	overlay.Update(uitest.Key("enter"))
	if overlay.preview == nil {
		t.Fatal("Apply did not open review")
	}
	review := overlay.HelpHints()
	for _, hint := range []string{"↑↓ / PgUp/PgDn scroll", "Shift+Enter Run reviewed query", "Esc choices"} {
		if !slices.Contains(review, hint) {
			t.Fatalf("review help missing %q: %v", hint, review)
		}
	}
	for _, hint := range []string{"↑↓ choose", "Space checkbox", "Enter Apply and review"} {
		if slices.Contains(review, hint) {
			t.Fatalf("review help advertises inactive control %q: %v", hint, review)
		}
	}
}

func TestResultColumnsStalePickerCannotExecuteAndEmptyCancel(t *testing.T) {
	t.Parallel()
	u, o, e := resultColumnsUIFixture(t)
	for i, r := range o.rows {
		if r.Addable {
			o.index = i
			o.Update(uitest.Key("space"))
			break
		}
	}
	o.Update(uitest.Key("enter"))
	u.workspaceGeneration++
	_, cmd, done := o.Update(uitest.Key("shift+enter"))
	if cmd != nil || done || e.calls != 0 {
		t.Fatal("stale preview executed")
	}
	_, cmd, done = o.Update(uitest.Key("esc"))
	if cmd != nil || done {
		t.Fatal("preview back failed")
	}
	_, cmd, done = o.Update(uitest.Key("esc"))
	if cmd != nil || !done || e.calls != 0 {
		t.Fatal("cancel executed")
	}
}

func TestResultColumnsOpenLoadsOnlyMetadataAndFitsSmallViewport(t *testing.T) {
	t.Parallel()
	u, _, e := resultColumnsUIFixture(t)
	cmd := u.openResultColumns()
	if cmd == nil {
		t.Fatal("picker not opened")
	}
	msg, ok := cmd().(resultColumnsLoadedMsg)
	if !ok || msg.err != nil || e.calls != 0 {
		t.Fatalf("metadata load %+v", msg)
	}
	o := msg.overlay
	o.Update(msg)
	for _, size := range [][2]int{{30, 8}, {15, 3}, {70, 20}} {
		view := o.View(size[0], size[1])
		if strings.Count(view, "\n")+1 > size[1] {
			t.Fatal("picker height overflow")
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("picker width overflow")
			}
		}
	}
	for i, r := range o.rows {
		if r.Addable {
			o.toggle(i)
			break
		}
	}
	o.Update(uitest.Key("enter"))
	for _, size := range [][2]int{{30, 8}, {15, 3}, {70, 20}} {
		view := o.View(size[0], size[1])
		if strings.Count(view, "\n")+1 > size[1] {
			t.Fatal("review height overflow")
		}
	}
	if e.calls != 0 {
		t.Fatal("view ran query")
	}
}
