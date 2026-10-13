package chat

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestChatUICtrlDDetachesLastAttachment is the M5 regression test (r1
// adversarial review of #289): ui.go's Ctrl+D handler detached the most
// recently attached workspace item, and had no chatshell equivalent after
// the ChatUI cutover. globalKeys (chatui_pickers.go) now restores it.
func TestChatUICtrlDDetachesLastAttachment(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, _ := sessions.Snapshot(ctx)
	workspaceTestRecord(t, store, session.ID)
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})

	// Ctrl+D with nothing attached is a no-op, not an error.
	u.shell.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("Ctrl+D with no attachments = %+v, want none", u.snapshot.Workspace.Attachments)
	}

	// Navigate the actual Explorer, select the table, and use its explicit
	// attach-to-composer action before exercising Ctrl+D.
	u.shell.FocusLeftPanel()
	selectedTable := false
	for range 32 {
		if object := u.explorer.selectedExplorerObject(); object != nil && object.Reference.Kind == "table" {
			selectedTable = true
			break
		}
		u.shell.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		u.shell.View()
	}
	if !selectedTable {
		t.Fatal("could not navigate Explorer to a table")
	}
	u.shell.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt})
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("attach = %+v, want 1", u.snapshot.Workspace.Attachments)
	}
	if u.shell.WorkspaceFocus() != "input" {
		t.Fatalf("Alt+A should return focus to the composer, got %q", u.shell.WorkspaceFocus())
	}

	u.shell.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("Ctrl+D did not detach the last attachment: %+v", u.snapshot.Workspace.Attachments)
	}
}
