package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
)

// projectsExplorePanel is an Explore pane that records a request for its
// Projects page.
type projectsExplorePanel struct {
	chatshell.SidePanel
	opened *int
}

type projectsOpenedMsg struct{}

func (p projectsExplorePanel) OpenProjects() tea.Cmd {
	*p.opened++
	return func() tea.Msg { return projectsOpenedMsg{} }
}

// Without a registered project F3 has no picker to show. It must lead to the
// Projects page, also after the transcript has replaced the Explore pane.
func TestF3WithoutProjectsReturnsToProjectsPage(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	opened := 0
	panel := projectsExplorePanel{SidePanel: u.explorer, opened: &opened}
	u.SetExplorePanel(panel)
	u.shell.ShowTranscript()
	if u.shell.CenterPanel() != nil {
		t.Fatal("the transcript did not replace the Explore pane")
	}

	cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3})
	if !handled || cmd == nil {
		t.Fatalf("F3 without projects: cmd=%v handled=%v, want the Projects page", cmd, handled)
	}
	if _, ok := cmd().(projectsOpenedMsg); !ok || opened != 1 {
		t.Fatalf("F3 did not ask the Explore pane for its Projects page: opened=%d", opened)
	}
	if u.shell.CenterPanel() == nil || u.shell.WorkspaceFocus() != "center" {
		t.Fatalf("Explore pane not restored with focus: focus=%q", u.shell.WorkspaceFocus())
	}
}

func TestF3WithoutProjectsOrExplorePaneSaysWhatToRun(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	cmd, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3})
	if cmd != nil || !handled {
		t.Fatalf("cmd=%v handled=%v, want nil/true", cmd, handled)
	}
	if u.workspaceFeedback != "No projects yet. Run: datatug demo install" {
		t.Fatalf("feedback = %q", u.workspaceFeedback)
	}
}

func TestProjectsAndSettingsAction(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	reason := func() (string, bool) {
		for _, a := range u.workspaceActions() {
			if a.id == "explore" {
				return a.disabled, true
			}
		}
		return "", false
	}
	if disabled, ok := reason(); !ok || disabled == "" {
		t.Fatalf("without an Explore pane the action must be listed as unavailable: listed=%v reason=%q", ok, disabled)
	}

	// An Explore pane that cannot open a page is still shown.
	u.SetExplorePanel(u.explorer)
	u.shell.ShowTranscript()
	if disabled, _ := reason(); disabled != "" {
		t.Fatalf("action unavailable with an Explore pane: %q", disabled)
	}
	if cmd := u.dispatchWorkspaceAction("explore"); cmd != nil {
		t.Fatalf("cmd = %v, want nil for a pane without OpenProjects", cmd)
	}
	if u.shell.CenterPanel() == nil {
		t.Fatal("the action did not restore the Explore pane")
	}
}

func TestSetupHintsNameTheWayToProjects(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	u.SetExplorePanel(u.explorer)
	u.shell.ShowTranscript()
	if hints := u.workspaceHints(120); len(hints) == 0 || hints[0] != "F3 Projects and Settings" {
		t.Fatalf("composer hints without a project: %v", hints)
	}
	u.shell.FocusLeftPanel()
	if hints := u.workspaceHints(120); len(hints) == 0 || hints[0] != "F3 Projects and Settings" {
		t.Fatalf("explorer hints without a project: %v", hints)
	}
}

// A first run has no project and no result: the side panels say what to do
// and what will appear, not result-grid vocabulary.
func TestFirstRunSidePanelsExplainThemselves(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	u.SetExplorePanel(u.explorer)
	if !u.nothingToInspect() {
		t.Fatal("a workspace without a catalog or a result has nothing to inspect")
	}
	inspect := ansi.Strip(u.workspace.inspectStackView(40, 12))
	if !strings.Contains(inspect, "Nothing to inspect yet.") || strings.Contains(inspect, "durable selection") {
		t.Fatalf("inspect empty state: %q", inspect)
	}
	explorer := ansi.Strip(u.explorer.projectExplorer(28, 12))
	for _, want := range []string{"No project open", "F3 Projects", "datatug demo install"} {
		if !strings.Contains(explorer, want) {
			t.Fatalf("explorer empty state lacks %q: %q", want, explorer)
		}
	}
}
