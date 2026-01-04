package azureui

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
)

const viewerID dtviewers.ViewerID = "azure"

func RegisterAsViewer() {
	dtviewers.RegisterViewer(dtviewers.Viewer{
		ID:          viewerID,
		Name:        "Microsoft Azure",
		Description: "(not implemented yet)",
		Shortcut:    'm',
		Action: func(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
			return GoAzureHome(&clouds.CloudContext{TUI: tui}, focusTo)
		},
	})
}

func GoAzureHome(cContext *AzureContext, focusTo sneatnav.FocusTo) error {
	return clouds.GoCloudPlaceholderHome(cContext, viewerID, "Microsoft Azure Viewer", "Azure is not implemented yet.", focusTo)
}
