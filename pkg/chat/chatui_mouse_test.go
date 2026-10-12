package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestChatUIF2TogglesMouseModeAndStatusHint is the M4 regression test (r1
// adversarial review of #289): ports ui.go's
// TestUIViewEnablesMouseWheelHistoryScrolling's F2/status-hint assertions
// onto ChatUI + chatshell. Mouse wheel scrolling itself is chatshell's own
// generic behavior now (handleMouseWheel) and needs no DataTug-level test;
// what's DataTug-owned and worth covering here is: mouse reporting defaults
// on (NewChatUI's chatshell.WithMouse(chatshell.MouseCellMotion)), F2
// (globalKeys, chatui_pickers.go) toggles it via SetMouseEnabled, and the
// status bar's mouseHint (chatui.go's statusBar) names what a press does.
func TestChatUIF2TogglesMouseModeAndStatusHint(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})

	if got := u.shell.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("initial mouse mode = %v, want MouseModeCellMotion (mouse reporting on by default)", got)
	}
	if view := flattenView(u.shell.View().Content); !strings.Contains(view, "F2 select") {
		t.Fatalf("status bar does not advertise F2 for terminal selection while mouse reporting is on:\n%s", view)
	}

	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if got := u.shell.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("mouse mode after F2 = %v, want MouseModeNone (terminal selection restored)", got)
	}
	if view := flattenView(u.shell.View().Content); !strings.Contains(view, "F2 wheel") {
		t.Fatalf("status bar does not advertise restoring wheel capture after F2:\n%s", view)
	}

	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if got := u.shell.View().MouseMode; got != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode after second F2 = %v, want MouseModeCellMotion", got)
	}
	if view := flattenView(u.shell.View().Content); !strings.Contains(view, "F2 select") {
		t.Fatalf("status bar did not revert to F2 select after the second F2:\n%s", view)
	}
}
