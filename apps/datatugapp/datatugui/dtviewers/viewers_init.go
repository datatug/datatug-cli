package dtviewers

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
)

var viewers []Viewer

func RegisterViewer(viewer Viewer) {
	viewers = append(viewers, viewer)
}

func RegisterModule() {
	datatugui.RegisterMainMenuItem(datatugui.RootScreenViewers,
		datatugui.MainMenuItem{
			Text:     "Viewers",
			Shortcut: 'v',
			Action:   GoViewersScreen,
		})
}
