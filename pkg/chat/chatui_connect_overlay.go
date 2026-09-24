package chat

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/chatshell"
)

// connectOverlay is ChatUI's chatshell.Overlay port of slash_commands.go's
// connectOverlay: a read-only preview of the current connection (checklist
// item #40 — "/connect" is a non-mutating preview both before and after
// this port; connection switching was already "coming soon" in ui.go).
type connectOverlay struct {
	ui *ChatUI
}

func (o *connectOverlay) Update(msg tea.Msg) (chatshell.Overlay, tea.Cmd, bool) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter", "esc":
			return o, nil, true
		}
	}
	return o, nil, false
}

func (o *connectOverlay) View(width, height int) string {
	width = max(24, min(width, 64))
	project := nonempty(o.ui.catalog.Title, "Current project")
	environment, database := "not available", "not available"
	if o.ui.sessions != nil && o.ui.sessions.store != nil {
		environment = nonempty(o.ui.sessions.store.info.Environment, environment)
		database = nonempty(o.ui.sessions.store.info.Database, database)
	}
	lines := []string{
		"Connect to data", "",
		"Project: " + project,
		"Environment: " + environment,
		"Database: " + database,
		"", "Connection switching is coming soon.",
		"The current connection has not changed.", "", "Enter/Esc close",
	}
	inside := max(1, width-6)
	for i, line := range lines {
		lines[i] = ansi.Truncate(sanitizeTerminalText(line), inside, "…")
	}
	return lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
}

var _ chatshell.Overlay = (*connectOverlay)(nil)
