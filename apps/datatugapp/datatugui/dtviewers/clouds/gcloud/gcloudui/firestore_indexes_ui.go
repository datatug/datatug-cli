package gcloudui

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func goFirestoreIndexes(gcProjCtx *CGProjectContext) error {
	breadcrumbs := newGCloudProjectBreadcrumbs(gcProjCtx)
	breadcrumbs.Push(sneatv.NewBreadcrumb("Firestore", nil))
	menu := firestoreMainMenu(gcProjCtx, firestoreScreenIndexes, "")

	list := tview.NewList()
	sneatv.DefaultBorderWithPadding(list.Box)
	list.SetTitle("Firestore Indexes")
	content := sneatnav.NewPanel(gcProjCtx.TUI, sneatv.WithDefaultBorders(list, list.Box))

	list.AddItem("Loading...", "(not implemented yet)", 0, nil)

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			gcProjCtx.TUI.Menu.TakeFocus()
			return nil
		case tcell.KeyUp:
			if list.GetCurrentItem() == 0 {
				gcProjCtx.TUI.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
				return nil
			}
			return event
		default:
			return event
		}
	})

	gcProjCtx.TUI.SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToContent))
	return nil
}
