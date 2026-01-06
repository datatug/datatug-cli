package dtviewers

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func GoViewersScreen(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	breadcrumbs := tui.Header.Breadcrumbs()
	breadcrumbs.Clear()
	breadcrumbs.Push(sneatv.NewBreadcrumb("Viewers", nil))

	menu := datatugui.NewDataTugMainMenu(tui, datatugui.RootScreenViewers)
	content := GetViewersListPanel(tui, " Viewers ", focusTo, ViewersListOptions{WithDescription: true})

	tui.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))
	dtlog.ScreenOpened("viewers", "Viewers")
	dtstate.SaveCurrentScreePath("viewers")
	return nil
}

type ViewersListOptions struct {
	WithDescription bool
}

func GetViewersListPanel(tui *sneatnav.TUI, title string, focusTo sneatnav.FocusTo, o ViewersListOptions) sneatnav.Panel {
	list := tview.NewList()

	for _, viewer := range viewers {
		var description string
		if o.WithDescription {
			description = viewer.Description
		}
		list.AddItem(viewer.Name, description, viewer.Shortcut, func() {
			_ = viewer.Action(tui, focusTo)
		})
	}

	// Set secondary text color to gray
	list.SetSecondaryTextColor(tcell.ColorDarkGray)

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyESC, tcell.KeyBacktab, tcell.KeyLeft:
			tui.SetFocus(tui.Menu)
			return nil
		case tcell.KeyUp:
			if list.GetCurrentItem() == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
				return nil
			}
			return event
		case tcell.KeyDown:
			// Prevent jumping to first item when on last item
			if list.GetCurrentItem() == list.GetItemCount()-1 {
				return nil
			}
			return event
		default:
			return event
		}
	})

	sneatv.DefaultBorderWithPadding(list.Box)
	// Set spacing between items to 1 line by increasing vertical padding
	list.SetBorderPadding(1, 1, 1, 1)
	list.SetTitle(title)
	list.SetTitleAlign(tview.AlignLeft)

	return sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(list, list.Box))
}
