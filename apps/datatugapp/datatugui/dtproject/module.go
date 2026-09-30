package dtproject

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/strongo/strongo-tui/pkg/nav"
)

// Module returns the Projects entry of the main menu.
func Module() datatugui.Module {
	return datatugui.Module{
		ID:       datatugui.ScreenProjects,
		Text:     "Projects",
		Shortcut: 'p',
		Root:     func() nav.Page { return nav.Page{Content: newProjects()} },
	}
}
