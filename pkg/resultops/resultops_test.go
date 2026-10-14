package resultops

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

func present(value any) Cell { return Cell{Present: true, Value: value} }
func missing() Cell          { return Cell{} }

func mustCapture(t *testing.T, columns []Column, rows ...Row) Snapshot {
	t.Helper()
	snapshot, err := Capture(columns, rows)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func cells(values ...Cell) Row { return Row{Cells: values} }

func TestGroupByDistinguishesMissingNullAndTypedEmptyValues(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "text", Type: "TEXT"}, {Name: "integer", Type: "BIGINT"}, {Name: "boolean", Type: "BOOLEAN"}},
		cells(missing(), missing(), present(false)),
		cells(present(nil), present(nil), present(false)),
		cells(present(""), present(json.Number("0")), present(true)),
		cells(present(""), present(json.Number("0")), present(true)),
	)
	textGroups, err := snapshot.GroupBy([]string{"text"})
	if err != nil {
		t.Fatal(err)
	}
	if len(textGroups) != 3 || textGroups[0].Keys[0].Present || !textGroups[1].Keys[0].Present || textGroups[1].Keys[0].Value != nil || textGroups[2].Keys[0].Value != "" {
		t.Fatalf("missing, null, and empty text groups collapsed: %+v", textGroups)
	}
	integerGroups, err := snapshot.GroupBy([]string{"integer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(integerGroups) != 3 || integerGroups[0].Keys[0].Present || integerGroups[1].Keys[0].Value != nil || integerGroups[2].Keys[0].Value != json.Number("0") {
		t.Fatalf("missing, null, and zero integer groups collapsed: %+v", integerGroups)
	}
	if integerGroups[2].Count != 2 || !reflect.DeepEqual(integerGroups[2].RowIndices, []int{2, 3}) {
		t.Fatalf("group count or source row order = %+v", integerGroups[2])
	}
	boolGroups, err := snapshot.GroupBy([]string{"boolean"})
	if err != nil || len(boolGroups) != 2 || boolGroups[0].Keys[0].Value != false || boolGroups[1].Keys[0].Value != true {
		t.Fatalf("false and true groups = %+v, %v", boolGroups, err)
	}
}

func TestGroupByCompositeTupleCannotCollideOnDisplayDelimiter(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "first", Type: "TEXT"}, {Name: "second", Type: "TEXT"}},
		cells(present("a|b"), present("c")),
		cells(present("a"), present("b|c")),
		cells(present("a|b"), present("c")),
	)
	groups, err := snapshot.GroupBy([]string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Count != 2 || groups[1].Count != 1 {
		t.Fatalf("composite keys collided: %+v", groups)
	}
	if !reflect.DeepEqual(groups[0].RowIndices, []int{0, 2}) || !reflect.DeepEqual(groups[1].RowIndices, []int{1}) {
		t.Fatalf("groups lost first occurrence or row order: %+v", groups)
	}
}

func TestGroupByNormalizesExactIntegersDecimalsFloatsAndInstants(t *testing.T) {
	t.Parallel()
	instantA := time.Date(2024, 5, 1, 10, 0, 0, 123, time.FixedZone("A", 0))
	instantB := time.Date(2024, 5, 1, 12, 0, 0, 123, time.FixedZone("B", 2*60*60))
	snapshot := mustCapture(t,
		[]Column{{Name: "integer", Type: "BIGINT"}, {Name: "decimal", Type: "DECIMAL(30,20)"}, {Name: "float", Type: "DOUBLE"}, {Name: "at", Type: "TIMESTAMP WITH TIME ZONE"}},
		cells(present(json.Number("9007199254740993")), present(json.Number("1.0000000000000000001")), present(float64(1.25)), present(instantA)),
		cells(present(json.Number("9007199254740993")), present(json.Number("1.0000000000000000001")), present(float64(1.25)), present(instantB)),
		cells(present(json.Number("9007199254740992")), present(json.Number("1.0000000000000000002")), present(float64(1.5)), present(instantA.Add(time.Second))),
	)
	for _, column := range []string{"integer", "decimal", "float", "at"} {
		groups, err := snapshot.GroupBy([]string{column})
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 2 || groups[0].Count != 2 {
			t.Errorf("%s values were grouped with lossy or timezone-sensitive equality: %+v", column, groups)
		}
	}
}

