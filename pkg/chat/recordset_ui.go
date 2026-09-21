package chat

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const recordsetPaneHeight = maxGridHeight

func (g *gridState) recordsetLayout(paneWidth int) recordsetLayout {
	return chooseRecordsetLayout(paneWidth, g.naturalWidth, g.activeView)
}

func (g *gridState) recordsetView(paneWidth int) string {
	layout := g.recordsetLayout(paneWidth)
	if g.activeView != recordsetTable && !layout.split {
		g.setSecondaryFocus(true)
	}
	if g.activeView == recordsetTable || layout.split {
		if g.width != layout.tableWidth {
			g.width = layout.tableWidth
			g.rebuild()
		}
	}
	header := g.recordsetHeader(paneWidth)
	if g.activeView == recordsetTable {
		return header + "\n" + g.view()
	}
	secondary := g.secondaryView(layout.secondaryWidth)
	if !layout.split {
		return header + "\n" + secondary
	}
	return header + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, g.view(), strings.Repeat(" ", recordsetPaneGap), secondary)
}

func (g *gridState) recordsetHeader(width int) string {
	tabs := []string{"1 Table", "2 Charts", "3 Current row"}
	selected := int(g.activeView)
	if selected >= 0 && selected < len(tabs) {
		tabs[selected] = "[" + tabs[selected] + "]"
	}
	controls := " | " + strings.Join(tabs, " | ")
	rows := ": " + strconv.Itoa(len(g.model.Rows)) + " rows"
	if ansi.StringWidth(rows+controls)+1 > width {
		tabs = []string{"1 T", "2 C", "3 Row"}
		if selected >= 0 && selected < len(tabs) {
			tabs[selected] = "[" + tabs[selected] + "]"
		}
		controls = " | " + strings.Join(tabs, " | ")
	}
	titleWidth := max(1, width-ansi.StringWidth(rows+controls))
	title := ansi.Truncate(sanitizeTerminalText(g.title), titleWidth, "…")
	return padAnsiLine(title+rows+controls, width)
}

func (g *gridState) secondaryView(width int) string {
	width = max(3, width)
	innerWidth := max(1, width-2)
	title := "Charts"
	var content string
	switch g.activeView {
	case recordsetCharts:
		if len(g.charts) == 0 {
			content = "No chart candidates for this recordset."
		} else {
			g.chartIndex = max(0, min(g.chartIndex, len(g.charts)-1))
			candidate := g.charts[g.chartIndex]
			content = "Chart " + strconv.Itoa(g.chartIndex+1) + "/" + strconv.Itoa(len(g.charts)) + ": " + sanitizeTerminalText(candidate.Spec.Title) + "\n" +
				"↑↓ candidates • " + sanitizeTerminalText(candidate.Reason) + "\n" +
				renderChart(candidate.Spec, innerWidth, max(4, recordsetPaneHeight-2))
		}
	case recordsetCurrentRow:
		title = "Current row"
		content = g.inspectorView(innerWidth, recordsetPaneHeight)
	}
	return recordsetCard(title, content, width, recordsetPaneHeight, g.focused && g.secondaryFocus)
}

func (g *gridState) inspectorView(width, height int) string {
	sourceRow := g.selectedSourceRow()
	previousOffset := g.inspector.YOffset()
	g.inspector.SetWidth(max(1, width))
	g.inspector.SetHeight(max(1, height))
	g.inspector.SetContent(currentRowContent(g.model, g.rowIndex, max(1, width)))
	if sourceRow != g.inspectorRow {
		g.inspector.GotoTop()
		g.inspectorRow = sourceRow
	} else {
		g.inspector.SetYOffset(previousOffset)
	}
	return g.inspector.View()
}

func recordsetCard(title, content string, width, bodyHeight int, focused bool) string {
	width = max(3, width)
	innerWidth := width - 2
	if focused {
		title = activeTitleStyle.Render("● " + title)
	} else {
		title = inactiveTitleStyle.Render("○ " + title)
	}
	top := borderLine("╭", title, "╮", width)
	bottom := borderLine("╰", "", "╯", width)
	if focused {
		top = activeBorderStyle.Render(top)
		bottom = activeBorderStyle.Render(bottom)
	} else {
		top = inactiveBorderStyle.Render(top)
		bottom = inactiveBorderStyle.Render(bottom)
	}
	lines := []string{padAnsiLine(top, width)}
	body := strings.Split(content, "\n")
	for index := 0; index < bodyHeight; index++ {
		line := ""
		if index < len(body) {
			line = body[index]
		}
		lines = append(lines, padAnsiLine("│"+padAnsiLine(line, innerWidth)+"│", width))
	}
	lines = append(lines, padAnsiLine(bottom, width))
	return strings.Join(lines, "\n")
}

func (g *gridState) updateSecondary(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	switch g.activeView {
	case recordsetCharts:
		switch msg.String() {
		case "up":
			if g.chartIndex > 0 {
				g.chartIndex--
			}
		case "down":
			if g.chartIndex+1 < len(g.charts) {
				g.chartIndex++
			}
		}
		return nil, true
	case recordsetCurrentRow:
		switch msg.String() {
		case "up", "down", "pgup", "pgdown", "home", "end":
			var cmd tea.Cmd
			g.inspector, cmd = g.inspector.Update(msg)
			return cmd, true
		}
		return nil, true
	default:
		return nil, false
	}
}
