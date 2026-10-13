package chat

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func TestWorkspaceActionsMouseRenderedRows(t *testing.T) {
	u, _ := uxUI(t)
	inspectIndex := -1
	for i, action := range u.workspaceActions() {
		if action.id == "inspect" {
			inspectIndex = i
			break
		}
	}
	if inspectIndex < 1 || u.workspaceActions()[inspectIndex-1].id != "explorer" {
		t.Fatal("Explorer and Inspect actions unavailable")
	}
	for _, selected := range []int{0, inspectIndex} {
		for _, point := range []struct {
			name string
			x, y int
		}{
			{"header", 2, 0}, {"search", 2, 1},
			{"footer", 2, 3}, {"blank", 2, 4}, {"below", 2, 6},
			{"left", -1, 2}, {"right", 40, 2}, {"above", 2, -1},
		} {
			t.Run(fmt.Sprintf("selection_%d/%s", selected, point.name), func(t *testing.T) {
				o := &workspaceActionsOverlay{ui: u, index: selected, message: "old error"}
				o.View(40, 5) // One visible action, followed by reason/message.
				before := o.index
				_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: point.x, Y: point.y})
				if cmd != nil || done || o.index != before || o.message != "old error" {
					t.Fatal("click outside rendered action changed selection or ran an action")
				}
			})
		}
	}
	t.Run("visible scrolled action", func(t *testing.T) {
		o := &workspaceActionsOverlay{ui: u, index: inspectIndex}
		view := o.View(40, 5)
		if !strings.Contains(view, "Toggle Inspect") {
			t.Fatal(view)
		}
		_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 2})
		if !done || cmd == nil || cmd().(workspaceActionMsg).id != "inspect" {
			t.Fatal("visible action did not run")
		}
	})
	t.Run("visible disabled action", func(t *testing.T) {
		o := &workspaceActionsOverlay{ui: u, search: "cancel", message: "old error"}
		o.View(80, 5)
		_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 2})
		if done || cmd != nil || o.message != "No running server operation" {
			t.Fatal("disabled action lost its reason or executed")
		}
	})
	t.Run("visible unselected row", func(t *testing.T) {
		o := &workspaceActionsOverlay{ui: u, index: inspectIndex, message: "old error"}
		o.View(80, 6)
		_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 2})
		if !done || cmd == nil || cmd().(workspaceActionMsg).id != "explorer" || o.message != "" {
			t.Fatal("visible row did not select the displayed action or retained error")
		}
	})
	t.Run("resize discards old rows", func(t *testing.T) {
		o := &workspaceActionsOverlay{ui: u, index: inspectIndex}
		o.View(80, 15)
		o.View(20, 5)
		_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 3})
		if done || cmd != nil || o.index != inspectIndex {
			t.Fatal("resized dialog retained a hidden clickable row")
		}
	})
	for _, key := range []string{"down", "up", "x", "backspace"} {
		t.Run("before redraw/"+key, func(t *testing.T) {
			o := &workspaceActionsOverlay{ui: u, index: inspectIndex}
			o.View(40, 5)
			o.Update(uitest.Key(key))
			before := o.index
			_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 2})
			if done || cmd != nil || o.index != before {
				t.Fatal("stale rendered row ran after selection/filter changed")
			}
		})
	}
	t.Run("before first render", func(t *testing.T) {
		o := &workspaceActionsOverlay{ui: u}
		_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 2})
		if done || cmd != nil || o.message != "" {
			t.Fatal("unrendered dialog accepted click")
		}
	})
}

