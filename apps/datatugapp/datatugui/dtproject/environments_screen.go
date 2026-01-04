package dtproject

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

func goEnvironmentsScreen(ctx ProjectContext, focusTo sneatnav.FocusTo) {

	menu := getOrCreateProjectMenuPanel(ctx, "environments")

	//project := ctx.Project()
	//menu.SetProject(project)

	content := newEnvironmentsPanel(ctx)

	ctx.TUI().SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))
}
