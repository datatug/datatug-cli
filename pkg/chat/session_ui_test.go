package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestSessionUIRestoresGridAndManagesSessions(t *testing.T) {
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
	u, err := NewSessionUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.entries) != 2 || u.entries[1].grid == nil || len(u.entries[1].grid.model.Rows) != 1 {
		t.Fatalf("restored history = %+v", u.entries)
	}
	if !strings.Contains(u.View().Content, "Prague") {
		t.Fatal("persisted grid was not rendered")
	}
	u.runSessionCommand("/new")
	secondID := u.sessionID
	if secondID == first.ID || len(u.entries) != 0 {
		t.Fatal("new session did not open empty")
	}
	u.runSessionCommand("/sessions")
	if len(u.entries) != 1 || !strings.Contains(u.entries[0].text, first.ID[:8]) {
		t.Fatalf("session list = %+v", u.entries)
	}
	u.runSessionCommand("/rename Other analysis")
	if u.sessionTitle != "Other analysis" {
		t.Fatalf("renamed title = %q", u.sessionTitle)
	}
	u.runSessionCommand("/switch " + first.ID[:8])
	if u.sessionID != first.ID || len(u.entries) != 2 || u.entries[1].grid == nil {
		t.Fatal("switch did not restore first grid")
	}
	u.runSessionCommand("/clear")
	if len(u.entries) != 3 || !strings.Contains(u.entries[2].text, "confirm") {
		t.Fatal("clear lacked confirmation")
	}
	u.runSessionCommand("/clear confirm")
	if len(u.entries) != 0 {
		t.Fatal("clear retained history")
	}
	u.runSessionCommand("/delete confirm")
	if u.sessionID != secondID || u.sessionTitle != "Other analysis" {
		t.Fatalf("delete switched to %q %q", u.sessionID, u.sessionTitle)
	}
}

func TestSessionUISubmissionPersistsThenRendersFromSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	agent := &contextualStub{turns: []Turn{{Queries: []QueryResult{{Title: "Invoices", DTQL: "from: {name: Invoice}\nlimit: 1", Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 412}}}}}}}}}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.input.SetValue("Show one invoice")
	_, cmd := u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || !u.busy {
		t.Fatal("submission did not start")
	}
	_, _ = u.Update(cmd())
	if u.busy || len(u.entries) != 2 || u.entries[1].grid == nil {
		t.Fatalf("result not restored from snapshot: %+v", u.entries)
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot.RecordSets) != 1 {
		t.Fatalf("persisted recordsets = %+v, %v", snapshot.RecordSets, err)
	}
}
