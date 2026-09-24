package chat

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestWorkspacePanelViewShowsTabsAndProjectExplorer(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = ProjectCatalog{ID: "proj1", Title: "Demo", Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "project", ObjectID: "proj1", Title: "Demo"}},
		{Reference: ContextReference{Kind: "source", SourceID: "src1", Title: "sqlite"}},
	}}
	view := u.workspace.View(40, 15, true)
	if !strings.Contains(view, "Proj") || !strings.Contains(view, "Marks") {
		t.Fatalf("expected tab labels in view:\n%s", view)
	}
	if !strings.Contains(view, "Demo") {
		t.Fatalf("expected project explorer content in view:\n%s", view)
	}
}

func TestWorkspacePanelLeftRightSwitchesTabs(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if u.workspace.tab != 0 {
		t.Fatalf("expected initial tab 0, got %d", u.workspace.tab)
	}
	u.workspace.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if u.workspace.tab != 1 {
		t.Fatalf("expected right/l to advance tab to 1, got %d", u.workspace.tab)
	}
	u.workspace.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if u.workspace.tab != 0 {
		t.Fatalf("expected left/h to return to tab 0, got %d", u.workspace.tab)
	}
}

func TestWorkspacePanelBookmarksTabListsBookmarks(t *testing.T) {
	u, sessions := newTestChatUI(t, nil, Turn{})
	drainCmd(t, u, u.Submit("Show invoices"))
	// Focus a grid-less selection path isn't wired yet; exercise the
	// bookmark listing/empty-state text directly instead.
	u.workspace.tab = 3
	view := u.workspace.View(40, 15, true)
	if !strings.Contains(view, "No bookmarks yet") {
		t.Fatalf("expected empty bookmarks state:\n%s", view)
	}
	_ = sessions
}

func TestWorkspacePanelDockedTabEmptyState(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 2
	view := u.workspace.View(40, 15, true)
	if !strings.Contains(view, "Nothing docked") {
		t.Fatalf("expected empty dock state:\n%s", view)
	}
}

func TestWorkspacePanelSelectedTabEmptyState(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.workspace.tab = 1
	view := u.workspace.View(40, 15, true)
	if !strings.Contains(view, "No durable selection") {
		t.Fatalf("expected empty-selection state:\n%s", view)
	}
}

// TestChatUIBookmarkWorkspaceTabOpensStructuredGrid is ported from the
// legacy UI's bookmark_phase4_test.go (TestBookmarkWorkspaceTabOpensStructuredGrid):
// same scenario (a bookmark's workspace tab renders its structured grid,
// Enter focuses it, "a" attaches it via the shared WorkspaceAction path),
// driven through workspacePanel directly (u.workspace.setTab/updateKey)
// instead of u.focusWorkspace/u.setWorkspaceTab/u.updateWorkspaceKey.
func TestChatUIBookmarkWorkspaceTabOpensStructuredGrid(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	catalog := ProjectCatalog{ID: testScope().ProjectID, Title: "Demo"}
	chatSessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", catalog)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := chatSessions.Snapshot(ctx)
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := chatSessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: recordID}, Title: "Saved customers"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, chatSessions, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.workspace.setTab(3)
	view := u.workspace.View(80, 30, true)
	if len(u.workspace.bookmarkItems) != 1 || !strings.Contains(view, "Saved customers") || !strings.Contains(view, "Source: chinook") || !strings.Contains(view, "snapshot:") || !strings.Contains(view, "DTQL:") {
		t.Fatalf("bookmark tab did not render: %+v", u.workspace.bookmarkItems)
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !u.workspace.bookmarkGridFocused || u.workspace.bookmarkGrid == nil || len(u.workspace.bookmarkGrid.Rows()) != 3 {
		t.Fatalf("bookmark grid did not open: %+v", u.workspace.bookmarkGrid)
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'a', Text: "a"})
	snapshot, err := chatSessions.Snapshot(ctx)
	if err != nil || len(snapshot.Workspace.Attachments) != 1 || !sameReference(snapshot.Workspace.Attachments[0], ref) {
		t.Fatalf("UI attachment did not use shared action: %+v, %v", snapshot.Workspace.Attachments, err)
	}
}

// TestChatUIEmptyBookmarkedGridNavigation is ported from the legacy UI's
// bookmark_phase4_test.go (TestEmptyBookmarkedGridNavigation).
func TestChatUIEmptyBookmarkedGridNavigation(t *testing.T) {
	ctx := context.Background()
	scope := testScope()
	store := openTestStore(t, testStorePath(t), scope)
	chatSessions, err := NewSessionChat(ctx, store, &contextualStub{}, scope.Sources[scope.Database], ProjectCatalog{ID: scope.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	session, err := chatSessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	user, err := store.AppendUser(ctx, session.ID, "show missing rows")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.AppendTurn(ctx, session.ID, user.ID, scope.Sources[scope.Database], Turn{Queries: []QueryResult{{
		Title: "Empty", DTQL: "from: {name: Customer}\nlimit: 1", SourceID: scope.Database,
		Result: secureread.Result{Columns: []string{"CustomerId"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chatSessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "bookmark_create", Reference: ContextReference{Kind: "recordset", ObjectID: turn.Queries[0].RecordSetID}}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, chatSessions, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	u.workspace.setTab(3)
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyDown})
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if u.workspace.bookmarkGrid == nil || len(u.workspace.bookmarkGrid.Rows()) != 0 {
		t.Fatalf("empty bookmark grid navigation = %+v", u.workspace.bookmarkGrid)
	}
}

func TestWorkspacePanelF6TogglesPanelVisibility(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.shell.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	before := strings.Contains(u.shell.View().Content, "Marks")
	if !before {
		t.Fatalf("expected the workspace pane to render at width 120 before F6:\n%s", u.shell.View().Content)
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	after := strings.Contains(u.shell.View().Content, "Marks")
	if after {
		t.Fatal("expected F6 to hide the workspace pane")
	}
}
