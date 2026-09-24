package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/grid"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// statusStyle/activeTitleStyle/userStyle/inactiveBorderStyle/
// activeBorderStyle/messageSurfaceBackground/selectedMessageBackground are
// DataTug's shared status/message-card styles, used both by ChatUI's own
// blocks (join_block.go, user_message_block.go) and, previously, by the
// legacy UI.
var (
	statusStyle               = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	activeTitleStyle          = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51"))
	activeBorderStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
	inactiveBorderStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	userStyle                 = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45"))
	messageSurfaceBackground  = lipgloss.Color("235")
	selectedMessageBackground = lipgloss.Color("237")
	// agentStyle is ui.go's original agentStyle var, kept unchanged:
	// http_document_block.go's httpDocumentBlock still uses it.
	agentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
)

// ProjectChoice identifies a configured DataTug project, not a database. It
// is ui.go's original type, moved here unchanged: ChatUI's
// SetProjectChoices/chatui_pickers.go's projectPickerOverlay still use it.
type ProjectChoice struct {
	Key    string
	Title  string
	Detail string
}

// responsiveGutter/contentWidth/padAnsiLine were DataTug's general
// terminal-layout helpers, used well beyond the grid (message cards, the
// workspace pane, the status bar) by the now-retired legacy UI; ChatUI
// computes its own widths through chatshell instead (see chatWidth's own
// comment). contentWidth survives only as grid_state_test.go's reference
// formula for the width a real ChatUI grid renders at -- responsiveGutter
// has no caller left outside it. padAnsiLine is still genuinely shared
// (chatui_sidepanel.go's View). tui/grid has its own copies (including
// borderLine, DataTug-side dead code now that the grid's own card border
// render fully moved there) for the grid's own card border, since a
// shared-code seam here would leak DataTug's rendering conventions into the
// generic package.
func responsiveGutter(width int) int {
	if width <= 2 {
		return 0
	}
	if width < 48 {
		return 1
	}
	return 2
}

func contentWidth(width int) int {
	return max(1, width-2*responsiveGutter(width))
}

func padAnsiLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = ansi.Truncate(line, width, "…")
	return line + strings.Repeat(" ", max(0, width-lipgloss.Width(line)))
}

// nextTableStyle cycles grid.Styles, treating a zero-valued current style
// (before the first explicit style change, or a saved-session restore
// that predates table-style persistence) as "currently Lines" so cycling
// starts at Soft instead of looping back to Lines itself.
func nextTableStyle(current grid.Style) grid.Style {
	if current.Name == "" {
		current = grid.StyleLines
	}
	for i, style := range grid.Styles {
		if style.Name == current.Name {
			return grid.Styles[(i+1)%len(grid.Styles)]
		}
	}
	return grid.Styles[0]
}

// formatLimitations renders a RecordSet's access limitations (row/column
// redaction, truncation, etc.) as a single note, or "" when there are none.
func formatLimitations(limitations []secureread.Limitation) string {
	lines := make([]string, 0, len(limitations))
	for _, limitation := range limitations {
		switch limitation.Kind {
		case secureread.LimitationPolicy, secureread.LimitationNativeSQL:
			if limitation.Note != "" {
				lines = append(lines, limitation.Note)
			}
		case secureread.LimitationRowsFiltered:
			lines = append(lines, "rows filtered by policy")
		case secureread.LimitationHiddenColumns:
			lines = append(lines, "hidden columns: "+strings.Join(limitation.Columns, ", "))
		}
	}
	return strings.Join(lines, "; ")
}

// userMessageView renders a user prompt as an OpenCode-style card: a left
// accent bar that brightens with selection, an elevated background, vertical
// padding around the text and the same background behind every segment so
// the text never resets to the terminal background. Used by ChatUI's
// user_message_block.go.
func userMessageView(text string, width int, selected bool) string {
	width = max(1, width)
	background := messageSurfaceBackground
	barStyle := inactiveBorderStyle
	if selected {
		background = selectedMessageBackground
		barStyle = activeBorderStyle
	}
	interior := max(1, width-1)
	label := userStyle.Background(background).Render("You: ")
	body := lipgloss.NewStyle().Background(background).Render(sanitizeTerminalText(text))
	card := lipgloss.NewStyle().
		Background(background).
		Padding(1, 1).
		Width(interior).
		Render(label + body)
	lines := strings.Split(card, "\n")
	for i, line := range lines {
		lines[i] = barStyle.Render("┃") + line
	}
	return strings.Join(lines, "\n")
}

// conciseError renders an error for the transcript: the message alone,
// trimmed and bounded, without a Go %v wrapper chain's noise.
func conciseError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:237] + "..."
	}
	return message
}
