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

// TestChatUIStatusBarWorkspaceFocusedHint covers the distinct Explorer and
// Inspect controls, including Tab cycling and Bookmarks states.
func TestChatUIStatusBarWorkspaceFocusedHint(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	u.shell.SetLeftPanel(u.explorer)
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	u.shell.FocusLeftPanel()

	status := u.statusBar(160)
	if !strings.Contains(status, "type to search") || !strings.Contains(status, "←→ tree") || !strings.Contains(status, "Ctrl+P Actions") {
		t.Fatalf("expected Explorer hint set:\n%s", status)
	}

	u.shell.FocusRightPanel()
	status = u.statusBar(160)
	if !strings.Contains(status, "Tab/Shift+Tab Row/Column/Result") || !strings.Contains(status, "F10 Related") || strings.Contains(status, "←→ tree") {
		t.Fatalf("expected Inspect hint set:\n%s", status)
	}

	beforeSubTab := u.workspace.inspectorSubTab
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if u.workspace.inspectorSubTab != (beforeSubTab+1)%3 {
		t.Fatalf("Tab should cycle the Inspect view: got %d after %d", u.workspace.inspectorSubTab, beforeSubTab)
	}
	status = u.statusBar(160)
	if !strings.Contains(status, "Tab/Shift+Tab Row/Column/Result") {
		t.Fatalf("expected Inspect hint after cycling view:\n%s", status)
	}

	u.workspace.tab = 3
	status = u.statusBar(160)
	if !strings.Contains(status, "↑↓ bookmarks") || !strings.Contains(status, "Ctrl+P Actions") {
		t.Fatalf("expected Bookmarks-tab hint set:\n%s", status)
	}

	u.workspace.bookmarkGridFocused = true
	status = u.statusBar(160)
	if !strings.Contains(status, "↑↓ rows") || !strings.Contains(status, "Ctrl+P Actions") {
		t.Fatalf("expected bookmark-grid-focused hint set:\n%s", status)
	}
	u.workspace.bookmarkGridFocused = false

	u.workspace.bookmarkMode = "rename"
	status = u.statusBar(160)
	if !strings.Contains(status, "Bookmark rename") || !strings.Contains(status, "Enter apply") {
		t.Fatalf("expected bookmark-input-mode hint set:\n%s", status)
	}
}

// TestChatUIStatusBarComposerChipHints covers attachment controls and the
// focus-return hint while preserving the composer draft.
func TestChatUIStatusBarComposerChipHints(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	u.snapshot.Workspace.Attachments = []ContextReference{{Kind: "bookmark", ObjectID: "saved"}}
	u.syncChips()

	wide := u.statusBar(160)
	for _, want := range []string{"Tab attachments", "Esc previous control", "Enter send", "F6 Explorer"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("expected the composer-chip hint set to contain %q:\n%s", want, wide)
		}
	}

	// statusBar renders through the shared theme.RenderHints chrome now
	// (strongo/aichat#chat-shared-look), which separates hints with its own
	// spacing rather than DataTug's old literal " · " join -- check each
	// compact hint is present instead of one exact joined string.
	narrow := u.statusBar(80)
	for _, want := range []string{"Esc back", "Enter send", "F7 Inspect"} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("statusBar(80) = %q, want it to contain the compact composer-chip hint %q", narrow, want)
		}
	}

	// Once attachments are gone, the default composer hint set returns even
	// on a narrow terminal.
	u.snapshot.Workspace.Attachments = nil
	u.syncChips()
	afterClear := u.statusBar(80)
	if strings.Contains(afterClear, "Tab attachments") {
		t.Fatalf("expected the attachment hint set to disappear once attachments are cleared:\n%s", afterClear)
	}
}

// TestChatUIStatusBarJoinFocusedHint keeps bare j in grid search and checks
// that the explicit Actions command opens the secure relationship chooser.
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
	if !strings.Contains(before, "Ctrl+P Actions") {
		t.Fatalf("expected an explicit JOIN chooser hint:\n%s", before)
	}

	workspaceRoutingKey(u, "j")
	if strings.Contains(u.shell.View().Content, "JOIN relationship") {
		t.Fatal("bare j should not open the JOIN chooser")
	}
	workspaceRoutingKey(u, "esc") // clear the single-character search
	workspaceRoutingKey(u, "esc") // leave search mode, retaining the grid
	uxSearchAction(t, u, "JOIN relationship")
	view := u.shell.View().Content
	if !strings.Contains(view, "JOIN relationship") {
		t.Fatalf("Actions did not open the relationship chooser:\n%s", view)
	}
	if !strings.Contains(view, "Customer") || !strings.Contains(view, "many-to-one") {
		t.Fatalf("JOIN chooser did not show the verified relationship candidate:\n%s", view)
	}
}
