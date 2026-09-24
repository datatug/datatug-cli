package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
