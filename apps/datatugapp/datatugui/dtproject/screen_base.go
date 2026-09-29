package dtproject

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/strongo/strongo-tui/pkg/nav"
	"github.com/strongo/strongo-tui/pkg/theme"
	"github.com/strongo/strongo-tui/pkg/widgets"
)

// geometry is what the shell last told a screen: its size and whether it holds
// focus.
type geometry struct {
	w, h    int
	focused bool
}

// track records a size or focus message and reports whether msg was one, so the
// screen can pass the new values on to its components.
func (g *geometry) track(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		g.w, g.h = msg.Width, msg.Height
		return true
	case nav.ScreenFocusMsg:
		g.focused = msg.Focused
		return true
	}
	return false
}

// textScreen is a screen with a title and a few centred lines of text, for the
// sections that have nothing to list yet.
type textScreen struct {
	geo   geometry
	title string
	text  string
}

var (
	_ nav.Screen       = textScreen{}
	_ widgets.Boundary = textScreen{}
	_ nav.Titled       = textScreen{}
)

func newTextScreen(title, text string) textScreen { return textScreen{title: title, text: text} }

// Init implements nav.Screen.
func (t textScreen) Init() tea.Cmd { return nil }

// Update implements nav.Screen.
func (t textScreen) Update(msg tea.Msg) (nav.Screen, tea.Cmd) {
	t.geo.track(msg)
	return t, nil
}

// View implements nav.Screen.
func (t textScreen) View() string {
	lines := strings.Split(t.text, "\n")
	for i, line := range lines {
		lines[i] = widgets.AlignIn(line, t.geo.w, widgets.AlignCenter)
	}
	return widgets.Fit(strings.Join(lines, "\n"), t.geo.w, t.geo.h)
}

// Title implements nav.Titled.
func (t textScreen) Title() string { return t.title }

// AtEdge implements widgets.Boundary: there is nothing to move inside.
func (t textScreen) AtEdge(widgets.Direction) bool { return true }

// errorText renders an error message in the danger colour.
func errorText(err error) string { return theme.Danger(err.Error()) }
