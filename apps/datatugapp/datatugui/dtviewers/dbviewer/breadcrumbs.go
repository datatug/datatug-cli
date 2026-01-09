package dbviewer

import (
	"strings"

	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
)

func getSqlDbBreadcrumbs(tui *sneatnav.TUI, dbContext dtviewers.DbContext) sneatnav.Breadcrumbs {
	breadcrumbs := GetDbViewersBreadcrumbs(tui)
	driverBreadcrumb := sneatv.NewBreadcrumb(dbContext.Driver().ShortTitle, func() error {
		return goSqliteHome(tui, sneatnav.FocusToContent)
	})
	breadcrumbs.Push(driverBreadcrumb)

	if name := dbContext.Name(); name != "" {
		for _, ext := range []string{".sqlite", ".sqlite3"} {
			name = strings.TrimSuffix(name, ext)
		}
		dbBreadcrumb := sneatv.NewBreadcrumb(name, func() error {
			return GoSqlDbHome(tui, dbContext)
		})
		breadcrumbs.Push(dbBreadcrumb)
	}
	return breadcrumbs
}
