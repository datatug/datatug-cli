package dtproject

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/rivo/tview"
)

func goProjectDashboards(ctx *ProjectContext) {
	menu := getOrCreateProjectMenuPanel(ctx, "dashboards")
	content := newDashboardsPanel(ctx)
	ctx.TUI().SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToMenu))
}

func newDashboardsPanel(ctx *ProjectContext) sneatnav.Panel {
	content := tview.NewTextView().SetTextAlign(tview.AlignCenter).SetText("List of dashboards here")

	sneatv.DefaultBorderWithPadding(content.Box)

	return sneatnav.NewPanel(ctx.TUI(), sneatv.WithDefaultBorders(content, content.Box))
}
