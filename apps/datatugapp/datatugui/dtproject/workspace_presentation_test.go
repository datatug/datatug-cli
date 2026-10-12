package dtproject

import (
	"testing"

	"charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

func TestWorkspaceProjectSelectionRemainsFunctionalWithNeutralPresentation(t *testing.T) {
	p := newProjects()
	s, _ := p.Update(projectsLoaded{projects: testProjects})
	s, _ = s.Update(tea.WindowSizeMsg{Width: 56, Height: 18})
	s, _ = s.Update(nav.ScreenFocusMsg{Focused: true})
	p = s.(projects)
	legacy := p.View()
	s, _ = p.Update(datatugui.WorkspacePresentationMsg{})
	p = s.(projects)
	neutral := p.View()
	if legacy == neutral || ansi.Strip(legacy) != ansi.Strip(neutral) || !p.tree.Focused() {
		t.Fatal("workspace presentation changed project content/keyboard ownership or retained active blue selection")
	}
	before, _ := p.tree.Current()
	s, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	p = s.(projects)
	after, _ := p.tree.Current()
	if before.ID == after.ID {
		t.Fatal("neutral projects lost arrow navigation")
	}
	s, _ = p.Update(projectsLoaded{projects: testProjects})
	if !s.(projects).neutralWorkspace || s.(projects).View() == legacy {
		t.Fatal("project refresh lost neutral workspace presentation")
	}
}
