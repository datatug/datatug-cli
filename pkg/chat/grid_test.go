package chat

import (
	"bytes"
	"testing"
	"time"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestNewGridModelPreservesStructureAndFormatsValues(t *testing.T) {
	when := time.Date(2026, 9, 20, 12, 30, 0, 0, time.UTC)
	result := secureread.Result{
		Columns: []string{"id", "name", "amount", "when", "empty", "blob"},
		Rows: []secureread.Row{{Data: map[string]any{
			"id": 2, "name": "Prague", "amount": 12.50, "when": when, "empty": nil, "blob": []byte{0xff},
		}}},
	}
	grid := NewGridModel(result)
	if len(grid.Columns) != 6 || !grid.Columns[0].Numeric || !grid.Columns[2].Numeric || grid.Columns[1].Numeric {
		t.Fatalf("columns = %+v", grid.Columns)
	}
	want := []string{"2", "Prague", "12.5", "2026-09-20T12:30:00Z", "NULL", "0xff"}
	for i := range want {
		if grid.Rows[0][i] != want[i] {
			t.Errorf("cell %d = %q, want %q", i, grid.Rows[0][i], want[i])
		}
	}
	if got := FormatValue(bytes.Repeat([]byte("a"), 2)); got != "aa" {
		t.Errorf("UTF-8 bytes = %q", got)
	}
}

func TestGridModelSortTogglesAndHandlesEmpty(t *testing.T) {
	grid := NewGridModel(secureread.Result{
		Columns: []string{"n"},
		Rows: []secureread.Row{
			{Data: map[string]any{"n": 10}},
			{Data: map[string]any{"n": 2}},
		},
	})
	grid.Sort(0)
	if grid.Rows[0][0] != "2" || grid.header(0) != "n ▲" {
		t.Fatalf("ascending rows/header = %+v / %q", grid.Rows, grid.header(0))
	}
	grid.Sort(0)
	if grid.Rows[0][0] != "10" || grid.header(0) != "n ▼" {
		t.Fatalf("descending rows/header = %+v / %q", grid.Rows, grid.header(0))
	}

	empty := NewGridModel(secureread.Result{Columns: []string{"id"}})
	empty.Sort(0)
	if len(empty.Rows) != 0 {
		t.Fatalf("empty rows = %+v", empty.Rows)
	}
}

func TestGridModelNumericSortKeepsEqualValuesStable(t *testing.T) {
	grid := GridModel{
		Columns:    []GridColumn{{Name: "n", Numeric: true}},
		Rows:       [][]string{{"2"}, {"2.0"}, {"10"}},
		sortColumn: -1,
	}
	grid.Sort(0)
	grid.Sort(0)
	if grid.Rows[0][0] != "10" || grid.Rows[1][0] != "2" || grid.Rows[2][0] != "2.0" {
		t.Fatalf("descending stable rows = %+v", grid.Rows)
	}
}

func TestGridModelSortsLargeIntegersExactly(t *testing.T) {
	grid := NewGridModel(secureread.Result{
		Columns: []string{"id"},
		Rows: []secureread.Row{
			{Data: map[string]any{"id": int64(9007199254740993)}},
			{Data: map[string]any{"id": int64(9007199254740992)}},
		},
	})
	grid.Sort(0)
	if grid.Rows[0][0] != "9007199254740992" {
		t.Fatalf("ascending rows = %+v", grid.Rows)
	}
	grid.Sort(0)
	if grid.Rows[0][0] != "9007199254740993" {
		t.Fatalf("descending rows = %+v", grid.Rows)
	}
}
