package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func newTestChatUI(t *testing.T, agent ContextualConversation, turns ...Turn) (*ChatUI, *SessionChat) {
	t.Helper()
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	stub := agent
	if stub == nil {
		stub = &contextualStub{turns: turns}
	}
	sessions, err := NewSessionChat(ctx, store, stub, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return u, sessions
}

// drainCmd runs cmd (and, since StartStream batches a stream.Start +
// spinner.Tick tea.Cmd via tea.Batch, every command batched inside it) and
// feeds every resulting tea.Msg back into u.shell.Update, the same pump a
// live tea.Program performs — until no further commands are produced. It
// lets these tests drive a real Submit -> StartStream -> stream.EventMsg/
// DoneMsg -> OnStreamDone round trip synchronously.
func drainCmd(t *testing.T, u *ChatUI, cmd tea.Cmd) {
	t.Helper()
	pending := []tea.Cmd{cmd}
	for i := 0; i < 200 && len(pending) > 0; i++ {
		next := pending[0]
		pending = pending[1:]
		if next == nil {
			continue
		}
		msg := next()
		if msg == nil {
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			pending = append(pending, batch...)
			continue
		}
		var model tea.Model
		var followUp tea.Cmd
		model, followUp = u.shell.Update(msg)
		_ = model
		if followUp != nil {
			pending = append(pending, followUp)
		}
	}
}

func TestChatUISubmitAsksAndRendersGrid(t *testing.T) {
	turn := Turn{Text: "Found one.", Queries: []QueryResult{{Title: "Customers", RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"CustomerId", "City"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1, "City": "Prague"}}},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	cmd := u.Submit("Show a Prague customer")
	drainCmd(t, u, cmd)

	view := u.shell.View().Content
	for _, want := range []string{"Found one.", "CustomerId", "City", "Prague"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestChatUISubmitWithNoQueriesAppendsFallbackText(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd := u.Submit("nonsense")
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "couldn't construct a valid query") {
		t.Errorf("expected fallback text in view:\n%s", view)
	}
}

func TestChatUISlashNewStartsFreshSession(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	firstID := u.sessionID
	cmd := u.Submit("/new")
	drainCmd(t, u, cmd)
	if u.sessionID == firstID {
		t.Fatal("expected /new to switch to a new session")
	}
	list, err := sessions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < 2 {
		t.Fatalf("expected at least 2 sessions after /new, got %d", len(list))
	}
}

func TestChatUISlashClearRequiresConfirm(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd := u.Submit("/clear")
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "type /clear confirm") {
		t.Fatalf("expected confirmation prompt in view:\n%s", view)
	}
}

func TestChatUISlashHelpListsCommands(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd := u.Submit("/help")
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "/new") || !strings.Contains(view, "/switch") {
		t.Fatalf("expected /help to list commands in view:\n%s", view)
	}
}

func TestChatUIUnknownCommandReportsError(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd := u.Submit("/bogus")
	drainCmd(t, u, cmd)
	view := u.shell.View().Content
	if !strings.Contains(view, "unknown chat command") {
		t.Fatalf("expected unknown-command error in view:\n%s", view)
	}
}

func TestChatUIJoinCandidatesWireIntoJoinBlock(t *testing.T) {
	// A RecordSet with FK-join candidates renders as a JoinBlock (not a bare
	// grid.Model) — checklist item #3 wired end to end. Ported from
	// ui.go's TestUIInlineFKJoinNavigationAndApply setup: a real persisted
	// RecordSet (store.AppendQuery, matching how SessionChat.ask's query
	// observer persists a live agent's queries) plus a configured
	// ForeignKeyJoinApplication.
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	query, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Invoices", DTQL: "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}})
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	block := u.blockForGrid(nil, query.RecordSetID)
	if _, ok := block.(*JoinBlock); !ok {
		// nil grid.Model is fine for this check: blockForGrid only needs to
		// decide which wrapper to use before touching Grid's own methods.
		t.Fatalf("expected a *JoinBlock for a RecordSet with join candidates, got %T", block)
	}
}

func TestChatUITopBarAndStatusBarShowProjectAndSession(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = ProjectCatalog{Title: "Demo"}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := u.shell.View().Content
	if !strings.Contains(view, "Demo") {
		t.Fatalf("expected project title in top bar:\n%s", view)
	}
	if !strings.Contains(view, "model: fake-model") {
		t.Fatalf("expected model name in status bar:\n%s", view)
	}
}

func TestChatUIMarkdownHTTPResponseRendersFormatted(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	ctx := context.Background()
	user, err := sessions.store.AppendUser(ctx, sessions.activeID, "/http get https://example.com/doc.md")
	if err != nil {
		t.Fatal(err)
	}
	response := HTTPResponse{Method: "GET", URL: "https://example.com/doc.md", StatusCode: 200, ContentType: "text/markdown", Body: []byte("# Heading\n\nSome *text*.")}
	if _, err := sessions.store.AppendHTTPResponse(ctx, sessions.activeID, user.ID, response, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.store.Load(ctx, sessions.activeID)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)
	view := u.shell.View().Content
	if !strings.Contains(view, "Heading") {
		t.Fatalf("expected rendered markdown heading in view:\n%s", view)
	}
}

