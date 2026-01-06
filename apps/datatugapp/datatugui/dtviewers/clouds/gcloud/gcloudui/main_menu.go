package gcloudui

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
)

type Screen int

const (
	ScreenProjects Screen = iota
	ScreenCredentials
)

func newMainMenu(cContext *GCloudContext, active Screen, isInContent bool) (menu sneatnav.Panel) {
	list := sneatnav.MainMenuList(cContext.TUI)
	list.SetTitle("Google Cloud")
	sneatv.DefaultBorderWithPadding(list.Box)

	list.AddItem("Projects", "", 'p', func() {
		_ = GoGCloudProjects(cContext, sneatnav.FocusToMenu)
	})
	list.AddItem("Credentials", "", 'c', func() {
		_ = GoCredentials(cContext, sneatnav.FocusToMenu)
	})

	list.SetCurrentItem(int(active))

	list.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		switch index { // Not ideal
		case 0:
			_ = GoGCloudProjects(cContext, sneatnav.FocusToMenu)
		case 1:
			_ = GoCredentials(cContext, sneatnav.FocusToMenu)
		}
	})

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRight:
			cContext.TUI.Content.TakeFocus()
		case tcell.KeyLeft:
			cContext.TUI.SetFocus(cContext.TUI.Menu)
		case tcell.KeyUp:
			if list.GetCurrentItem() == 0 {
				cContext.TUI.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
				return nil
			}
			return event
		case tcell.KeyEnter:
			if isInContent {
				return event
			}
			cContext.TUI.Content.TakeFocus()
			cContext.TUI.Content.InputHandler()(event, cContext.TUI.SetFocus)
			return nil
		default:
			return event
		}
		return event
	})

	return sneatnav.NewPanel(cContext.TUI, sneatv.WithDefaultBorders(list, list.Box))
}
