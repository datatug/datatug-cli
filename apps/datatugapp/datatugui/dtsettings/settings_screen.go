// Package dtsettings is the Settings screen of the DataTug terminal UI: the
// config file, syntax highlighted.
package dtsettings

import (
	tea "charm.land/bubbletea/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-core/pkg/dtconfig"
	"github.com/strongo/strongo-tui/pkg/highlight"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
	"gopkg.in/yaml.v3"
)

const fileName = " Config File: ~/.datatug.yaml"

// Seams replaced in tests.
var (
	// getLexerFn returns the chroma lexer of a language.
	getLexerFn = func(s string) chroma.Lexer { return lexers.Get(s) }
	// getSettingsFn reads the settings from the config file.
	getSettingsFn = dtconfig.GetSettings
	// marshalFn renders the settings as YAML.
	marshalFn = func(v any) ([]byte, error) { return yaml.Marshal(v) }
)

// Module registers the settings screen in the main menu.
func Module() datatugui.Module {
	return datatugui.Module{
		ID: datatugui.ScreenSettings, Text: "Settings", Shortcut: 's',
		Root: func() nav.Page { return nav.Page{Content: newSettings()} },
	}
}

// settingsLoaded is the result of loadSettings: the highlighted text to show.
type settingsLoaded struct {
	text string
	err  error
}

// loadSettings reads the config off the event loop. A config that cannot be
// read or rendered is shown as its error text, as the file itself would be.
func loadSettings() tea.Cmd {
	return func() tea.Msg {
		var text string
		settings, err := getSettingsFn()
		if err != nil {
			text = err.Error()
		} else if data, err := marshalFn(settings); err != nil {
			text = err.Error()
		} else {
			text = string(data)
		}
		colored, err := highlight.Colorize(text, highlight.DefaultStyle, getLexerFn("yaml"))
		return settingsLoaded{text: colored, err: err}
	}
}

// settings shows the config file.
type settings struct {
	pane widgets.TextPane
}

func newSettings() settings {
	return settings{pane: widgets.NewTextPane("settings")}
}

// Init implements nav.Screen.
func (s settings) Init() tea.Cmd { return loadSettings() }

// Update implements nav.Screen.
func (s settings) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case settingsLoaded:
		if msg.err != nil {
			return s, datatugui.ReportError("show settings", msg.err)
		}
		s.pane.SetContent(msg.text)
		return s, nil
	case nav.ScreenFocusMsg:
		if msg.Focused {
			s.pane.Focus()
		} else {
			s.pane.Blur()
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.pane, cmd = s.pane.Update(msg)
	return s, cmd
}

// View implements nav.Screen.
func (s settings) View() string { return s.pane.View() }

// Title implements nav.Titled.
func (s settings) Title() string { return fileName }

// AtEdge implements widgets.Boundary.
func (s settings) AtEdge(dir widgets.Direction) bool { return s.pane.AtEdge(dir) }
