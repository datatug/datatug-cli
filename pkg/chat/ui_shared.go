package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/grid"
	"github.com/strongo/aichat/tui/theme"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

// statusStyle/activeTitleStyle/agentStyle are DataTug's shared in-card text
// styles (join_block.go, http_document_block.go) -- FUNCTIONS, not package
// vars: every one of them must reflect tui/theme's CURRENT Dark variant on
// every render, not whatever it was when the package first loaded (theme.
// SetDark can change it at runtime). Every card/block these render inside
// is now a shared theme.Card (strongo/aichat#chat-shared-look), so their
// colours come from theme too -- never a local lipgloss.Color(...) literal.
func statusStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(theme.MutedColor()) }
func activeTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(theme.FocusColor())
}
func agentStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(theme.AccentColor())
}

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

// userMessageView renders a user prompt's CONTENT only: plain sanitized
// text, no border, no background of its own. It used to draw an
// "OpenCode-style" card here directly (a left accent bar, its own
// background, padding) — that framing is now theme.Card's job, applied
// automatically by transcript around every Block's View (userMessageBlock
// reports theme.RoleUser via the Roled capability, so it gets the exact
// same card a plain, non-Block user message does) — drawing it AGAIN here
// was a double frame (strongo/aichat#chat-shared-look).
func userMessageView(text string, _ int, _ bool) string {
	return sanitizeTerminalText(text)
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