func TestSortUsesExactDeclaredTypeAndStableMultiColumnPriority(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "rank", Type: "INTEGER"}, {Name: "name", Type: "TEXT"}},
		cells(present(json.Number("1")), present("beta")),
		cells(present(json.Number("1")), present("gamma")),
		cells(present(json.Number("1")), present("gamma")),
		cells(present(json.Number("0")), present("zeta")),
	)
	order, err := snapshot.Sort([]SortKey{{Column: "rank"}, {Column: "name", Descending: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []int{3, 1, 2, 0}) {
		t.Fatalf("stable multicolumn order = %v", order)
	}
	if !reflect.DeepEqual(snapshot.OriginalOrder(), []int{0, 1, 2, 3}) {
		t.Fatal("sorting mutated the captured snapshot order")
	}
}

func TestSortOrdersMissingNullAndValuesThenReversesForDescending(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "n", Type: "INTEGER"}},
		cells(present(json.Number("2"))),
		cells(present(nil)),
		cells(missing()),
		cells(present(json.Number("1"))),
	)
	asc, err := snapshot.Sort([]SortKey{{Column: "n"}})
	if err != nil {
		t.Fatal(err)
	}
	desc, err := snapshot.Sort([]SortKey{{Column: "n", Descending: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(asc, []int{2, 1, 3, 0}) || !reflect.DeepEqual(desc, []int{0, 3, 1, 2}) {
		t.Fatalf("asc %v desc %v", asc, desc)
	}
}

func TestSortComparesUnicodeBooleanTemporalAndBinaryValues(t *testing.T) {
	t.Parallel()
	sameInstant := time.Date(2022, 1, 2, 0, 0, 0, 0, time.FixedZone("west", -5*60*60))
	snapshot := mustCapture(t,
		[]Column{{Name: "text", Type: "TEXT"}, {Name: "bool", Type: "BOOLEAN"}, {Name: "instant", Type: "TIMESTAMP"}, {Name: "bytes", Type: "BLOB"}},
		cells(present("\U00010000"), present(true), present(sameInstant), present([]byte{0xff})),
		cells(present("\ue000"), present(false), present(sameInstant.In(time.UTC)), present([]byte{0x01, 0xff})),
	)
	for _, tc := range []struct {
		key  string
		want []int
	}{
		{"text", []int{1, 0}},
		{"bool", []int{1, 0}},
		{"instant", []int{0, 1}}, // equal instants retain source order
		{"bytes", []int{1, 0}},
	} {
		got, err := snapshot.Sort([]SortKey{{Column: tc.key}})
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("sort %s = %v, %v; want %v", tc.key, got, err, tc.want)
		}
	}
}

func TestSortPreservesBigIntegerAndDecimalPrecision(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "big", Type: "BIGINT"}, {Name: "decimal", Type: "DECIMAL"}},
		cells(present(json.Number("9007199254740993")), present(json.Number("1.0000000000000000002"))),
		cells(present(json.Number("9007199254740992")), present(json.Number("1.0000000000000000001"))),
		cells(present(json.Number("18446744073709551616")), present(json.Number("1.0000000000000000003"))),
	)
	ints, err := snapshot.Sort([]SortKey{{Column: "big", Descending: true}})
	if err != nil || !reflect.DeepEqual(ints, []int{2, 0, 1}) {
		t.Fatalf("bigint sort = %v, %v", ints, err)
	}
	decimals, err := snapshot.Sort([]SortKey{{Column: "decimal"}})
	if err != nil || !reflect.DeepEqual(decimals, []int{1, 0, 2}) {
		t.Fatalf("decimal sort = %v, %v", decimals, err)
	}
	if _, err := exactInteger(json.Number("18446744073709551616")); err != nil {
		t.Fatal(err)
	}
}

func TestSortPreservesFloatPrecisionAndComparesTemporalInstants(t *testing.T) {
	t.Parallel()
	base := time.Date(2024, 6, 1, 10, 0, 0, 0, time.FixedZone("offset", 3*60*60))
	snapshot := mustCapture(t,
		[]Column{{Name: "float", Type: "DOUBLE"}, {Name: "at", Type: "TIMESTAMP WITH TIME ZONE"}},
		cells(present(float64(1.0000000000000004)), present(base.Add(time.Second))),
		cells(present(float64(1.0000000000000002)), present(base.In(time.UTC))),
		cells(present(float64(1.0)), present(base.Add(-time.Second))),
	)
	floats, err := snapshot.Sort([]SortKey{{Column: "float"}})
	if err != nil || !reflect.DeepEqual(floats, []int{2, 1, 0}) {
		t.Fatalf("float order lost adjacent representable values: %v, %v", floats, err)
	}
	instants, err := snapshot.Sort([]SortKey{{Column: "at"}})
	if err != nil || !reflect.DeepEqual(instants, []int{2, 1, 0}) {
		t.Fatalf("temporal order ignored normalized instants: %v, %v", instants, err)
	}
}

