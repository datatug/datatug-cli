package secureread

import (
	"errors"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/condeval"
	"github.com/dal-go/dalgo/dal"
)

// collectRows drains reader into Rows, normalising every record's data to a
// map so struct, map and pointer targets render the same way. It mirrors
// apps/datatugapp/commands/query_output.go's collectRows; duplicated here
// (rather than shared) because that package is internal to the CLI app and
// this package must not import it, nor be imported by it.
func collectRows(reader dal.RecordsReader) ([]Row, error) {
	if reader == nil {
		return nil, nil
	}
	defer func() { _ = reader.Close() }()
	var rows []Row
	for {
		rec, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) || (err == nil && rec == nil) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		row := Row{Key: fmt.Sprint(rec.Key().ID)}
		if rec.Exists() {
			data, err := condeval.ToMap(rec.Data())
			if err != nil {
				return nil, fmt.Errorf("secureread: record %s: %w", rec.Key(), err)
			}
			row.Data = data
		}
		rows = append(rows, row)
	}
}

// columnsFor returns the output columns: when query names explicit columns
// (dal.StructuredQuery.Columns()), those that appear in at least one
// returned row, in request order; otherwise the sorted union of the
// returned rows' fields — the shape both a wildcard/--from-style structured
// select and every native-SQL query have (dal.TextQuery names no columns of
// its own).
func columnsFor(query dal.Query, rows []Row) []string {
	present := map[string]bool{}
	for _, row := range rows {
		for name := range row.Data {
			present[name] = true
		}
	}
	if structured, ok := query.(dal.StructuredQuery); ok && len(structured.Columns()) > 0 {
		var names []string
		seen := map[string]bool{}
		for _, column := range structured.Columns() {
			name := column.Alias
			if field, isField := column.Expression.(dal.FieldRef); isField && name == "" {
				name = field.Name()
			}
			if name != "" && present[name] && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
		return names
	}
	names := make([]string, 0, len(present))
	for name := range present {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
