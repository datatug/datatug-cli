package commands

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/filetug"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/rivo/tview"
)

const viewerID dtviewers.ViewerID = "fsm"

func RegisterAsViewer() {
	dtviewers.RegisterViewer(dtviewers.Viewer{
		ID:       viewerID,
		Name:     "FileTug - files viewer",
		Shortcut: '2',
		Action:   goFileTug,
	})
}

func goFileTug(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	breadcrumbs := tui.Header.Breadcrumbs()
	breadcrumbs.Clear()
	breadcrumbs.Push(sneatv.NewBreadcrumb("Viewers", func() error {
		return dtviewers.GoViewersScreen(tui, sneatnav.FocusToContent)
	}))
	breadcrumbs.Push(sneatv.NewBreadcrumb("FileTug", nil))

	n := filetug.NewNavigator(tui.App, filetug.OnMoveFocusUp(func(source tview.Primitive) {
		tui.Header.SetFocus(sneatnav.ToBreadcrumbs, source)
	}))
	navigatorPanel := sneatnav.NewPanelWithoutBorders[filetug.Navigator](tui, n, n.Box)
	tui.SetPanels(nil, navigatorPanel, sneatnav.WithFocusTo(focusTo))
	n.SetFocus()
	return nil
}
