package dtproject

import (
	"github.com/datatug/datatug-core/pkg/datatug"
	"github.com/datatug/datatug/pkg/sneatview/sneatnav"
)

func goDatabasesScreen(ctx ProjectContext, focusTo sneatnav.FocusTo) {

	menu := getOrCreateProjectMenuPanel(ctx, "environments")

	//project := ctx.Project()
	//menu.SetProject(project)

	content := newDatabasesPanel(ctx)

	ctx.TUI().SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))
}

func newDatabasesPanel(ctx ProjectContext) sneatnav.Panel {
	project := ctx.Project()
	dbs, err := project.GetDBs(ctx)
	return newListPanel(ctx.TUI(), "Databases", dbs, func(s *datatug.ProjDbDriver) (string, string) {
		return s.ID, s.Title
	}, err)
}
