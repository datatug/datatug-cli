package dtproject

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

func goProjectDashboards(ctx ProjectContext) {
	menu := getOrCreateProjectMenuPanel(ctx, "dashboards")
	content := newDashboardsPanel(ctx)
	ctx.TUI().SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToMenu))
}
