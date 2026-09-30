package dbviewer

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/dal-go/dalgo/recordset"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/theme"
)

// valueKind classifies a cell value for colouring.
type valueKind int

const (
	kindNull valueKind = iota
	kindString
	kindBool
	kindFloat
	kindInt
	kindTime
	kindSlice
	kindOther
	kindError
)

// cellValue is what a recordset grid row holds for one cell: the display text
// (fmt.Sprint of the value, so the grid shows it as is) and the kind that
// decides its colour.
type cellValue struct {
	text string
	kind valueKind
}

func (v cellValue) String() string { return v.text }

// newCellValue formats a value read from a recordset.
func newCellValue(v any, err error) cellValue {
	if err != nil {
		return cellValue{fmt.Sprintf("ERROR: %v", err), kindError}
	}
	if v == nil {
		return cellValue{}
	}
	if t := reflect.TypeOf(v); t.Kind() == reflect.Slice {
		item := t.Elem().String()
		if item == "uint8" {
			item = "byte"
		}
		return cellValue{fmt.Sprintf("[]%v - %d", item, reflect.ValueOf(v).Len()), kindSlice}
	}
	switch x := v.(type) {
	case string:
		return cellValue{x, kindString}
	case bool:
		if x {
			return cellValue{"YES", kindBool}
		}
		return cellValue{"NO", kindBool}
	case float32, float64:
		return cellValue{fmt.Sprintf("%v", x), kindFloat}
	case int, int64, int32, int16, int8:
		return cellValue{fmt.Sprintf("%d", x), kindInt}
	case time.Time:
		return cellValue{formatTime(x), kindTime}
	}
	return cellValue{fmt.Sprintf("%T:%v", v, v), kindOther}
}

// formatTime shows a date without a time part when the time is midnight, and the
// zone only when it is not UTC.
func formatTime(t time.Time) string {
	_, offset := t.Zone()
	midnight := t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0
	switch {
	case midnight && offset == 0:
		return t.Format("2006-01-02")
	case midnight:
		return t.Format("2006-01-02") + " " + t.Location().String()
	case offset == 0:
		return t.Format("2006-01-02 15:04:05")
	}
	return t.Format(time.RFC3339)
}

// recordsetSource is a grid.RowSource over a recordset that is already in
// memory: only the page on screen is turned into rows.
type recordsetSource struct {
	rs     recordset.Recordset
	follow map[int]string // column -> table its values identify
	self   map[int]bool   // the column holds the ID of this very table
}

var _ grid.RowSource = recordsetSource{}

// newRecordsetSource wraps rs. A column that starts a foreign key identifies the
// rows of the referenced table; without one, a column named "<Table>ID" is taken
// to identify the rows of "<Table>s".
func newRecordsetSource(rs recordset.Recordset, fks []schemer.ForeignKey) recordsetSource {
	s := recordsetSource{rs: rs, follow: map[int]string{}, self: map[int]bool{}}
	for c := range rs.ColumnsCount() {
		name := rs.GetColumnByIndex(c).Name()
		if fk, ok := findForeignKey(fks, name); ok {
			s.follow[c] = fk.To.Name
			continue
		}
		if base, ok := strings.CutSuffix(name, "ID"); ok {
			if target := base + "s"; base == "" || target == rs.Name() {
				s.self[c] = true
			} else {
				s.follow[c] = target
			}
		}
	}
	return s
}

// Len implements grid.RowSource.
func (s recordsetSource) Len() int { return s.rs.RowsCount() }

// Row implements grid.RowSource.
func (s recordsetSource) Row(i int) grid.Row {
	values := make([]any, s.rs.ColumnsCount())
	for c := range values {
		v, err := s.rs.GetColumnByIndex(c).GetValue(i)
		values[c] = newCellValue(v, err)
	}
	return grid.Row{Key: strconv.Itoa(i), Values: values}
}

// columns describes the recordset's columns for the grid.
func (s recordsetSource) columns() []grid.Column {
	cols := make([]grid.Column, s.rs.ColumnsCount())
	for c := range cols {
		col := s.rs.GetColumnByIndex(c)
		cols[c] = grid.Column{Name: col.Name(), Numeric: isNumeric(col.ValueType())}
	}
	return cols
}

func isNumeric(t reflect.Type) bool {
	if t == nil {
		return false
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// newGrid builds the grid of the recordset, styled by cell kind.
func (s recordsetSource) newGrid(id string) *grid.Model {
	return grid.New(nil, nil, grid.WithID(id), grid.WithoutFrame(), grid.WithFilterDisabled(),
		grid.WithRowSource(s, s.columns()), grid.WithCellStyle(s.cellStyle))
}

// cellStyle colours a cell by the kind of its value; identifying columns are
// underlined when Enter follows them and tinted when they are the table's own.
func (s recordsetSource) cellStyle(_ grid.Row, column int, value any) lipgloss.Style {
	style := lipgloss.NewStyle()
	v, _ := value.(cellValue)
	switch v.kind {
	case kindString:
		style = style.Foreground(theme.LightSteelBlue)
	case kindBool:
		style = style.Foreground(theme.CornflowerBlue)
	case kindFloat:
		style = style.Foreground(theme.LightGoldenrodYellow)
	case kindInt:
		style = style.Foreground(theme.LightBlue)
	case kindTime:
		style = style.Foreground(theme.LightSalmon)
		if column%2 == 0 {
			style = style.Foreground(theme.LightCoral)
		}
	case kindSlice:
		style = style.Foreground(theme.Gray)
	case kindOther:
		style = style.Foreground(theme.LightGray)
	case kindError:
		style = style.Foreground(theme.Red)
	}
	if s.self[column] {
		style = style.Foreground(theme.PaleVioletRed)
	} else if s.follow[column] != "" {
		style = style.Underline(true)
	}
	return style
}
