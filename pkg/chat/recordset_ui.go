package chat

import (
	"sort"
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
	if g.rawBody != nil {
		labels = append(labels, "4 Raw")
		labels = append(labels, "5 Headers")
	}
	if width < 62 {
		labels = []string{"1 Table", "2 Chart", "3 Row"}
		if g.rawBody != nil {
			labels = append(labels, "4 Raw")
			labels = append(labels, "5 Headers")
		}
	}
	if width < 42 {
		labels = []string{"1 Tab", "2 Chart", "3 Row"}
		if g.rawBody != nil {
			labels = append(labels, "4 Raw")
			labels = append(labels, "5 Headers")
		}
	}
	if width > 24 && width < 36 {
		labels = []string{"1 T", "2 C", "3 Row"}
		if g.rawBody != nil {
			labels = append(labels, "4 Raw")
			labels = append(labels, "5 Hdr")
		}
	}
	if width <= 24 {
		labels = []string{"1", "2", "3"}
		if g.rawBody != nil {
			labels = append(labels, "4")
			labels = append(labels, "5")
		}
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
		if g.versionBadge != "" {
			return padAnsiLine(ansi.Truncate(g.versionBadge+" · "+styledControls, width, "…"), width)
		}
		return padAnsiLine(styledControls, width)
	}
	titleWidth := max(1, available-ansi.StringWidth(controls)-3)
	titleText := g.title
	if g.versionBadge != "" {
		titleText = g.versionBadge + " · " + titleText
	}
	title := ansi.Truncate(sanitizeTerminalText(titleText), titleWidth, "…")
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
	case recordsetRaw:
		title = "Raw response"
		g.inspector.SetWidth(innerWidth)
		g.inspector.SetHeight(recordsetPaneHeight)
		g.inspector.SetContent(sanitizeMultilineText(boundedText(g.rawBody)))
		content = g.inspector.View()
	case recordsetHeaders:
		title = "Request / response headers"
		g.inspector.SetWidth(innerWidth)
		g.inspector.SetHeight(recordsetPaneHeight)
		g.inspector.SetContent(g.headersContent(innerWidth))
		content = g.inspector.View()
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
		top = selectedOutlineStyle.Render(top)
		bottom = selectedOutlineStyle.Render(bottom)
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
		leftBorder := inactiveBorderStyle
		if focused {
			leftBorder = activeBorderStyle
		}
		rightBorder := inactiveBorderStyle
		if focused {
			rightBorder = selectedOutlineStyle
		}
		lines = append(lines, padAnsiLine(leftBorder.Render("│")+padAnsiLine(line, innerWidth)+rightBorder.Render("│"), width))
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
	case recordsetCurrentRow, recordsetRaw, recordsetHeaders:
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

func (g *gridState) headersContent(width int) string {
	if g.httpResponse == nil {
		return "No HTTP response metadata."
	}
	return g.httpResponse.headersContent(width)
}

func (response *HTTPResponse) headersContent(width int) string {
	request := []string{"Request", nonempty(response.Method, "GET") + " " + sanitizeTerminalText(response.URL), ""}
	request = append(request, formatHTTPHeaderLines(response.RequestHeaders)...)
	if len(response.RequestHeaders) == 0 {
		request = append(request, "(Request headers were not captured for this older result.)")
	}
	result := []string{
		"Response",
		"Status: " + strconv.Itoa(response.StatusCode),
		"Time to response: " + response.TimeToResponse.Round(1).String(),
		"Download: " + response.DownloadTime.Round(1).String(),
	}
	if len(response.Redirects) > 0 {
		result = append(result, "", "Redirects:")
		for i, hop := range response.Redirects {
			result = append(result, strconv.Itoa(i+1)+". "+strconv.Itoa(hop.StatusCode)+" "+sanitizeTerminalText(hop.URL)+" ("+hop.Elapsed.Round(1).String()+")")
		}
		result = append(result, "Final: "+sanitizeTerminalText(response.FinalURL))
	}
	result = append(result, "")
	result = append(result, formatHTTPHeaderLines(response.Headers)...)
	if width < 72 {
		return strings.Join(append(append(request, ""), result...), "\n")
	}
	leftWidth := (width - 3) / 2
	rightWidth := width - leftWidth - 3
	left, right := make([]string, 0, max(len(request), len(result))), make([]string, 0, max(len(request), len(result)))
	for i := 0; i < max(len(request), len(result)); i++ {
		leftLine, rightLine := "", ""
		if i < len(request) {
			leftLine = request[i]
		}
		if i < len(result) {
			rightLine = result[i]
		}
		left = append(left, lipgloss.NewStyle().Width(leftWidth).Render(ansi.Truncate(leftLine, leftWidth, "…")))
		right = append(right, ansi.Truncate(rightLine, rightWidth, "…"))
	}
	separator := strings.TrimSuffix(strings.Repeat(" │ \n", len(left)), "\n")
	return lipgloss.JoinHorizontal(lipgloss.Top, strings.Join(left, "\n"), separator, strings.Join(right, "\n"))
}

func formatHTTPHeaderLines(headers map[string][]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		for _, value := range headers[name] {
			lines = append(lines, sanitizeTerminalText(name)+": "+sanitizeTerminalText(value))
		}
	}
	return lines
}
