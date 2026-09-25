package chat

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/theme"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestWorkspacePanelViewShowsTabsAndProjectExplorer(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = ProjectCatalog{ID: "proj1", Title: "Demo", Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "project", ObjectID: "proj1", Title: "Demo"}},
		{Reference: ContextReference{Kind: "source", SourceID: "src1", Title: "sqlite"}},
	}}
	view := u.workspace.View(40, 15, true)
	// width 40 is exactly wide enough for the full tab strip ("●
	// Project · Inspect · Docked · Bookmarks" is 40 columns) --
	// tabStripHeader prefers full labels whenever they fit (founder
	// 2026-09-25, r9: "full names when wide").
	if !strings.Contains(view, "Project") || !strings.Contains(view, "Bookmarks") {
		t.Fatalf("expected tab labels in view:\n%s", view)
	}
	if !strings.Contains(view, "Demo") {
		t.Fatalf("expected project explorer content in view:\n%s", view)
	}
}

// TestWorkspacePanelInLightThemeUsesLightSurfaceNoHardcodedDarkBackground
// covers a real regression, founder-flagged verbatim: "project explorer in
// light theme is black - wrong". panelCard/explorerNodesView/bookmarksView
// used to hardcode ANSI-256 dark greys (238/235 background, a selected-row
// "57" background) regardless of tui/theme.Dark; in a light terminal that
// rendered the whole side panel as a near-black band. They now read
// theme.SurfaceColors()/FocusSurfaceColors() fresh on every render, so this
// asserts (a) none of the old literal dark codes reappear and (b) the
// panel actually carries theme's LIGHT surface background when
// theme.Dark is false.
func TestWorkspacePanelInLightThemeUsesLightSurfaceNoHardcodedDarkBackground(t *testing.T) {
	theme.SetDark(false)
	t.Cleanup(func() { theme.SetDark(true) })

	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = ProjectCatalog{ID: "proj1", Title: "Demo", Objects: []ProjectObject{
		{Reference: ContextReference{Kind: "project", ObjectID: "proj1", Title: "Demo"}},
		{Reference: ContextReference{Kind: "source", SourceID: "src1", Title: "sqlite"}},
	}}
	u.workspace.explorerIndex = 1 // select a row so the FocusSurfaceColors-highlighted branch renders too.
	view := u.workspace.View(40, 15, true)

	for _, stale := range []string{"48;5;238", "48;5;235", "48;5;57", "38;5;255", "38;5;252", "38;5;229", "38;5;231", "38;5;244"} {
		if strings.Contains(view, "\x1b["+stale+"m") || strings.Contains(view, "\x1b[1;"+stale+"m") {
			t.Fatalf("panel view still carries a hardcoded ANSI-256 colour %q, want it to come from tui/theme: %q", stale, view)
		}
	}
	bg, fg := theme.SurfaceColors()
	probe := lipgloss.NewStyle().Foreground(fg).Background(bg).Render("x")
	prefix := strings.TrimSuffix(probe, "x\x1b[m")
	if prefix == "" || prefix == probe {
		t.Fatalf("could not derive a probe SGR prefix from theme.SurfaceColors(): %q", probe)
	}
	if !strings.Contains(view, prefix) {
		t.Fatalf("expected the light-theme surface fill %q somewhere in the panel view: %q", prefix, view)
	}
}

