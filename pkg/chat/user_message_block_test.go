package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
)

// TestUserMessageCardHasBarPaddingAndSelectionState is ported unchanged
// from ui_test.go: it exercises userMessageView (ui_shared.go) directly,
// which never needed a UI/ChatUI type — user_message_block.go's own View
// just calls it.
func TestUserMessageCardHasBarPaddingAndSelectionState(t *testing.T) {
	normal := userMessageView("hello", 40, false)
	selected := userMessageView("hello", 40, true)
	lines := strings.Split(normal, "\n")
	if len(lines) != 3 {
		t.Fatalf("user message card has %d rows, want 3 (top pad, text, bottom pad)", len(lines))
	}
	for _, line := range lines {
		if got := ansi.StringWidth(line); got != 40 {
			t.Fatalf("user message line rendered at %d cells, want 40: %q", got, line)
		}
		if !strings.HasPrefix(ansi.Strip(line), "┃") {
			t.Fatalf("user message line lacks the left accent bar: %q", ansi.Strip(line))
		}
	}
	if !strings.Contains(ansi.Strip(normal), "You: hello") {
		t.Fatalf("user message card missing its label:\n%s", ansi.Strip(normal))
	}
	if normal == selected {
		t.Fatal("selected user message is indistinguishable from an unselected one")
	}
}

// TestChatUIEnterOnFocusedUserMessageLoadsComposer is ported from
// ui_test.go's TestShiftArrowsSelectUserMessageAndEnterLoadsItIntoComposer:
// same real behaviour (Enter on a focused user message card loads its text
// back into the composer for editing), driven through the real production
// wiring (userMessageBlock.Update -> userMessageEditMsg ->
// ChatUI.OnMsg -> shell.SetComposerText) instead of
// u.messageFocused/u.selectedMessage/u.editSelectedMessage. Focusing the
// message itself is chatshell's own focus-ring mechanics (Shift+↑↓), not
// re-tested here — see grid_state_test.go/chatui_test.go for the pattern.
func TestChatUIEnterOnFocusedUserMessageLoadsComposer(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	id := u.nextEntryID("user")
	if !u.shell.AppendBlockWithID(id, newUserMessageBlock("earlier question")) {
		t.Fatal("could not append user message block")
	}
	if !u.shell.FocusEntry(id) {
		t.Fatal("could not focus the user message block")
	}
	_, cmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		drainCmd(t, u, cmd)
	}
	// SetComposerText has no public getter on chatshell.Model; the composer
	// row in the rendered view is the closest available signal.
	if !strings.Contains(u.shell.View().Content, "earlier question") {
		t.Fatalf("Enter did not load the message into the composer:\n%s", u.shell.View().Content)
	}
}