func TestWorkspaceTrailMouseRenderedRows(t *testing.T) {
	u, _ := uxUI(t)
	ids := make([]string, 10)
	for i := range ids {
		ids[i] = fmt.Sprintf("trail-%d", i)
		u.trailNodes[ids[i]] = &explorationNode{id: ids[i], label: ids[i]}
	}
	for _, selected := range []int{0, 6} {
		for _, point := range []struct {
			name string
			x, y int
		}{
			{"header", 2, 0}, {"footer", 2, 2}, {"blank", 2, 3},
			{"left", -1, 1}, {"right", 40, 1}, {"above", 2, -1},
		} {
			t.Run(fmt.Sprintf("selection_%d/%s", selected, point.name), func(t *testing.T) {
				o := &explorationTrailOverlay{ui: u, ids: ids, index: selected}
				o.View(40, 3)
				_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: point.x, Y: point.y})
				if cmd != nil || done || o.index != selected {
					t.Fatal("click outside rendered trail row navigated")
				}
			})
		}
	}
	t.Run("visible scrolled row", func(t *testing.T) {
		o := &explorationTrailOverlay{ui: u, ids: ids, index: 6}
		o.View(40, 3)
		_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 1})
		if !done || cmd == nil || cmd().(trailVisitMsg).id != ids[6] {
			t.Fatal("visible trail row did not navigate")
		}
	})
	t.Run("resize discards old rows", func(t *testing.T) {
		o := &explorationTrailOverlay{ui: u, ids: ids, index: 6}
		o.View(80, 10)
		o.View(20, 3)
		_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 2})
		if done || cmd != nil || o.index != 6 {
			t.Fatal("resized trail retained a hidden clickable row")
		}
	})
	for _, key := range []string{"up", "down"} {
		t.Run("before redraw/"+key, func(t *testing.T) {
			o := &explorationTrailOverlay{ui: u, ids: ids, index: 6}
			o.View(40, 3)
			o.Update(uitest.Key(key))
			before := o.index
			_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 1})
			if done || cmd != nil || o.index != before {
				t.Fatal("stale trail row ran before redraw")
			}
		})
	}
	t.Run("before first render", func(t *testing.T) {
		o := &explorationTrailOverlay{ui: u, ids: ids}
		_, cmd, done := o.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 1})
		if done || cmd != nil {
			t.Fatal("unrendered trail accepted click")
		}
	})
}

func TestWorkspaceActionsEmptyMatchesAndMessageRecovery(t *testing.T) {
	u, _ := uxUI(t)
	o := &workspaceActionsOverlay{ui: u, search: "cancel"}
	o.View(80, 12)
	o.Update(uitest.Key("enter"))
	if o.message != "No running server operation" {
		t.Fatal("disabled reason missing")
	}
	o.Update(uitest.Text("z"))
	view := o.View(80, 12)
	if !strings.Contains(view, "No matching actions") || !strings.Contains(view, "Backspace") || o.message != "" {
		t.Fatalf("empty matches did not explain recovery or clear obsolete error: %s", view)
	}
	_, cmd, done := o.Update(uitest.Key("enter"))
	if done || cmd != nil {
		t.Fatal("empty results ran an action")
	}
	o.Update(uitest.Key("backspace"))
	if view = o.View(80, 12); strings.Contains(view, "No matching actions") || !strings.Contains(view, "Cancel running operation") {
		t.Fatal("backspace did not restore matches")
	}
	o.search, o.index, o.message = "", 13, "No running server operation"
	o.Update(uitest.Key("down"))
	if o.message != "" {
		t.Fatal("selection change retained obsolete error")
	}
	o.Update(uitest.Text("/1Ω"))
	if o.search != "/1Ω" || o.index != 0 {
		t.Fatal("literal search changed")
	}
}

func TestWorkspaceDialogsShellEntryAndBlankClicks(t *testing.T) {
	u, parent := uxUI(t)
	uxKey(t, u, "ctrl+p")
	view := ansi.Strip(u.shell.View().Content)
	if !strings.Contains(view, "Actions · type to search") {
		t.Fatal("keyboard entry did not open actions")
	}
	_, cmd := u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 29})
	drainCmd(t, u, cmd)
	if !strings.Contains(u.shell.View().Content, "Actions · type to search") || u.activeRecordSetID() != parent.ID {
		t.Fatal("outside click dismissed Actions or navigated")
	}
	uxKey(t, u, "esc")
	uxKey(t, u, "ctrl+t")
	_, cmd = u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 29})
	drainCmd(t, u, cmd)
	if !strings.Contains(u.shell.View().Content, "Trail · Alt+") || u.activeRecordSetID() != parent.ID {
		t.Fatal("outside click dismissed Trail or navigated")
	}
}