// TestWorkspacePanelTabAndShiftTabSwitchTabs covers Tab/Shift+Tab, the
// panel's tab-switch bindings -- h/l are the Project explorer's own
// fold/unfold keys (like main's ui.go), not a tab switch, so they no longer
// belong here (see TestExplorerArrowsFoldTreeAndTabSwitchesWorkspace for
// h/l's real behaviour).
func TestWorkspacePanelTabAndShiftTabSwitchTabs(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	if u.workspace.tab != 0 {
		t.Fatalf("expected initial tab 0, got %d", u.workspace.tab)
	}
	u.workspace.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if u.workspace.tab != 1 {
		t.Fatalf("expected tab to advance tab to 1, got %d", u.workspace.tab)
	}
	u.workspace.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if u.workspace.tab != 0 {
		t.Fatalf("expected shift+tab to return to tab 0, got %d", u.workspace.tab)
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

// The tests below are ported from the legacy UI's workspace_test.go. Tests
// that exercise SessionChat/store logic only (no UI/ChatUI type) are
// unchanged in workspace_test.go.

// Ported from TestProjectExplorerShowsSourceIssueInPlace.
func TestChatUIProjectExplorerShowsSourceIssueInPlace(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	u.catalog.Objects[1].Issue = "Schema unavailable: relation main.Customer has no columns file"
	nodes := u.workspace.explorerNodes()
	found := false
	for i, node := range nodes {
		if node.id == "source:chinook-local" {
			u.workspace.explorerIndex = i
		}
		if node.issue && node.id == "source:chinook-local:issue" && node.objectIndex == -1 && strings.Contains(node.label, "main.Customer") {
			found = true
		}
	}
	if !found {
		t.Fatalf("source-local error node missing: %+v", nodes)
	}
	view := u.workspace.projectExplorer(100, 20)
	if !strings.Contains(view, "Status: Schema unavailable") || !strings.Contains(view, "Customer") {
		t.Fatalf("source details did not show the schema error: %q", view)
	}
	u.workspace.explorerCollapsed["source:chinook-local"] = true
	found = false
	for _, node := range u.workspace.explorerNodes() {
		if node.id == "source:chinook-local" && node.issue && strings.Contains(node.label, "⚠") {
			found = true
		}
		if node.id == "source:chinook-local:issue" {
			t.Fatal("collapsed source still shows its error child")
		}
	}
	if !found {
		t.Fatal("collapsed source lost its warning indicator")
	}
}

// Ported from TestProjectExplorerIssueDetailsRemainVisibleInLongTree.
func TestChatUIProjectExplorerIssueDetailsRemainVisibleInLongTree(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	u.catalog.Objects[1].Issue = "Schema unavailable: relation main.Customer has no columns file"
	for i := range 30 {
		object := ProjectObject{Reference: ContextReference{
			Kind: "source", SourceID: fmt.Sprintf("extra-%02d", i), ObjectID: fmt.Sprintf("extra-%02d", i), Title: "Extra source",
		}}
		if i == 20 {
			object.Issue = "Schema unavailable: deep source failed"
		}
		u.catalog.Objects = append(u.catalog.Objects, object)
	}
	for i, node := range u.workspace.explorerNodes() {
		if node.id == "source:chinook-local:issue" {
			u.workspace.explorerIndex = i
			break
		}
	}
	view := u.workspace.projectExplorer(80, 8)
	if !strings.Contains(view, "Status: Schema unavailable") {
		t.Fatalf("selected issue details hidden below long explorer: %q", view)
	}
	for i, node := range u.workspace.explorerNodes() {
		if node.id == "source:extra-20:issue" {
			u.workspace.explorerIndex = i
			break
		}
	}
	view = u.workspace.projectExplorer(80, 8)
	if !strings.Contains(view, "Status: Schema unavailable: deep source failed") {
		t.Fatalf("deep selected issue details hidden below viewport: %q", view)
	}
}

// Ported from TestExplorerGroupsObjectsByDeclaredSourceAndCollapses.
func TestChatUIExplorerGroupsObjectsByDeclaredSourceAndCollapses(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	u.catalog = workspaceTestCatalog()
	u.catalog.Objects = append(u.catalog.Objects,
		ProjectObject{Reference: ContextReference{Kind: "query", ProjectID: "chinook", SourceID: "chinook-local", ObjectID: "by-city", Title: "By city"}},
		ProjectObject{Reference: ContextReference{Kind: "query", ProjectID: "chinook", ObjectID: "unbound", Title: "Unbound"}},
	)
	nodes := u.workspace.explorerNodes()
	var sourceDepth, boundDepth, unboundDepth int
	for _, node := range nodes {
		switch node.label {
		case "Chinook local":
			sourceDepth = node.depth
		case "By city":
			boundDepth = node.depth
		case "Unbound":
			unboundDepth = node.depth
		}
	}
	if sourceDepth != 2 || boundDepth != 2 || unboundDepth != 2 {
		t.Fatalf("unexpected explorer hierarchy: %+v", nodes)
	}
	var databases, queryGroups int
	for _, node := range nodes {
		if node.label == "Databases (1)" {
			databases++
		}
		if strings.HasPrefix(node.label, "Queries (") {
			queryGroups++
		}
	}
	if databases != 1 || queryGroups != 1 {
		t.Fatalf("want one Databases and one Queries group: %+v", nodes)
	}
	u.workspace.explorerCollapsed["source:chinook-local"] = true
	for _, node := range u.workspace.explorerNodes() {
		if node.label == "Customer" {
			t.Fatalf("collapsed source still exposes child %q", node.label)
		}
	}
}

// Ported from TestWorkspaceSplitAndKeyboardSelection. The mouse-click
// attachment-close affordance (ui.go's attachmentLine/attachmentCloseAt) is
// not ported — ChatUI's topBar does not render attachment chips at all yet,
// a real, documented gap. The underlying detach behaviour it exercised is
// covered here instead via the explorer's own "space" toggle (attach then
// detach the same table reference), which is a supported, already-wired
// input path to the identical WorkspaceAction{Kind: "detach"}.
func TestChatUIWorkspaceSplitAndKeyboardSelection(t *testing.T) {
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
	view := u.shell.View().Content
	for _, want := range []string{"Project: Chinook", "● Project", "Inspect", "Docked", "Customer"} {
		if !strings.Contains(view, want) {
			t.Errorf("split view missing %q", want)
		}
	}
	// F6 focuses the workspace pane (native chatshell toggle).
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	for index, node := range u.workspace.explorerNodes() {
		if node.objectIndex >= 0 && u.catalog.Objects[node.objectIndex].Reference.Kind == "table" {
			u.workspace.explorerIndex = index
			break
		}
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(u.snapshot.Workspace.Attachments) != 1 || u.snapshot.Workspace.Attachments[0].Kind != "table" {
		t.Fatalf("project attachment = %+v", u.snapshot.Workspace.Attachments)
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeySpace}) // toggles: attach -> detach
	if len(u.snapshot.Workspace.Attachments) != 0 {
		t.Fatalf("second space toggle did not detach: %+v", u.snapshot.Workspace.Attachments)
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeySpace}) // attach again
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid missing")
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if len(u.snapshot.Workspace.Selections) != 1 || u.snapshot.Workspace.CurrentSelectionID == "" {
		t.Fatalf("row selection = %+v", u.snapshot.Workspace)
	}
	if len(u.snapshot.Workspace.Attachments) != 1 {
		t.Fatalf("row selection changed attachments: %+v", u.snapshot.Workspace.Attachments)
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	u.workspace.updateKey(tea.KeyPressMsg{Code: tea.KeySpace}) // explicitly attach selected rows (tab 1, Selected)
	if len(u.snapshot.Workspace.Attachments) != 2 {
		t.Fatalf("selected rows were not attached: %+v", u.snapshot.Workspace.Attachments)
	}
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if len(u.snapshot.Workspace.Docks) != 1 {
		t.Fatalf("dock state = %+v", u.snapshot.Workspace.Docks)
	}
	if !strings.Contains(u.shell.View().Content, "Inspect") || !strings.Contains(u.shell.View().Content, "Docked") {
		t.Fatal("workspace disappeared after selection/dock")
	}
}

// TestPanelFocusedLayoutFitsTabStripAndHintsAtWidth130 covers the r10
// coordinator review, which reported both the tab strip and the hints
// bar clipped at the terminal edge in a panel-focused layout ("Project ·
// Inspect · Docked · Bo…" / "Space attac…"). Renders at the SAME
// panel-focused width/state the round-9 snapshot used (130 cols, panel
// visible and FOCUSED via Shift+Right) and asserts: every rendered line
// is exactly 130 columns wide (no overflow past the terminal edge), the
// tab strip's full label set is intact (not cut mid-label), and no hint
// segment/pair is truncated with an ellipsis.
func TestPanelFocusedLayoutFitsTabStripAndHintsAtWidth130(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{Text: "ok"})
	u.shell.Update(tea.WindowSizeMsg{Width: 130, Height: 32})
	drainCmd(t, u, u.Submit("hello"))
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
	content := u.shell.View().Content

	for i, line := range strings.Split(content, "\n") {
		if w := ansi.StringWidth(line); w != 130 {
			t.Fatalf("line %d width = %d, want 130 (terminal edge overflow/clip): %q", i, w, ansi.Strip(line))
		}
	}
	plain := ansi.Strip(content)
	if !strings.Contains(plain, "Project · Inspect · Docked · Bookmarks") {
		t.Fatalf("expected the FULL tab strip label set intact, not clipped:\n%s", plain)
	}
	if !strings.Contains(plain, "Space attach") {
		t.Fatalf("expected the full 'Space attach' hint, not truncated:\n%s", plain)
	}
	if strings.Contains(plain, "…") {
		t.Fatalf("expected no ellipsis truncation anywhere in the panel-focused view:\n%s", plain)
	}
}

// TestPanelFocusedLayoutNeverOverflowsTerminalWidth covers the r10
// coordinator's follow-up review, verbatim: "hints line 1 is cut at the
// terminal edge... and the panel tab strip is clipped... Check it by
// measuring the rendered line widths... rather than by eye... Add a test
// that renders the panel-focused screen at 110x32 and 80x24 and asserts
// every line's display width <= terminal width." Uses the SAME workspace-
// focused hint set (a grid result present, panel focused via
// Shift+Right) that produces the "Ctrl+←→ resize   Space attach" /
// "● Project · Inspect · Docked · Bookmarks" content the coordinator's
// own review quoted, at both required terminal sizes, measured with
// ansi.StringWidth (the same package this codebase's own rendering code
// uses for wrapping/truncation decisions) rather than a byte or rune
// count that could disagree with it.
func TestPanelFocusedLayoutNeverOverflowsTerminalWidth(t *testing.T) {
	for _, dims := range []struct{ w, h int }{{110, 32}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", dims.w, dims.h), func(t *testing.T) {
			catalog := ProjectCatalog{ID: "chinook", Title: "Chinook"}
			chat, err := NewSessionChat(context.Background(), openTestStore(t, testStorePath(t), testScope()), &contextualStub{turns: []Turn{{
				Text:    "Here are the customers I found.",
				Queries: []QueryResult{{Title: "Customers", RecordSetID: "rs1", Result: secureread.Result{Columns: []string{"ID", "Name", "City"}, Rows: []secureread.Row{{Data: map[string]any{"ID": 1, "Name": "Customer", "City": "City"}}}}}},
			}}}, "sqlite:///fixture.db", catalog)
			if err != nil {
				t.Fatal(err)
			}
			u, err := NewSessionChatUI(context.Background(), chat, "fake-model")
			if err != nil {
				t.Fatal(err)
			}
			u.shell.Update(tea.WindowSizeMsg{Width: dims.w, Height: dims.h})
			drainCmd(t, u, u.Submit("Show me customers"))
			u.shell.Update(tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift})
			content := u.shell.View().Content
			for i, line := range strings.Split(content, "\n") {
				if w := ansi.StringWidth(line); w > dims.w {
					t.Fatalf("%dx%d: line %d display width = %d, exceeds terminal width %d: %q", dims.w, dims.h, i, w, dims.w, ansi.Strip(line))
				}
			}
		})
	}
}

