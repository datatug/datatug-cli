package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// newChipTestChatUI builds a ChatUI whose session already has the Customer
// table attached, matching workspace_test.go's own workspaceTestCatalog/
// attach setup.
func newChipTestChatUI(t *testing.T) (*ChatUI, *SessionChat) {
	t.Helper()
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	catalog := workspaceTestCatalog()
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", catalog)
	if err != nil {
		t.Fatal(err)
	}
	ref := catalog.Objects[2].Reference
	if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, chat, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return u, chat
}

// chipCloseCoordinates scans the rendered composer for "Customer ×]" and
// returns the tea.MouseClickMsg X/Y a click on the × glyph itself would
// carry -- ansi.Strip'd column/row within the rendered content, mirroring
// aichat's own TestMouseClickFindsCloseGlyphInRenderedView (scanning the
// real View() output rather than an internal helper this package has no
// access to).
func chipCloseCoordinates(t *testing.T, content string) (x, y int) {
	t.Helper()
	for row, line := range strings.Split(content, "\n") {
		plain := ansi.Strip(line)
		if idx := strings.Index(plain, "×]"); idx >= 0 {
			return idx, row
		}
	}
	t.Fatalf("no chip close glyph found in view:\n%s", ansi.Strip(content))
	return 0, 0
}

// TestChatUIComposerAttachmentChipsCanBeFocusedClearedAndRestored ports
// origin/main's TestComposerAttachmentChipsCanBeFocusedClearedAndRestored
// (workspace_test.go) onto ChatUI/chatshell v0.2.0's own composer chips
// (B5): DataTug no longer owns chip focus/removal/undo itself (see
// chatui_chips.go) -- chatshell's built-in Tab/Esc/Shift+Esc/Backspace/
// Ctrl+D/mouse-× handling does, with OnChipsChange keeping
// u.snapshot.Workspace.Attachments in sync -- so this drives those same
// real keys/clicks and asserts the same durable, persisted outcome the
// original test did (attachment count), not chatshell's own internal chip
// state.
func TestChatUIComposerAttachmentChipsCanBeFocusedClearedAndRestored(t *testing.T) {
	u, _ := newChipTestChatUI(t)
	u.shell.SetComposerText("Top 5 rows")
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Customer") {
		t.Fatal("attached Customer chip is not visible in the composer")
	}

	// Tab focuses the chip; Backspace on a focused chip removes it (proving
	// Tab actually reached the chip, not the input).
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("Tab+Backspace did not detach the chip: %+v", u.snapshot.Workspace.Attachments)
	}
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Top 5 rows") {
		t.Fatalf("Backspace on a focused chip should not touch the composer text:\n%s", ansi.Strip(u.shell.View().Content))
	}

	// Shift+Esc restores the chip removed by Backspace, and with it the
	// underlying attachment.
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatal("Shift+Esc did not restore the chip removed with Backspace")
	}

	// First Esc clears the composer TEXT only, not the chip/attachment.
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if strings.Contains(ansi.Strip(u.shell.View().Content), "Top 5 rows") || len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("first Esc should clear text only: attachments=%+v\n%s", u.snapshot.Workspace.Attachments, ansi.Strip(u.shell.View().Content))
	}
	// Second Esc (text already empty) detaches every chip.
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("second Esc should clear attachments: %+v", u.snapshot.Workspace.Attachments)
	}
	// Shift+Esc restores both the text and the attachment cleared by the
	// two-step Esc.
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Top 5 rows") || len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("Shift+Esc did not restore draft and attachment: attachments=%+v\n%s", u.snapshot.Workspace.Attachments, ansi.Strip(u.shell.View().Content))
	}

	// A click on the chip's × glyph removes it.
	x, y := chipCloseCoordinates(t, u.shell.View().Content)
	u.shell.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatal("clicking the visible × did not detach the attachment")
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatal("Shift+Esc did not restore the chip removed with the mouse")
	}

	// Ctrl+D removes the last chip, matching the old DataTug-owned
	// shortcut's behaviour -- now via chatshell's own built-in handling.
	u.shell.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatal("Ctrl+D did not detach the last attachment")
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatal("Shift+Esc did not restore the chip removed with Ctrl+D")
	}
}

// TestChatUIComposerShrinksAsWrappedAttachmentsAreRemoved ports
// origin/main's TestComposerShrinksAsWrappedAttachmentsAreRemoved: several
// attachments that wrap onto more than one chip row make the composer
// taller, and detaching them (down to one) makes it shrink back --
// chatshell's own chipsHeight/historyHeight accounting (M9 in aichat
// v0.2.0's own changelog), exercised here only through ChatUI's real
// attach/detach path.
func TestChatUIComposerShrinksAsWrappedAttachmentsAreRemoved(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	catalog := ProjectCatalog{ID: "chinook", Title: "Chinook"}
	for i := range 8 {
		obj := ProjectObject{Reference: ContextReference{
			Kind: "table", ProjectID: "chinook", SourceID: "chinook-local",
			ObjectID: "table-" + string(rune('A'+i)), Title: "VeryLongTableName" + string(rune('A'+i)),
		}}
		catalog.Objects = append(catalog.Objects, obj)
	}
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range catalog.Objects {
		if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "attach", Reference: obj.Reference}); err != nil {
			t.Fatal(err)
		}
	}
	u, err := NewSessionChatUI(ctx, chat, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	wrapped := strings.Count(ansi.Strip(u.shell.View().Content), "VeryLongTableName")
	if wrapped != 8 {
		t.Fatalf("expected all 8 attachment chips visible, found %d", wrapped)
	}
	chipRowsWithMany := countChipRows(u.shell.View().Content)
	if chipRowsWithMany < 2 {
		t.Fatalf("expected 8 long-labelled chips at width 60 to wrap onto more than one row, got %d row(s)", chipRowsWithMany)
	}

	for _, obj := range catalog.Objects[1:] {
		if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "detach", Reference: obj.Reference}); err != nil {
			t.Fatal(err)
		}
	}
	u.snapshot, err = chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.syncChips()
	chipRowsWithOne := countChipRows(u.shell.View().Content)
	if chipRowsWithOne != 1 {
		t.Fatalf("expected exactly 1 chip row left with 1 attachment, got %d", chipRowsWithOne)
	}
	if chipRowsWithOne >= chipRowsWithMany {
		t.Fatalf("composer's chip area did not shrink as chips were removed: many=%d one=%d", chipRowsWithMany, chipRowsWithOne)
	}
}

// countChipRows counts the rendered lines carrying at least one chip's
// close glyph ("×]") -- one per wrapped chip row.
func countChipRows(content string) int {
	rows := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(ansi.Strip(line), "×]") {
			rows++
		}
	}
	return rows
}
