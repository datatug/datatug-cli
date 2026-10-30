package chat

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
)

// Catalog details are read-only and follow the Explorer selection captured at
// opening. They never execute a query or duplicate the tree in Inspect.
type catalogDetailsOverlay struct {
	object   ProjectObject
	viewport viewport.Model
}

func (p *catalogDetailsOverlay) HelpHints() []string {
	return []string{"Read-only catalog details", "↑↓ / PgUp/PgDn scroll", "Home/End edges", "Esc return to Explorer"}
}

func newCatalogDetailsOverlay(object ProjectObject) *catalogDetailsOverlay {
	return &catalogDetailsOverlay{object: object, viewport: viewport.New()}
}
func (p *catalogDetailsOverlay) View(w, h int) string {
	title, body := projectObjectDetails(p.object, max(1, w))
	p.viewport.SetWidth(max(1, w))
	p.viewport.SetHeight(max(1, h-2))
	p.viewport.SetContent(ansi.Wrap(body, max(1, w), " "))
	return ansi.Truncate(sanitizeTerminalText(title), max(1, w), "…") + "\n" + p.viewport.View() + "\n" + ansi.Truncate("↑↓ / PgUp/PgDn scroll · Home/End · Esc back", max(1, w), "…")
}
func (p *catalogDetailsOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return p, nil, true
		case "home":
			p.viewport.GotoTop()
			return p, nil, false
		case "end":
			p.viewport.GotoBottom()
			return p, nil, false
		}
	}
	var cmd tea.Cmd
	p.viewport, cmd = p.viewport.Update(msg)
	return p, cmd, false
}
