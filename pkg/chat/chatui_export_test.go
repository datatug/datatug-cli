package chat

// TestChatUIBucketAndExportCommandOnChatUI is ported from export_test.go's
// TestChatUIBucketAndExportCommand — despite its name, that test still used
// the legacy NewSessionUI/u.updateGrid/u.runSessionCommand/u.Update. It's
// renamed here to disambiguate from the leftover pure-function-named test.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatUIBucketAndExportCommandOnChatUI(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	chat, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := workspaceTestRecord(t, store, session.ID)
	u, err := NewSessionChatUI(ctx, chat, "stub")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if u.lastGridEntryID == "" || !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid was not restored")
	}
	if _, handled := u.handleGridKey(nil, tea.KeyPressMsg{Code: 'B', Text: "B"}); !handled {
		t.Fatal("B did not toggle bucket")
	}
	if len(u.snapshot.Workspace.ExportBucket) != 1 || u.snapshot.Workspace.ExportBucket[0] != id {
		t.Fatalf("bucket action did not persist: %#v", u.snapshot.Workspace.ExportBucket)
	}
	path := filepath.Join(t.TempDir(), "customers.csv")
	cmd, err := u.exportCommand("current csv " + path)
	if err != nil || cmd == nil {
		t.Fatalf("export command did not start: %v", err)
	}
	drainCmd(t, u, cmd)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("export missing: %v", err)
	}
}
