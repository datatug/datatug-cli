package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/aichat/tui/grid"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestChatUIF4OpensSessionPickerAndSwitches(t *testing.T) {
	ctx := context.Background()
	u, sessions := newTestChatUI(t, nil, Turn{})
	firstID := u.sessionID
	second, err := sessions.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Switch back to the first session so the picker has something to do.
	if _, err := sessions.Switch(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)

	cmd, consumed := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF4})
	if !consumed {
		t.Fatal("expected F4 to be consumed by globalKeys")
	}
	drainCmd(t, u, cmd)

	view := u.shell.View().Content
	if !strings.Contains(view, "Sessions") {
		t.Fatalf("expected session picker overlay in view:\n%s", view)
	}
	// Move down to the second session and press enter to switch.
	u.shell.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if u.sessionID != second.ID {
		t.Fatalf("expected F4 Enter to switch to the second session, got %s (want %s)", u.sessionID, second.ID)
	}
}

func TestChatUIF3OpensProjectPickerAndSelects(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.SetProjectChoices([]ProjectChoice{{Key: "p1", Title: "Project One"}, {Key: "p2", Title: "Project Two"}})

	cmd, consumed := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3})
	if !consumed {
		t.Fatal("expected F3 to be consumed by globalKeys")
	}
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "Project One") || !strings.Contains(view, "Project Two") {
		t.Fatalf("expected project picker overlay in view:\n%s", view)
	}

	_, quitCmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if quitCmd == nil {
		t.Fatal("expected Enter on the project picker to return tea.Quit")
	}
	if u.SelectedProject() != "p1" {
		t.Fatalf("expected SelectedProject p1, got %q", u.SelectedProject())
	}
}

func TestChatUIF3NoOpWithoutChoices(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	_, consumed := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3})
	if !consumed {
		t.Fatal("expected F3 to be consumed even with no project choices (no-op)")
	}
	view := u.shell.View().Content
	if strings.Contains(view, "Projects") {
		t.Fatalf("expected no project picker overlay without choices:\n%s", view)
	}
}

func TestChatUISessionPickerEscCloses(t *testing.T) {
	overlay := &sessionPickerOverlay{ui: nil, sessions: []ChatSession{{ID: "a", Title: "A"}}}
	_, _, done := overlay.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !done {
		t.Fatal("expected Esc to close the session picker overlay")
	}
}

func TestChatUICtrlRRefreshesLastRecordSet(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := store.AppendUser(ctx, sessions.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	doc := "from: {name: Invoice}\nlimit: 20"
	query, err := store.AppendQuery(ctx, sessions.activeID, origin.ID, "sqlite:///chinook.db", QueryResult{Title: "Invoices", DTQL: doc, SourceID: "chinook", Result: secureread.Result{Columns: []string{"InvoiceId"}}})
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 412}}}}}
	sessions.ConfigureQueryExecutor(executor)

	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.lastGridRecordSetID = query.RecordSetID

	cmd, consumed := u.globalKeys(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if !consumed {
		t.Fatal("expected Ctrl+R to be consumed by globalKeys")
	}
	drainCmd(t, u, cmd)
	if executor.calls != 1 {
		t.Fatalf("expected the refresh to re-run the saved DTQL once, got %d calls", executor.calls)
	}
}

// Ported from workspace_test.go's TestProjectPickerSelectsConfiguredProject:
// the Down-navigation half TestChatUIF3OpensProjectPickerAndSelects above
// doesn't already cover (that one selects the first/default choice; this
// one moves down to the second before selecting).
func TestChatUIProjectPickerDownThenEnterSelectsSecondChoice(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	u.SetProjectChoices([]ProjectChoice{{Key: "/projects/chinook", Title: "Chinook"}, {Key: "sales", Title: "Sales"}})

	cmd, consumed := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3})
	if !consumed {
		t.Fatal("expected F3 to be consumed by globalKeys")
	}
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "Sales") {
		t.Fatalf("project picker did not open:\n%s", view)
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, quitCmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := u.SelectedProject(); got != "sales" || quitCmd == nil {
		t.Fatalf("project switch = %q, quit command = %v", got, quitCmd)
	}
}

// TestChatUIAltSCyclesAllTablesAndRestoresSessionStyle is ported from
// ui_test.go's TestAltSCyclesAllTablesAndRestoresSessionStyle: same
// scenario (Alt+S cycles every tracked grid's style, persists it, a new/
// reopened session picks it up), driven through ChatUI's real production
// wiring instead of u.entries/u.tableStyle/u.styleNotice/u.statusLines.
// The self-expiring shortcut-hint swap (tableStyleNoticeExpired) has no
// ChatUI equivalent — see cycleTableStyle's own comment — so this checks
// the AppendAssistant confirmation it uses instead.
func TestChatUIAltSCyclesAllTablesAndRestoresSessionStyle(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	result := secureread.Result{Columns: []string{"ID"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1}}}}
	agent := &contextualStub{turns: []Turn{{Queries: []QueryResult{{Result: result}, {Result: result}}}}}
	sessions, err := NewSessionChat(ctx, store, agent, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	drainCmd(t, u, u.Submit("Show two"))
	if len(u.gridsByRecordSetID) != 2 {
		t.Fatalf("expected 2 grids before cycling style, got %d", len(u.gridsByRecordSetID))
	}
	cmd, consumed := u.globalKeys(tea.KeyPressMsg{Code: 's', Mod: tea.ModAlt})
	if !consumed {
		t.Fatal("expected Alt+S to be consumed by globalKeys")
	}
	drainCmd(t, u, cmd)
	if u.tableStyle.Name != grid.StyleSoft.Name {
		t.Fatalf("style after Alt+S = %s, want Soft", u.tableStyle.Name)
	}
	if !strings.Contains(u.shell.View().Content, "Table style: Soft") {
		t.Fatalf("expected a style-change confirmation in view:\n%s", u.shell.View().Content)
	}
	for _, g := range u.gridsByRecordSetID {
		if g.Style().Name != grid.StyleSoft.Name {
			t.Fatal("existing result did not adopt the style")
		}
	}
	storedStyle, err := sessions.TableStyle(ctx)
	if err != nil || storedStyle != "Soft" {
		t.Fatalf("persisted style = %q, %v", storedStyle, err)
	}
	newSession, err := sessions.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(newSession)
	if u.tableStyle.Name != grid.StyleSoft.Name {
		t.Fatal("new session lost the shared table style")
	}
	reopened, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil || reopened.tableStyle.Name != grid.StyleSoft.Name {
		t.Fatalf("restored style = %v, %v", reopened.tableStyle, err)
	}
}

// TestChatUIMacOptionSCyclesTableStyle is ported from
// ui_test.go's TestMacOptionSCyclesTableStyle.
func TestChatUIMacOptionSCyclesTableStyle(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd, consumed := u.globalKeys(tea.KeyPressMsg{Code: 'ß', Text: "ß"})
	if !consumed {
		t.Fatal("expected ß (Option+S) to be consumed by globalKeys")
	}
	drainCmd(t, u, cmd)
	if u.tableStyle.Name != grid.StyleSoft.Name {
		t.Fatalf("style after Option+S = %s, want Soft", u.tableStyle.Name)
	}
	if !strings.Contains(u.shell.View().Content, "Table style: Soft") {
		t.Fatalf("expected a style-change confirmation in view:\n%s", u.shell.View().Content)
	}
}
