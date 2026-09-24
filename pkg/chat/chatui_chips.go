package chat

import (
	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/ai/session"
	"github.com/strongo/aichat/tui/chatshell"
)

// attachmentChipRefType tags a composer chip's session.EntityRef so
// referenceFromChip can round-trip it back into a ContextReference --
// mirrors grid_state.go's gridRecordSetRefType for the same reason
// (chatshell's Chip.Ref is a product-neutral {Type, Keys} pair, not
// DataTug's own ContextReference).
const attachmentChipRefType = "datatug.attachment"

// attachmentChipID is a stable per-attachment chip identity, matching the
// explorer node ID convention (chatui_sidepanel.go's appendGroup) so the
// same reference always produces the same chip ID across a session.
func attachmentChipID(ref ContextReference) string {
	return ref.Kind + ":" + ref.SourceID + ":" + ref.ObjectID
}

// attachmentChip converts one workspace attachment into a chatshell.Chip.
func attachmentChip(ref ContextReference) chatshell.Chip {
	return chatshell.Chip{
		ID:    attachmentChipID(ref),
		Label: sanitizeTerminalText(ref.Title),
		Ref: &session.EntityRef{Type: attachmentChipRefType, Keys: map[string]string{
			"kind": ref.Kind, "projectId": ref.ProjectID, "sourceId": ref.SourceID, "objectId": ref.ObjectID, "title": ref.Title,
		}},
	}
}

// referenceFromChip is attachmentChip's inverse: it reports ok=false for a
// chip chatshell created (or a product other than this attachment sync
// ever set) without an attachmentChipRefType Ref.
func referenceFromChip(chip chatshell.Chip) (ContextReference, bool) {
	if chip.Ref == nil || chip.Ref.Type != attachmentChipRefType {
		return ContextReference{}, false
	}
	keys := chip.Ref.Keys
	return ContextReference{Kind: keys["kind"], ProjectID: keys["projectId"], SourceID: keys["sourceId"], ObjectID: keys["objectId"], Title: keys["title"]}, true
}

// syncChips pushes u.snapshot.Workspace.Attachments onto the composer as
// chatshell chips -- called after every u.snapshot assignment that could
// change Attachments (loadSession, applyWorkspaceAction). SetChips itself
// never calls OnChipsChange (see aichat's own doc on SetChips), so this
// never loops back into OnChipsChange below.
func (u *ChatUI) syncChips() {
	if u.shell == nil {
		return
	}
	chips := make([]chatshell.Chip, len(u.snapshot.Workspace.Attachments))
	for i, ref := range u.snapshot.Workspace.Attachments {
		chips[i] = attachmentChip(ref)
	}
	u.shell.SetChips(chips)
}

// OnChipsChange satisfies chatshell.ChipObserver: chatshell calls it after
// IT changes the chip list on its own (Backspace/Delete/Ctrl+D/mouse-×
// removal, or a Shift+Esc/Ctrl+Y restore) so ChatUI's own attachment state
// (the durable, persisted source of truth) stays in sync. A chip present
// before but missing now is detached; a chip present now that wasn't
// attached a moment ago (a restore bringing one back) is re-attached.
// applyWorkspaceAction's own u.snapshot assignment re-enters syncChips,
// which is a no-op here since it rebuilds the exact same chip list chatshell
// already has (SetChips doesn't itself trigger another OnChipsChange).
func (u *ChatUI) OnChipsChange(chips []chatshell.Chip) tea.Cmd {
	if u.sessions == nil {
		return nil
	}
	want := make(map[string]ContextReference, len(chips))
	for _, chip := range chips {
		if ref, ok := referenceFromChip(chip); ok {
			want[attachmentChipID(ref)] = ref
		}
	}
	for _, ref := range append([]ContextReference(nil), u.snapshot.Workspace.Attachments...) {
		if _, stillWanted := want[attachmentChipID(ref)]; !stillWanted {
			if err := u.applyWorkspaceAction(WorkspaceAction{Kind: "detach", Reference: ref}); err != nil {
				u.shell.AppendAssistant(conciseError(err))
			}
		}
	}
	have := make(map[string]bool, len(u.snapshot.Workspace.Attachments))
	for _, ref := range u.snapshot.Workspace.Attachments {
		have[attachmentChipID(ref)] = true
	}
	for id, ref := range want {
		if !have[id] {
			if err := u.applyWorkspaceAction(WorkspaceAction{Kind: "attach", Reference: ref}); err != nil {
				u.shell.AppendAssistant(conciseError(err))
			}
		}
	}
	return nil
}

var _ chatshell.ChipObserver = (*ChatUI)(nil)