func TestCaptureAndResultsDoNotShareMutableInput(t *testing.T) {
	t.Parallel()
	binaryValue := []byte{1, 2, 3}
	inputMap := map[string]any{"nested": []any{"before"}}
	snapshot := mustCapture(t,
		[]Column{{Name: "binary", Type: "BLOB"}, {Name: "nested", Type: "OTHER"}},
		cells(present(binaryValue), present(inputMap)),
	)
	binaryValue[0] = 9
	inputMap["nested"].([]any)[0] = "after"
	groups, err := snapshot.GroupBy([]string{"binary"})
	if err != nil {
		t.Fatal(err)
	}
	gotBinary := groups[0].Keys[0].Value.([]byte)
	if !reflect.DeepEqual(gotBinary, []byte{1, 2, 3}) {
		t.Fatalf("captured binary changed with input: %v", gotBinary)
	}
	gotBinary[0] = 8
	again, err := snapshot.GroupBy([]string{"binary"})
	if err != nil || !reflect.DeepEqual(again[0].Keys[0].Value, []byte{1, 2, 3}) {
		t.Fatalf("returned group key mutated the snapshot: %+v, %v", again, err)
	}
	if inputMap["nested"].([]any)[0] != "after" {
		t.Fatal("test fixture unexpectedly changed")
	}
}

func TestInvalidTypesAndShapesFailWithoutStringCoercion(t *testing.T) {
	t.Parallel()
	if _, err := Capture([]Column{{Name: "a", Type: "TEXT"}}, []Row{cells(present("x"), present("extra"))}); err == nil {
		t.Fatal("accepted a row with the wrong cell count")
	}
	snapshot := mustCapture(t, []Column{{Name: "n", Type: "INTEGER"}}, cells(present("12")))
	if _, err := snapshot.Sort([]SortKey{{Column: "n"}}); err == nil {
		t.Fatal("coerced a string cell to an integer")
	}
	unsupported := mustCapture(t, []Column{{Name: "v", Type: "UNKNOWN"}}, cells(present("x")))
	if _, err := unsupported.GroupBy([]string{"v"}); err == nil {
		t.Fatal("accepted an unsupported declared type")
	}
	for _, typ := range []string{"JSON", "BIT"} {
		if _, err := declaredType(typ); err == nil {
			t.Errorf("accepted ambiguous declared type %q", typ)
		}
	}
	if _, err := snapshot.GroupBy(nil); err != ErrNoGroupColumns {
		t.Fatalf("empty grouping error = %v", err)
	}
	decimal, err := exactDecimal(json.Number("1e1000"))
	if err != nil || decimal.Sign() <= 0 {
		t.Fatalf("exact exponent decimal = %v, %v", decimal, err)
	}
	if _, err := exactDecimal(json.Number("1/3")); err == nil {
		t.Fatal("accepted fraction syntax as decimal")
	}
}

func TestMetadataAccessAndSelectionValidation(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t, []Column{{Name: "key", Type: "TEXT"}}, cells(present("value")))
	if snapshot.RowCount() != 1 || !reflect.DeepEqual(snapshot.Columns(), []Column{{Name: "key", Type: "TEXT"}}) {
		t.Fatal("snapshot metadata or row count is incorrect")
	}
	if _, err := snapshot.GroupBy([]string{"missing"}); err == nil {
		t.Fatal("grouping accepted an unknown column")
	}
	if _, err := snapshot.GroupBy([]string{"key", "key"}); err == nil {
		t.Fatal("grouping accepted a repeated key")
	}
	if _, err := snapshot.Sort([]SortKey{{Column: "missing"}}); err == nil {
		t.Fatal("sorting accepted an unknown column")
	}
	if _, err := snapshot.Sort([]SortKey{{Column: "key"}, {Column: "key"}}); err == nil {
		t.Fatal("sorting accepted a repeated key")
	}
	if _, err := Capture([]Column{{Name: " "}}, nil); err == nil {
		t.Fatal("capture accepted an empty column name")
	}
	if _, err := Capture([]Column{{Name: "x"}, {Name: "x"}}, nil); err == nil {
		t.Fatal("capture accepted duplicate names")
	}
	empty, err := Capture(nil, nil)
	if err != nil || empty.RowCount() != 0 || len(empty.OriginalOrder()) != 0 {
		t.Fatalf("empty snapshot = %+v, %v", empty, err)
	}
}

