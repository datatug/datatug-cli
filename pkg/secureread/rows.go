package secureread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"time"

	"github.com/dal-go/dalgo/dal"
)

// collectRows drains reader into Rows, normalising every record's data to a
// map so struct, map and pointer targets render the same way. It mirrors
// apps/datatugapp/commands/query_output.go's collectRows; duplicated here
// (rather than shared) because that package is internal to the CLI app and
// this package must not import it, nor be imported by it.
func collectRows(reader dal.RecordsReader) ([]Row, *statisticsAccumulator, error) {
	if reader == nil {
		return nil, newStatisticsAccumulator(), nil
	}
	defer func() { _ = reader.Close() }()
	var rows []Row
	statistics := newStatisticsAccumulator()
	for {
		rec, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) || (err == nil && rec == nil) {
			return rows, statistics, nil
		}
		if err != nil {
			return nil, nil, err
		}
		row := Row{Key: fmt.Sprint(rec.Key().ID)}
		if rec.Exists() {
			data, err := resultDataMap(rec.Data())
			if err != nil {
				return nil, nil, fmt.Errorf("secureread: record %s: %w", rec.Key(), err)
			}
			row.Data = data
		}
		statistics.addRow(row.Data)
		rows = append(rows, row)
	}
}

// Native map cells already carry the driver's numeric types. A JSON round
// trip would turn int64 into float64 before the durable typed codec sees it.
// Copy the map because readers may reuse their destination between rows.
func resultDataMap(data any) (map[string]any, error) {
	value := reflect.ValueOf(data)
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return nil, nil
		}
		value = value.Elem()
	}
	if value.IsValid() && value.Kind() == reflect.Map && value.Type().Key().Kind() == reflect.String {
		result := make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cell, err := detachResultCell(iter.Value().Interface(), 0)
			if err != nil {
				return nil, fmt.Errorf("column %q: %w", iter.Key().String(), err)
			}
			result[iter.Key().String()] = cell
		}
		return result, nil
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	err = decoder.Decode(&result)
	return result, err
}

// Detach mutable driver buffers before Next or Close can reuse them. Native
// numeric and temporal scalars retain their types; unsupported/cyclic values
// fail explicitly rather than becoming shared or rounded snapshot data.
func detachResultCell(data any, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("result cell nesting exceeds 64 levels")
	}
	switch value := data.(type) {
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number, time.Time:
		return value, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("non-finite result number")
		}
		return value, nil
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("non-finite result number")
		}
		return value, nil
	case []byte:
		return append([]byte(nil), value...), nil
	}
	value := reflect.ValueOf(data)
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return nil, nil
		}
		return detachResultCell(value.Elem().Interface(), depth+1)
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("result object requires string keys")
		}
		result := make(map[string]any, value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cell, err := detachResultCell(iter.Value().Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			result[iter.Key().String()] = cell
		}
		return result, nil
	case reflect.Slice, reflect.Array:
		result := make([]any, value.Len())
		for i := range result {
			cell, err := detachResultCell(value.Index(i).Interface(), depth+1)
			if err != nil {
				return nil, err
			}
			result[i] = cell
		}
		return result, nil
	default:
		encoded, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		var result any
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		err = decoder.Decode(&result)
		return result, err
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
