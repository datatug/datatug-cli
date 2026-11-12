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
		t.Fatalf("cmd = %v, want nil: the action restores the pane as it was left", cmd)
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
	if hints := u.workspaceHints(120); len(hints) == 0 || hints[0] != "F3 projects" {
		t.Fatalf("composer hints without a project: %v", hints)
	}
	u.shell.FocusLeftPanel()
	if hints := u.workspaceHints(120); len(hints) == 0 || hints[0] != "F3 projects" {
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
	explorer := strings.Join(strings.Fields(ansi.Strip(u.explorer.projectExplorer(28, 12))), " ")
	for _, want := range []string{"No project open", "F3 projects", "create or add your first"} {
		if !strings.Contains(explorer, want) {
			t.Fatalf("explorer empty state lacks %q: %q", want, explorer)
		}
	}
}

// F3 must not reset the Explore pane: restoring it keeps the page it was left
// on. Only the Projects request itself goes to the pane.
func TestExploreActionKeepsThePaneAndClosesTheAPIMonitor(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	opened := 0
	u.SetExplorePanel(projectsExplorePanel{SidePanel: u.explorer, opened: &opened})

	// F8 shows the API monitor as a temporary center; Explore must end it.
	u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF8})
	if u.apiScreen == nil {
		t.Fatal("F8 did not open the API monitor")
	}
	u.dispatchWorkspaceAction("explore")
	if u.apiScreen != nil || u.apiReturn != nil {
		t.Fatal("Explore left the API monitor half open")
	}
	if opened != 0 {
		t.Fatalf("restoring Explore reset the pane to Projects %d time(s)", opened)
	}
	if u.shell.CenterPanel() == nil || u.shell.WorkspaceFocus() != "center" {
		t.Fatalf("Explore not shown with focus: %q", u.shell.WorkspaceFocus())
	}
}

// A project created or added in Explore during the session is offered by F3
// without a restart.
func TestF3ReloadsRegisteredProjects(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	opened := 0
	u.SetExplorePanel(projectsExplorePanel{SidePanel: u.explorer, opened: &opened})
	var registered []ProjectChoice
	u.SetProjectChoicesLoader(func() []ProjectChoice { return registered })

	if cmd, _ := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3}); cmd == nil || opened != 1 {
		t.Fatalf("F3 with nothing registered must open Projects: opened=%d", opened)
	}
	registered = []ProjectChoice{{Key: "shop", Title: "Shop", Detail: "~/datatug/shop"}}
	if _, handled := u.globalKeys(tea.KeyPressMsg{Code: tea.KeyF3}); !handled || opened != 1 {
		t.Fatalf("F3 after a project was registered must open the picker, not Projects: opened=%d", opened)
	}
	if view := ansi.Strip(u.shell.View().Content); !strings.Contains(view, "Shop") {
		t.Fatalf("picker does not list the new project: %s", view)
	}
}

// A project without databases is still an open project.
func TestEmptyProjectIsNotAFirstRun(t *testing.T) {
	t.Parallel()
	u, _ := newTestChatUI(t, nil, Turn{})
	u.SetExplorePanel(u.explorer)
	u.catalog = ProjectCatalog{ID: "shop", Title: "Shop"}
	if u.noProject() || u.nothingToInspect() {
		t.Fatal("an open project without objects was treated as no project")
	}
	explorer := strings.Join(strings.Fields(ansi.Strip(u.explorer.projectExplorer(28, 12))), " ")
	if strings.Contains(explorer, "No project open") || !strings.Contains(explorer, "no databases or queries yet") {
		t.Fatalf("explorer text for an empty project: %q", explorer)
	}
}
