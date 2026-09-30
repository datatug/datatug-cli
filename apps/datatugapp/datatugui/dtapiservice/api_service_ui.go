// Package dtapiservice is the API Monitor screen of the DataTug terminal UI.
package dtapiservice

import (
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

const (
	screenTitle = "Web UI & Local API Service Monitor"
	monitorText = "Open web UI: https://datatug.app/pwa/#api=localhost:8080"
)

// Module registers the API monitor in the main menu.
func Module() datatugui.Module {
	return datatugui.Module{
		ID: datatugui.ScreenAPIMonitor, Text: "API Monitor", Shortcut: 'w',
		Root: func() nav.Page { return nav.Page{Content: newMonitor()} },
	}
}

// monitor shows how to reach the web UI and the local API service.
type monitor struct {
	pane widgets.TextPane
}

func newMonitor() monitor {
	pane := widgets.NewTextPane("api-monitor")
	pane.SetContent(monitorText)
	return monitor{pane: pane}
}

// Init implements nav.Screen.
func (m monitor) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (m monitor) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if focus, ok := msg.(nav.ScreenFocusMsg); ok {
		if focus.Focused {
			m.pane.Focus()
		} else {
			m.pane.Blur()
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.pane, cmd = m.pane.Update(msg)
	return m, cmd
}

// View implements nav.Screen.
func (m monitor) View() string { return m.pane.View() }

// Title implements nav.Titled.
func (m monitor) Title() string { return screenTitle }

// AtEdge implements widgets.Boundary.
func (m monitor) AtEdge(dir widgets.Direction) bool { return m.pane.AtEdge(dir) }
