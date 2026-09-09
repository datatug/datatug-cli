package dtviewers

import (
	"github.com/datatug/datatug-cli/pkg/sneatv"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

func GetViewersBreadcrumbs(tui *sneatnav.TUI) sneatnav.Breadcrumbs {
	breadcrumbs := tui.Header.Breadcrumbs()
	breadcrumbs.Clear()
	breadcrumbs.Push(sneatv.NewBreadcrumb("Viewers", func() error {
		return GoViewersScreen(tui, sneatnav.FocusToContent)
	}))
	return breadcrumbs
}