func TestWorkspacePanelF6TogglesPanelVisibility(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	// 140, not 120: strongo/aichat's chat-look-polish fix gives PanelFrame
	// content the same left/right inset Card text already had
	// (panelPaddingCols, 2 columns each side -- previously 0), so the
	// panel's own usable content width is narrower at any given terminal
	// width than before. 120 columns no longer leaves tabStripHeader room
	// for its full label set ("Bookmarks"); 140 does.
	u.shell.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	// The panel is wide enough here for tabStripHeader's full label set
	// ("Bookmarks", not the narrow "Marks" abbreviation).
	before := strings.Contains(u.shell.View().Content, "Bookmarks")
	if !before {
		t.Fatalf("expected the workspace pane to render at width 140 before F6:\n%s", u.shell.View().Content)
	}
	u.shell.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	after := strings.Contains(u.shell.View().Content, "Bookmarks")
	if after {
		t.Fatal("expected F6 to hide the workspace pane")
	}
}

// TestWorkspacePanelTabStripHeaderShortensOnNarrowWidth covers the r9
// coordinator regression directly: at a panel width too narrow for even
// the mixed (active-tab-full) label set, the tab strip used to be cut down
// by padAnsiLine's ellipsis truncation to just the active tab's label,
// silently dropping the other three tabs. tabStripHeader must instead fall
// back to short labels for every tab, so all four remain visible.
func TestWorkspacePanelTabStripHeaderShortensOnNarrowWidth(t *testing.T) {
	u, _ := newTestChatUI(t, nil, Turn{})
	header := u.workspace.tabStripHeader(28)
	flat := ansi.Strip(header)
	for _, want := range []string{"Proj", "Sel", "Dock", "Marks"} {
		if !strings.Contains(flat, want) {
			t.Fatalf("tab %q missing from narrow tab strip: %q", want, flat)
		}
	}
	if strings.Contains(flat, "Project") {
		t.Fatalf("expected the FULL active-tab label to have been dropped too once even the mixed set doesn't fit: %q", flat)
	}
	if w := lipgloss.Width(header); w > 28 {
		t.Fatalf("tabStripHeader(28) width = %d, want <= 28: %q", w, header)
	}

	// Even narrower than the short label set itself needs: still falls
	// back to short labels (the least-bad option) rather than panicking
	// or returning something wider.
	tooNarrow := u.workspace.tabStripHeader(10)
	if !strings.Contains(ansi.Strip(tooNarrow), "Proj") {
		t.Fatalf("expected the short-label fallback even when it still doesn't fit width 10: %q", tooNarrow)
	}
}

