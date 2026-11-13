package chat

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/chatshell"
)

// recordingDialog is a dialog that remembers what it was sent and closes on
// the message "close".
type recordingDialog struct {
	got  []tea.Msg
	next tea.Cmd
}

func (d *recordingDialog) View(int, int) string { return "dialog" }
func (d *recordingDialog) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	d.got = append(d.got, msg)
	return d, d.next, msg == "close"
}

// /ai opens the caller's dialog, and while AI is missing a question in plain
// words opens it too, with the question kept: nothing sends the person out of
// the workspace to set AI up.
func TestAISetupOpensFromTheComposer(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{Text: "answered"})
	if cmd, taken := u.aiSetupFor(AISetupCommand); taken || cmd != nil {
		t.Fatal("/ai was taken with no AI setup offered")
	}

	var dialog *recordingDialog
	var pendings []string
	setup := AISetup{Missing: true, Open: func(_ *ChatUI, pending string) chatshell.Overlay {
		pendings = append(pendings, pending)
		dialog = &recordingDialog{}
		return dialog
	}}
	u.SetAISetup(setup)

	u.Submit("  " + AISetupCommand + " ")
	if dialog == nil || !u.shell.HasOverlay(dialog) || pendings[0] != "" {
		t.Fatalf("/ai did not open the dialog: pendings=%q", pendings)
	}
	u.shell.CloseOverlay(dialog)

	u.Submit("who buys the most?")
	if !u.shell.HasOverlay(dialog) || pendings[1] != "who buys the most?" {
		t.Fatalf("a question without AI did not open the dialog: pendings=%q", pendings)
	}
	if got := u.shell.ComposerText(); got != "who buys the most?" {
		t.Fatalf("the question was lost from the composer: %q", got)
	}
	u.shell.CloseOverlay(dialog)

	// A command is a command, and a ready-made question is asked as usual.
	if _, taken := u.aiSetupFor("/help"); taken {
		t.Fatal("a command opened the AI setup")
	}
	setup.AnswersWithoutAI = func(message string) bool { return message == "1" }
	u.SetAISetup(setup)
	if _, taken := u.aiSetupFor("1"); taken {
		t.Fatal("a ready-made question opened the AI setup")
	}
	if _, taken := u.aiSetupFor("2"); !taken {
		t.Fatal("a question the conversation cannot answer did not open the AI setup")
	}

	// With AI set up a question is simply asked.
	setup.Missing = false
	u.SetAISetup(setup)
	if _, taken := u.aiSetupFor("who buys the most?"); taken {
		t.Fatal("a question opened the AI setup although AI answers")
	}
}

// The result of what a dialog started in the background reaches it only while
// it is open, and closes it when the dialog says so.
func TestOverlayMsgReachesAnOpenDialog(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	if cmd := u.OnMsg(OverlayMsg{}); cmd != nil {
		t.Fatal("a message for no dialog returned a command")
	}
	dialog := &recordingDialog{}
	if u.OnMsg(OverlayMsg{Overlay: dialog, Msg: "late"}); len(dialog.got) != 0 {
		t.Fatal("a closed dialog received a message")
	}
	u.shell.PushOverlay(dialog)
	dialog.next = func() tea.Msg { return "follow-up" }
	cmd := u.OnMsg(OverlayMsg{Overlay: dialog, Msg: "progress"})
	if cmd == nil || cmd() != "follow-up" || len(dialog.got) != 1 || !u.shell.HasOverlay(dialog) {
		t.Fatalf("an open dialog did not receive its message: got=%v", dialog.got)
	}
	u.OnMsg(OverlayMsg{Overlay: dialog, Msg: "close"})
	if u.shell.HasOverlay(dialog) {
		t.Fatal("the dialog stayed open after it said it was done")
	}
}

func TestReopenKeepsTheQuestionForTheNextOpening(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	if reopen, _ := u.Reopened(); reopen {
		t.Fatal("a new workspace says it is to be reopened")
	}
	if cmd := u.Reopen("my question"); cmd == nil {
		t.Fatal("Reopen did not close the workspace")
	}
	if reopen, pending := u.Reopened(); !reopen || pending != "my question" {
		t.Fatalf("Reopened = %v, %q", reopen, pending)
	}
	u.SetComposerText("my question")
	u.ShowChat()
	if u.shell.ComposerText() != "my question" || u.shell.WorkspaceFocus() != "input" || u.shell.CenterPanel() != nil {
		t.Fatalf("the reopened workspace is not on the chat: focus=%q", u.shell.WorkspaceFocus())
	}
}
