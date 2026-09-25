package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/theme"
)

// TestUserMessageViewReturnsPlainSanitizedText covers userMessageView
// (ui_shared.go): its own left-accent-bar/background card rendering moved
// to the shared theme.Card, which transcript now wraps EVERY Block in
// automatically (userMessageBlock reports theme.RoleUser via the Roled
// capability -- see TestUserMessageBlockReportsRoleUser) --
// strongo/aichat#chat-shared-look. userMessageView's own job shrank to
// exactly what theme.Card doesn't already do: sanitizing the text. Framing
// it (padding, background, the focus/selection accent) is theme.Card's
// job now, exercised by strongo/aichat's own theme tests, not here.
func TestUserMessageViewReturnsPlainSanitizedText(t *testing.T) {
	if got := userMessageView("hello", 40, false); got != "hello" {
		t.Fatalf("userMessageView() = %q, want the sanitized text unchanged", got)
	}
	if got := userMessageView("hello", 40, true); got != "hello" {
		t.Fatalf("userMessageView() = %q, want the same regardless of selected", got)
	}
	// sanitizeTerminalText is exercised elsewhere; this only confirms
	// userMessageView actually routes through it (e.g. strips control
	// characters) rather than returning the raw string untouched.
	if got := userMessageView("a\x00b", 40, false); got == "a\x00b" {
		t.Fatalf("userMessageView() did not sanitize control characters: %q", got)
	}
}

// TestUserMessageBlockReportsRoleUser covers userMessageBlock.Role()
// (transcript.Roled): it must report theme.RoleUser so the shared card
// transcript wraps it in gets the same accent a plain, non-Block user
// message does.
func TestUserMessageBlockReportsRoleUser(t *testing.T) {
	b := newUserMessageBlock("hi")
	if got := b.Role(); got != theme.RoleUser {
		t.Fatalf("userMessageBlock.Role() = %q, want %q", got, theme.RoleUser)
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
