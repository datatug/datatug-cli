package clouds

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// GoCloudPlaceholderHome shows a placeholder screen for a cloud that is not implemented yet
func GoCloudPlaceholderHome(cContext *CloudContext, viewerID dtviewers.ViewerID, title, message string, focusTo sneatnav.FocusTo) error {
	menu := dtviewers.NewCloudsMenu(cContext.TUI, viewerID)

	textView := tview.NewTextView()
	sneatv.DefaultBorderWithPadding(textView.Box)
	textView.SetTitle(title)
	textView.SetText(message)

	textView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyUp:
			row, _ := textView.GetScrollOffset()
			if row == 0 {
				cContext.TUI.Header.SetFocus(sneatnav.ToBreadcrumbs, textView)
				return nil
			}
			return event
		case tcell.KeyLeft:
			cContext.TUI.Menu.TakeFocus()
			return nil
		default:
			return event
		}
	})

	content := sneatnav.NewPanel(cContext.TUI, sneatv.WithDefaultBorders(textView, textView.Box))
	cContext.TUI.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))

	return nil
}
