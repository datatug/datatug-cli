// Package dtapiservice is the API Monitor screen of the DataTug terminal UI.
package dtapiservice

import (
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui"
	"github.com/datatug/datatug-cli/pkg/apimonitor"
	"github.com/tuigoff/tuigoff/pkg/nav"
	"github.com/tuigoff/tuigoff/pkg/widgets"
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
	pane                  widgets.TextPane
	width, height, offset int
}

func newMonitor() monitor {
	pane := widgets.NewTextPane("api-monitor")
	pane.SetContent(apimonitor.Render(apimonitor.Default.Snapshot(), 80, 24, 0))
	return monitor{pane: pane}
}

// Init implements nav.Screen.
func (m monitor) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (m monitor) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = size.Width, size.Height
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "up", "pgup":
			m.offset = min(apimonitor.MaxRequests, m.offset+1)
		case "down", "pgdown":
			m.offset = max(0, m.offset-1)
		case "end":
			m.offset = 0
		}
	}
	m.pane.SetContent(apimonitor.Render(apimonitor.Default.Snapshot(), max(20, m.width), max(10, m.height), m.offset))
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
func (m monitor) View() string {
	return apimonitor.Render(apimonitor.Default.Snapshot(), max(20, m.width), max(10, m.height), m.offset)
}

// Title implements nav.Titled.
func (m monitor) Title() string { return screenTitle }

// AtEdge implements widgets.Boundary.
func (m monitor) AtEdge(dir widgets.Direction) bool { return m.pane.AtEdge(dir) }
