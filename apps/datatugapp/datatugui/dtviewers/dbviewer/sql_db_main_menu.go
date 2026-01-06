package dbviewer

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type SqlDbRootScreen int

const (
	SqlDbScreenTables SqlDbRootScreen = iota
	SqlDbScreenViews
)

func newSqlDbMenu(tui *sneatnav.TUI, selectedScreen SqlDbRootScreen, dbContext dtviewers.DbContext) sneatnav.Panel {
	list := tview.NewList()
	list.SetWrapAround(false)

	list.AddItem("Tables", "", 't', func() {
		_ = goTables(tui, sneatnav.FocusToContent, dbContext)
	})
	list.AddItem("Views", "", 'v', func() {
		_ = goViews(tui, sneatnav.FocusToContent, dbContext)
	})

	list.SetCurrentItem(int(selectedScreen))

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRight:
			tui.Content.TakeFocus()
			return nil
		case tcell.KeyUp:
			if list.GetCurrentItem() == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
				return nil
			}
		default:
			return event
		}
		return event
	})

	list.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		switch mainText {
		case "Tables":
			_ = goTables(tui, sneatnav.FocusToMenu, dbContext)
			return
		case "Views":
			_ = goViews(tui, sneatnav.FocusToMenu, dbContext)
		}
	})

	return sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(list, list.Box))
}
