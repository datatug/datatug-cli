package dtproject

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-core/pkg/datatug"
)

func newEnvironmentsPanel(ctx ProjectContext) sneatnav.Panel {
	project := ctx.Project()
	environments, err := project.GetEnvironments(ctx)
	return newListPanel(ctx.TUI(), "Environments", environments, func(e *datatug.Environment) (string, string) {
		return e.ID, e.Title
	}, err)
}
