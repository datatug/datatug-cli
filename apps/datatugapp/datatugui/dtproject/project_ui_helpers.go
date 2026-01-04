package dtproject

import (
	"fmt"

	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func newListPanel[T any](tui *sneatnav.TUI, title string, items []T, getIDTitle func(T) (string, string), err error) sneatnav.Panel {
	if err != nil {
		textView := tview.NewTextView()
		textView.SetText(err.Error())
		textView.SetTextColor(tcell.ColorRed)
		return sneatnav.NewPanel(tui, sneatnav.WithBox(textView, textView.Box))
	}

	list := tview.NewList()
	list.SetTitle(fmt.Sprintf("%s (%d)", title, len(items)))
	list.SetWrapAround(false)
	for _, item := range items {
		itemID, itemTitle := getIDTitle(item)
		if itemTitle == itemID {
			itemTitle = ""
		}
		list.AddItem(itemID, itemTitle, rune(itemID[0]), nil)
	}

	return sneatnav.NewPanel(tui, sneatnav.WithBox(list, list.Box))
}
