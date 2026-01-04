package dtproject

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

func newDashboardsPanel(ctx ProjectContext) sneatnav.Panel {
	content := tview.NewTextView().SetTextAlign(tview.AlignCenter).SetText("List of dashboards here")

	sneatv.DefaultBorderWithPadding(content.Box)

	return sneatnav.NewPanel(ctx.TUI(), sneatnav.WithBox(content, content.Box))
}
