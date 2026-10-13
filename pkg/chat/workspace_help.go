package chat

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/tuigoff/tuigoff/pkg/theme"
)

type workspaceHelpRow struct{ key, description string }
type workspaceHelpSection struct {
	title string
	rows  []workspaceHelpRow
}

// Keep compound chords and sequences whole. The same structured rows drive
// the styled F1 overlay and the plain /help response.
var workspaceHelpSections = []workspaceHelpSection{
	{"Navigation", []workspaceHelpRow{
		{"F3", "choose project"}, {"F4", "composer"}, {"F6", "Explorer"}, {"F7", "Inspect"},
		{"F8", "Server"}, {"F9", "Expand result"}, {"F10", "Related"},
		{"Shift+↑↓", "history"}, {"Shift+←/→", "move between panels"}, {"Esc", "restore/composer"},
	}},
	{"Chats", []workspaceHelpRow{
		{"F11", "focus chat tabs"}, {"←/→", "select tab"}, {"Enter", "activate selected tab"},
		{"Esc", "restore previous control"}, {"Ctrl+Tab", "next chat"}, {"Ctrl+Shift+Tab", "previous chat"},
		{"+ New", "create chat"}, {"…", "restore archives"}, {"Ctrl+W", "choose Archive/Delete/Cancel"},
		{"F12 then A", "archive directly"}, {"F12 then D", "delete directly"},
		{"/sessions", "choose chat"}, {"/switch", "switch chat"}, {"/new", "create chat"}, {"/rename", "rename chat"},
	}},
	{"Explorer and grid", []workspaceHelpRow{
		{"", "Type to search"}, {"Ctrl+F", "filter objects"}, {"/", "search"}, {"↑↓←→", "navigate tree"},
		{"Esc", "clear search, then leave search"}, {"Ctrl+Shift+←/→", "resize focused side panel"},
	}},
	{"Result", []workspaceHelpRow{
		{"Tab/Shift+Tab", "switch Table, Charts, Current row and Query"},
		{"Ctrl+P", "Actions for Group, Sort, JOIN and table style"}, {"Alt+Shift+↑↓", "loaded sort"},
		{"Ctrl+Shift+↑↓", "server-sort preview"}, {"Enter", "details"}, {"Ctrl+Space", "result actions"},
		{"Ctrl+S", "save query"}, {"Ctrl+E", "export"}, {"Ctrl+R", "refresh"}, {"Alt+1..0", "Related"},
	}},
	{"Related", []workspaceHelpRow{
		{"F10", "focus"}, {"Alt+1..0", "open numbered relationship"}, {"↑↓", "select"}, {"Enter", "open"}, {"Esc", "fields"},
	}},
	{"Composer", []workspaceHelpRow{
		{"Enter", "send"}, {"Shift+Enter", "newline"}, {"Tab", "attachments / Select text action"},
		{"Backspace", "remove focused attachment"}, {"Ctrl+D", "remove last attachment"}, {"Esc", "previous control, retain draft"},
	}},
	{"Global", []workspaceHelpRow{
		{"F1", "Help"}, {"Ctrl+P", "Actions"}, {"Ctrl+F1", "toolbar"}, {"F2", "mouse selection"}, {"F5", "web"}, {"Ctrl+C", "quit"},
	}},
	{"Commands", []workspaceHelpRow{
		{"/query", "query editor"}, {"/queries", "saved queries"}, {"/http", "HTTP request"},
		{"/connect", "connect source"}, {"/export", "export results"}, {"/bucket", "export bucket"},
		{"/settings", "settings"}, {"/plan", "hosted plan"}, {"/keys", "keyboard diagnostic"}, {"/trail", "retained results"},
	}},
	{"API", []workspaceHelpRow{{"↑↓", "scroll requests"}, {"End", "follow"}, {"Esc", "back"}}},
}

func plainWorkspaceHelp() string {
	var sections []string
	for _, section := range workspaceHelpSections {
		var rows []string
		for _, row := range section.rows {
			rows = append(rows, strings.TrimSpace(row.key+" "+row.description))
		}
		sections = append(sections, section.title+": "+strings.Join(rows, " • "))
	}
	return strings.Join(sections, "\n\n")
}

func currentControlHelpRow(text string) workspaceHelpRow {
	// Context hints predate structured help; prefer exact structured keys,
	// longest first, instead of splitting a multi-word sequence.
	var best string
	for _, section := range workspaceHelpSections {
		for _, row := range section.rows {
			if row.key != "" && strings.HasPrefix(text, row.key+" ") && len(row.key) > len(best) {
				best = row.key
			}
		}
	}
	if best != "" {
		return workspaceHelpRow{best, strings.TrimPrefix(text, best+" ")}
	}
	key, description, ok := strings.Cut(text, " ")
	if ok && (strings.HasPrefix(key, "Ctrl+") || strings.HasPrefix(key, "Alt+") || strings.HasPrefix(key, "Shift+") || strings.ContainsAny(key, "↑↓←→") || key == "PgUp/PgDn" || key == "Tab/Shift+Tab" || (len(key) > 1 && key[0] == 'F' && key[1] >= '0' && key[1] <= '9')) {
		return workspaceHelpRow{key, description}
	}
	return workspaceHelpRow{description: text}
}

func renderWorkspaceHelpRow(row workspaceHelpRow, width int) []string {
	width = max(1, width)
	// At ordinary overlay widths, descriptions share a fixed column. In a
	// narrow overlay stack keys above indented, hard-wrapped descriptions.
	const keyWidth = 17
	if width >= keyWidth+14 && ansi.StringWidth(row.key) <= keyWidth {
		description := strings.Split(ansi.Wrap(row.description, width-keyWidth-2, " "), "\n")
		for i, line := range description {
			key := ""
			if i == 0 {
				key = row.key
			}
			description[i] = theme.WorkspaceKeyHint(key) + strings.Repeat(" ", keyWidth+2-ansi.StringWidth(key)) + line
		}
		return description
	}
	var lines []string
	if row.key != "" {
		lines = append(lines, strings.Split(ansi.Hardwrap(theme.WorkspaceKeyHint(row.key), width, false), "\n")...)
	}
	indent := min(2, width-1)
	for _, line := range strings.Split(ansi.Wrap(row.description, max(1, width-indent), " "), "\n") {
		lines = append(lines, strings.Repeat(" ", indent)+line)
	}
	return lines
}
