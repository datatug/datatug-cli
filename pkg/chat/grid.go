package chat

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
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

func truncateGridText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

// GridColumn is the UI-ready description of a structured result column.
type GridColumn struct {
	Name    string
	Numeric bool
}

// GridModel is the terminal-grid boundary. Values are formatted only here,
// after query execution has produced a structured secureread.Result.
type GridModel struct {
	Columns []GridColumn
	Rows    [][]string
	// RawRows preserves the structured source values alongside formatted cells
	// for future typed interactions without leaking database types into the UI
	// component adapter.
	RawRows [][]any
	// SourceRows maps a displayed (possibly sorted) row back to its immutable
	// RecordSet row index for durable selections.
	SourceRows []int

	sortColumn int
	sortDesc   bool
}

// NewGridModel converts a structured query result into display cells while
// preserving explicit result-column order.
func NewGridModel(result secureread.Result) GridModel {
	model := GridModel{Columns: make([]GridColumn, len(result.Columns)), Rows: make([][]string, len(result.Rows)), RawRows: make([][]any, len(result.Rows)), SourceRows: make([]int, len(result.Rows)), sortColumn: -1}
	for i, name := range result.Columns {
		model.Columns[i] = GridColumn{Name: sanitizeTerminalText(name), Numeric: columnIsNumeric(result.Rows, name)}
	}
	for rowIndex, row := range result.Rows {
		model.SourceRows[rowIndex] = rowIndex
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

// Sort toggles ascending/descending ordering for one visible column.
func (m *GridModel) Sort(column int) {
	if column < 0 || column >= len(m.Columns) {
		return
	}
	if len(m.SourceRows) != len(m.Rows) {
		m.SourceRows = make([]int, len(m.Rows))
		for i := range m.SourceRows {
			m.SourceRows[i] = i
		}
	}
	if m.sortColumn == column {
		m.sortDesc = !m.sortDesc
	} else {
		m.sortColumn = column
		m.sortDesc = false
	}
	order := make([]int, len(m.Rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		leftIndex, rightIndex := order[i], order[j]
		left, right := m.Rows[leftIndex][column], m.Rows[rightIndex][column]
		comparison := strings.Compare(left, right)
		if m.Columns[column].Numeric {
			leftNumber, leftOK := new(big.Rat).SetString(left)
			rightNumber, rightOK := new(big.Rat).SetString(right)
			if leftOK && rightOK {
				comparison = leftNumber.Cmp(rightNumber)
			}
		}
		if m.sortDesc {
			return comparison > 0
		}
		return comparison < 0
	})
	rows := make([][]string, len(m.Rows))
	copy(rows, m.Rows)
	if len(m.RawRows) > 0 {
		rawRows := make([][]any, len(m.Rows))
		copy(rawRows, m.RawRows)
		sourceRows := append([]int(nil), m.SourceRows...)
		sortedRawRows := make([][]any, len(m.Rows))
		for i, index := range order {
			m.Rows[i] = rows[index]
			sortedRawRows[i] = rawRows[index]
			m.SourceRows[i] = sourceRows[index]
		}
		m.RawRows = sortedRawRows
		return
	}
	for i, index := range order {
		m.Rows[i] = rows[index]
	}
}

func (m GridModel) header(column int) string {
	name := m.Columns[column].Name
	if m.sortColumn != column {
		return name
	}
	if m.sortDesc {
		return name + " ▼"
	}
	return name + " ▲"
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

// FormatValue applies basic terminal-safe value formatting.
func FormatValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "NULL"
	case string:
		return v
	case []byte:
		if utf8.Valid(v) {
			return string(v)
		}
		return "0x" + hex.EncodeToString(v)
	case time.Time:
		return v.Format(time.RFC3339)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
