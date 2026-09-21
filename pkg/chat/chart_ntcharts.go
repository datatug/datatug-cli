package chat

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/NimbleMarkets/ntcharts/v2/barchart"
	"github.com/NimbleMarkets/ntcharts/v2/linechart/timeserieslinechart"
	"github.com/charmbracelet/x/ansi"
)

// renderChart is the only NTCharts-specific boundary. Chart inference and
// RecordSet statistics do not import or depend on terminal chart types.
func renderChart(spec ChartSpec, width, height int) string {
	if width < 24 || height < 4 {
		return "Chart needs a wider pane."
	}
	if len(spec.Points) == 0 {
		return "No chart data."
	}
	var rendered string
	switch spec.Kind {
	case ChartBar:
		chart := barchart.New(width, height,
			barchart.WithHorizontalBars(),
			barchart.WithNoAutoBarWidth(),
			barchart.WithBarWidth(1),
			barchart.WithBarGap(0),
			barchart.WithStyles(
				lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
				lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
			),
		)
		limit := min(len(spec.Points), max(1, height-2))
		for _, point := range spec.Points[:limit] {
			label := ansi.Truncate(sanitizeTerminalText(point.Label), 14, "…")
			chart.Push(barchart.BarData{Label: fmt.Sprintf("%s %.0f", label, point.Value), Values: []barchart.BarValue{{Name: spec.Measure, Value: point.Value, Style: lipgloss.NewStyle().Foreground(lipgloss.Color("45"))}}})
		}
		chart.Draw()
		rendered = chart.View()
	case ChartLine:
		chart := timeserieslinechart.New(width, height)
		for _, point := range spec.Points {
			var stamp time.Time
			var err error
			if spec.Bucket == "month" {
				stamp, err = time.ParseInLocation("2006-01", point.Label, time.UTC)
			} else {
				stamp, err = time.ParseInLocation("2006-01-02", point.Label, time.UTC)
			}
			if err != nil {
				return "Chart dates could not be displayed."
			}
			chart.Push(timeserieslinechart.TimePoint{Time: stamp, Value: point.Value})
		}
		chart.DrawBraille()
		rendered = chart.View()
	default:
		return "This chart type is not available in the terminal."
	}
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}