func TestExactNumericHelpersAcceptTypedRepresentations(t *testing.T) {
	t.Parallel()
	integers := []any{int(-2), int8(-2), int16(-2), int32(-2), int64(-2), uint(2), uint8(2), uint16(2), uint32(2), uint64(2), json.Number("2"), big.NewInt(2), *big.NewInt(2)}
	for _, value := range integers {
		if _, err := exactInteger(value); err != nil {
			t.Errorf("exactInteger(%T): %v", value, err)
		}
	}
	for _, value := range []any{float32(1.5), float64(1.5), json.Number("1.5")} {
		if _, ok := numericFloat(value); !ok {
			t.Errorf("numericFloat(%T) rejected", value)
		}
	}
	if _, ok := numericFloat(json.Number("not-a-number")); ok {
		t.Fatal("numericFloat accepted invalid numeric text")
	}
	if _, err := exactInteger(json.Number("1.0")); err == nil {
		t.Fatal("exactInteger accepted fractional syntax")
	}
	if _, err := exactDecimal(big.NewInt(4)); err != nil {
		t.Fatalf("integer to exact decimal: %v", err)
	}
	if _, err := exactDecimal(big.Rat{}); err != nil {
		t.Fatalf("big.Rat decimal: %v", err)
	}
	if _, err := exactDecimal(float64(1.25)); err == nil {
		t.Fatal("exactDecimal accepted a lossy float")
	}
}

func TestCaptureRejectsUnsafeCyclesAndMutablePointerShapes(t *testing.T) {
	t.Parallel()
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	for _, value := range []any{cyclic, map[int]string{1: "x"}, new(int), struct{ N int }{N: 1}} {
		if _, err := Capture([]Column{{Name: "v", Type: "OTHER"}}, []Row{cells(present(value))}); err == nil {
			t.Errorf("capture accepted unsupported mutable value %T", value)
		}
	}
}

func TestDeepCopySupportsTypedSlicesArraysAndNilCollections(t *testing.T) {
	t.Parallel()
	array := [2]string{"a", "b"}
	values := []any{
		array,
		[]int{1, 2},
		map[string]any{"items": []any{"value"}},
		[]byte(nil),
		map[string]any(nil),
		(*big.Int)(nil),
		(*big.Rat)(nil),
	}
	for _, value := range values {
		if _, err := cloneValue(value); err != nil {
			t.Errorf("cloneValue(%T): %v", value, err)
		}
	}
	if _, err := cloneValue(nil); err != nil {
		t.Fatalf("cloneValue(nil): %v", err)
	}
}

func TestInvalidTypedCellsReturnExplicitErrors(t *testing.T) {
	t.Parallel()
	invalid := []struct {
		kind  valueType
		value any
	}{
		{typeText, []byte("not text")},
		{typeText, string([]byte{0xff})},
		{typeInteger, float64(1)},
		{typeDecimal, float64(1)},
		{typeFloat, math.NaN()},
		{typeFloat, json.Number("1e9999")},
		{typeBoolean, "true"},
		{typeTemporal, "2024-01-01"},
		{typeBinary, "bytes"},
		{0, "unknown"},
	}
	for _, tc := range invalid {
		if _, err := normalizeValue(tc.kind, tc.value); err == nil {
			t.Errorf("normalizeValue(%d, %T) unexpectedly succeeded", tc.kind, tc.value)
		}
	}
	if compareNormalized(normalizedCell{state: 2, value: "x"}, normalizedCell{state: 2, value: "y"}, 0) != 0 {
		t.Fatal("unknown normalized type should not invent an ordering")
	}
}

func TestEmptyTextAndPrefixOrderingUseUnicodeCodePoints(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t, []Column{{Name: "text", Type: "VARCHAR(12)"}},
		cells(present("aa")), cells(present("")), cells(present("a")))
	order, err := snapshot.Sort([]SortKey{{Column: "text"}})
	if err != nil || !reflect.DeepEqual(order, []int{1, 2, 0}) {
		t.Fatalf("empty/prefix text order = %v, %v", order, err)
	}
}

