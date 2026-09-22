package chat

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// recordsetView is presentation state local to one inline result. It never
// changes the underlying immutable RecordSet or its selected source row.
type recordsetView uint8

const (
	recordsetTable recordsetView = iota
	recordsetCharts
	recordsetCurrentRow
)

const (
	minSecondaryCells   = 40
	minUsefulTableCells = 32
	maxUsefulTableCells = 72
	recordsetPaneGap    = 1
)

type recordsetLayout struct {
	split          bool
	tableWidth     int
	secondaryWidth int
}

// chooseRecordsetLayout uses terminal character cells, not database columns.
// A table wider than maxUsefulTableCells retains bubble-table horizontal
// scrolling, leaving useful room for the selected secondary view.
func chooseRecordsetLayout(totalWidth, naturalTableWidth int, view recordsetView) recordsetLayout {
	totalWidth = max(1, totalWidth)
	if view == recordsetTable {
		return recordsetLayout{tableWidth: totalWidth}
	}
	useful := max(minUsefulTableCells, min(maxUsefulTableCells, naturalTableWidth))
	if totalWidth-useful-recordsetPaneGap >= minSecondaryCells {
		return recordsetLayout{split: true, tableWidth: useful, secondaryWidth: totalWidth - useful - recordsetPaneGap}
	}
	return recordsetLayout{secondaryWidth: totalWidth}
}

func naturalGridWidth(model GridModel) int {
	width := 2 // left border and scrollbar/right border
	for columnIndex := range model.Columns {
		columnWidth := ansi.StringWidth(model.header(columnIndex))
		for _, row := range model.Rows {
			if columnIndex < len(row) {
				columnWidth = max(columnWidth, ansi.StringWidth(row[columnIndex]))
			}
		}
		width += min(28, max(6, columnWidth))
		if columnIndex+1 < len(model.Columns) {
			width++
		}
	}
	return width
}

// currentRowContent renders the table-selected row into a vertical form. The
// surrounding Bubble Tea viewport owns scrolling; this function owns neither
// selection nor terminal layout.
func currentRowContent(model GridModel, rowIndex, width int) string {
	if rowIndex < 0 || rowIndex >= len(model.RawRows) {
		return "No current row."
	}
	width = max(1, width)
	var lines []string
	for columnIndex, column := range model.Columns {
		if columnIndex > 0 {
			lines = append(lines, "")
		}
		name := sanitizeTerminalText(column.Name)
		lines = append(lines, ansi.Wrap(name, width, " "))
		value := "—"
		if columnIndex < len(model.RawRows[rowIndex]) {
			if raw := model.RawRows[rowIndex][columnIndex]; raw == nil {
				value = "NULL"
			} else {
				value = sanitizeTerminalText(FormatValue(raw))
			}
		}
		lines = append(lines, ansi.Wrap(value, width, " "))
	}
	return strings.Join(lines, "\n")
}
