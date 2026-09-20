package chat

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

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

	sortColumn int
	sortDesc   bool
}

// NewGridModel converts a structured query result into display cells while
// preserving explicit result-column order.
func NewGridModel(result secureread.Result) GridModel {
	model := GridModel{Columns: make([]GridColumn, len(result.Columns)), Rows: make([][]string, len(result.Rows)), sortColumn: -1}
	for i, name := range result.Columns {
		model.Columns[i] = GridColumn{Name: name, Numeric: columnIsNumeric(result.Rows, name)}
	}
	for rowIndex, row := range result.Rows {
		cells := make([]string, len(result.Columns))
		for columnIndex, name := range result.Columns {
			cells[columnIndex] = FormatValue(row.Data[name])
		}
		model.Rows[rowIndex] = cells
	}
	return model
}

// Sort toggles ascending/descending ordering for one visible column.
func (m *GridModel) Sort(column int) {
	if column < 0 || column >= len(m.Columns) {
		return
	}
	if m.sortColumn == column {
		m.sortDesc = !m.sortDesc
	} else {
		m.sortColumn = column
		m.sortDesc = false
	}
	sort.SliceStable(m.Rows, func(i, j int) bool {
		left, right := m.Rows[i][column], m.Rows[j][column]
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
