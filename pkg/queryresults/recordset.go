package queryresults

import (
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// Result file formats and bounds.
const (
	Version    = 1
	DataFormat = "datatug.query-result.data"
	MetaFormat = "datatug.query-result.meta"

	// DefaultRows, MinRows and MaxRows bound --result-rows.
	DefaultRows = 2000
	MinRows     = 1
	MaxRows     = 10000

	// MaxDataBytes keeps data.json under the mod SDK's 4 MiB read limit.
	MaxDataBytes = 4_000_000
	// MaxCellBytes is where one cell is cut, on a UTF-8 boundary.
	MaxCellBytes = 64 << 10
	// MaxMetaBytes bounds meta.json.
	MaxMetaBytes = 1 << 20
	// MaxQueryBytes bounds query.dtql and query.parameters together.
	MaxQueryBytes = 256 << 10
	// MaxMetaColumns is how many columns meta.json describes.
	MaxMetaColumns = 400

	// KeyColumn is the record key column, always first.
	KeyColumn = "$key"

	EncodingBinary = "binary-placeholder"
	EncodingJSON   = "json-text"

	TruncatedNone  = "none"
	TruncatedRows  = "rows"
	TruncatedBytes = "bytes"
)

// cell is one converted value and how it was encoded.
type cell struct {
	value    apicontract.TypedValue
	encoding string
	cut      bool
}

type collectedRow struct {
	key   cell
	cells map[string]cell
}

// Collector keeps the first rows of a result, within the row and byte bounds.
type Collector struct {
	limit       int
	rows        []collectedRow
	bytes       int
	hasMore     bool
	truncatedBy string
}

// NewCollector keeps at most limit rows.
func NewCollector(limit int) *Collector {
	return &Collector{limit: limit, truncatedBy: TruncatedNone}
}

// Add takes one record: its key as text and its fields. It answers false once
// no further record is wanted; the caller then stops reading, so a source is
// read for at most limit + 1 records.
func (c *Collector) Add(key string, data map[string]any) bool {
	if len(c.rows) >= c.limit {
		c.hasMore, c.truncatedBy = true, TruncatedRows
		return false
	}
	row := collectedRow{key: textCell(key), cells: make(map[string]cell, len(data))}
	size := encodedSize(row.key)
	for name, value := range data {
		converted := convert(value)
		row.cells[name] = converted
		size += encodedSize(converted)
	}
	if c.bytes+size > MaxDataBytes {
		c.hasMore, c.truncatedBy = true, TruncatedBytes
		return false
	}
	c.bytes += size
	c.rows = append(c.rows, row)
	return true
}

// FieldNames lists, per kept row, the names of its fields.
func (c *Collector) FieldNames() [][]string {
	names := make([][]string, len(c.rows))
	for i, row := range c.rows {
		for name := range row.cells {
			names[i] = append(names[i], name)
		}
		sort.Strings(names[i])
	}
	return names
}

// Data is a built data.json and what meta.json says about it.
type Data struct {
	Bytes   []byte
	Columns []Column
	Rows    Rows
}

// Build encodes data.json for id with the columns given ($key is put first).
// A column's type is its one observed kind; a column of several kinds is a
// string column with each cell written as its text, and it records the kinds
// in ObservedTypes. Rows are dropped from the end while the file would exceed
// MaxDataBytes.
func (c *Collector) Build(id string, columns []string) Data {
	names := append([]string{KeyColumn}, columns...)
	cellAt := func(row collectedRow, i int) cell {
		if i == 0 {
			return row.key
		}
		if found, ok := row.cells[names[i]]; ok {
			return found
		}
		return cell{value: apicontract.NewNullValue()}
	}
	types := make([]string, len(names))
	observed := make([][]string, len(names))
	for i := range names {
		kinds := map[string]bool{}
		for _, row := range c.rows {
			if value := cellAt(row, i).value; value.Type != apicontract.ValueTypeNull {
				kinds[string(value.Type)] = true
			}
		}
		types[i] = string(apicontract.ValueTypeString)
		for kind := range kinds {
			observed[i] = append(observed[i], kind)
		}
		sort.Strings(observed[i])
		if len(observed[i]) == 1 {
			types[i], observed[i] = observed[i][0], nil
		}
	}
	encodedRows := make([][]byte, len(c.rows))
	for r, row := range c.rows {
		values := make([]apicontract.TypedValue, len(names))
		for i := range names {
			values[i] = cellAt(row, i).value
			if observed[i] != nil {
				values[i] = asText(values[i])
			}
		}
		encodedRows[r], _ = json.Marshal(values)
	}
	recordColumns := make([]apicontract.Column, len(names))
	for i, name := range names {
		recordColumns[i] = apicontract.Column{Name: name, Type: types[i]}
	}
	envelope := func(rows json.RawMessage) []byte {
		encoded, _ := json.Marshal(dataFile{Format: DataFormat, Version: Version, ID: id, Recordset: dataRecordset{Columns: recordColumns, Rows: rows}})
		return encoded
	}
	total := len(envelope(json.RawMessage("[]")))
	kept := 0
	for kept < len(encodedRows) {
		size := len(encodedRows[kept])
		if kept > 0 {
			size++
		}
		if total+size > MaxDataBytes {
			break
		}
		total += size
		kept++
	}
	rows := Rows{Kept: kept, HasMore: c.hasMore, TruncatedBy: c.truncatedBy}
	if kept < len(encodedRows) {
		rows.HasMore, rows.TruncatedBy = true, TruncatedBytes
	}
	joined := make([]byte, 0, total)
	joined = append(joined, '[')
	for r := 0; r < kept; r++ {
		if r > 0 {
			joined = append(joined, ',')
		}
		joined = append(joined, encodedRows[r]...)
	}
	joined = append(joined, ']')
	meta := make([]Column, len(names))
	for i, name := range names {
		meta[i] = Column{Name: name, Type: types[i], ObservedTypes: observed[i]}
		for _, row := range c.rows[:kept] {
			found := cellAt(row, i)
			if found.encoding != "" && meta[i].Encoding == "" {
				meta[i].Encoding = found.encoding
			}
			if found.cut {
				meta[i].CutCells++
			}
		}
	}
	return Data{Bytes: envelope(joined), Columns: meta, Rows: rows}
}

// dataFile is the shape of data.json.
type dataFile struct {
	Format    string        `json:"format"`
	Version   int           `json:"version"`
	ID        string        `json:"id"`
	Recordset dataRecordset `json:"recordset"`
}

type dataRecordset struct {
	Columns []apicontract.Column `json:"columns"`
	Rows    json.RawMessage      `json:"rows"`
}

func encodedSize(c cell) int {
	encoded, _ := json.Marshal(c.value)
	return len(encoded) + 1
}

// textCell is a string cell, cut to MaxCellBytes.
func textCell(text string) cell {
	text, cut := cutText(text)
	return cell{value: apicontract.NewStringValue(text), cut: cut}
}

// cutText cuts text to at most MaxCellBytes, on a UTF-8 character boundary.
func cutText(text string) (string, bool) {
	if len(text) <= MaxCellBytes {
		return text, false
	}
	end := MaxCellBytes
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end], true
}