func TestSortSupportsExactArbitraryPrecisionValues(t *testing.T) {
	t.Parallel()
	large := new(big.Int).Lsh(big.NewInt(1), 200)
	copyOfLarge := new(big.Int).Set(large)
	snapshot := mustCapture(t,
		[]Column{{Name: "n", Type: "INTEGER"}},
		cells(present(large)),
		cells(present(new(big.Int).Sub(copyOfLarge, big.NewInt(1)))),
	)
	order, err := snapshot.Sort([]SortKey{{Column: "n"}})
	if err != nil || !reflect.DeepEqual(order, []int{1, 0}) {
		t.Fatalf("arbitrary precision order = %v, %v", order, err)
	}
}

func TestFloatJSONNumberRejectsLossyRoundingAndUnderflow(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"9007199254740993", "1e-10000", "0.1", "1e99999999999999999999", strings.Repeat("1", maxDecimalTextLength+1)} {
		if _, ok := numericFloat(json.Number(raw)); ok {
			t.Errorf("accepted inexact/out-of-range floating text %q", raw)
		}
	}
	if got, ok := numericFloat(json.Number("1.5")); !ok || got != 1.5 {
		t.Fatalf("exact floating text = %v, %v", got, ok)
	}
	snapshot := mustCapture(t, []Column{{Name: "f", Type: "DOUBLE"}},
		cells(present(json.Number("9007199254740992"))),
		cells(present(json.Number("9007199254740993"))),
	)
	if _, err := snapshot.GroupBy([]string{"f"}); err == nil {
		t.Fatal("grouping silently collapsed an inexact JSON float with a neighboring value")
	}
	if _, err := snapshot.Sort([]SortKey{{Column: "f"}}); err == nil {
		t.Fatal("sorting silently rounded an inexact JSON float")
	}
}

func TestDeclaredTypeParametersAndSuffixesAreValidated(t *testing.T) {
	t.Parallel()
	valid := []string{"VARCHAR(12)", "CHARACTER(1)", "DECIMAL(10, 2)", "NUMERIC(10)", "FLOAT(24)", "TIMESTAMP(6) WITH TIME ZONE", "TIMESTAMP WITHOUT TIME ZONE"}
	for _, typ := range valid {
		if _, err := declaredType(typ); err != nil {
			t.Errorf("declaredType(%q): %v", typ, err)
		}
	}
	invalid := []string{
		"INTEGER(not-a-type", "INTEGER(", "INTEGER(4)", "NUMERIC(10,2)[]", "NUMERIC(10,2) ARRAY",
		"NUMERIC(10,2", "NUMERIC(10,2))", "NUMERIC(10,2) junk", "DECIMAL(0,0)", "DECIMAL(10,11)",
		"VARCHAR()", "VARCHAR(-1)", "FLOAT(54)", "BOOLEAN(1)", "TIMESTAMP(10)",
	}
	for _, typ := range invalid {
		if _, err := declaredType(typ); err == nil {
			t.Errorf("accepted malformed or unsupported declared type %q", typ)
		}
	}
}

func TestGroupByAggregatesKeepsIntegerAndDecimalSumsExact(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "bucket", Type: "TEXT"}, {Name: "n", Type: "BIGINT"}, {Name: "d", Type: "DECIMAL(12,4)"}},
		cells(present("a"), present(json.Number("9007199254740993")), present(json.Number("0.1"))),
		cells(present("a"), present(json.Number("9007199254740993")), present(json.Number("0.2"))),
		cells(present("b"), present(json.Number("7")), present(json.Number("0.3"))),
	)
	terms := []AggregateTerm{
		{Column: "n", Function: AggregateSum, Alias: "total_n"},
		{Column: "d", Function: AggregateSum, Alias: "total_d"},
	}
	groups, err := snapshot.GroupByAggregates([]string{"bucket"}, true, terms)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Count != 2 || !reflect.DeepEqual(groups[0].RowIndices, []int{0, 1}) {
		t.Fatalf("group mapping/count = %+v", groups)
	}
	wantInteger := new(big.Int).SetInt64(0)
	wantInteger.SetString("18014398509481986", 10)
	if got, ok := groups[0].Aggregates[0].Value.(*big.Int); !groups[0].Aggregates[0].Present || !ok || got.Cmp(wantInteger) != 0 {
		t.Fatalf("exact integer SUM = %#v", groups[0].Aggregates[0])
	}
	wantDecimal := new(big.Rat).SetFrac(big.NewInt(3), big.NewInt(10))
	if got, ok := groups[0].Aggregates[1].Value.(*big.Rat); !groups[0].Aggregates[1].Present || !ok || got.Cmp(wantDecimal) != 0 {
		t.Fatalf("exact decimal SUM = %#v", groups[0].Aggregates[1])
	}
	if groups[1].Count != 1 || groups[1].Aggregates[0].Value.(*big.Int).String() != "7" {
		t.Fatalf("second group = %+v", groups[1])
	}
	groups[0].Aggregates[0].Value.(*big.Int).SetInt64(-1)
	groups[0].Aggregates[1].Value.(*big.Rat).SetInt64(-1)
	again, err := snapshot.GroupByAggregates([]string{"bucket"}, true, terms)
	if err != nil || again[0].Aggregates[0].Value.(*big.Int).String() != "18014398509481986" || again[0].Aggregates[1].Value.(*big.Rat).Cmp(wantDecimal) != 0 {
		t.Fatalf("aggregate output mutated snapshot/repeated result: %+v, %v", again, err)
	}
}

