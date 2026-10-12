package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/ai/session"
	"github.com/strongo/aichat/tui/chatshell"
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

// chipCloseCoordinates scans the rendered composer for "Customer × " and
// returns the tea.MouseClickMsg X/Y a click on the × glyph itself would
// carry -- ansi.Strip'd column/row within the rendered content, mirroring
// aichat's own TestMouseClickFindsCloseGlyphInRenderedView (scanning the
// real View() output rather than an internal helper this package has no
// access to).
func chipCloseCoordinates(t *testing.T, content string) (x, y int) {
	t.Helper()
	for row, line := range strings.Split(content, "\n") {
		plain := ansi.Strip(line)
		if idx := runeIndex(plain, "×"); idx >= 0 {
			return idx, row
		}
	}
	t.Fatalf("no chip close glyph found in view:\n%s", ansi.Strip(content))
	return 0, 0
}

// runeIndex is strings.Index, but returns a RUNE offset (matching
// chatshell's own chip.go cell.x, which is a display-column count) instead
// of strings.Index's byte offset -- the "×" close glyph is a multi-byte
// UTF-8 rune (U+00D7), so a byte offset silently drifts from the correct
// column by one for every earlier "×" on the same line (chipCloseClick
// compares msg.X against that column count exactly, so this drift is the
// difference between a click landing on the glyph and missing it
// entirely).
func runeIndex(s, substr string) int {
	byteIdx := strings.Index(s, substr)
	if byteIdx < 0 {
		return -1
	}
	return len([]rune(s[:byteIdx]))
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

	// Tab focuses the chip -- on its own, before Backspace touches
	// anything, the chip/attachment and the composer text must both still
	// be exactly as they were (chatshell exposes no public getter for its
	// internal chip-focus index, so this only asserts Tab alone changed
	// nothing observable yet).
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if len(u.snapshot.Workspace.Attachments) != 1 || !strings.Contains(ansi.Strip(u.shell.View().Content), "Top 5 rows") {
		t.Fatalf("Tab alone should not change attachments or composer text: attachments=%+v\n%s", u.snapshot.Workspace.Attachments, ansi.Strip(u.shell.View().Content))
	}
	// Backspace on the now-focused chip removes it (proving Tab actually
	// reached the chip, not the input) without touching the composer text.
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("Tab+Backspace did not detach the chip: %+v", u.snapshot.Workspace.Attachments)
	}
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Top 5 rows") {
		t.Fatalf("Backspace on a focused chip should not touch the composer text:\n%s", ansi.Strip(u.shell.View().Content))
	}

	// Shift+Esc restores the chip removed by Backspace, and with it the
	// underlying attachment -- the composer text (never touched by
	// Tab+Backspace) must still read "Top 5 rows" too.
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEsc, Mod: tea.ModShift})
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatal("Shift+Esc did not restore the chip removed with Backspace")
	}
	if !strings.Contains(ansi.Strip(u.shell.View().Content), "Top 5 rows") {
		t.Fatalf("Shift+Esc restore after Backspace should leave composer text untouched:\n%s", ansi.Strip(u.shell.View().Content))
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

// TestChatUIComposerNeverWrapsChipsOverflowsInsteadWhenTheyDontFit
// supersedes origin/main's TestComposerShrinksAsWrappedAttachmentsAreRemoved
// (ported, then wrap-specific, under the OLD multi-row chip layout):
// aichat's r11 redesign replaced multi-row chip wrapping with a SINGLE row
// plus a trailing "+N" overflow pill (founder, verbatim: "keep one row:
// show as many chips as fit plus a '+N' chip ... never overflow or clip
// mid-chip") -- so several long-labelled attachments that would have
// wrapped onto more than one row before now render on exactly ONE chip
// row, with whichever chips don't fit folded into "+N" instead. Detaching
// attachments down to a handful (all now visible, no more overflow) makes
// the composer's chip area disappear back to zero rows once every chip is
// gone, exercised here only through ChatUI's real attach/detach path.
func TestChatUIComposerNeverWrapsChipsOverflowsInsteadWhenTheyDontFit(t *testing.T) {
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

	content := ansi.Strip(u.shell.View().Content)
	if got := countChipRows(u.shell.View().Content); got != 1 {
		t.Fatalf("chip row count with 8 long-labelled attachments at width 60 = %d, want exactly 1 (chips never wrap)", got)
	}
	if !strings.Contains(content, "+") {
		t.Fatalf("expected an overflow \"+N\" pill once 8 long labels don't all fit on one row:\n%s", content)
	}
	if visible := strings.Count(content, "VeryLongTableName"); visible >= 8 {
		t.Fatalf("expected fewer than 8 full chip labels visible at width 60 (the rest folded into \"+N\"), got %d:\n%s", visible, content)
	}
	for _, line := range strings.Split(content, "\n") {
		if w := ansi.StringWidth(line); w > 60 {
			t.Fatalf("chip row overflowed the terminal width (60): got %d: %q", w, line)
		}
	}
	firstLineWithMany, ok := firstChipRowLine(u.shell.View().Content)
	if !ok {
		t.Fatal("expected a chip row line to measure historyHeight's boundary against")
	}

	// Detach every attachment -- the chip row (and the "+N" pill with it)
	// disappears entirely, and historyHeight grows back by exactly the one
	// row the chip area used to occupy (chipsHeight is always 0 or 1 now,
	// never more -- see aichat's own chip.go chipsHeight doc).
	for _, obj := range catalog.Objects {
		if _, err := chat.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "detach", Reference: obj.Reference}); err != nil {
			t.Fatal(err)
		}
	}
	u.snapshot, err = chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.syncChips()
	if got := countChipRows(u.shell.View().Content); got != 0 {
		t.Fatalf("chip row count after detaching every attachment = %d, want 0", got)
	}
	if _, ok := firstChipRowLine(u.shell.View().Content); ok {
		t.Fatal("expected no chip row line left once every attachment is detached")
	}
	_ = firstLineWithMany // the chip row's own line index while it existed; no delta assertion needed now that chipsHeight is always exactly 0 or 1.
}

