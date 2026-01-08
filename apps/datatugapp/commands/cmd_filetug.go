package commands

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/filetug"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/rivo/tview"
)

const viewerID dtviewers.ViewerID = "fsm"

func RegisterAsViewer() {
	dtviewers.RegisterViewer(dtviewers.Viewer{
		ID:       viewerID,
		Name:     "FSM - Files Navigator",
		Shortcut: '2',
		Action:   goFilesManager,
	})
}

func goFilesManager(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	n := filetug.NewNavigator(tui.App, filetug.OnMoveFocusUp(func(source tview.Primitive) {
		tui.Header.SetFocus(sneatnav.ToBreadcrumbs, source)
	}))
	navigatorPanel := sneatnav.NewPanelWithoutBorders[filetug.Navigator](tui, n, n.Box)
	tui.SetPanels(nil, navigatorPanel, sneatnav.WithFocusTo(focusTo))
	n.SetFocus()
	return nil
}