// Ported from TestDockedGridSortAndCellSelectionUseSourceCoordinates.
func TestChatUIDockedGridSortAndCellSelectionUseSourceCoordinates(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0, 2}, Title: "Subset"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	u.workspace.tab, u.workspace.dockGridFocused = 2, true
	dock := u.snapshot.Workspace.Docks[0]
	dockGrid := u.workspace.dockGrids[dock.ID]
	dockGrid.SelectColumn(0)
	u.workspace.updateKey(tea.KeyPressMsg{Code: 's', Text: "s"}) // ascending
	u.workspace.updateKey(tea.KeyPressMsg{Code: 's', Text: "s"}) // descending
	dockGrid = u.workspace.dockGrids[dock.ID]
	if got := []int{dockGrid.sourceIndexAt(0), dockGrid.sourceIndexAt(1)}; !reflect.DeepEqual(got, []int{2, 0}) {
		t.Fatalf("docked sorted source rows = %v", got)
	}
	dockGrid.SelectColumn(1)
	dockGrid.SelectRow(0) // City in source row 2
	u.workspace.updateKey(tea.KeyPressMsg{Code: 'c', Text: "c"})
	saved, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selection := saved.Workspace.Selections[saved.Workspace.CurrentSelectionID]
	if !reflect.DeepEqual(selection.Rows, []int{2}) || !reflect.DeepEqual(selection.Columns, []string{"City"}) {
		t.Fatalf("docked cell selection = %+v", selection)
	}
	if !reflect.DeepEqual(selection.Ranges, []CellRange{{FirstRow: 2, LastRow: 2, FirstCol: 1, LastCol: 1}}) {
		t.Fatalf("docked cell coordinates = %+v", selection.Ranges)
	}
}

