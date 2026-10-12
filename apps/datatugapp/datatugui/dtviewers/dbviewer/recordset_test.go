package dbviewer

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dal-go/dalgo/recordset"
	"github.com/datatug/datatug-core/pkg/schemer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tuigoff/tuigoff/pkg/grid"
	"github.com/tuigoff/tuigoff/pkg/theme"
	"github.com/tuigoff/tuigoff/pkg/uitest"
)

func TestNewCellValue(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	cases := []struct {
		name  string
		value any
		err   error
		text  string
		kind  valueKind
	}{
		{"error", nil, errors.New("boom"), "ERROR: boom", kindError},
		{"null", nil, nil, "NULL", kindNull},
		{"string", "alice", nil, "alice", kindString},
		{"bytes", []byte("raw"), nil, "[]byte - 3", kindSlice},
		{"ints", []int{1, 2}, nil, "[]int - 2", kindSlice},
		{"true", true, nil, "YES", kindBool},
		{"false", false, nil, "NO", kindBool},
		{"float", 3.5, nil, "3.5", kindFloat},
		{"int", int64(42), nil, "42", kindInt},
		{"date", time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), nil, "2023-01-01", kindTime},
		{"date in zone", time.Date(2023, 1, 1, 0, 0, 0, 0, ny), nil, "2023-01-01 America/New_York", kindTime},
		{"time", time.Date(2023, 1, 1, 15, 4, 5, 0, time.UTC), nil, "2023-01-01 15:04:05", kindTime},
		{"time in zone", time.Date(2023, 1, 1, 15, 4, 5, 0, ny), nil, "2023-01-01T15:04:05-05:00", kindTime},
		{"other", complex(1, 2), nil, "complex128:(1+2i)", kindOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := newCellValue(c.value, c.err)
			assert.Equal(t, c.text, got.String())
			assert.Equal(t, c.kind, got.kind)
		})
	}
}

func userRecordset() *testRecordset {
	return &testRecordset{name: "users", rows: 2, columns: []recordset.Column[any]{
		&testColumn{name: "ID", values: []any{int64(1), int64(2)}, typ: reflect.TypeFor[int64]()},
		&testColumn{name: "name", values: []any{"a", "b"}, typ: reflect.TypeFor[string]()},
		&testColumn{name: "orderID", values: []any{int64(1), int64(2)}},
		&testColumn{name: "userID", values: []any{int64(1), int64(2)}},
		&testColumn{name: "bad", err: errors.New("unreadable")},
	}}
}

func TestRecordsetSource_FollowsForeignKeysAndIDColumns(t *testing.T) {
	fks := []schemer.ForeignKey{{From: schemer.FKAnchor{Name: "users", Columns: []string{"name"}}, To: schemer.FKAnchor{Name: "people", Columns: []string{"id"}}}}
	src := newRecordsetSource(userRecordset(), fks)
	assert.Equal(t, map[int]string{1: "people", 2: "orders"}, src.follow)
	assert.Equal(t, map[int]bool{0: true, 3: true}, src.self) // "ID" and "user"+"ID" are the table's own
}

func TestRecordsetSource_RowsAndColumns(t *testing.T) {
	src := newRecordsetSource(userRecordset(), nil)
	assert.Equal(t, 2, src.Len())
	row := src.Row(1)
	assert.Equal(t, "1", row.Key)
	assert.Equal(t, "2", row.Values[0].(cellValue).String())
	assert.Equal(t, "b", row.Values[1].(cellValue).String())
	assert.Equal(t, kindError, row.Values[4].(cellValue).kind)

	cols := src.columns()
	assert.Equal(t, grid.Column{Name: "ID", Numeric: true}, cols[0])
	assert.Equal(t, grid.Column{Name: "name"}, cols[1])
	assert.Equal(t, grid.Column{Name: "orderID"}, cols[2]) // no value type
}

func TestIsNumeric(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[int](), reflect.TypeFor[uint8](), reflect.TypeFor[float32]()} {
		assert.True(t, isNumeric(typ), typ)
	}
	for _, typ := range []reflect.Type{nil, reflect.TypeFor[string](), reflect.TypeFor[bool]()} {
		assert.False(t, isNumeric(typ), typ)
	}
}

