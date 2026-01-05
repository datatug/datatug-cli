package dtproject

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
)

func RegisterModule() {
	datatugui.RegisterMainMenuItem(datatugui.RootScreenProjects,
		datatugui.MainMenuItem{
			Text:     "Projects",
			Shortcut: 'p',
			Action:   GoDataTugProjectsScreen,
		})
}
