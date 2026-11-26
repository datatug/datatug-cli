package dtproject

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/tuigoff/tuigoff/pkg/nav"
)

// Module returns the Projects entry of the main menu.
func Module() datatugui.Module {
	return projectsModule(false)
}

// DemoModule shows registered projects without the standard GitHub flows.
func DemoModule() datatugui.Module {
	return projectsModule(true)
}

func projectsModule(demo bool) datatugui.Module {
	return datatugui.Module{
		ID:       datatugui.ScreenProjects,
		Text:     "Projects",
		Shortcut: 'p',
		Root:     func() nav.Page { return nav.Page{Content: projects{demo: demo}} },
	}
}