func TestRecordsetSource_CellStyle(t *testing.T) {
	src := newRecordsetSource(userRecordset(), nil)
	fg := func(v cellValue, column int) any {
		return src.cellStyle(grid.Row{}, column, v).GetForeground()
	}
	assert.Equal(t, theme.LightSteelBlue, fg(cellValue{kind: kindString}, 1))
	assert.Equal(t, theme.CornflowerBlue, fg(cellValue{kind: kindBool}, 1))
	assert.Equal(t, theme.LightGoldenrodYellow, fg(cellValue{kind: kindFloat}, 1))
	assert.Equal(t, theme.LightBlue, fg(cellValue{kind: kindInt}, 1))
	assert.Equal(t, theme.LightCoral, fg(cellValue{kind: kindTime}, 2)) // even column
	assert.Equal(t, theme.LightSalmon, fg(cellValue{kind: kindTime}, 1))
	assert.Equal(t, theme.Gray, fg(cellValue{kind: kindSlice}, 1))
	assert.Equal(t, theme.LightGray, fg(cellValue{kind: kindOther}, 1))
	assert.Equal(t, theme.Red, fg(cellValue{kind: kindError}, 1))
	assert.Equal(t, theme.PaleVioletRed, fg(cellValue{kind: kindInt}, 0)) // the table's own ID
	assert.True(t, src.cellStyle(grid.Row{}, 2, cellValue{kind: kindInt}).GetUnderline())
	assert.False(t, src.cellStyle(grid.Row{}, 1, "not a cell value").GetUnderline())
}

func TestRecordsetSource_Grid(t *testing.T) {
	g := newRecordsetSource(userRecordset(), nil).newGrid("g")
	g.SetSize(60, 10)
	view := uitest.Plain(g.View(60, true))
	assert.Contains(t, view, "name")
	assert.Contains(t, view, "ERROR: unreadable")
}

// A NULL is nil in a recordset (dalgo2sql since v0.26.5, for SQLite and PostgreSQL alike), and the viewer shows it as
// NULL, in a cell of its own style: never as 0, an empty text, NO or the zero time, which are values.
func TestRecordsetSource_ANullIsShownAsNullInEveryKindOfColumn(t *testing.T) {
	rs := &testRecordset{name: "readings", rows: 2, columns: []recordset.Column[any]{
		&testColumn{name: "n", values: []any{nil, int64(0)}, typ: reflect.TypeFor[int64]()},
		&testColumn{name: "s", values: []any{nil, ""}, typ: reflect.TypeFor[string]()},
		&testColumn{name: "b", values: []any{nil, false}, typ: reflect.TypeFor[bool]()},
		&testColumn{name: "ts", values: []any{nil, time.Time{}}, typ: reflect.TypeFor[time.Time]()},
	}}
	src := newRecordsetSource(rs, nil)

	null, zero := src.Row(0), src.Row(1)
	for column := range 4 {
		got := null.Values[column].(cellValue)
		assert.Equal(t, "NULL", got.String(), "column %d", column)
		assert.Equal(t, kindNull, got.kind, "column %d", column)
	}
	// The zero values are values, and read as what they are.
	assert.Equal(t, "0", zero.Values[0].(cellValue).String())
	assert.Equal(t, "", zero.Values[1].(cellValue).String())
	assert.Equal(t, "NO", zero.Values[2].(cellValue).String())
	assert.Equal(t, "0001-01-01", zero.Values[3].(cellValue).String())
	for column := range 4 {
		assert.NotEqual(t, kindNull, zero.Values[column].(cellValue).kind, "column %d: a zero value is not a NULL", column)
	}
}

// The NULL cell has a style of its own: not that of an empty text, a zero, NO or a date.
func TestRecordsetSource_ANullCellHasItsOwnStyle(t *testing.T) {
	src := newRecordsetSource(&testRecordset{name: "t"}, nil)
	null := src.cellStyle(grid.Row{}, 1, newCellValue(nil, nil))
	assert.True(t, null.GetItalic(), "a NULL is set apart from text in the same colour")
	assert.Equal(t, theme.DarkGray, null.GetForeground())
	for name, value := range map[string]any{"text": "", "zero": int64(0), "no": false, "date": time.Time{}, "float": 0.0, "bytes": []byte(nil)} {
		other := src.cellStyle(grid.Row{}, 1, newCellValue(value, nil))
		assert.NotEqual(t, null.GetForeground(), other.GetForeground(), name)
		assert.False(t, other.GetItalic(), name)
	}
}
