package fsmanager

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

const viewerID dtviewers.ViewerID = "fsm"

func RegisterAsViewer() {
	dtviewers.RegisterViewer(dtviewers.Viewer{
		ID:       viewerID,
		Name:     "FSM - Files Navigator",
		Shortcut: '2',
		Action:   GoFilesManager,
	})
}

func GoFilesManager(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	n := NewNavigator()
	navigatorPanel := sneatnav.NewPanelWithoutBorders[Navigator](tui, n, n.Box)
	tui.SetPanels(nil, navigatorPanel, sneatnav.WithFocusTo(focusTo))
	return nil
}
