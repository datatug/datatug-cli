package datatugapp

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtproject"
	"github.com/datatug/datatug-cli/apps/global"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/rivo/tview"
)

func NewDatatugTUI() (tui *sneatnav.TUI) {
	global.App = tview.NewApplication()
	global.App.EnableMouse(true)

	tui = sneatnav.NewTUI(global.App, sneatv.NewBreadcrumb(" ⛴ DataTug", func() error {
		return goProjectScreen(tui, sneatnav.FocusToMenu)
	}))
	return
}

var goProjectScreen = dtproject.GoDataTugProjectsScreen