func TestChatUIAskUsesStreamAsk(t *testing.T) {
	// StreamAsk shares SessionChat.Ask's persistence path (query/workspace/
	// bookmark observers via ctx, AppendUser, a final AppendTurn) — verify a
	// turn asked through ChatUI.Submit lands in the durable session, not
	// just the transcript view.
	turn := Turn{Text: "Found one.", Queries: []QueryResult{{Title: "Customers", RecordSetID: "rs1", Result: secureread.Result{
		Columns: []string{"CustomerId"},
		Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 1}}},
	}}}}
	u, sessions := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show a customer"))
	snapshot, err := sessions.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RecordSets) != 1 {
		t.Fatalf("expected the streamed turn's query to be persisted, got %d RecordSets", len(snapshot.RecordSets))
	}
}

// TestChatUIInlineFKJoinNavigationAndApply is ported from ui_test.go's
// TestUIInlineFKJoinNavigationAndApply: JoinBlock's own key mechanics
// (navigate/details/apply) already have dedicated unit tests in
// join_block_test.go, and TestChatUIJoinCandidatesWireIntoJoinBlock above
// already checks blockForGrid wires a JoinBlock in. This is the remaining
// real gap: the full round trip through ChatUI's actual production wiring
// (focus the grid -> "j" -> navigate -> Space applies -> result persists
// and re-renders as the joined grid), which unit tests of the pieces don't
// cover.
func TestChatUIInlineFKJoinNavigationAndApply(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	defer func() { _ = store.Close() }()
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, sessions.activeID, "Show invoices")
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Invoices", DTQL: "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}})
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	view := ansi.Strip(u.shell.View().Content)
	if !strings.Contains(view, "You can") || !strings.Contains(view, "JOIN") || !strings.Contains(view, "Customer") {
		t.Fatalf("inline FK area missing:\n%s", view)
	}
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid could not be focused")
	}
	u.shell.Update(tea.KeyPressMsg{Text: "j"})
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(u.shell.View().Content, "Invoice.CustomerId") {
		t.Fatal("exact FK details not displayed")
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	u.shell.Update(tea.KeyPressMsg{Text: "j"})
	_, cmd := u.shell.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if cmd == nil {
		t.Fatal("Space did not invoke JOIN application")
	}
	drainCmd(t, u, cmd)
	snapshot, err := sessions.Snapshot(ctx)
	if err != nil || len(snapshot.RecordSets) != 2 {
		t.Fatalf("JOIN result was not persisted: %d RecordSets, %v", len(snapshot.RecordSets), err)
	}
	if snapshot.RecordSets[base.RecordSetID].Lineage != nil {
		t.Fatal("base RecordSet was mutated")
	}
	if !strings.Contains(u.shell.View().Content, "Invoices + Customer") {
		t.Fatal("joined grid not rendered")
	}
}

// TestChatUIShowsAppliedLimitationsIncludingEmptyResults is ported from
// ui_test.go's TestUIShowsAppliedLimitationsIncludingEmptyResults.
func TestChatUIShowsAppliedLimitationsIncludingEmptyResults(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{Result: secureread.Result{
		Columns: []string{"CustomerId"},
		Limitations: []secureread.Limitation{
			{Kind: secureread.LimitationPolicy, Note: `access: policy "support" restricted the query`},
			{Kind: secureread.LimitationRowsFiltered},
			{Kind: secureread.LimitationHiddenColumns, Columns: []string{"Email"}},
		},
	}}}}
	u, _ := newTestChatUI(t, nil, turn)
	drainCmd(t, u, u.Submit("Show customers"))
	// Word-wrapped rendering can split a checked phrase across a line
	// boundary; collapse runs of whitespace (including the wrap newline)
	// to a single space before matching, like the /help check below.
	view := strings.Join(strings.Fields(ansi.Strip(u.shell.View().Content)), " ")
	for _, want := range []string{"No rows returned.", `policy "support"`, "rows filtered by policy", "hidden columns: Email"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

// TestChatUISlashHelpDocumentsGridControls is ported from ui_test.go's
// TestSessionHelpDocumentsRecordsetAndExistingGridControls: /help must
// still document every grid key handleGridKey wires (chatui_inspector.go),
// not just the session-management commands
// TestChatUISlashHelpListsCommands already checks.
func TestChatUISlashHelpDocumentsGridControls(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("/help"))
	view := strings.Join(strings.Fields(ansi.Strip(u.shell.View().Content)), " ")
	for _, want := range []string{
		"1 Table", "2 Charts", "3 Current row", "Tab panes", "Shift+↑↓ select",
		"j JOINs", "Space row", "c cell", "r range", "a attach", "d dock", "b bookmark", "s sort", "Enter details", "Esc composer",
		"F3", "F4", "Ctrl+C",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("help missing %q: %s", want, view)
		}
	}
}
