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

	const dbViewersTitle = "DB Viewers"
	breadcrumbs := dtviewers.GetViewersBreadcrumbs(tui)
	breadcrumbs.Push(sneatv.NewBreadcrumb(dbViewersTitle, nil))

	dbViewerMenu := getDbViewerMenu(tui, focusTo, dbViewersTitle)

	mainMenu := dtviewers.GetViewersListPanel(tui, " Viewers ", focusTo, dtviewers.ViewersListOptions{
		WithDescription: false,
	})

	content := sneatnav.NewPanel(tui, sneatnav.WithBox(dbViewerMenu, dbViewerMenu.Box))
	tui.SetPanels(mainMenu, content, sneatnav.WithFocusTo(focusTo))
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
	list.AddItem("inGitDB", "", 'g', func() {
		_ = goIngitdbBHome(tui, focusTo)
	})

	list.AddItem("PostgreSQL", "", 'p', nil)

	setDefaultInputCaptureForList(tui, list)
	return list
}