// TestChatUIDockGridHasNoViewSwitcher is the regression test for m9,
// ported from workspace_test.go's TestDockGridHasNoViewSwitcher: a dock
// grid never had a view switcher in main (no Charts/Current-row views to
// jump to with "2"/"3") — it already shows a narrow, purpose-built row set.
func TestChatUIDockGridHasNoViewSwitcher(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, testStorePath(t), testScope())
	sessions, err := NewSessionChat(ctx, store, &contextualStub{}, "sqlite:///chinook.db", workspaceTestCatalog())
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recordID := workspaceTestRecord(t, store, session.ID)
	ref, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "select", RecordSetID: recordID, Rows: []int{0, 2}, Title: "Subset"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.ApplyWorkspaceAction(ctx, WorkspaceAction{Kind: "dock", Reference: ref}); err != nil {
		t.Fatal(err)
	}
	u, err := NewSessionChatUI(ctx, sessions, "fake-model")
	if err != nil {
		t.Fatal(err)
	}
	u.shell.Update(tea.WindowSizeMsg{Width: 150, Height: 30})
	dock := u.snapshot.Workspace.Docks[0]
	dockGrid := u.workspace.dockGrids[dock.ID]
	if got := dockGrid.ExtraViews(); len(got) != 0 {
		t.Fatalf("dock grid ExtraViews() = %+v, want none", got)
	}
	if header := ansi.Strip(dockGrid.HeaderLine(60)); strings.Contains(header, "2 Charts") || strings.Contains(header, "3 Current row") {
		t.Fatalf("dock grid header still advertises a view switcher: %q", header)
	}
}

