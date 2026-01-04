package gcloudui

import (
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func newProjectBreadcrumbs(gcProjectCtx *CGProjectContext) sneatnav.Breadcrumbs {
	breadcrumbs := newBreadcrumbsProjects(gcProjectCtx.GCloudContext)
	breadcrumbs.Push(sneatv.NewBreadcrumb(gcProjectCtx.Project.DisplayName, func() error {
		return goProject(gcProjectCtx)
	}))
	return breadcrumbs
}

func newGProjectMenu(gcProjCtx *CGProjectContext) sneatnav.Panel {
	list := tview.NewList()
	sneatv.DefaultBorderWithPadding(list.Box)
	list.SetTitle(gcProjCtx.Project.DisplayName)

	list.AddItem("Firestore Database", "", 0, func() {
		_ = goFirestoreDb(gcProjCtx)
	})

	list.AddItem("Firebase Users", "", 0, func() {
	})

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
		case tcell.KeyEnter:
			gcProjCtx.TUI.Content.TakeFocus()
			gcProjCtx.TUI.Content.InputHandler()(event, gcProjCtx.TUI.SetFocus)
			return nil
		default:
			return event
		}
	})
	return sneatnav.NewPanel(gcProjCtx.TUI, sneatnav.WithBox(list, list.Box))
}

func goProject(gcProjCtx *CGProjectContext) error {
	_ = newProjectBreadcrumbs(gcProjCtx)

	//menu := newMenuWithProjects(gcProjCtx.GCloudContext)
	menu := newGProjectMenu(gcProjCtx)

	content := firestoreMainMenu(gcProjCtx, firestoreScreenCollections, "")
	gcProjCtx.TUI.SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToMenu))
	return nil
}

//func newMenuWithProjects(cContext *GCloudContext) (menu sneatnav.Panel) {
//	list := sneatnav.MainMenuList()
//	list.SetTitle("Projects")
//	sneatv.DefaultBorderWithPadding(list.Box)
//	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
//		switch event.Key() {
//		case tcell.KeyUp:
//			cContext.TUI.Header.SetFocus(sneatnav.ToBreadcrumbs, list)
//		case tcell.KeyRight:
//			cContext.TUI.Content.TakeFocus()
//		case tcell.KeyEnter:
//			cContext.TUI.Content.TakeFocus()
//			cContext.TUI.Content.InputHandler()(event, cContext.TUI.SetFocus)
//		default:
//			return event
//		}
//		return event
//	})
//
//	projects, err := cContext.GetProjects()
//	if err != nil {
//		list.AddItem("Failed to load  projects:", err.Error(), 0, nil)
//		return sneatnav.NewPanelFromList(cContext.TUI, list)
//	}
//	for _, project := range projects {
//		list.AddItem(project.DisplayName, "", 0, func() {})
//	}
//	return sneatnav.NewPanelFromList(cContext.TUI, list)
//}
