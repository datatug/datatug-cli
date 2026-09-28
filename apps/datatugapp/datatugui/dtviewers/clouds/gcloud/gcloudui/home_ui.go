package gcloudui

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

const viewerID dtviewers.ViewerID = "gc"

var lastRegisteredViewer dtviewers.Viewer

func RegisterAsViewer() {
	v := dtviewers.Viewer{
		ID:          viewerID,
		Name:        "Google Cloud",
		Description: "Firestore, Cloud SQL, etc.",
		Shortcut:    'g',
		Action: func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
			return goHome(&GCloudContext{
				CloudContext: &clouds.CloudContext{TUI: tui},
			}, focusTo)
		},
	}
	lastRegisteredViewer = v
	dtviewers.RegisterViewer(v)
}

func goHome(cContext *GCloudContext, focusTo sneatnav.FocusTo) error {
	menu := dtviewers.NewCloudsMenu(cContext.TUI, viewerID)
	content := newMainMenu(cContext, ScreenProjects, true)
	go func() {
		_, _ = cContext.GetProjects()
	}()
	cContext.TUI.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))
	return nil
}