// Ported from TestWorkspaceInspectorsUseCatalogTypes.
func TestChatUIWorkspaceInspectorsUseCatalogTypes(t *testing.T) {
	turn := Turn{Queries: []QueryResult{{
		Title: "Customers",
		Result: secureread.Result{
			Columns: []string{"CustomerId", "City"},
			Rows:    []secureread.Row{{Data: map[string]any{"CustomerId": 42, "City": "Prague"}}},
		},
	}}}
	u, _ := newTestChatUI(t, nil, turn)
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Customer", Title: "Customer"}, Columns: []string{"CustomerId", "City"}, ColumnTypes: map[string]string{"CustomerId": "INTEGER", "City": "TEXT"}}}}
	drainCmd(t, u, u.Submit("Show customers"))
	if !u.shell.FocusEntry(u.lastGridEntryID) {
		t.Fatal("grid missing")
	}
	u.workspace.tab = 1
	for _, tc := range []struct{ key, want string }{{"1", "INTEGER"}, {"2", "main.Customer.CustomerId"}, {"3", "main.Customer.City"}} {
		u.workspace.updateKey(tea.KeyPressMsg{Text: tc.key})
		if view := ansi.Strip(u.workspace.View(65, 22, true)); !strings.Contains(view, tc.want) {
			t.Fatalf("inspector %s lacks %q:\n%s", tc.key, tc.want, view)
		}
	}
}

// Ported from TestInspectorDoesNotAttributeDerivedOutputByDisplayName.
func TestChatUIInspectorDoesNotAttributeDerivedOutputByDisplayName(t *testing.T) {
	u := NewChatUI(context.Background(), nil, "fake-model")
	u.catalog = ProjectCatalog{Objects: []ProjectObject{{Reference: ContextReference{Kind: "table", ObjectID: "main.Customer", SourceID: "chinook"}, Columns: []string{"CustomerId", "City"}, ColumnTypes: map[string]string{"CustomerId": "INTEGER", "City": "TEXT"}}}}
	record := RecordSet{Database: "chinook", DTQL: "from: {name: Customer}\ncolumns:\n  - aggregate: {function: COUNT, args: [{star: true}]}\n    as: CustomerId\nlimit: 10\n"}
	if got := u.columnMeta(&record, "CustomerId"); got.qualified != "" || got.dbType != "" {
		t.Fatalf("aggregate was falsely attributed to a physical column: %+v", got)
	}
	record.DTQL = "from: {name: Customer}\ncolumns:\n  - field: City\n    as: CustomerId\nlimit: 10\n"
	if got := u.columnMeta(&record, "CustomerId"); got.qualified != "main.Customer.City" || got.dbType != "TEXT" {
		t.Fatalf("aliased field provenance = %+v", got)
	}
}
