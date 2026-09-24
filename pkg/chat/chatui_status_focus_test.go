package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// TestChatUIStatusBarMessageFocusedHint is ui.go's u.messageFocused hint set
// (r1b item 5a), ported: a focused user-message card (transcriptEntryKinds
// tags it "message" when ChatUI appends it, see loadSession) shows
// "message selected · Enter edit", not the default composer hint set.
func TestChatUIStatusBarMessageFocusedHint(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///fixture.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendUser(ctx, sessions.activeID, "earlier question"); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	var id string
	for entryID, kind := range u.transcriptEntryKinds {
		if kind == transcriptEntryKindMessage {
			id = entryID
		}
	}
	if id == "" {
		t.Fatal("expected a tracked message entry after loadSession")
	}
	if !u.shell.FocusEntry(id) {
		t.Fatal("could not focus the user message block")
	}
	status := u.statusBar(160)
	if !strings.Contains(status, "message selected") || !strings.Contains(status, "Enter edit") {
		t.Fatalf("expected message-focused hint set:\n%s", status)
	}
}

// TestChatUIStatusBarHTTPDocumentFocusedHint is ui.go's u.messageFocused
// HTTP-document variant.
func TestChatUIStatusBarHTTPDocumentFocusedHint(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	ctx := context.Background()
	user, err := sessions.store.AppendUser(ctx, sessions.activeID, "/http get https://example.com/doc")
	if err != nil {
		t.Fatal(err)
	}
	response := HTTPResponse{Method: "GET", URL: "https://example.com/doc", StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}
	if _, err := sessions.store.AppendHTTPResponse(ctx, sessions.activeID, user.ID, response, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sessions.store.Load(ctx, sessions.activeID)
	if err != nil {
		t.Fatal(err)
	}
	u.loadSession(snapshot)

	var id string
	for entryID, kind := range u.transcriptEntryKinds {
		if kind == transcriptEntryKindHTTP {
			id = entryID
		}
	}
	if id == "" {
		t.Fatal("expected a tracked HTTP document entry after loadSession")
	}
	if !u.shell.FocusEntry(id) {
		t.Fatal("could not focus the HTTP document block")
	}
	status := u.statusBar(160)
	if !strings.Contains(status, "HTTP document") || !strings.Contains(status, "1 Rendered") || !strings.Contains(status, "2 Raw") || !strings.Contains(status, "3 Headers") {
		t.Fatalf("expected HTTP-document hint set:\n%s", status)
	}
}

// TestChatUIStatusBarWorkspaceFocusedHint is ui.go's u.workspaceFocused
// hint set, including its tab==1 (Selected) and tab==3 (Bookmarks) variants.
func TestChatUIStatusBarWorkspaceFocusedHint(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})

	status := u.statusBar(160)
	if !strings.Contains(status, "←→ tabs") || !strings.Contains(status, "b bookmark") {
		t.Fatalf("expected default workspace-focused hint set:\n%s", status)
	}

	u.workspace.tab = 1
	status = u.statusBar(160)
	if !strings.Contains(status, "1 row") || !strings.Contains(status, "2 column") || !strings.Contains(status, "3 recordset") {
		t.Fatalf("expected Selected-tab hint set:\n%s", status)
	}

	u.workspace.tab = 3
	status = u.statusBar(160)
	if !strings.Contains(status, "↑↓ browse") || !strings.Contains(status, "r rename") {
		t.Fatalf("expected Bookmarks-tab hint set:\n%s", status)
	}

	u.workspace.bookmarkGridFocused = true
	status = u.statusBar(160)
	if !strings.Contains(status, "Tab list") || !strings.Contains(status, "s sort") {
		t.Fatalf("expected bookmark-grid-focused hint set:\n%s", status)
	}
	u.workspace.bookmarkGridFocused = false

	u.workspace.bookmarkMode = "rename"
	status = u.statusBar(160)
	if !strings.Contains(status, "Bookmark rename") || !strings.Contains(status, "Enter apply") {
		t.Fatalf("expected bookmark-input-mode hint set:\n%s", status)
	}
}

// TestChatUIStatusBarJoinFocusedHint is ui.go's u.joinFocused hint set,
// overriding the plain grid hint set exactly as ui.go's separate
// (non-else) `if u.joinFocused` block did.
func TestChatUIStatusBarJoinFocusedHint(t *testing.T) {
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
	if _, err := store.AppendQuery(ctx, sessions.activeID, user.ID, "sqlite:///fixture.db", QueryResult{
		Title: "Invoices", DTQL: "from: {name: Invoice}\ncolumns: [{field: InvoiceId}]\nlimit: 5\n",
		Result: secureread.Result{Columns: []string{"InvoiceId"}, Rows: []secureread.Row{{Data: map[string]any{"InvoiceId": 1}}}},
	}); err != nil {
		t.Fatal(err)
	}
	sessions.ConfigureJoinApplication(ForeignKeyJoinApplication{Source: "sqlite:///fixture.db", Snapshot: joinSnapshot(), Executor: &joinExecutorStub{}})
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid could not be focused")
	}

	before := u.statusBar(160)
	if !strings.Contains(before, "j JOIN") {
		t.Fatalf("expected plain grid hint set before entering JOIN mode:\n%s", before)
	}

	u.shell.Update(tea.KeyPressMsg{Text: "j"})
	after := u.statusBar(160)
	if !strings.Contains(after, "JOIN candidates") || !strings.Contains(after, "Space add JOIN") {
		t.Fatalf("expected JOIN-focused hint set after pressing j:\n%s", after)
	}
	if strings.Contains(after, "j JOIN") {
		t.Fatalf("JOIN-focused hint set should replace, not extend, the grid hint set:\n%s", after)
	}
}