func TestGroupByAggregatesNullCountsAndOptionalRowCount(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "bucket", Type: "TEXT"}, {Name: "n", Type: "INTEGER"}, {Name: "label", Type: "TEXT"}},
		cells(present("a"), present(nil), present(nil)),
		cells(present("a"), present(nil), present(nil)),
		cells(present("b"), present(json.Number("2")), present("z")),
		cells(present("b"), present(json.Number("5")), present("a")),
	)
	terms := []AggregateTerm{
		{Column: "n", Function: AggregateSum, Alias: "sum_n"},
		{Column: "n", Function: AggregateCount, Alias: "count_n"},
		{Column: "label", Function: AggregateMin, Alias: "min_label"},
		{Column: "label", Function: AggregateMax, Alias: "max_label"},
	}
	groups, err := snapshot.GroupByAggregates([]string{"bucket"}, false, terms)
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Count != 2 {
		t.Fatalf("Group.Count lost actual row count when COUNT(*) output was unrequested: %d", groups[0].Count)
	}
	if !groups[0].Aggregates[0].Present || groups[0].Aggregates[0].Value != nil || groups[0].Aggregates[1].Value != 0 || groups[0].Aggregates[2].Value != nil || groups[0].Aggregates[3].Value != nil {
		t.Fatalf("all-null SUM/MIN/MAX and COUNT(column) semantics: %+v", groups[0].Aggregates)
	}
	counted, err := snapshot.GroupByAggregates([]string{"bucket"}, true, nil)
	if err != nil || counted[0].Count != 2 || counted[1].Count != 2 {
		t.Fatalf("requested COUNT(*) = %+v, %v", counted, err)
	}
	if groups[1].Aggregates[0].Value.(*big.Int).String() != "7" || groups[1].Aggregates[1].Value != 2 || groups[1].Aggregates[2].Value != "a" || groups[1].Aggregates[3].Value != "z" {
		t.Fatalf("numeric/text aggregates = %+v", groups[1].Aggregates)
	}
}

func TestGroupByAggregatesRejectsMissingAndInvalidTerms(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "bucket", Type: "TEXT"}, {Name: "n", Type: "INTEGER"}, {Name: "label", Type: "TEXT"}},
		cells(present("a"), missing(), present("x")),
	)
	if _, err := snapshot.GroupByAggregates([]string{"bucket"}, true, []AggregateTerm{{Column: "n", Function: AggregateSum, Alias: "sum"}}); err == nil {
		t.Fatal("SUM silently accepted a missing cell")
	}
	validSnapshot := mustCapture(t, []Column{{Name: "bucket", Type: "TEXT"}, {Name: "n", Type: "INTEGER"}, {Name: "label", Type: "TEXT"}}, cells(present("a"), present(json.Number("1")), present("x")))
	invalid := [][]AggregateTerm{
		{{Column: "n", Function: AggregateSum, Alias: "same"}, {Column: "n", Function: AggregateCount, Alias: "SAME"}},
		{{Column: "n", Function: AggregateSum, Alias: "BUCKET"}},
		{{Column: "n", Function: AggregateSum, Alias: "Count"}},
		{{Column: "label", Function: AggregateSum, Alias: "sum_label"}},
		{{Column: "n", Function: AggregateAvg, Alias: "average"}},
		{{Column: "n", Function: "MEDIAN", Alias: "median"}},
		{{Column: "*", Function: AggregateCount, Alias: "count_star"}},
		{{Column: "n", Function: AggregateSum}},
	}
	for i, terms := range invalid {
		if _, err := validSnapshot.GroupByAggregates([]string{"bucket"}, true, terms); err == nil {
			t.Errorf("invalid term set %d was accepted: %+v", i, terms)
		}
	}
	if _, err := validSnapshot.GroupByAggregates([]string{"bucket"}, false, []AggregateTerm{{Column: "n", Function: AggregateSum, Alias: "count"}}); err != nil {
		t.Fatalf("COUNT alias should be available when COUNT(*) output is not requested: %v", err)
	}
}

