package datatugui

import (
	"context"
	"fmt"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/strongo/logus"
)

type MainMenuItem struct {
	id       int
	Text     string
	Shortcut rune
	Action   func(tui2 *sneatnav.TUI, focusTo sneatnav.FocusTo) error
}

func RegisterMainMenuItem(id RootScreen, item MainMenuItem) {
	for _, existingItem := range mainMenuItems {
		if existingItem.id == int(id) {
			panic(fmt.Errorf("duplicate main menu item %d: adding '%s' already exists '%s'", id, item.Text, existingItem.Text))
		}
	}
	item.id = int(id)
	mainMenuItems = append(mainMenuItems, item)
}

var mainMenuItems []MainMenuItem

func NewDataTugMainMenu(tui *sneatnav.TUI, active RootScreen) (menu sneatnav.Panel) {
	handleMenuAction := func(action func(tui2 *sneatnav.TUI, focusTo sneatnav.FocusTo) error) func() {
		return func() {
			if err := action(tui, sneatnav.FocusToContent); err != nil {
				logus.Errorf(context.Background(), "Failed to execute action: %v", err)
				panic(err)
			}
			//tui.SetRootScreen(screen)
		}
	}

	list := sneatnav.MainMenuList(tui)

	for _, item := range mainMenuItems {
		list.AddItem(item.Text, "", item.Shortcut, handleMenuAction(item.Action))
	}

	list.AddItem("Exit", "", 'q', func() {
		tui.App.Stop()
	})

	var activeIndex int
	for i, item := range mainMenuItems {
		if item.id == int(active) {
			activeIndex = i
			break
		}
	}
	list.SetCurrentItem(activeIndex)

	list.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		if index < len(mainMenuItems) {
			if err := mainMenuItems[index].Action(tui, sneatnav.FocusToMenu); err != nil {
				panic(err)
			}
		}
	})

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// Handle the logic from NewDataTugMainMenu: move focus to breadcrumbs when on first item
		switch event.Key() {
		case tcell.KeyEnter:
			tui.Content.TakeFocus()
			tui.Content.InputHandler()(event, tui.SetFocus)
			return nil
		case tcell.KeyUp:
			if list.GetCurrentItem() == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
				return nil
			}
			return event
		case tcell.KeyRight:
			tui.SetFocus(tui.Content)
			return nil
		case tcell.KeyBacktab:
			// Move focus to header (breadcrumbs) when Shift+Tab or Up arrow is pressed on the menu.
			tui.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
			return nil // consume the event
		default:
			return event
		}
	})

	sneatv.DefaultBorderWithPadding(list.Box)

	return sneatnav.NewPanel(tui, sneatnav.WithBox(list, list.Box))
}
