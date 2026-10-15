package queryresults

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

type myInt int
type myString string

func TestConvert(t *testing.T) {
	when := time.Date(2026, 10, 15, 12, 0, 0, 5, time.FixedZone("x", 3600))
	cases := []struct {
		name     string
		in       any
		want     string
		encoding string
	}{
		{"nil", nil, `{"type":"null","value":null}`, ""},
		{"string", "Ann", `{"type":"string","value":"Ann"}`, ""},
		{"bytes", []byte{1, 2, 3}, `{"type":"string","value":"<binary 3 bytes>"}`, EncodingBinary},
		{"bool", true, `{"type":"boolean","value":true}`, ""},
		{"int64 above 2^53", int64(1<<62 + 1), `{"type":"integer","value":"4611686018427387905"}`, ""},
		{"named int", myInt(7), `{"type":"integer","value":"7"}`, ""},
		{"uint", uint64(math.MaxUint64), `{"type":"integer","value":"18446744073709551615"}`, ""},
		{"float", 1.5, `{"type":"number","value":1.5}`, ""},
		{"float32", float32(2), `{"type":"number","value":2}`, ""},
		{"NaN", math.NaN(), `{"type":"string","value":"NaN"}`, ""},
		{"infinity", math.Inf(1), `{"type":"string","value":"+Inf"}`, ""},
		{"json integer", json.Number("9007199254740993"), `{"type":"integer","value":"9007199254740993"}`, ""},
		{"json fraction", json.Number("2.25"), `{"type":"number","value":2.25}`, ""},
		{"json not a number", json.Number("x"), `{"type":"string","value":"x"}`, ""},
		{"time", when, `{"type":"datetime","value":"2026-10-15T11:00:00.000000005Z"}`, ""},
		{"named string", myString("s"), `{"type":"string","value":"s"}`, ""},
		{"map", map[string]any{"a": 1}, `{"type":"string","value":"{\"a\":1}"}`, EncodingJSON},
		{"slice", []any{1, "b"}, `{"type":"string","value":"[1,\"b\"]"}`, EncodingJSON},
		{"text marshaller", big.NewFloat(1.25), `{"type":"string","value":"1.25"}`, ""},
		{"exact decimal", json.RawMessage("1.250"), `{"type":"decimal","value":"1.250"}`, ""},
		{"big integer", new(big.Int).Lsh(big.NewInt(1), 70), `{"type":"integer","value":"1180591620717411303424"}`, ""},
		{"exponent out of range", json.RawMessage("1e400"), `{"type":"string","value":"1e400"}`, ""},
		{"exponent", json.RawMessage("1e5"), `{"type":"number","value":100000}`, ""},
		{"JSON string", json.RawMessage(`"q"`), `{"type":"string","value":"q"}`, ""},
		{"JSON true", json.RawMessage(`true`), `{"type":"boolean","value":true}`, ""},
		{"JSON null", json.RawMessage(`null`), `{"type":"null","value":null}`, ""},
		{"unreadable", make(chan int), `{"type":"string","value":"<unreadable value>"}`, EncodingJSON},
	}
	for _, tc := range cases {
		got := convert(tc.in)
		var want apicontract.TypedValue
		if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got.value != want || got.encoding != tc.encoding {
			t.Errorf("%s: %+v %q, want %+v %q", tc.name, got.value, got.encoding, want, tc.encoding)
		}
	}
	if ConvertValue(int8(3)).Str != "3" {
		t.Fatal("ConvertValue")
	}
}

func TestCutText(t *testing.T) {
	ascii := strings.Repeat("a", 70<<10)
	cut, wasCut := cutText(ascii)
	if !wasCut || len(cut) != MaxCellBytes {
		t.Fatalf("ascii cut to %d", len(cut))
	}
	// a three-byte character straddling the bound is dropped whole
	multi := strings.Repeat("a", MaxCellBytes-1) + "€" + "tail"
	cut, wasCut = cutText(multi)
	if !wasCut || !utf8.ValidString(cut) || len(cut) != MaxCellBytes-1 {
		t.Fatalf("utf-8 cut to %d valid=%v", len(cut), utf8.ValidString(cut))
	}
	if short, wasCut := cutText("short"); wasCut || short != "short" {
		t.Fatal("short text cut")
	}
	nested := convert(map[string]any{"k": strings.Repeat("x", 70<<10)})
	if !nested.cut || len(nested.value.Str) != MaxCellBytes {
		t.Fatal("nested value not cut")
	}
}

func decodeData(t *testing.T, data Data) (apicontract.Recordset, map[string]any) {
	t.Helper()
	var file struct {
		Format    string                `json:"format"`
		Version   int                   `json:"version"`
		ID        string                `json:"id"`
		Recordset apicontract.Recordset `json:"recordset"`
	}
	if err := json.Unmarshal(data.Bytes, &file); err != nil {
		t.Fatalf("data.json: %v\n%s", err, data.Bytes)
	}
	if file.Format != DataFormat || file.Version != 1 {
		t.Fatalf("header %+v", file)
	}
	if err := file.Recordset.Validate(); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data.Bytes, &raw)
	return file.Recordset, raw
}

