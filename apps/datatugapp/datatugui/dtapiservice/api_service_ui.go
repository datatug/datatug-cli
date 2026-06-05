package dtapiservice

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func RegisterModule() {
	datatugui.RegisterMainMenuItem(datatugui.RootScreenWebUI,
		datatugui.MainMenuItem{
			Text:     "API Monitor",
			Shortcut: 'w',
			Action:   GoApiServiceMonitor,
		})
}

// newTextViewFunc is a seam that lets tests capture the textView built by GoApiServiceMonitor.
var newTextViewFunc = func() *tview.TextView { return tview.NewTextView() }

func GoApiServiceMonitor(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	breadcrumbs := tui.Header.Breadcrumbs()
	breadcrumbs.Clear()
	breadcrumbs.Push(sneatv.NewBreadcrumb("API Monitor", func() error {
		return GoApiServiceMonitor(tui, sneatnav.FocusToContent)
	}))

	menu := datatugui.NewDataTugMainMenu(tui, datatugui.RootScreenWebUI)
	textView := newTextViewFunc()
	sneatv.DefaultBorderWithPadding(textView.Box)
	textView.SetTitle("Web UI & Local API Service Monitor")
	textView.SetText("Open web UI: https://datatug.app/pwa/#api=localhost:8080")
	textView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft, tcell.KeyESC, tcell.KeyBackspace:
			tui.Menu.TakeFocus()
			return nil
		case tcell.KeyUp:
			tui.SetFocus(tui.Header)
		default:
			return event
		}
		return event
	})

	content := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(textView, textView.Box))

	tui.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))
	dtlog.ScreenOpened("api_monitor", "API Monitor")
	dtstate.SaveCurrentScreePath("api_monitor")
	return nil
}
