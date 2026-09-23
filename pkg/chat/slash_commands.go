package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type slashCommand struct {
	name        string
	description string
	arguments   bool
}

var slashCommands = []slashCommand{
	{"/new", "Start a new chat session", false},
	{"/export", "Export a RecordSet or bucket", false},
	{"/clear", "Clear this session (requires confirm)", true},
	{"/connect", "Connection dialog (preview)", false},
	{"/http", "Compose an HTTP request", false},
	{"/query", "Find and run a saved project query", true},
	{"/queries", "Browse saved project queries", true},
	{"/settings", "Chat settings (result versions)", true},
	{"/sessions", "List chat sessions", false},
	{"/switch", "Switch to a session", true},
	{"/rename", "Rename this session", true},
	{"/delete", "Delete this session (requires confirm)", true},
	{"/bucket", "Show or clear the export bucket", false},
	{"/help", "Show chat help", false},
}

func (c slashCommand) insertText() string {
	if c.arguments {
		return c.name + " "
	}
	return c.name
}

func (u *UI) commandMenuMatches() []slashCommand {
	if u.busy || u.gridFocused || u.messageFocused || u.workspaceFocused || !u.input.Focused() {
		return nil
	}
	input := u.input.Value()
	if !strings.HasPrefix(input, "/") || strings.ContainsAny(input, " \t\n") || u.commandMenuDismissed == input {
		return nil
	}
	matches := make([]slashCommand, 0, len(slashCommands))
	for _, command := range slashCommands {
		if strings.HasPrefix(command.name, strings.ToLower(input)) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (u *UI) commandMenuVisible() bool { return len(u.commandMenuMatches()) > 0 }

func (u *UI) commandMenuHeight() int {
	if matches := u.commandMenuMatches(); len(matches) > 0 {
		return 1 + min(6, len(matches))
	}
	return 0
}

func (u *UI) commandMenuView(width int) string {
	matches := u.commandMenuMatches()
	if len(matches) == 0 {
		return ""
	}
	width = max(1, width)
	visible := min(6, len(matches))
	selected := min(u.commandMenuIndex, len(matches)-1)
	start := max(0, selected-visible+1)
	if start+visible > len(matches) {
		start = len(matches) - visible
	}
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("235"))
	base := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("235"))
	active := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")).Background(lipgloss.Color("238"))
	lines := []string{muted.Width(width).Render(" Commands · ↑↓ choose · Enter insert · Esc close")}
	for i := start; i < start+visible; i++ {
		command := matches[i]
		prefix := "  "
		style := base
		if i == selected {
			prefix = "› "
			style = active
		}
		line := prefix + command.name + "  " + command.description
		lines = append(lines, style.Width(width).Render(ansi.Truncate(line, width, "…")))
	}
	return strings.Join(lines, "\n")
}

func (u *UI) connectOverlay(background string) string {
	width := max(24, min(u.width-4, 64))
	project := nonempty(u.catalog.Title, "Current project")
	environment, database := "not available", "not available"
	if u.sessions != nil && u.sessions.store != nil {
		environment = nonempty(u.sessions.store.info.Environment, environment)
		database = nonempty(u.sessions.store.info.Database, database)
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
	box := lipgloss.NewStyle().Width(width-2).Padding(0, 1).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Background(lipgloss.Color("235")).Foreground(lipgloss.Color("252")).Render(strings.Join(lines, "\n"))
	canvas := lipgloss.NewCanvas(u.width, u.height)
	canvas.Compose(lipgloss.NewLayer(background))
	canvas.Compose(lipgloss.NewLayer(box).X(max(0, (u.width-lipgloss.Width(box))/2)).Y(max(0, (u.height-lipgloss.Height(box))/2)))
	return canvas.Render()
}
