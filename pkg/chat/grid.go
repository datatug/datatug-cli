package chat

import (
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/strongo/aichat/tui/grid"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

const maxGridTitleWidth = 60

// normalizeGridTitle keeps model-supplied presentation text safe for a
// single-line terminal border. Empty or unusable titles use a deterministic
// fallback so the grid never depends on prose from the model.
func normalizeGridTitle(title string) string {
	// Titles are presentation metadata supplied by a model. Strip terminal
	// controls/escapes before putting the value into a border line.
	title = sanitizeTerminalText(title)
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return "Query result"
	}
	if ansi.StringWidth(title) > maxGridTitleWidth {
		return truncateGridText(title, maxGridTitleWidth)
	}
	return title
}

// sanitizeTerminalText makes arbitrary model/database text safe for terminal
// rendering while preserving ordinary international text and display values.
// ZWJ/ZWNJ are retained because they are required by many normal emoji and
// script grapheme clusters; bidi/format controls remain replaced by spaces.
func sanitizeTerminalText(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if r == '\u200c' || r == '\u200d' {
			return r
		}
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
}

// sanitizeMultilineText retains layout for downloaded documents while removing
// terminal control sequences and invisible control characters.
func sanitizeMultilineText(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\u200c' || r == '\u200d' {
			return r
		}
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
}

func truncateGridText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

// GridColumn is the UI-ready description of a structured result column. It is
// an alias for strongo/aichat's tui/grid.Column: DataTug and Sneat Chat share
// the same generic grid, so a result column has one definition, not two.
type GridColumn = grid.Column

// GridModel is the secureread → grid adapter: the terminal-grid boundary
// where structured query results are formatted into display cells. It is
// pure data — sorting, column selection, the table itself and its chrome all
// live in the shared strongo/aichat tui/grid.Model that gridState (ui.go)
// wraps; newGridState converts a GridModel into that Model's
// Columns/Rows once, and grid.Model owns everything from there (including
// its own display-order permutation on Sort — RawRows below stays fixed in
// the RecordSet's own row order, since gridState resolves a display row back
// to it via grid.Row.Key, not a parallel-sorted slice).
type GridModel struct {
	Columns []GridColumn
	Rows    [][]string
	// RawRows preserves the structured source values, indexed by the
	// RecordSet's own (never reordered) row order, alongside formatted cells
	// for typed interactions (FK lookups, saved-query parameter values, cell
	// detail) without leaking database types into the table itself.
	RawRows [][]any
}

// NewGridModel converts a structured query result into display cells while
// preserving explicit result-column order.
func NewGridModel(result secureread.Result) GridModel {
	model := GridModel{Columns: make([]GridColumn, len(result.Columns)), Rows: make([][]string, len(result.Rows)), RawRows: make([][]any, len(result.Rows))}
	for i, name := range result.Columns {
		model.Columns[i] = GridColumn{Name: sanitizeTerminalText(name), Numeric: columnIsNumeric(result.Rows, name)}
	}
	for rowIndex, row := range result.Rows {
		cells := make([]string, len(result.Columns))
		raw := make([]any, len(result.Columns))
		for columnIndex, name := range result.Columns {
			value, present := row.Data[name]
			if !present {
				// Sparse cell-range selections leave unselected cells absent;
				// distinguish them visually from an explicitly selected SQL NULL.
				continue
			}
			raw[columnIndex] = value
			cells[columnIndex] = sanitizeTerminalText(formatGridValue(name, value))
		}
		model.Rows[rowIndex] = cells
		model.RawRows[rowIndex] = raw
	}
	return model
}

func formatGridValue(columnName string, value any) string {
	if !isDateColumn(columnName) {
		return FormatValue(value)
	}
	var parsed time.Time
	switch v := value.(type) {
	case time.Time:
		parsed = v
	case string:
		var err error
		parsed, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return FormatValue(value)
		}
	case []byte:
		var err error
		parsed, err = time.Parse(time.RFC3339, string(v))
		if err != nil {
			return FormatValue(value)
		}
	default:
		return FormatValue(value)
	}
	if parsed.Hour() == 0 && parsed.Minute() == 0 && parsed.Second() == 0 && parsed.Nanosecond() == 0 {
		return parsed.Format(time.DateOnly)
	}
	return FormatValue(value)
}

func isDateColumn(name string) bool {
	if strings.Contains(name, "Date") {
		return true
	}
	lower := strings.ToLower(name)
	for offset := 0; offset < len(lower); {
		index := strings.Index(lower[offset:], "date")
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len("date")
		beforeBoundary := start == 0 || !isASCIIAlphaNumeric(lower[start-1])
		afterBoundary := end == len(lower) || !isASCIIAlphaNumeric(lower[end])
		if beforeBoundary && afterBoundary {
			return true
		}
		offset = end
	}
	return false
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func columnIsNumeric(rows []secureread.Row, name string) bool {
	for _, row := range rows {
		value := row.Data[name]
		if value == nil {
			continue
		}
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return true
		default:
			return false
		}
	}
	return false
}

// FormatValue applies basic terminal-safe value formatting. It is
// strongo/aichat's tui/grid.FormatValue: DataTug and Sneat Chat format grid
// values identically rather than each keeping its own copy.
var FormatValue = grid.FormatValue
