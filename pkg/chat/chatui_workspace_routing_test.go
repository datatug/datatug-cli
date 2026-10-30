package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/pkg/secureread"
	"github.com/strongo/aichat/tui/chatshell"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func workspaceRoutingKey(u *ChatUI, key string) {
	u.shell.Update(uitest.Key(key))
	u.shell.View()
}

func TestUnifiedWorkspaceAPIReturnPreservesExpandedResult(t *testing.T) {
	t.Parallel()
	u := focusedGridTestUI(t)
	g, record, ok := u.activeGrid()
	if !ok {
		t.Fatal("missing grid")
	}
	workspaceRoutingKey(u, "f9")
	workspaceRoutingKey(u, "f8")
	if u.apiScreen == nil || u.shell.Expanded() || !strings.Contains(u.shell.View().Content, "API") {
		t.Fatal("API overview did not replace expanded result")
	}
	workspaceRoutingKey(u, "f8")
	returned, returnedRecord, ok := u.activeGrid()
	if !ok || returned != g || returnedRecord != record || !u.shell.Expanded() || u.shell.WorkspaceFocus() != "transcript" {
		t.Fatal("API return changed result identity or expansion/focus")
	}
	workspaceRoutingKey(u, "f8")
	workspaceRoutingKey(u, "esc")
	if u.apiScreen != nil || !u.shell.Expanded() {
		t.Fatal("Esc did not restore API origin")
	}
}

func TestUnifiedWorkspaceGridSearchEscKeepsExpansionUntilThirdPress(t *testing.T) {
	t.Parallel()
	u := focusedGridTestUI(t)
	g, _, _ := u.activeGrid()
	workspaceRoutingKey(u, "f9")
	workspaceRoutingKey(u, "a")
	workspaceRoutingKey(u, "esc")
	if !u.shell.Expanded() || !g.Editing() {
		t.Fatal("search clear restored expansion or exited search")
	}
	workspaceRoutingKey(u, "esc")
	if !u.shell.Expanded() || g.Editing() {
		t.Fatal("search exit restored expansion")
	}
	workspaceRoutingKey(u, "esc")
	if u.shell.Expanded() {
		t.Fatal("third Esc did not restore layout")
	}
}

func TestUnifiedWorkspaceExplorerSearchAndComposerDraft(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	u.shell.FocusLeftPanel()
	u.shell.Update(uitest.Text("z1ß"))
	if u.explorer.explorerSearch != "z1ß" {
		t.Fatal("first printable input was lost")
	}
	workspaceRoutingKey(u, "f4")
	u.shell.SetComposerText("/query 123 ß")
	workspaceRoutingKey(u, "esc")
	if u.shell.WorkspaceFocus() != "explorer" || !strings.Contains(u.shell.View().Content, "/query 123 ß") || u.explorer.explorerSearch != "z1ß" {
		t.Fatal("focus transfer lost search or draft")
	}
	workspaceRoutingKey(u, "esc")
	if !u.explorer.explorerSearchFocused || u.explorer.explorerSearch != "" || u.shell.WorkspaceFocus() != "explorer" {
		t.Fatal("search clear consumed multiple layers")
	}
	workspaceRoutingKey(u, "esc")
	if u.explorer.explorerSearchFocused || u.shell.WorkspaceFocus() != "explorer" {
		t.Fatal("search exit changed workspace focus")
	}
	workspaceRoutingKey(u, "f4")
	u.shell.SetComposerText("")
	u.shell.Update(uitest.Text("/"))
	u.shell.Update(uitest.Text("ß"))
	if !strings.Contains(u.shell.View().Content, "/ß") {
		t.Fatal("printable composer input invoked an action")
	}
}

func TestUnifiedWorkspaceInspectPlainDigitsNeverActivate(t *testing.T) {
	t.Parallel()
	u := focusedGridTestUI(t)
	u.shell.FocusRightPanel()
	u.workspace.tab = 1
	u.workspace.inspectorSubTab = 0
	for _, key := range []string{"1", "2", "3", "j", "r", "e"} {
		workspaceRoutingKey(u, key)
		if u.workspace.inspectorSubTab != 0 || u.shell.WorkspaceFocus() != "sidebar" {
			t.Fatalf("bare %q activated Inspect action", key)
		}
	}
	u.workspace.relatedFocused = true
	u.workspace.relatedIndex = 0
	workspaceRoutingKey(u, "2")
	if !u.workspace.relatedFocused || u.workspace.relatedIndex != 0 || u.shell.WorkspaceFocus() != "sidebar" {
		t.Fatal("Related digit activated an action")
	}
}

type workspaceRoutingDialog struct{ shifts int }

func (d *workspaceRoutingDialog) View(int, int) string { return "Routing dialog" }
func (d *workspaceRoutingDialog) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if key.String() == "shift+enter" {
			d.shifts++
		}
		return d, nil, key.String() == "esc"
	}
	return d, nil, false
}

