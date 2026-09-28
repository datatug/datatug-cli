package dbviewer

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/dal-go/dalgo/recordset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testColumn struct {
	name   string
	values []any
	err    error
}

func (c *testColumn) Name() string                     { return c.name }
func (c *testColumn) DefaultValue() any                { return nil }
func (c *testColumn) DbType() string                   { return "TEXT" }
func (c *testColumn) ValueType() reflect.Type          { return reflect.TypeOf("") }
func (c *testColumn) IsBitmap() bool                   { return false }
func (c *testColumn) Add(value any) error              { c.values = append(c.values, value); return nil }
func (c *testColumn) SetValue(row int, val any) error  { return nil }
func (c *testColumn) Values() []any                    { return c.values }
func (c *testColumn) GetValue(row int) (any, error) {
	if c.err != nil {
		return nil, c.err
	}
	if row < len(c.values) {
		return c.values[row], nil
	}
	return nil, nil
}

type testRecordset struct {
	name    string
	columns []recordset.Column[any]
	rows    int
}

func (r *testRecordset) Name() string                          { return r.name }
func (r *testRecordset) NewRow() recordset.Row                 { return nil }
func (r *testRecordset) GetRow(i int) recordset.Row            { return nil }
func (r *testRecordset) RowsCount() int                        { return r.rows }
func (r *testRecordset) ColumnsCount() int                     { return len(r.columns) }
func (r *testRecordset) GetColumnByIndex(i int) recordset.Column[any] { return r.columns[i] }
func (r *testRecordset) GetColumnByName(name string) recordset.Column[any] {
	for _, c := range r.columns {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
func (r *testRecordset) Columns() []recordset.Column[any]      { return r.columns }
func (r *testRecordset) GetColumnIndex(name string) int {
	for i, c := range r.columns {
		if c.Name() == name {
			return i
		}
	}
	return -1
}

func TestTableContentRecordset(t *testing.T) {
	locNY, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	cols := []recordset.Column[any]{
		&testColumn{
			name: "UserID",
			values: []any{
				"alice",
				nil,
				[]byte("rawbytes"),
				[]int{1, 2, 3},
				true,
				false,
				3.1415,
				int64(42),
				time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC),
				time.Date(2023, 1, 1, 0, 0, 0, 0, locNY),
				time.Date(2023, 1, 1, 15, 4, 5, 0, time.UTC),
				time.Date(2023, 1, 1, 15, 4, 5, 0, locNY),
				complex(1, 2),
			},
		},
		&testColumn{
			name: "UserID",
			values: []any{
				"bob",
				time.Date(2023, 5, 5, 12, 0, 0, 0, time.UTC),
			},
		},
		&testColumn{
			name: "ErrCol",
			err:  errors.New("simulated read failure"),
		},
	}

	rs := &testRecordset{
		name:    "Users", // Notice "User" + "s" == "Users", matching UserID
		columns: cols,
		rows:    13,
	}

	tc := TableContentRecordset{recordset: rs}

	assert.Equal(t, 14, tc.GetRowCount())
	assert.Equal(t, 3, tc.GetColumnCount())

	// Header
	headerCell := tc.GetCell(0, 0)
	assert.Equal(t, "UserID", headerCell.Text)

	// Column 0 rows:
	// Row 1: string "alice", UserID where refTableName ("Users") == rsName ("Users")
	c1 := tc.GetCell(1, 0)
	assert.Equal(t, "alice", c1.Text)

	// Row 2: nil
	c2 := tc.GetCell(2, 0)
	assert.Equal(t, "", c2.Text)

	// Row 3: []byte
	c3 := tc.GetCell(3, 0)
	assert.Contains(t, c3.Text, "[]byte - 8")

	// Row 4: []int
	c4 := tc.GetCell(4, 0)
	assert.Contains(t, c4.Text, "[]int - 3")

	// Row 5: bool true
	c5 := tc.GetCell(5, 0)
	assert.Equal(t, "YES", c5.Text)

	// Row 6: bool false
	c6 := tc.GetCell(6, 0)
	assert.Equal(t, "NO", c6.Text)

	// Row 7: float64
	c7 := tc.GetCell(7, 0)
	assert.Equal(t, "3.1415", c7.Text)

	// Row 8: int64
	c8 := tc.GetCell(8, 0)
	assert.Equal(t, "42", c8.Text)

	// Row 9: midnight UTC
	c9 := tc.GetCell(9, 0)
	assert.Equal(t, "2023-01-01", c9.Text)

	// Row 10: midnight with non-UTC offset
	c10 := tc.GetCell(10, 0)
	assert.Contains(t, c10.Text, "2023-01-01 America/New_York")

	// Row 11: non-midnight UTC
	c11 := tc.GetCell(11, 0)
	assert.Equal(t, "2023-01-01 15:04:05", c11.Text)

	// Row 12: non-midnight with non-UTC offset
	c12 := tc.GetCell(12, 0)
	assert.Contains(t, c12.Text, "2023-01-01T15:04:05")

	// Row 13: complex (default fallback)
	c13 := tc.GetCell(13, 0)
	assert.Contains(t, c13.Text, "complex128:(1+2i)")

	// Column 1, Row 2: odd column time styling & different rsName matching
	rsOther := &testRecordset{
		name:    "Orders", // refTableName "Users" != "Orders"
		columns: cols,
		rows:    2,
	}
	tcOther := TableContentRecordset{recordset: rsOther}
	cOddColTime := tcOther.GetCell(2, 1)
	assert.Equal(t, "2023-05-05 12:00:00", cOddColTime.Text)

	// Error column:
	cErr := tc.GetCell(1, 2)
	assert.Contains(t, cErr.Text, "ERROR: simulated read failure")
}