func TestGroupByAggregatesRejectsFloatingSumOverflow(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "bucket", Type: "TEXT"}, {Name: "f", Type: "DOUBLE"}},
		cells(present("a"), present(math.MaxFloat64)),
		cells(present("a"), present(math.MaxFloat64)),
	)
	if _, err := snapshot.GroupByAggregates([]string{"bucket"}, true, []AggregateTerm{{Column: "f", Function: AggregateSum, Alias: "sum_f"}}); err == nil {
		t.Fatal("floating SUM overflow was accepted")
	}
}

func TestGroupByAggregatesUsesSourceOrderForFloatingSum(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "bucket", Type: "TEXT"}, {Name: "f", Type: "DOUBLE"}},
		cells(present("a"), present(1e16)),
		cells(present("a"), present(1.0)),
		cells(present("a"), present(-1e16)),
	)
	groups, err := snapshot.GroupByAggregates([]string{"bucket"}, false, []AggregateTerm{{Column: "f", Function: AggregateSum, Alias: "sum_f"}})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := groups[0].Aggregates[0].Value.(float64); !ok || got != 0 {
		t.Fatalf("sequential IEEE-754 sum = %#v, want 0", got)
	}
}

func TestGroupByAggregatesIntegerMinMaxUseExactIntegerValues(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "bucket", Type: "TEXT"}, {Name: "n", Type: "BIGINT"}},
		cells(present("a"), present(json.Number("9007199254740993"))),
		cells(present("a"), present(json.Number("9007199254740992"))),
	)
	groups, err := snapshot.GroupByAggregates([]string{"bucket"}, false, []AggregateTerm{
		{Column: "n", Function: AggregateMin, Alias: "min_n"},
		{Column: "n", Function: AggregateMax, Alias: "max_n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	min, minOK := groups[0].Aggregates[0].Value.(*big.Int)
	max, maxOK := groups[0].Aggregates[1].Value.(*big.Int)
	if !minOK || !maxOK || min.String() != "9007199254740992" || max.String() != "9007199254740993" {
		t.Fatalf("integer MIN/MAX types and values = %#v, %#v", groups[0].Aggregates[0], groups[0].Aggregates[1])
	}
}

func TestDecimalPrecisionBudgetAcceptsBoundariesAndRejectsUnboundedExponents(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"1e1000", "1e-1000", strings.Repeat("9", maxNumericDigits)} {
		if _, err := parseDecimal(raw); err != nil {
			t.Errorf("parseDecimal(%q at boundary): %v", abbreviatedNumber(raw), err)
		}
	}
	for _, raw := range []string{
		"1e1001", "1e-1001", "1e99999999999999999999",
		strings.Repeat("9", maxNumericDigits+1), strings.Repeat("1", maxDecimalTextLength+1),
	} {
		if _, err := parseDecimal(raw); err == nil {
			t.Errorf("parseDecimal accepted input beyond its resource budget: %q", abbreviatedNumber(raw))
		}
	}
	for _, raw := range []string{"1e", "1e+", ".", "1.2.3", "1/2"} {
		if _, err := parseDecimal(raw); err == nil {
			t.Errorf("parseDecimal accepted malformed decimal %q", raw)
		}
	}
}

func TestDecimalPrecisionBudgetAppliesThroughSnapshotOperations(t *testing.T) {
	t.Parallel()
	columns := []Column{{Name: "bucket", Type: "TEXT"}, {Name: "amount", Type: "DECIMAL(1000,0)"}}
	for _, operation := range []struct {
		name string
		run  func(Snapshot) error
	}{
		{name: "group", run: func(s Snapshot) error { _, err := s.GroupBy([]string{"amount"}); return err }},
		{name: "sort", run: func(s Snapshot) error { _, err := s.Sort([]SortKey{{Column: "amount"}}); return err }},
		{name: "aggregate", run: func(s Snapshot) error {
			_, err := s.GroupByAggregates([]string{"bucket"}, true, []AggregateTerm{{Column: "amount", Function: AggregateSum, Alias: "total"}})
			return err
		}},
	} {
		snapshot := mustCapture(t, columns, cells(present("a"), present(json.Number("1e99999999999999999999"))))
		if err := operation.run(snapshot); err == nil {
			t.Errorf("%s accepted an oversized decimal exponent", operation.name)
		}
	}
}

func TestDecimalExponentBoundarySurvivesAggregateCloningAndRepeatCalls(t *testing.T) {
	t.Parallel()
	snapshot := mustCapture(t,
		[]Column{{Name: "bucket", Type: "TEXT"}, {Name: "amount", Type: "DECIMAL"}},
		cells(present("a"), present(json.Number("1e1000"))),
		cells(present("a"), present(json.Number("2e1000"))),
	)
	terms := []AggregateTerm{
		{Column: "amount", Function: AggregateSum, Alias: "sum_amount"},
		{Column: "amount", Function: AggregateMin, Alias: "min_amount"},
		{Column: "amount", Function: AggregateMax, Alias: "max_amount"},
	}
	groups, err := snapshot.GroupByAggregates([]string{"bucket"}, true, terms)
	if err != nil {
		t.Fatal(err)
	}
	wantMin, _ := parseDecimal("1e1000")
	wantSum, _ := parseDecimal("3e1000")
	wantMax, _ := parseDecimal("2e1000")
	if groups[0].Aggregates[0].Value.(*big.Rat).Cmp(wantSum) != 0 || groups[0].Aggregates[1].Value.(*big.Rat).Cmp(wantMin) != 0 || groups[0].Aggregates[2].Value.(*big.Rat).Cmp(wantMax) != 0 {
		t.Fatalf("large decimal aggregate values = %+v", groups[0].Aggregates)
	}
	groups[0].Aggregates[1].Value.(*big.Rat).SetInt64(0)
	again, err := snapshot.GroupByAggregates([]string{"bucket"}, true, terms)
	if err != nil || again[0].Aggregates[1].Value.(*big.Rat).Cmp(wantMin) != 0 {
		t.Fatalf("MIN result mutation affected a later result: %+v, %v", again, err)
	}
	maxBoundary, err := parseDecimal(strings.Repeat("9", maxNumericDigits) + "e1000")
	if err != nil {
		t.Fatalf("maximum bounded decimal parse: %v", err)
	}
	if _, err := cloneValue(maxBoundary); err != nil {
		t.Fatalf("maximum bounded decimal clone: %v", err)
	}
}

func TestNumericPrecisionBudgetBoundsIntegerAndNativeBigValuesBeforeCaptureCopy(t *testing.T) {
	t.Parallel()
	maxIntegerText := strings.Repeat("9", maxNumericDigits)
	if _, err := parseInteger(maxIntegerText); err != nil {
		t.Fatalf("1000-digit integer text rejected: %v", err)
	}
	if _, err := parseInteger(maxIntegerText + "9"); err == nil {
		t.Fatal("1001-digit integer text accepted")
	}
	maxInteger, ok := new(big.Int).SetString(strings.Repeat("9", maxNumericDigits), 10)
	if !ok {
		t.Fatal("failed to build bounded integer test value")
	}
	tooLarge, ok := new(big.Int).SetString("1"+strings.Repeat("0", maxNumericDigits), 10)
	if !ok {
		t.Fatal("failed to build oversized integer test value")
	}
	if _, err := Capture([]Column{{Name: "n", Type: "BIGINT"}}, []Row{cells(present(maxInteger))}); err != nil {
		t.Fatalf("1000-digit integer rejected: %v", err)
	}
	textSnapshot := mustCapture(t, []Column{{Name: "n", Type: "BIGINT"}}, cells(present(json.Number(tooLarge.String()))))
	if _, err := textSnapshot.Sort([]SortKey{{Column: "n"}}); err == nil {
		t.Fatal("1001-digit integer text passed snapshot normalization")
	}
	if _, err := Capture([]Column{{Name: "n", Type: "BIGINT"}}, []Row{cells(present(tooLarge))}); err == nil {
		t.Fatal("oversized native big.Int accepted")
	}
	tooLargeRatDenom, ok := new(big.Int).SetString("1"+strings.Repeat("0", maxDecimalValueDigits), 10)
	if !ok {
		t.Fatal("failed to build oversized rational denominator")
	}
	largeRat := new(big.Rat).SetFrac(big.NewInt(1), tooLargeRatDenom)
	if _, err := Capture([]Column{{Name: "n", Type: "DECIMAL"}}, []Row{cells(present(largeRat))}); err == nil {
		t.Fatal("oversized native big.Rat accepted")
	}
}

func abbreviatedNumber(value string) string {
	if len(value) <= 48 {
		return value
	}
	return value[:20] + "..." + value[len(value)-20:]
}