func TestUnifiedWorkspaceModalOwnsShiftAndBlocksGlobals(t *testing.T) {
	t.Parallel()
	u := focusedGridTestUI(t)
	d := &workspaceRoutingDialog{}
	u.shell.PushOverlay(d)
	workspaceRoutingKey(u, "shift+enter")
	workspaceRoutingKey(u, "shift+left")
	for _, key := range []string{"f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10"} {
		workspaceRoutingKey(u, key)
	}
	if d.shifts != 1 || u.apiScreen != nil || u.shell.WorkspaceFocus() != "transcript" || !strings.Contains(ansi.Strip(u.shell.View().Content), "Esc close dialog to return to workspace") {
		t.Fatalf("modal routing escaped to workspace: shifts=%d api=%v focus=%q\n%s", d.shifts, u.apiScreen != nil, u.shell.WorkspaceFocus(), ansi.Strip(u.shell.View().Content))
	}
	workspaceRoutingKey(u, "f1")
	if !strings.Contains(u.shell.View().Content, "Keyboard help") {
		t.Fatal("help unavailable over modal")
	}
	workspaceRoutingKey(u, "esc")
	if !strings.Contains(u.shell.View().Content, "Routing dialog") {
		t.Fatal("help did not return to original modal")
	}
	workspaceRoutingKey(u, "esc")
	if u.shell.WorkspaceFocus() != "transcript" {
		t.Fatal("dialog close lost prior focus")
	}
}

func TestUnifiedWorkspaceActionMenuKeepsReplacementDialog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		index int
		want  string
	}{{"detail", 3, "Cell · Id"}, {"save", 12, "Save as project query"}, {"export", 13, "Export RecordSet"}, {"join", 14, "JOIN relationship"}} {
		t.Run(tc.name, func(t *testing.T) {
			u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Widgets", TugQL: "from: {name: Widget}\nlimit: 2\n", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1}}}}})
			u.savedQueryService = &savedQueryStub{}
			workspaceRoutingKey(u, "ctrl+space")
			for i := 0; i < tc.index; i++ {
				workspaceRoutingKey(u, "down")
			}
			workspaceRoutingKey(u, "enter")
			view := u.shell.View().Content
			if !strings.Contains(view, tc.want) || strings.Contains(view, "Result actions ·") {
				t.Fatalf("menu replacement was lost:\n%s", view)
			}
			workspaceRoutingKey(u, "esc")
			view = u.shell.View().Content
			if strings.Contains(view, tc.want) || strings.Contains(view, "Result actions ·") || u.shell.WorkspaceFocus() != "transcript" {
				t.Fatal("cancel did not close exactly the replacement and restore the grid")
			}
		})
	}
}

func TestUnifiedWorkspaceAPIEscapeFromComposerReturnsToAPI(t *testing.T) {
	t.Parallel()
	for _, expanded := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "expanded"}[expanded], func(t *testing.T) {
			u := focusedGridTestUI(t)
			grid, _, _ := u.activeGrid()
			id := u.activeRecordSetID()
			if expanded {
				workspaceRoutingKey(u, "f9")
			}
			workspaceRoutingKey(u, "f8")
			api := u.apiScreen
			workspaceRoutingKey(u, "f4")
			u.shell.SetComposerText("keep API draft")
			workspaceRoutingKey(u, "esc")
			if u.apiScreen != api || u.shell.WorkspaceFocus() != "center" || !strings.Contains(u.shell.View().Content, "keep API draft") {
				t.Fatal("composer Esc closed API or lost draft/return focus")
			}
			workspaceRoutingKey(u, "esc")
			got, _, ok := u.activeGrid()
			if u.apiScreen != nil || !ok || got != grid || u.activeRecordSetID() != id || u.shell.Expanded() != expanded || u.shell.WorkspaceFocus() != "transcript" {
				t.Fatal("API Back did not restore exact result and layout")
			}
		})
	}
}

func TestUnifiedWorkspaceF10FromComposerRetainsResultAndDraft(t *testing.T) {
	t.Parallel()
	u := focusedGridTestUI(t)
	g, _, _ := u.activeGrid()
	id := u.activeRecordSetID()
	workspaceRoutingKey(u, "f4")
	u.shell.SetComposerText("keep Related draft")
	workspaceRoutingKey(u, "f10")
	got, _, ok := u.activeGrid()
	if u.shell.WorkspaceFocus() != "sidebar" || !u.workspace.relatedFocused || !ok || got != g || u.activeRecordSetID() != id || !strings.Contains(u.shell.View().Content, "keep Related draft") {
		t.Fatal("F10 did not focus Related from composer with its retained result/draft")
	}
}

