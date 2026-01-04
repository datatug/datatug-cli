package dbviewer

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

const viewerID dtviewers.ViewerID = "sql"

func RegisterAsViewer() {
	dtviewers.RegisterViewer(dtviewers.Viewer{
		ID:       viewerID,
		Name:     "DB viewer",
		Shortcut: '1',
		Action:   goSqlDbHome,
	})
}

func goSqlDbHome(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	return GoDbViewerSelector(tui, focusTo)
}
