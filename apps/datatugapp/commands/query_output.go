package commands

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dal-go/dalgo/condeval"
	"github.com/dal-go/dalgo/dal"
	"gopkg.in/yaml.v3"
)

const keyColumn = "$key"

var queryFormats = []string{"grid", "json", "jsonl", "yaml", "csv"}

// queryRow is one result record: its key and its JSON-shaped data.
type queryRow struct {
	key  string
	data map[string]any
}

// collectRows drains a records reader, normalising every record's data to a
// map so struct, map and pointer targets render the same way.
func collectRows(reader dal.RecordsReader) ([]queryRow, error) {
	if reader == nil {
		return nil, nil
	}
	defer func() { _ = reader.Close() }()
	var rows []queryRow
	for {
		rec, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) || (err == nil && rec == nil) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		row := queryRow{key: fmt.Sprint(rec.Key().ID)}
		if rec.Exists() {
			data, err := condeval.ToMap(rec.Data())
			if err != nil {
				return nil, fmt.Errorf("record %s: %w", rec.Key(), err)
			}
			row.data = data
		}
		rows = append(rows, row)
	}
}

// errReservedKeyField is returned when a record carries a data field named
// like the key column.
var errReservedKeyField = errors.New("a record has a data field named " + keyColumn + ", which is reserved for the record key")

// rowColumns returns the output columns after enforcement: when the query
// names columns, those that appear in at least one returned row, in request
// order (possibly none, leaving $key alone); otherwise the sorted union of the
// returned fields.
func rowColumns(query dal.Query, rows []queryRow) ([]string, error) {
	present := map[string]bool{}
	for _, row := range rows {
		for name := range row.data {
			if name == keyColumn {
				return nil, errReservedKeyField
			}
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
		return names, nil
	}
	names := make([]string, 0, len(present))
	for name := range present {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func writeQueryRows(w io.Writer, format string, columns []string, rows []queryRow) error {
	switch format {
	case "json":
		return writeJSONRows(w, columns, rows, true)
	case "jsonl":
		return writeJSONRows(w, columns, rows, false)
	case "yaml":
		return writeYAMLRows(w, columns, rows)
	case "csv":
		return writeCSVRows(w, columns, rows)
	case "grid":
		return writeGridRows(w, columns, rows)
	default:
		return fmt.Errorf("unsupported format %q: want one of %s", format, strings.Join(queryFormats, ", "))
	}
}

func writeJSONRows(w io.Writer, columns []string, rows []queryRow, asArray bool) error {
	if asArray {
		if _, err := io.WriteString(w, "[\n"); err != nil {
			return err
		}
	}
	for i, row := range rows {
		var b strings.Builder
		b.WriteString("{")
		writeJSONField(&b, keyColumn, row.key)
		for _, column := range columns {
			value, ok := row.data[column]
			if !ok {
				continue
			}
			b.WriteString(",")
			writeJSONField(&b, column, value)
		}
		b.WriteString("}")
		if asArray && i < len(rows)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
	}
	if asArray {
		if _, err := io.WriteString(w, "]\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeJSONField(b *strings.Builder, name string, value any) {
	encodedName, _ := json.Marshal(name)
	encodedValue, err := json.Marshal(value)
	if err != nil {
		encodedValue, _ = json.Marshal(fmt.Sprint(value))
	}
	b.Write(encodedName)
	b.WriteString(":")
	b.Write(encodedValue)
}

func writeYAMLRows(w io.Writer, columns []string, rows []queryRow) error {
	sequence := &yaml.Node{Kind: yaml.SequenceNode}
	for _, row := range rows {
		mapping := &yaml.Node{Kind: yaml.MappingNode}
		add := func(name string, value any) error {
			var valueNode yaml.Node
			if err := valueNode.Encode(plainNumbers(value)); err != nil {
				return err
			}
			mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: name}, &valueNode)
			return nil
		}
		if err := add(keyColumn, row.key); err != nil {
			return err
		}
		for _, column := range columns {
			if value, ok := row.data[column]; ok {
				if err := add(column, value); err != nil {
					return err
				}
			}
		}
		sequence.Content = append(sequence.Content, mapping)
	}
	if len(rows) == 0 {
		_, err := io.WriteString(w, "[]\n")
		return err
	}
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	if err := encoder.Encode(sequence); err != nil {
		return err
	}
	return encoder.Close()
}

func writeCSVRows(w io.Writer, columns []string, rows []queryRow) error {
	writer := csv.NewWriter(w)
	if err := writer.Write(append([]string{keyColumn}, columns...)); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writer.Write(cellValues(row, columns)); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func writeGridRows(w io.Writer, columns []string, rows []queryRow) error {
	header := append([]string{keyColumn}, columns...)
	widths := make([]int, len(header))
	for i, name := range header {
		widths[i] = utf8.RuneCountInString(name)
	}
	cells := make([][]string, len(rows))
	for r, row := range rows {
		cells[r] = cellValues(row, columns)
		for i, cell := range cells[r] {
			if n := utf8.RuneCountInString(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}
	line := func(values []string) string {
		parts := make([]string, len(values))
		for i, value := range values {
			parts[i] = value + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(value))
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ") + "\n"
	}
	if _, err := io.WriteString(w, line(header)); err != nil {
		return err
	}
	for _, row := range cells {
		if _, err := io.WriteString(w, line(row)); err != nil {
			return err
		}
	}
	return nil
}

func cellValues(row queryRow, columns []string) []string {
	values := make([]string, 0, len(columns)+1)
	values = append(values, row.key)
	for _, column := range columns {
		values = append(values, cellText(row.data[column]))
	}
	return values
}

func cellText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case map[string]any, []any:
		encoded, _ := json.Marshal(v)
		return string(encoded)
	default:
		return fmt.Sprint(v)
	}
}

// plainNumbers turns the float64 values JSON normalisation produced back into
// integers where they are integral, recursively, so YAML never shows 1e+06.
func plainNumbers(value any) any {
	switch v := value.(type) {
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1<<53 {
			return int64(v)
		}
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = plainNumbers(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = plainNumbers(item)
		}
		return out
	default:
		return value
	}
}
