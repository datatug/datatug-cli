package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/chatshell"
)

// AISetup is how a workspace lets the person choose the AI that answers
// questions in plain words without leaving it. The dialog itself belongs to
// the caller: this package only opens it and reopens the workspace after it.
type AISetup struct {
	// Open builds the dialog. pending is the question the person had just
	// asked when the dialog opened by itself, "" when they typed /ai.
	Open func(ui *ChatUI, pending string) chatshell.Overlay
	// Missing says that nothing answers a question in plain words yet, so
	// asking one opens the dialog instead of a refusal.
	Missing bool
	// AnswersWithoutAI reports a message the conversation answers by itself
	// (a ready-made question): it is asked as usual. Optional.
	AnswersWithoutAI func(message string) bool
}

// AISetupCommand is the composer command that opens the AI setup dialog.
const AISetupCommand = "/ai"

// SetAISetup offers the AI setup dialog on /ai and, while AI is missing, on a
// question in plain words.
func (u *ChatUI) SetAISetup(setup AISetup) { u.aiSetup = setup }

// aiSetupFor returns the command that opens the dialog for a submitted text,
// and whether the text was taken by it.
func (u *ChatUI) aiSetupFor(text string) (tea.Cmd, bool) {
	if u.aiSetup.Open == nil {
		return nil, false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == AISetupCommand {
		return u.shell.PushOverlay(u.aiSetup.Open(u, "")), true
	}
	if !u.aiSetup.Missing || strings.HasPrefix(trimmed, "/") {
		return nil, false
	}
	if u.aiSetup.AnswersWithoutAI != nil && u.aiSetup.AnswersWithoutAI(trimmed) {
		return nil, false
	}
	// The question stays in the composer: closing the dialog loses nothing.
	u.shell.SetComposerText(text)
	return u.shell.PushOverlay(u.aiSetup.Open(u, text)), true
}

// OverlayMsg delivers the result of background work to a dialog. A dialog
// receives only keys, paste and the mouse from the shell; anything it started
// itself (a sign-in, a request) comes back through this message, and is
// dropped when the dialog was closed meanwhile.
type OverlayMsg struct {
	Overlay chatshell.Overlay
	Msg     tea.Msg
}

func (u *ChatUI) overlayMsg(msg OverlayMsg) tea.Cmd {
	if msg.Overlay == nil || !u.shell.HasOverlay(msg.Overlay) {
		return nil
	}
	_, cmd, done := msg.Overlay.Update(msg.Msg)
	if done {
		u.shell.CloseOverlay(msg.Overlay)
	}
	return cmd
}

// Reopen closes this workspace so that its caller opens it again on the same
// project, with what changed meanwhile (the AI that was just set up). pending
// is put back into the composer of the reopened workspace.
func (u *ChatUI) Reopen(pending string) tea.Cmd {
	u.reopen, u.reopenPending = true, pending
	return tea.Quit
}

// Reopened reports that the workspace closed to be opened again (see Reopen),
// and the text to put back into its composer.
func (u *ChatUI) Reopened() (bool, string) { return u.reopen, u.reopenPending }

// SetComposerText puts text into the composer, as if the person had typed it.
func (u *ChatUI) SetComposerText(text string) { u.shell.SetComposerText(text) }

// ShowChat shows the conversation with the keyboard in the composer: where a
// workspace reopened after the AI setup continues.
func (u *ChatUI) ShowChat() {
	u.shell.ShowTranscript()
	u.shell.FocusComposer()
}
