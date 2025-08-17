package projectui

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-core/pkg/appconfig"
)

func goEnvironmentsScreen(tui *sneatnav.TUI, project *appconfig.ProjectConfig) {

	menu := newProjectMenuPanel(tui, project, "environments")
	content := newEnvironmentsPanel(tui, project)
	tui.SetPanels(menu, content)
}