func TestWorkspaceDialogsClippedRowsAndPendingResize(t *testing.T) {
	u, parent := uxUI(t)
	for _, size := range []struct{ w, h int }{{40, 0}, {40, 1}, {40, 2}, {0, 5}, {-1, 5}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			actions := &workspaceActionsOverlay{ui: u, search: "keyboard"}
			actions.View(size.w, size.h)
			_, cmd, done := actions.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 2})
			if done || cmd != nil {
				t.Fatal("clipped action accepted click")
			}
			// Enter remains available when the viewport has no mouse rows.
			_, cmd, done = actions.Update(uitest.Key("enter"))
			if !done || cmd == nil || cmd().(workspaceActionMsg).id != "keyboard" {
				t.Fatal("clipped dialog lost keyboard activation")
			}
			trail := &explorationTrailOverlay{ui: u, ids: []string{parent.ID}}
			trail.View(size.w, min(size.h, 1))
			_, cmd, done = trail.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 1})
			if done || cmd != nil {
				t.Fatal("clipped trail accepted click")
			}
		})
	}
	actions := &workspaceActionsOverlay{ui: u, search: "keyboard"}
	actions.View(80, 10)
	actions.Update(tea.WindowSizeMsg{Width: 40, Height: 5})
	_, cmd, done := actions.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 2})
	if done || cmd != nil {
		t.Fatal("action used old geometry before resized render")
	}
	trail := &explorationTrailOverlay{ui: u, ids: []string{parent.ID}}
	trail.View(80, 10)
	trail.Update(tea.WindowSizeMsg{Width: 40, Height: 5})
	_, cmd, done = trail.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 1})
	if done || cmd != nil {
		t.Fatal("trail used old geometry before resized render")
	}
	empty := &explorationTrailOverlay{ui: u}
	if !strings.Contains(empty.View(80, 10), "Open data to begin") {
		t.Fatal("empty trail guidance missing")
	}
	_, cmd, done = empty.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 2, Y: 1})
	if done || cmd != nil {
		t.Fatal("empty trail guidance accepted click")
	}
}

func TestWorkspaceDialogsShellFrameRejectsClicks(t *testing.T) {
	for _, dialog := range []string{"actions", "trail"} {
		for _, target := range []string{"right border", "right padding", "content"} {
			t.Run(dialog+"/"+target, func(t *testing.T) {
				u, parent := uxUI(t)
				label := parent.Title
				if dialog == "actions" {
					uxKey(t, u, "ctrl+p")
					for _, r := range "keyboard" {
						uxKey(t, u, string(r))
					}
					label = "Keyboard diagnostic ["
				} else {
					uxKey(t, u, "ctrl+t")
				}
				view := ansi.Strip(u.shell.View().Content)
				for y, line := range strings.Split(view, "\n") {
					at := strings.Index(line, label)
					if at < 0 {
						continue
					}
					x := ansi.StringWidth(line[:at])
					if target != "content" {
						border := strings.LastIndex(line, "│")
						if border < 0 {
							t.Fatal("rendered row has no frame border")
						}
						x = ansi.StringWidth(line[:border])
						if target == "right padding" {
							x--
						}
					}
					_, cmd := u.shell.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
					drainCmd(t, u, cmd)
					stillOpen := u.shell.ActiveOverlayHints() != nil
					if target == "content" {
						if dialog == "actions" {
							if !strings.Contains(u.shell.View().Content, "Keyboard diagnostic · Esc close") {
								t.Fatal("visible action did not activate")
							}
						} else if stillOpen {
							t.Fatal("visible trail row did not activate")
						}
					} else if !stillOpen || u.activeRecordSetID() != parent.ID || (dialog == "actions" && !strings.Contains(u.shell.View().Content, "Actions · type to search")) {
						t.Fatal("frame click activated or dismissed the dialog")
					}
					return
				}
				t.Fatalf("no visible %s row in %s", dialog, view)
			})
		}
	}
}
