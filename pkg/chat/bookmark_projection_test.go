package chat

import (
	"testing"

	"github.com/datatug/datatug-cli/pkg/secureread"
)

func TestBookmarkProjectionPreservesOnlySelectedRangeCells(t *testing.T) {
	record := RecordSet{Result: secureread.Result{
		Columns: []string{"CustomerId", "City"},
		Rows: []secureread.Row{
			{Data: map[string]any{"CustomerId": int64(5), "City": "Prague"}},
			{Data: map[string]any{"CustomerId": int64(6), "City": "Berlin"}},
		},
	}}
	view := RecordSetView{RowIndices: []int{0, 1}, Columns: []string{"CustomerId", "City"}}
	selection := Selection{
		Rows: []int{0, 1}, Columns: []string{"CustomerId", "City"},
		Ranges: []CellRange{
			{FirstRow: 0, LastRow: 0, FirstCol: 0, LastCol: 0},
			{FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 1},
		},
	}
	bookmark := Bookmark{Snapshot: BookmarkSnapshot{RecordSet: record, View: &view, Selection: &selection}}
	result, sourceRows := bookmarkResult(bookmark)
	if len(result.Rows) != 2 || len(sourceRows) != 2 || sourceRows[0] != 0 || sourceRows[1] != 1 {
		t.Fatalf("projection rows = %#v, source = %#v", result.Rows, sourceRows)
	}
	if got := result.Rows[0].Data["CustomerId"]; got != int64(5) {
		t.Fatalf("typed ID = %#v", got)
	}
	if _, ok := result.Rows[0].Data["City"]; ok {
		t.Fatal("unselected Prague cell leaked into projection")
	}
	if _, ok := result.Rows[1].Data["CustomerId"]; ok {
		t.Fatal("unselected ID cell leaked into projection")
	}
	if got := result.Rows[1].Data["City"]; got != "Berlin" {
		t.Fatalf("selected City = %#v", got)
	}
	grid := NewGridModel(result)
	if grid.Rows[0][1] != "" || grid.Rows[1][0] != "" {
		t.Fatalf("unselected cells are not blank: %#v", grid.Rows)
	}
	session := ChatSession{
		Bookmarks: map[string]Bookmark{"saved": bookmark},
		Workspace: WorkspaceState{Attachments: []ContextReference{{Kind: "bookmark", ObjectID: "saved"}}},
	}
	params := selectionParameters(session)
	ids := params["selection_1_c1"].([]any)
	cities := params["selection_1_c2"].([]any)
	if len(ids) != 1 || ids[0] != int64(5) || len(cities) != 1 || cities[0] != "Berlin" {
		t.Fatalf("local binding includes wrong cells: %#v", params)
	}
}

func TestRangeProjectionRejectsMismatchedRowsOrColumns(t *testing.T) {
	columns := []string{"CustomerId", "City"}
	ranges := []CellRange{{FirstRow: 0, LastRow: 1, FirstCol: 0, LastCol: 1}}
	for _, test := range []struct {
		name    string
		rows    []int
		columns []string
	}{
		{"missing row", []int{0}, columns},
		{"missing column", []int{0, 1}, []string{"CustomerId"}},
		{"extra row", []int{0, 1, 2}, columns},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateSelectionRangeProjection(columns, test.rows, test.columns, ranges); err == nil {
				t.Fatal("mismatched range was accepted")
			}
		})
	}
	if err := validateSelectionRangeProjection(columns, []int{0, 1}, columns, ranges); err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}
}
