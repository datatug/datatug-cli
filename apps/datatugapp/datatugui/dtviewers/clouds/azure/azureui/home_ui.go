// Package azureui is the Microsoft Azure viewer.
package azureui

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds"
)

const viewerID dtviewers.ViewerID = "azure"

// Viewer returns the Azure viewer of the Viewers list.
func Viewer() dtviewers.Viewer {
	return clouds.PlaceholderViewer(viewerID, "Microsoft Azure", 'm', "Azure is not implemented yet.")
}
