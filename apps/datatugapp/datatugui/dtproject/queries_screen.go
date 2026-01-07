package dtproject

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

func goProjectQueries(ctx *ProjectContext) {
	menu := getOrCreateProjectMenuPanel(ctx, "queries")
	content := newQueriesPanel(ctx)
	tui := ctx.TUI()
	tui.SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToMenu))
}

func newQueriesPanel(ctx *ProjectContext) sneatnav.Panel {
	content := tview.NewTextView().SetTextAlign(tview.AlignCenter).SetText("List of queries here")

	sneatv.DefaultBorderWithPadding(content.Box)

	return sneatnav.NewPanel(ctx.TUI(), sneatv.WithDefaultBorders(content, content.Box))
}
