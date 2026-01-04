package datatugapp

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

func NewDatatugTUI() (tui *sneatnav.TUI) {
	app := tview.NewApplication()
	app.EnableMouse(true)

	tui = sneatnav.NewTUI(app, sneatv.NewBreadcrumb(" ⛴ DataTug", func() error {
		return goProjectScreen(tui, sneatnav.FocusToMenu)
	}))
	return
}

var goProjectScreen = dtproject.GoProjectsScreen
