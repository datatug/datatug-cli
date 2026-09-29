// Package clouds holds what the cloud viewers (Google Cloud, AWS, Azure) share.
package clouds

import (
	tea "charm.land/bubbletea/v2"
	"github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

// PlaceholderViewer returns the viewer of a cloud that is not implemented yet:
// its page shows message.
func PlaceholderViewer(id dtviewers.ViewerID, name string, shortcut rune, message string) dtviewers.Viewer {
	return dtviewers.Viewer{
		ID:          id,
		Name:        name,
		Description: "(not implemented yet)",
		Shortcut:    shortcut,
		Root: func() nav.Page {
			return nav.Page{Content: NewPlaceholder(name, message)}
		},
	}
}

// Placeholder is the screen of a cloud that is not implemented yet: a scrollable
// text under the cloud's title.
type Placeholder struct {
	title string
	pane  widgets.TextPane
}

var (
	_ nav.Screen       = Placeholder{}
	_ nav.Titled       = Placeholder{}
	_ widgets.Boundary = Placeholder{}
)

// NewPlaceholder creates the screen that shows message under title.
func NewPlaceholder(title, message string) Placeholder {
	pane := widgets.NewTextPane("clouds.placeholder")
	pane.SetContent(message)
	return Placeholder{title: title, pane: pane}
}

// Init implements nav.Screen.
func (Placeholder) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (p Placeholder) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.pane.SetSize(msg.Width, msg.Height)
		return p, nil
	case nav.ScreenFocusMsg:
		if msg.Focused {
			p.pane.Focus()
		} else {
			p.pane.Blur()
		}
		return p, nil
	}
	var cmd tea.Cmd
	p.pane, cmd = p.pane.Update(msg)
	return p, cmd
}

// View implements nav.Screen.
func (p Placeholder) View() string { return p.pane.View() }

// Title implements nav.Titled.
func (p Placeholder) Title() string { return p.title }

// AtEdge implements widgets.Boundary: Up at the top and Left leave the screen.
func (p Placeholder) AtEdge(dir widgets.Direction) bool { return p.pane.AtEdge(dir) }
