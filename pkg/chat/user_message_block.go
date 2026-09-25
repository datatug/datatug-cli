package chat

import (
	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/theme"
	"github.com/strongo/aichat/tui/transcript"
)

// userMessageEditMsg asks the product to load text back into the composer
// for editing — the new-architecture equivalent of ui.go's
// editSelectedMessage/focusMessage Enter behaviour
// (TestShiftArrowsSelectUserMessageAndEnterLoadsItIntoComposer). ChatUI's
// OnMsg (chatshell.MsgHandler) handles it by calling shell.SetComposerText.
type userMessageEditMsg struct{ text string }

// userMessageBlock is a focusable transcript.Block for a user turn: it
// renders like transcript's own plain user card, but implements Update so
// Enter (while focused) triggers a userMessageEditMsg — transcript.Entry's
// own built-in RoleUser rendering has no Block and so cannot react to keys
// (see transcript.Model.Update: a nil-Block entry's keys are dropped).
type userMessageBlock struct {
	text string
}

func newUserMessageBlock(text string) *userMessageBlock {
	return &userMessageBlock{text: text}
}

func (b *userMessageBlock) Focusable() bool { return true }

// Role satisfies transcript.Roled: this Block still renders as a RoleUser
// card, exactly like transcript's own built-in (non-Block) plain user
// message, since it exists only to make Enter-to-edit work, not to look
// different from an ordinary user message (strongo/aichat#chat-shared-
// look: a Block wrapped in the generic untitled RoleBlock card by default
// would otherwise lose the accent a user message gets).
func (b *userMessageBlock) Role() theme.Role { return theme.RoleUser }

func (b *userMessageBlock) View(width int, focused bool) string {
	return userMessageView(b.text, width, focused)
}

func (b *userMessageBlock) Update(msg tea.Msg) (transcript.Block, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" {
		text := b.text
		return b, func() tea.Msg { return userMessageEditMsg{text: text} }
	}
	return b, nil
}

var _ transcript.Block = (*userMessageBlock)(nil)
