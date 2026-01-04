package dtsettings

import (
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatv"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
)

func RegisterModule() {
	datatugui.RegisterMainMenuItem(datatugui.RootScreenSettings,
		datatugui.MainMenuItem{
			Text:     "Settings",
			Shortcut: 's',
			Action:   goSettingsScreen,
		})
}

func goSettingsScreen(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	breadcrumbs := tui.Header.Breadcrumbs()
	breadcrumbs.Clear()
	breadcrumbs.Push(sneatv.NewBreadcrumb("Settings", func() error {
		return goSettingsScreen(tui, sneatnav.FocusToContent)
	}))

	textView := tview.NewTextView()
	var settingsStr string
	setting, err := dtconfig.GetSettings()
	if err != nil {
		settingsStr = err.Error()
	}

	if settingsStr == "" {
		data, err := yaml.Marshal(setting)
		if err != nil {
			settingsStr = err.Error()
		} else {
			settingsStr = string(data)
		}
	}

	const fileName = " Config File: ~/.datatug.yaml"
	textView.SetText(settingsStr)

	content := sneatnav.NewPanel(tui, sneatnav.WithBox(textView, textView.Box))

	sneatv.DefaultBorderWithPadding(textView.Box)
	textView.SetTitle(fileName)
	textView.SetTitleAlign(tview.AlignLeft)

	textView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyLeft:
			tui.Menu.TakeFocus()
			return nil
		case tcell.KeyUp:
			row, _ := textView.GetScrollOffset()
			if row == 0 {
				tui.Header.SetFocus(sneatnav.ToBreadcrumbs, textView)
				return nil
			}
			return event
		default:
			return event
		}
	})

	menu := datatugui.NewDataTugMainMenu(tui, datatugui.RootScreenSettings)
	tui.SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToMenu))
	if focusTo == sneatnav.FocusToContent {
		tui.App.SetFocus(content)
	}
	return nil
}
