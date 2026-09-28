package dbviewer

import (
	"strings"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

func getSqlDbBreadcrumbs(tui *sneatnav.TUI, dbContext dtviewers.DbContext) sneatnav.Breadcrumbs {
	breadcrumbs := GetDbViewersBreadcrumbs(tui)
	driverAction := func() error {
		return goSqliteHome(tui, sneatnav.FocusToContent)
	}
	if onBreadcrumbAction != nil {
		onBreadcrumbAction("driver", driverAction)
	}
	driverBreadcrumb := sneatv.NewBreadcrumb(dbContext.Driver().ShortTitle, driverAction)
	breadcrumbs.Push(driverBreadcrumb)

	if name := dbContext.Name(); name != "" {
		for _, ext := range []string{".sqlite", ".sqlite3"} {
			name = strings.TrimSuffix(name, ext)
		}
		dbAction := func() error {
			return GoSqlDbHome(tui, dbContext)
		}
		if onBreadcrumbAction != nil {
			onBreadcrumbAction("db", dbAction)
		}
		dbBreadcrumb := sneatv.NewBreadcrumb(name, dbAction)
		breadcrumbs.Push(dbBreadcrumb)
	}
	return breadcrumbs
}

var onBreadcrumbAction func(kind string, action func() error)
