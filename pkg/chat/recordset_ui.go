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
	header := strings.TrimRight(g.recordsetHeader(paneWidth), " ")
	if g.activeView == recordsetTable {
		return g.viewWithTitle(header)
	}
	if !layout.split {
		return g.secondaryView(paneWidth, header)
	}
	secondary := g.secondaryView(layout.secondaryWidth, "")
	return lipgloss.JoinHorizontal(lipgloss.Top, g.viewWithTitle(header), strings.Repeat(" ", recordsetPaneGap), secondary)
}

func (g *gridState) recordsetHeader(width int) string {
	labels := []string{"1 Table", "2 Charts", "3 Current row"}
	if width < 62 {
		labels = []string{"1 Table", "2 Chart", "3 Row"}
	}
	if width < 42 {
		labels = []string{"1 Tab", "2 Chart", "3 Row"}
	}
	if width > 24 && width < 36 {
		labels = []string{"1 T", "2 C", "3 Row"}
	}
	if width <= 24 {
		labels = []string{"1", "2", "3"}
	}
	separator := " · "
	if width < 42 {
		separator = " "
	}
	controls := strings.Join(labels, separator)
	styledLabels := make([]string, len(labels))
	for i, label := range labels {
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
		if i == int(g.activeView) {
			style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231"))
		}
		styledLabels[i] = style.Render(label)
	}
	styledControls := strings.Join(styledLabels, separator)
	available := max(1, width-6)
	if ansi.StringWidth(controls) >= available {
		return padAnsiLine(styledControls, width)
	}
	titleWidth := max(1, available-ansi.StringWidth(controls)-3)
	title := ansi.Truncate(sanitizeTerminalText(g.title), titleWidth, "…")
	if g.focused {
		title = activeTitleStyle.Render(title)
	} else {
		title = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Render(title)
	}
	return padAnsiLine(title+" │ "+styledControls, width)
}

func (g *gridState) secondaryView(width int, heading string) string {
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
	if heading != "" {
		title = heading
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
		top = activeBorderStyle.Render(strings.TrimSuffix(top, "╮")) + inactiveBorderStyle.Render("╮")
	} else {
		top = inactiveBorderStyle.Render(top)
	}
	bottom = inactiveBorderStyle.Render(bottom)
	lines := []string{padAnsiLine(top, width)}
	body := strings.Split(content, "\n")
	for index := 0; index < bodyHeight; index++ {
		line := ""
		if index < len(body) {
			line = body[index]
		}
		leftBorder := inactiveBorderStyle
		if focused {
			leftBorder = activeBorderStyle
		}
		lines = append(lines, padAnsiLine(leftBorder.Render("│")+padAnsiLine(line, innerWidth)+inactiveBorderStyle.Render("│"), width))
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
