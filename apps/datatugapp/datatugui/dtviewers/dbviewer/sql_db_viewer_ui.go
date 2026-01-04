package dbviewer

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/rivo/tview"
)

type RecentDB struct {
	Name string
	Path string
}

func GetDbViewersBreadcrumbs(tui *sneatnav.TUI) sneatnav.Breadcrumbs {
	breadcrumbs := dtviewers.GetViewersBreadcrumbs(tui)
	breadcrumbs.Push(sneatv.NewBreadcrumb("DB", func() error {
		return GoDbViewerSelector(tui, sneatnav.FocusToContent)
	}))
	return breadcrumbs
}

func GoDbViewerSelector(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {

	breadcrumbs := dtviewers.GetViewersBreadcrumbs(tui)
	breadcrumbs.Push(sneatv.NewBreadcrumb("DB Viewers", nil))

	menu := getDbViewerMenu(tui, focusTo, "DB Viewers")

	content := sneatnav.NewPanel(tui, sneatnav.WithBox(menu, menu.Box))
	tui.SetPanels(tui.Menu, content, sneatnav.WithFocusTo(focusTo))
	return nil
}

func getDbViewerMenu(tui *sneatnav.TUI, focusTo sneatnav.FocusTo, title string) *tview.List {
	list := sneatnav.MainMenuList(tui)
	if title != "" {
		list.SetTitle(title)
	}
	list.AddItem("SQLLite", "", 'l', func() {
		_ = goSqliteHome(tui, focusTo)
	})
	list.AddItem("PostgreSQL", "", 'p', nil)
	setDefaultInputCaptureForList(tui, list)
	return list
}
