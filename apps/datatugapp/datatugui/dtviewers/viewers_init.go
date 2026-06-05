package dtviewers

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
)

var viewers []Viewer

func RegisterViewer(viewer Viewer) {
	viewers = append(viewers, viewer)
}

// seam for testing
var registerMainMenuItem = datatugui.RegisterMainMenuItem

func RegisterModule() {
	registerMainMenuItem(datatugui.RootScreenViewers,
		datatugui.MainMenuItem{
			Text:     "Viewers",
			Shortcut: 'v',
			Action:   GoViewersScreen,
		})
}
