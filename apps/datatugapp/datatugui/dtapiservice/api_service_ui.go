package dtapiservice

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func RegisterModule() {
	datatugui.RegisterMainMenuItem(datatugui.RootScreenWebUI,
		datatugui.MainMenuItem{
			Text:     "API Monitor",
			Shortcut: 'w',
			Action:   goApiServiceMonitor,
		})
}

func goApiServiceMonitor(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	breadcrumbs := tui.Header.Breadcrumbs()
	breadcrumbs.Clear()
	breadcrumbs.Push(sneatv.NewBreadcrumb("API Monitor", func() error {
		return goApiServiceMonitor(tui, sneatnav.FocusToContent)
	}))

	menu := datatugui.NewDataTugMainMenu(tui, datatugui.RootScreenWebUI)
	textView := tview.NewTextView()
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

	content := sneatnav.NewPanel(tui, sneatnav.WithBox(textView, textView.Box))

	tui.SetPanels(menu, content, sneatnav.WithFocusTo(focusTo))
	return nil
}