// asText is value as a string value; null stays null.
func asText(value apicontract.TypedValue) apicontract.TypedValue {
	switch value.Type {
	case apicontract.ValueTypeNull, apicontract.ValueTypeString:
		return value
	case apicontract.ValueTypeNumber:
		return apicontract.NewStringValue(strconv.FormatFloat(value.Num, 'f', -1, 64))
	case apicontract.ValueTypeBoolean:
		return apicontract.NewStringValue(strconv.FormatBool(value.Bool))
	default:
		return apicontract.NewStringValue(value.Str)
	}
}

// ConvertValue is v as a TypedValue, as data.json writes it.
func ConvertValue(v any) apicontract.TypedValue { return convert(v).value }

// convert turns a driver value into a cell: integers stay exact (as text),
// binary becomes a placeholder, and a nested value becomes its JSON text.
func convert(v any) cell {
	switch x := v.(type) {
	case nil:
		return cell{value: apicontract.NewNullValue()}
	case string:
		return textCell(x)
	case []byte:
		return cell{value: apicontract.NewStringValue("<binary " + strconv.Itoa(len(x)) + " bytes>"), encoding: EncodingBinary}
	case json.Number:
		return convertNumberText(x.String())
	case time.Time:
		return cell{value: apicontract.NewDatetimeValue(x.UTC().Format(time.RFC3339Nano))}
	}
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Bool:
		return cell{value: apicontract.NewBooleanValue(value.Bool())}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return cell{value: apicontract.NewIntegerValue(strconv.FormatInt(value.Int(), 10))}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return cell{value: apicontract.NewIntegerValue(strconv.FormatUint(value.Uint(), 10))}
	case reflect.Float32, reflect.Float64:
		return floatCell(value.Float())
	case reflect.String:
		return textCell(value.String())
	}
	return convertJSON(v)
}

func floatCell(f float64) cell {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return cell{value: apicontract.NewStringValue(strconv.FormatFloat(f, 'g', -1, 64))}
	}
	return cell{value: apicontract.NewNumberValue(f)}
}

// convertNumberText keeps an integer exact and a decimal fraction as a number.
func convertNumberText(text string) cell {
	if integer := apicontract.NewIntegerValue(text); integer.Validate() == nil {
		return cell{value: integer}
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return floatCell(f)
	}
	return textCell(text)
}

// convertJSON handles any other value by its JSON form: a JSON number keeps
// its exact text as a decimal, and an object or array is stored as JSON text.
func convertJSON(v any) cell {
	encoded, err := json.Marshal(v)
	if err != nil {
		return cell{value: apicontract.NewStringValue("<unreadable value>"), encoding: EncodingJSON}
	}
	switch encoded[0] {
	case '"':
		var text string
		_ = json.Unmarshal(encoded, &text)
		return textCell(text)
	case 't', 'f':
		return cell{value: apicontract.NewBooleanValue(encoded[0] == 't')}
	case 'n':
		return cell{value: apicontract.NewNullValue()}
	case '{', '[':
		text, cut := cutText(string(encoded))
		return cell{value: apicontract.NewStringValue(text), encoding: EncodingJSON, cut: cut}
	}
	if integer := apicontract.NewIntegerValue(string(encoded)); integer.Validate() == nil {
		return cell{value: integer}
	}
	if decimal := apicontract.NewDecimalValue(string(encoded)); decimal.Validate() == nil {
		return cell{value: decimal}
	}
	return convertNumberText(string(encoded))
}