func TestCollectorBuild(t *testing.T) {
	c := NewCollector(3)
	rows := []map[string]any{
		{"id": int64(1), "name": "Ann", "mixed": int64(1), "blob": []byte("xy"), "big": strings.Repeat("é", 40<<10)},
		{"id": int64(2), "name": nil, "mixed": "two", "extra": true},
		{"id": int64(3), "name": "Cid", "mixed": 3.5},
		{"id": int64(4)},
	}
	for i, row := range rows {
		more := c.Add(string(rune('a'+i)), row)
		if more != (i < 3) {
			t.Fatalf("Add %d answered %v", i, more)
		}
	}
	if names := c.FieldNames(); len(names) != 3 || strings.Join(names[1], ",") != "extra,id,mixed,name" {
		t.Fatalf("field names %v", names)
	}
	data := c.Build("t1", []string{"id", "name", "mixed", "blob", "big", "absent"})
	recordset, raw := decodeData(t, data)
	if raw["id"] != "t1" {
		t.Fatalf("id %v", raw["id"])
	}
	if data.Rows != (Rows{Kept: 3, HasMore: true, TruncatedBy: TruncatedRows}) {
		t.Fatalf("rows %+v", data.Rows)
	}
	wantTypes := []string{"string", "integer", "string", "string", "string", "string", "string"}
	for i, column := range recordset.Columns {
		if column.Type != wantTypes[i] {
			t.Errorf("column %s type %s, want %s", column.Name, column.Type, wantTypes[i])
		}
	}
	if recordset.Columns[0].Name != KeyColumn {
		t.Fatal("$key is not first")
	}
	if got := recordset.Rows[2][3]; got.Type != apicontract.ValueTypeString || got.Str != "3.5" {
		t.Fatalf("mixed cell written as %+v", got)
	}
	if got := recordset.Rows[1][3]; got.Str != "two" {
		t.Fatalf("mixed text cell %+v", got)
	}
	mixed := data.Columns[3]
	if strings.Join(mixed.ObservedTypes, ",") != "integer,number,string" {
		t.Fatalf("observed %v", mixed.ObservedTypes)
	}
	if data.Columns[4].Encoding != EncodingBinary || data.Columns[5].CutCells != 1 || data.Columns[1].ObservedTypes != nil {
		t.Fatalf("columns %+v", data.Columns)
	}
	if recordset.Rows[0][6].Type != apicontract.ValueTypeNull {
		t.Fatal("an absent field must be null")
	}

	empty := NewCollector(5).Build("e", nil)
	recordset, _ = decodeData(t, empty)
	if len(recordset.Rows) != 0 || empty.Rows != (Rows{TruncatedBy: TruncatedNone}) || recordset.Columns[0].Type != "string" {
		t.Fatalf("empty %+v %+v", recordset, empty.Rows)
	}
}

func TestAsText(t *testing.T) {
	for _, tc := range []struct {
		in   apicontract.TypedValue
		want apicontract.TypedValue
	}{
		{apicontract.NewNullValue(), apicontract.NewNullValue()},
		{apicontract.NewStringValue("s"), apicontract.NewStringValue("s")},
		{apicontract.NewNumberValue(1.5), apicontract.NewStringValue("1.5")},
		{apicontract.NewBooleanValue(false), apicontract.NewStringValue("false")},
		{apicontract.NewIntegerValue("9"), apicontract.NewStringValue("9")},
	} {
		if got := asText(tc.in); got != tc.want {
			t.Errorf("asText(%+v) = %+v", tc.in, got)
		}
	}
}

func TestCollectorByteBound(t *testing.T) {
	// the reader stops once the next row would pass the bound
	c := NewCollector(MaxRows)
	cell := strings.Repeat("x", 60<<10)
	added := 0
	for c.Add("k", map[string]any{"v": cell}) {
		added++
	}
	data := c.Build("b", []string{"v"})
	if len(data.Bytes) > MaxDataBytes || data.Rows.Kept != added || !data.Rows.HasMore || data.Rows.TruncatedBy != TruncatedBytes {
		t.Fatalf("%d bytes, rows %+v, added %d", len(data.Bytes), data.Rows, added)
	}

	// rows that grow when built (more columns than the estimate saw) are dropped from the end
	c = NewCollector(MaxRows)
	for i := 0; i < 200; i++ {
		row := map[string]any{"v": strings.Repeat("y", 19000)}
		if i == 0 {
			for j := 0; j < 200; j++ {
				row[fmt.Sprintf("c%03d", j)] = nil
			}
		}
		c.Add("k", row)
	}
	columns := []string{"v"}
	for name := range c.rows[0].cells {
		if name != "v" {
			columns = append(columns, name)
		}
	}
	data = c.Build("g", columns)
	if len(data.Bytes) > MaxDataBytes || data.Rows.TruncatedBy != TruncatedBytes || !data.Rows.HasMore || data.Rows.Kept == 0 {
		t.Fatalf("%d bytes, rows %+v", len(data.Bytes), data.Rows)
	}
	decodeData(t, data)
}

func TestRecordData(t *testing.T) {
	type rec struct {
		Big json.Number `json:"big"`
		N   int64       `json:"n"`
	}
	m := map[string]any{"a": 1}
	if got, _ := RecordData(m); got["a"] != 1 {
		t.Fatal("map")
	}
	if got, _ := RecordData(&m); got["a"] != 1 {
		t.Fatal("map pointer")
	}
	var nilMap *map[string]any
	if got, err := RecordData(nilMap); err != nil || len(got) != 0 {
		t.Fatal("nil map pointer")
	}
	if got, err := RecordData(nil); err != nil || len(got) != 0 {
		t.Fatal("nil")
	}
	got, err := RecordData(rec{Big: "9007199254740993", N: 9007199254740993})
	if err != nil || got["n"] != json.Number("9007199254740993") || got["big"] != json.Number("9007199254740993") {
		t.Fatalf("struct %v %v", got, err)
	}
	var nilRec *rec
	if got, err := RecordData(nilRec); err != nil || len(got) != 0 {
		t.Fatal("nil struct pointer")
	}
	if _, err := RecordData(make(chan int)); err == nil {
		t.Fatal("unreadable data")
	}
	if _, err := RecordData("SECRET-VALUE"); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("a non-object: %v", err)
	}
}
