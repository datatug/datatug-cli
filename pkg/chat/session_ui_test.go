package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// Ported from the legacy UI's session_ui_test.go: same session-management
// flows (/new /sessions /rename /switch /clear /delete, restoring a
// persisted grid on NewSessionChatUI), driven through ChatUI's public
// Submit/drainCmd round trip and shell.View().Content instead of u.entries.
func TestChatUISessionRestoresGridAndManagesSessions(t *testing.T) {
	ctx := context.Background()
	path := testStorePath(t)
	store := openTestStore(t, path, testScope())
	agent := &contextualStub{turns: []Turn{{Queries: []QueryResult{{Title: "Prague customers", DTQL: "from: {name: Customer}\nlimit: 1", Result: secureread.Result{
		Columns: []string{"CustomerId", "City"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 5, "City": "Prague"}}},
	}}}}}}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	first, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Ask(ctx, "Show a Prague customer"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	reloaded := openTestStore(t, path, testScope())
	sessions, err = NewSessionChat(ctx, reloaded, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if len(u.gridsByRecordSetID) != 1 {
		t.Fatalf("restored grids = %+v", u.gridsByRecordSetID)
	}
	if !strings.Contains(u.shell.View().Content, "Prague") {
		t.Fatal("persisted grid was not rendered")
	}

	drainCmd(t, u, u.Submit("/new"))
	secondID := u.sessionID
	if secondID == first.ID || len(u.gridsByRecordSetID) != 0 {
		t.Fatal("new session did not open empty")
	}

	drainCmd(t, u, u.Submit("/sessions"))
	if !strings.Contains(u.shell.View().Content, first.ID[:8]) {
		t.Fatalf("session list missing first session in view:\n%s", u.shell.View().Content)
	}

	drainCmd(t, u, u.Submit("/rename Other analysis"))
	if u.snapshot.Title != "Other analysis" {
		t.Fatalf("renamed title = %q", u.snapshot.Title)
	}

	drainCmd(t, u, u.Submit("/switch "+first.ID[:8]))
	if u.sessionID != first.ID || len(u.gridsByRecordSetID) != 1 {
		t.Fatal("switch did not restore first grid")
	}

	drainCmd(t, u, u.Submit("/clear"))
	if !strings.Contains(u.shell.View().Content, "confirm") {
		t.Fatal("clear lacked confirmation")
	}

	drainCmd(t, u, u.Submit("/clear confirm"))
	if len(u.gridsByRecordSetID) != 0 {
		t.Fatal("clear retained history")
	}

	drainCmd(t, u, u.Submit("/delete confirm"))
	if u.sessionID != secondID || u.snapshot.Title != "Other analysis" {
		t.Fatalf("delete switched to %q %q", u.sessionID, u.snapshot.Title)
	}
}

func TestChatUISessionSubmissionPersistsThenRendersFromSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	agent := &contextualStub{turns: []Turn{{Queries: []QueryResult{{Title: "Invoices", DTQL: "from: {name: Invoice}\nlimit: 1", Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 412}}}}}}}}}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	drainCmd(t, u, u.Submit("Show one invoice"))
	if len(u.gridsByRecordSetID) != 1 {
		t.Fatalf("result not restored from snapshot: %+v", u.gridsByRecordSetID)
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot.RecordSets) != 1 {
		t.Fatalf("persisted recordsets = %+v, %v", snapshot.RecordSets, err)
	}
}