func TestUnifiedWorkspaceHelpAdvertisesReachableGridActions(t *testing.T) {
	t.Parallel()
	u := focusedGridTestUI(t)
	hints := strings.Join(u.workspaceHints(150), " · ")
	for _, forbidden := range []string{"1 Table", "2 Charts", "3 Current row", "j JOIN", "e export", "q save", "B bucket"} {
		if strings.Contains(hints, forbidden) {
			t.Fatalf("grid advertises printable action %q", forbidden)
		}
	}
	for _, want := range []string{"Ctrl+P Actions", "Ctrl+S", "Ctrl+E", "type to search"} {
		if !strings.Contains(hints, want) {
			t.Fatalf("missing reachable action %q: %s", want, hints)
		}
	}
	for _, unavailable := range []string{"Esc clear text", "Alt+G Group by", "click Session"} {
		if strings.Contains(chatHelpText, unavailable) {
			t.Fatalf("help advertises unavailable behavior %q", unavailable)
		}
	}
	workspaceRoutingKey(u, "ctrl+space")
	if !strings.Contains(u.shell.View().Content, "Result actions") {
		t.Fatal("advertised Actions binding did not dispatch")
	}
	workspaceRoutingKey(u, "esc")
	workspaceRoutingKey(u, "f4")
	u.shell.SetComposerText("draft from help")
	workspaceRoutingKey(u, "esc")
	if !strings.Contains(u.shell.View().Content, "draft from help") || u.shell.WorkspaceFocus() != "transcript" {
		t.Fatal("advertised composer Esc must retain draft and return")
	}
}

func TestUnifiedWorkspaceDeferredGridBrowserFeedbackIsVisible(t *testing.T) {
	t.Parallel()
	u := focusedGridTestUI(t)
	g, _, _ := u.activeGrid()
	id, entry, count := u.activeRecordSetID(), u.lastGridEntryID, len(u.snapshot.RecordSets)
	workspaceRoutingKey(u, "shift+enter")
	view := strings.Join(strings.Fields(uitest.Plain(u.shell.View().Content)), " ")
	if !strings.Contains(view, "Selected-result browser links are not supported yet") || !strings.Contains(view, "F5 opens the web workspace") {
		t.Fatalf("unavailable browser handoff lacks visible explanation:\n%s", view)
	}
	got, _, ok := u.activeGrid()
	if !ok || got != g || u.activeRecordSetID() != id || u.lastGridEntryID != entry || len(u.snapshot.RecordSets) != count || u.shell.WorkspaceFocus() != "transcript" {
		t.Fatal("deferred browser action changed the result or input owner")
	}
}

func TestUnifiedWorkspaceGridHintsFollowSearchAndExpansionLayers(t *testing.T) {
	t.Parallel()
	u := focusedGridTestUI(t)
	workspaceRoutingKey(u, "f9")
	if !strings.Contains(strings.Join(u.workspaceHints(150), " · "), "Esc restore") {
		t.Fatal("expanded grid lacks restore hint")
	}
	workspaceRoutingKey(u, "a")
	hints := strings.Join(u.workspaceHints(150), " · ")
	if strings.Contains(hints, "Esc restore") || strings.Contains(hints, "Enter details") || !strings.Contains(hints, "Esc clear search then leave search") || !strings.Contains(hints, "Enter leave search") {
		t.Fatal("searched expanded grid advertises underlay actions")
	}
	workspaceRoutingKey(u, "enter")
	g, _, _ := u.activeGrid()
	if g.Editing() || u.pendingDetail != nil || !u.shell.Expanded() {
		t.Fatal("advertised Enter search action opened detail or restored layout")
	}
}

func TestUnifiedWorkspaceJoinBlockAdvertisesModifiedPicker(t *testing.T) {
	t.Parallel()
	b := newTestJoinBlock()
	if !strings.Contains(uitest.Plain(b.View(100, true)), "press j") {
		t.Fatal("legacy JOIN shortcut changed")
	}
	b.workspace = true
	view := uitest.Plain(b.View(100, true))
	if strings.Contains(view, "press j") || !strings.Contains(view, "Ctrl+P Actions") {
		t.Fatal("workspace JOIN advertises printable action")
	}
}

func TestUnifiedWorkspaceAdvertisedModifiedDialogsDispatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ key, want string }{{"ctrl+s", "Save as project query"}, {"ctrl+e", "Export RecordSet"}} {
		t.Run(tc.key, func(t *testing.T) {
			u := focusedGridStoreBackedTestUI(t, QueryResult{Title: "Widgets", TugQL: "from: {name: Widget}\nlimit: 2\n", Result: secureread.Result{Columns: []string{"Id"}, Rows: []secureread.Row{{Data: map[string]any{"Id": 1}}}}})
			u.savedQueryService = &savedQueryStub{}
			workspaceRoutingKey(u, tc.key)
			if !strings.Contains(u.shell.View().Content, tc.want) {
				t.Fatalf("advertised %s did not open %s", tc.key, tc.want)
			}
		})
	}
}
