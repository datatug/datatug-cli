package dtviewers

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
)

type ViewerID string

func NewCloudsMenu(tui *sneatnav.TUI, active ViewerID) (menu sneatnav.Panel) {
	list := sneatnav.MainMenuList(tui)
	sneatv.DefaultBorderWithPadding(list.Box)

	current := -1
	for i, viewer := range viewers {
		list.AddItem(viewer.Name, "", viewer.Shortcut, func() {
			_ = viewer.Action(tui, sneatnav.FocusToMenu)
		})
		if viewer.ID == active {
			current = i
		}
	}

	if current >= 0 {
		list.SetCurrentItem(current)
	}

	list.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		_ = viewers[index].Action(tui, sneatnav.FocusToMenu)
	})

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRight:
			tui.SetFocus(tui.Content)
		//list.GetItemSelectedFunc(list.GetCurrentItem())()
		case tcell.KeyUp:
			if list.GetCurrentItem() == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
				return nil
			}
			return event
		case tcell.KeyEnter:
			tui.Content.TakeFocus()
			tui.Content.InputHandler()(event, tui.SetFocus)
			return nil
		default:
			return event
		}
		return event
	})

	return sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(list, list.Box))
}
