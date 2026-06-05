package dtsettings

import (
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/datatug-core/dtconfig"
	"github.com/datatug/datatug-cli/pkg/dtlog"
	"github.com/datatug/datatug-cli/pkg/dtstate"
	"github.com/datatug/datatug-cli/pkg/sneatview/sneatnav"
	"github.com/datatug/filetug/pkg/chroma2tcell"
	"github.com/datatug/filetug/pkg/sneatv"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"gopkg.in/yaml.v3"
)

func RegisterModule() {
	datatugui.RegisterMainMenuItem(datatugui.RootScreenSettings,
		datatugui.MainMenuItem{
			Text:     "Settings",
			Shortcut: 's',
			Action:   GoSettingsScreen,
		})
}

// getLexerFn is the seam used in tests to substitute a custom chroma.Lexer.
var getLexerFn = func(s string) chroma.Lexer {
	return lexers.Get(s)
}

// getSettingsFn is the seam used in tests to simulate dtconfig.GetSettings errors.
var getSettingsFn func() (dtconfig.Settings, error) = dtconfig.GetSettings

// marshalFn is the seam used in tests to simulate yaml.Marshal errors.
var marshalFn = func(v interface{}) ([]byte, error) { return yaml.Marshal(v) }

// onTextViewReady is a test seam; nil in production.
// When non-nil, GoSettingsScreen calls it with the textView after SetInputCapture is registered.
var onTextViewReady func(tv *tview.TextView)

// onBreadcrumbPushed is a test seam; nil in production.
// When non-nil, GoSettingsScreen calls it with the breadcrumb action after Push.
var onBreadcrumbPushed func(action func() error)

func GoSettingsScreen(tui *sneatnav.TUI, focusTo sneatnav.FocusTo) error {
	breadcrumbs := tui.Header.Breadcrumbs()
	breadcrumbs.Clear()
	bcAction := func() error {
		return GoSettingsScreen(tui, sneatnav.FocusToContent)
	}
	breadcrumbs.Push(sneatv.NewBreadcrumb("Settings", bcAction))
	if onBreadcrumbPushed != nil {
		onBreadcrumbPushed(bcAction)
	}

	textView := tview.NewTextView()
	var settingsStr string
	setting, err := getSettingsFn()
	if err != nil {
		settingsStr = err.Error()
	}

	if settingsStr == "" {
		data, err := marshalFn(setting)
		if err != nil {
			settingsStr = err.Error()
		} else {
			settingsStr = string(data)
		}
	}

	const fileName = " Config File: ~/.datatug.yaml"

	settingsStr, err = chroma2tcell.ColorizeYAMLForTview(settingsStr, getLexerFn)
	if err != nil {
		return err
	}

	textView.
		SetDynamicColors(true).
		SetScrollable(true).
		SetText(settingsStr)

	content := sneatnav.NewPanel(tui, sneatv.WithDefaultBorders(textView, textView.Box))

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
	if onTextViewReady != nil {
		onTextViewReady(textView)
	}

	menu := datatugui.NewDataTugMainMenu(tui, datatugui.RootScreenSettings)
	tui.SetPanels(menu, content, sneatnav.WithFocusTo(sneatnav.FocusToMenu))
	if focusTo == sneatnav.FocusToContent {
		tui.App.SetFocus(content)
	}
	dtlog.ScreenOpened("settings", "Settings")
	dtstate.SaveCurrentScreePath("settings")
	return nil
}