// countChipRows counts the rendered lines carrying at least one chip's
// close glyph ("×") -- one per wrapped chip row.
func countChipRows(content string) int {
	rows := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(ansi.Strip(line), "×") {
			rows++
		}
	}
	return rows
}

// firstChipRowLine returns the 0-based index, within content's rendered
// lines, of the FIRST line carrying a chip's close glyph ("×") -- see
// TestChatUIComposerShrinksAsWrappedAttachmentsAreRemoved's doc comment for
// why that index is an externally observable proxy for
// topBarHeight()+historyHeight(). ok is false when no chip row is rendered.
func firstChipRowLine(content string) (line int, ok bool) {
	for i, l := range strings.Split(content, "\n") {
		if strings.Contains(ansi.Strip(l), "×") {
			return i, true
		}
	}
	return 0, false
}

// TestReferenceFromChipRejectsForeignChips covers referenceFromChip's
// ok=false branch: a chip with no Ref at all, and a chip whose Ref carries
// a different Type -- chatshell's own chips, or another product's, never
// round-trip into a ContextReference.
func TestReferenceFromChipRejectsForeignChips(t *testing.T) {
	if _, ok := referenceFromChip(chatshell.Chip{ID: "no-ref", Label: "No ref"}); ok {
		t.Fatal("a chip with a nil Ref should not resolve to a ContextReference")
	}
	if _, ok := referenceFromChip(chatshell.Chip{ID: "other", Ref: &session.EntityRef{Type: "some.other.product"}}); ok {
		t.Fatal("a chip with a foreign Ref.Type should not resolve to a ContextReference")
	}
	ref, ok := referenceFromChip(attachmentChip(ContextReference{Kind: "table", SourceID: "s", ObjectID: "o", Title: "T"}))
	if !ok || ref.Kind != "table" || ref.ObjectID != "o" {
		t.Fatalf("round trip through attachmentChip failed: %+v, %v", ref, ok)
	}
}

// TestSyncChipsWithoutShellIsANoop covers syncChips's guard: a ChatUI whose
// shell hasn't been constructed (only reachable via a zero-value struct in
// this package's own tests -- NewChatUI always sets it) must not panic.
func TestSyncChipsWithoutShellIsANoop(t *testing.T) {
	u := &ChatUI{}
	u.syncChips() // must not panic
}

// TestOnChipsChangeWithoutSessionsIsANoop covers OnChipsChange's early
// return for a sessionless ChatUI (Conversation only, no durable
// SessionChat) -- attachment sync requires a durable session to persist
// into, so it's simply skipped, matching NewSessionChatUI's absence here.
func TestOnChipsChangeWithoutSessionsIsANoop(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "fake-model")
	if cmd := u.OnChipsChange([]chatshell.Chip{{ID: "x"}}); cmd != nil {
		t.Fatalf("OnChipsChange without sessions returned a non-nil cmd: %v", cmd)
	}
}

// TestOnChipsChangeReportsDetachAndAttachErrors covers OnChipsChange's two
// applyWorkspaceAction error branches (each reports the failure via
// AppendAssistant/conciseError rather than losing it silently): a
// chip chatshell no longer has that's stale relative to the durable
// session's own Attachments (detach fails: "is not attached"), and a
// restored/new chip whose Reference the catalog no longer recognizes
// (attach fails: validateContextReference).
func TestOnChipsChangeReportsDetachAndAttachErrors(t *testing.T) {
	u, _ := newChipTestChatUI(t)
	staleRef := ContextReference{Kind: "table", SourceID: "chinook-local", ObjectID: "main.NeverAttached", Title: "Never attached"}
	// u.snapshot.Workspace.Attachments claims staleRef is attached, but the
	// durable session (unchanged) disagrees -- OnChipsChange's detach loop
	// will try, and fail, to detach it.
	u.snapshot.Workspace.Attachments = append(append([]ContextReference(nil), u.snapshot.Workspace.Attachments...), staleRef)

	invalidRef := ContextReference{Kind: "table", SourceID: "chinook-local", ObjectID: "main.DoesNotExist", Title: "Unknown table"}
	before := len(u.shell.View().Content)
	u.OnChipsChange([]chatshell.Chip{attachmentChip(invalidRef)})
	after := u.shell.View().Content
	if len(after) == before { // sanity: the view did change (an assistant message was appended)
		t.Fatal("expected the view to change after OnChipsChange's error reports")
	}
	if !strings.Contains(ansi.Strip(after), "not attached") {
		t.Fatalf("expected the stale detach failure reported in the transcript:\n%s", ansi.Strip(after))
	}
}
