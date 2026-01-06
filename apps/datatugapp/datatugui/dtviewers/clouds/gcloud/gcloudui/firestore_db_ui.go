package gcloudui

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
)

type firestoreScreen int

const (
	firestoreScreenCollections = iota
	firestoreScreenIndexes
)

func firestoreBreadcrumbs(gcProjCtx *CGProjectContext) sneatnav.Breadcrumbs {
	breadcrumbs := newGCloudProjectBreadcrumbs(gcProjCtx)
	breadcrumbs.Push(sneatv.NewBreadcrumb("Firestore", nil))
	return breadcrumbs
}

func goFirestoreDb(gcProjCtx *CGProjectContext) error {
	_ = firestoreBreadcrumbs(gcProjCtx)
	menu := dtviewers.NewCloudsMenu(gcProjCtx.TUI, viewerID)

	content := firestoreMainMenu(gcProjCtx, firestoreScreenCollections, "Firestore Database")

	gcProjCtx.TUI.SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToContent))
	return nil
}

func firestoreMainMenu(gcProjCtx *CGProjectContext, active firestoreScreen, title string) (menu sneatnav.Panel) {
	list := sneatnav.MainMenuList(gcProjCtx.TUI)
	sneatv.DefaultBorderWithPadding(list.Box)
	if title != "" {
		list.SetTitle(title)
	}

	list.AddItem("Collections", "", 0, func() {
		_ = goFirestoreCollections(gcProjCtx)
	})

	list.AddItem("Indexes", "", 0, func() {
		_ = goFirestoreIndexes(gcProjCtx)
	})

	list.SetCurrentItem(int(active))

	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyRight:
			gcProjCtx.TUI.SetFocus(gcProjCtx.TUI.Content)
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

	return sneatnav.NewPanel(gcProjCtx.TUI, sneatv.WithDefaultBorders(list, list.Box))
}
