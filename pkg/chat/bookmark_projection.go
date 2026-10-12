package chat

import (
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// projectSnapshot copies only visible rows and cells. A sparse cell-range
// selection must not turn the unselected cross-product into query context.
func projectSnapshot(record RecordSet, view *RecordSetView, selection *Selection) (secureread.Result, []int) {
	columns := append([]string(nil), record.Result.Columns...)
	rows := make([]int, len(record.Result.Rows))
	for i := range rows {
		rows[i] = i
	}
	if view != nil {
		rows = append([]int(nil), view.RowIndices...)
		if len(view.Columns) > 0 {
			columns = append([]string(nil), view.Columns...)
		}
	}
	if selection != nil {
		rows = append([]int(nil), selection.Rows...)
		if len(selection.Columns) > 0 {
			columns = append([]string(nil), selection.Columns...)
		}
	}
	result := secureread.Result{Columns: columns, Rows: make([]secureread.Row, 0, len(rows))}
	sourceRows := make([]int, 0, len(rows))
	for _, sourceRow := range rows {
		if sourceRow < 0 || sourceRow >= len(record.Result.Rows) {
			continue
		}
		data := make(map[string]any, len(columns))
		for _, column := range columns {
			columnIndex := columnIndexOf(record.Result.Columns, column)
			if columnIndex < 0 || (selection != nil && len(selection.Ranges) > 0 && !selectedCell(selection.Ranges, sourceRow, columnIndex)) {
				continue
			}
			data[column] = record.Result.Rows[sourceRow].Data[column]
		}
		result.Rows = append(result.Rows, secureread.Row{Key: record.Result.Rows[sourceRow].Key, Data: data})
		sourceRows = append(sourceRows, sourceRow)
	}
	return result, sourceRows
}

func selectedCell(ranges []CellRange, row, column int) bool {
	for _, span := range ranges {
		if row >= span.FirstRow && row <= span.LastRow && column >= span.FirstCol && column <= span.LastCol {
			return true
		}
	}
	return false
}

func bookmarkResult(bookmark Bookmark) (secureread.Result, []int) {
	return projectSnapshot(bookmark.Snapshot.RecordSet, bookmark.Snapshot.View, bookmark.Snapshot.Selection)
}
