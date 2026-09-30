// Package awsui is the Amazon Web Services viewer.
package awsui

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers/clouds"
)

const viewerID dtviewers.ViewerID = "aws"

// Viewer returns the AWS viewer of the Viewers list.
func Viewer() dtviewers.Viewer {
	return clouds.PlaceholderViewer(viewerID, "Amazon Web Services", 'a', "AWS is not implemented yet.")
}
