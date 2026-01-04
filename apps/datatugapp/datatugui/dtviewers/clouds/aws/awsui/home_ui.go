package awsui

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

const viewerID dtviewers.ViewerID = "aws"

func RegisterAsViewer() {
	dtviewers.RegisterViewer(dtviewers.Viewer{
		ID:          viewerID,
		Name:        "Amazon Web Services",
		Description: "(not implemented yet)",
		Shortcut:    'a',
		Action: func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
			return goAwsHome(&clouds.CloudContext{TUI: tui}, focusTo)
		},
	})
}

func goAwsHome(cContext *AwsContext, focusTo sneatnav.FocusTo) error {
	return clouds.GoCloudPlaceholderHome(cContext, viewerID, "Amazon Web Services", "AWS is not implemented yet.", focusTo)
}
