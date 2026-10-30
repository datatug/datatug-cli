package resultops

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Not parallel: mutates ratSetString
func TestResultops_RatSetStringFailure(t *testing.T) {
	orig := ratSetString
	defer func() { ratSetString = orig }()
	ratSetString = func(r *big.Rat, s string) (*big.Rat, bool) {
		return nil, false
	}
	_, err := parseDecimal("123.45")
	assert.ErrorContains(t, err, "invalid decimal")

	_, ok := numericFloat(json.Number("123.45"))
	assert.False(t, ok)
}

// Not parallel: mutates cloneValueHook
func TestResultops_CloneValueHookFailure(t *testing.T) {
	orig := cloneValueHook
	defer func() { cloneValueHook = orig }()

	snap := Snapshot{
		columns: []Column{{Name: "col", Type: "TEXT"}},
		rows:    []Row{{Cells: []Cell{{Present: true, Value: "hello"}}}},
	}
	cloneValueHook = func(value any) (any, error) {
		return nil, errors.New("simulated clone error")
	}
	// GroupBy line 167
	_, err := snap.GroupBy([]string{"col"})
	assert.ErrorContains(t, err, "simulated clone error")

	// aggregateGroup line 329
	termMin := struct {
		column int
		kind   valueType
		fn     string
	}{column: 0, kind: typeText, fn: AggregateMin}
	_, err = snap.aggregateGroup([]int{0}, termMin)
	assert.ErrorContains(t, err, "simulated clone error")
}

// Not parallel: mutates encodeNormalizedCell
func TestResultops_EncodeNormalizedCellFailure(t *testing.T) {
	orig := encodeNormalizedCell
	defer func() { encodeNormalizedCell = orig }()
	encodeNormalizedCell = func(n normalizedCell, kind valueType) ([]byte, error) {
		return nil, errors.New("simulated encoding error")
	}
	_, _, err := encodeGroupCell(typeText, "TEXT", Cell{Present: true, Value: "abc"})
	assert.ErrorContains(t, err, "simulated encoding error")
}

func TestResultops_GroupBy_And_Aggregates_Errors(t *testing.T) {
	t.Parallel()

	// GroupByAggregates: declaredType error (line 236)
	snapBadType := Snapshot{
		columns: []Column{{Name: "c", Type: "INVALID[]"}},
		rows:    []Row{},
	}
	_, err := snapBadType.GroupByAggregates([]string{}, false, []AggregateTerm{{Alias: "a", Function: "COUNT", Column: "c"}})
	assert.Error(t, err)

	// GroupByAggregates: s.GroupBy error (line 246)
	snapGroupErr := Snapshot{
		columns: []Column{{Name: "grp", Type: "INVALID[]"}, {Name: "val", Type: "INT"}},
		rows:    []Row{},
	}
	_, err = snapGroupErr.GroupByAggregates([]string{"grp"}, false, []AggregateTerm{{Alias: "a", Function: "COUNT", Column: "val"}})
	assert.Error(t, err)

	// aggregateGroup unsupported fn (line 333)
	termUnknown := struct {
		column int
		kind   valueType
		fn     string
	}{column: 0, kind: typeInteger, fn: "UNKNOWN"}
	_, err = Snapshot{}.aggregateGroup([]int{}, termUnknown)
	assert.ErrorContains(t, err, "unsupported aggregate function")

	// Sort declaredType error (line 354)
	_, err = snapBadType.Sort([]SortKey{{Column: "c"}})
	assert.Error(t, err)
}

func TestResultops_DeclaredType_And_TypeParsing(t *testing.T) {
	t.Parallel()

	// line 444
	_, err := declaredType("TEXT(10)")
	assert.ErrorContains(t, err, "does not accept parameters")

	// line 462
	_, err = declaredType("MONEY(2)")
	assert.ErrorContains(t, err, "does not accept parameters")

	// line 464: unparameterized MONEY
	vt, err := declaredType("MONEY")
	assert.NoError(t, err)
	assert.Equal(t, typeDecimal, vt)

	// line 484
	_, err = declaredType("BLOB(10)")
	assert.ErrorContains(t, err, "does not accept parameters")

	// line 487
	_, err = declaredType("BINARY(0)")
	assert.ErrorContains(t, err, "invalid length parameters")

	// line 500
	assert.False(t, isLengthType("UNKNOWN"))

	// line 506 & 509
	assert.False(t, validPrecisionScale([]int64{1, 2, 3}))
	assert.False(t, validPrecisionScale([]int64{0}))
	assert.False(t, validPrecisionScale([]int64{1001}))

	// line 538
	_, _, err = parseDeclaredType("(10)")
	assert.ErrorContains(t, err, "missing base type")

	// line 554
	_, _, err = parseDeclaredType("VARCHAR(10, )")
	assert.ErrorContains(t, err, "empty type parameter")

	// line 558
	_, _, err = parseDeclaredType("VARCHAR(abc)")
	assert.ErrorContains(t, err, "invalid type parameter")
}

func TestResultops_Encoding_Normalization_And_Comparison(t *testing.T) {
	t.Parallel()

	// line 581: encodeGroupCell with Present=true, Value=nil
	enc, cellOut, err := encodeGroupCell(typeText, "TEXT", Cell{Present: true, Value: nil})
	assert.NoError(t, err)
	assert.Equal(t, []byte{1}, enc)
	assert.True(t, cellOut.Present)
	assert.Nil(t, cellOut.Value)

	// line 631: normalizeValue float -0.0
	negZero, err := normalizeValue(typeFloat, math.Copysign(0, -1))
	assert.NoError(t, err)
	assert.Equal(t, float64(0), negZero)

	// line 683, 689: exactInteger over budget
	overInt, _ := new(big.Int).SetString("1"+strings.Repeat("0", maxNumericDigits+5), 10)
	_, err = exactInteger(*overInt)
	assert.Error(t, err)
	_, err = exactInteger(overInt)
	assert.Error(t, err)

	// line 699, 703: parseInteger
	_, err = parseInteger(" 123 ")
	assert.Error(t, err)
	posInt, err := parseInteger("+123")
	assert.NoError(t, err)
	assert.Equal(t, big.NewInt(123), posInt)

	// line 721, 727, 733, 739, 747: exactDecimal
	overRatDenom, _ := new(big.Int).SetString("1"+strings.Repeat("0", maxDecimalValueDigits+5), 10)
	overRat := new(big.Rat).SetFrac(big.NewInt(1), overRatDenom)
	_, err = exactDecimal(*overRat)
	assert.Error(t, err)
	_, err = exactDecimal(overRat)
	assert.Error(t, err)
	_, err = exactDecimal(*overInt)
	assert.Error(t, err)
	_, err = exactDecimal(overInt)
	assert.Error(t, err)
	_, err = exactDecimal("not_a_num")
	assert.Error(t, err)

	// line 729: valid *big.Rat
	validRat, err := exactDecimal(big.NewRat(1, 2))
	assert.NoError(t, err)
	assert.Equal(t, big.NewRat(1, 2), validRat)

	// line 735: valid big.Int
	validFromInt, err := exactDecimal(*big.NewInt(42))
	assert.NoError(t, err)
	assert.Equal(t, big.NewRat(42, 1), validFromInt)

	// line 745: valid int64 via exactInteger
	validFromInt64, err := exactDecimal(int64(42))
	assert.NoError(t, err)
	assert.Equal(t, big.NewRat(42, 1), validFromInt64)

	// line 769: validateDecimalBudget text too long
	assert.Error(t, validateDecimalBudget(strings.Repeat("1", maxDecimalTextLength+1)))

	// line 773: validateDecimalBudget empty or untrimmed
	assert.Error(t, validateDecimalBudget(""))
	assert.Error(t, validateDecimalBudget("  1.23  "))

	// line 774: validateDecimalBudget with "+" or "-" alone
	assert.Error(t, validateDecimalBudget("+"))
	assert.Error(t, validateDecimalBudget("-"))

	// line 828, 833, 837: validateBigIntLimit
	assert.NoError(t, validateBigIntLimit(nil, 10, 10, "test"))
	assert.Error(t, validateBigIntLimit(new(big.Int).Lsh(big.NewInt(1), 20), 10, 10, "test"))
	assert.NoError(t, validateBigIntLimit(big.NewInt(-999), 3, 100, "test"))

	// line 847, 853, 854: validateBigRatBudget
	assert.NoError(t, validateBigRatBudget(nil))
	badNumRat := new(big.Rat).SetFrac(new(big.Int).Lsh(big.NewInt(1), 20000), big.NewInt(1))
	assert.Error(t, validateBigRatBudget(badNumRat))
	badDenomRat := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 20000))
	assert.Error(t, validateBigRatBudget(badDenomRat))

	// line 871: numericFloat with float overflow / inf
	_, ok := numericFloat(json.Number("1e500"))
	assert.False(t, ok)
	// line 883: numericFloat unsupported type
	_, ok = numericFloat("text")
	assert.False(t, ok)

	// line 912: encoding default case
	_, err = (normalizedCell{state: 2, value: "x"}).encoding(valueType(99))
	assert.ErrorContains(t, err, "unsupported value type")

	// line 924: compareNormalized state != 2 (e.g. state 0 vs 0 or 1 vs 1)
	assert.Equal(t, 0, compareNormalized(normalizedCell{state: 0}, normalizedCell{state: 0}, typeText))
	assert.Equal(t, 0, compareNormalized(normalizedCell{state: 1}, normalizedCell{state: 1}, typeText))

	// line 938, 940: float > and ==
	assert.Equal(t, 1, compareNormalized(normalizedCell{state: 2, value: 5.0}, normalizedCell{state: 2, value: 3.0}, typeFloat))
	assert.Equal(t, 0, compareNormalized(normalizedCell{state: 2, value: 3.0}, normalizedCell{state: 2, value: 3.0}, typeFloat))

	// line 945, 958: bool ==, false < true, and true > false
	assert.Equal(t, 0, compareNormalized(normalizedCell{state: 2, value: true}, normalizedCell{state: 2, value: true}, typeBoolean))
	assert.Equal(t, 1, compareNormalized(normalizedCell{state: 2, value: true}, normalizedCell{state: 2, value: false}, typeBoolean))

	// line 954, 957, 959: temporal Before, After, and equal
	now := time.Now()
	assert.Equal(t, -1, compareNormalized(normalizedCell{state: 2, value: now}, normalizedCell{state: 2, value: now.Add(time.Hour)}, typeTemporal))
	assert.Equal(t, 1, compareNormalized(normalizedCell{state: 2, value: now.Add(time.Hour)}, normalizedCell{state: 2, value: now}, typeTemporal))
	assert.Equal(t, 0, compareNormalized(normalizedCell{state: 2, value: now}, normalizedCell{state: 2, value: now}, typeTemporal))
}

func TestResultops_CloneValue_EdgeCases(t *testing.T) {
	t.Parallel()

	overInt, _ := new(big.Int).SetString("1"+strings.Repeat("0", maxNumericDigits+5), 10)
	overRatDenom, _ := new(big.Int).SetString("1"+strings.Repeat("0", maxDecimalValueDigits+5), 10)
	overRat := new(big.Rat).SetFrac(big.NewInt(1), overRatDenom)

	// lines 1016, 1024, 1029, 1037
	_, err := cloneValue(*overInt)
	assert.Error(t, err)
	_, err = cloneValue(overInt)
	assert.Error(t, err)
	_, err = cloneValue(*overRat)
	assert.Error(t, err)
	_, err = cloneValue(overRat)
	assert.Error(t, err)

	// lines 1026, 1030, 1039, 1043: valid big.Int / *big.Int and big.Rat / *big.Rat
	clonedIntVal, err := cloneValue(*big.NewInt(42))
	assert.NoError(t, err)
	assert.Equal(t, *big.NewInt(42), clonedIntVal)

	clonedInt, err := cloneValue(big.NewInt(42))
	assert.NoError(t, err)
	assert.Equal(t, big.NewInt(42), clonedInt)

	clonedRatVal, err := cloneValue(*big.NewRat(1, 2))
	assert.NoError(t, err)
	assert.Equal(t, *big.NewRat(1, 2), clonedRatVal)

	clonedRat, err := cloneValue(big.NewRat(1, 2))
	assert.NoError(t, err)
	assert.Equal(t, big.NewRat(1, 2), clonedRat)

	// line 1065: reflect.Interface element in slice
	clonedInterfaceSlice, err := cloneValue([]any{"nested", nil})
	assert.NoError(t, err)
	assert.Equal(t, []any{"nested", nil}, clonedInterfaceSlice)

	// line 1068: nil slice
	var nilSlice []int
	clonedSlice, err := cloneValue(nilSlice)
	assert.NoError(t, err)
	assert.Nil(t, clonedSlice)

	// line 1072: cyclic slice
	cyclic := make([]any, 1)
	cyclic[0] = cyclic
	_, err = cloneValue(cyclic)
	assert.ErrorContains(t, err, "unsupported cyclic value")

	// line 1080: slice with invalid item
	_, err = cloneValue([]any{map[int]int{1: 2}})
	assert.ErrorContains(t, err, "unsupported mutable map key type")

	// line 1090: array with invalid item
	_, err = cloneValue([1]any{map[int]int{1: 2}})
	assert.ErrorContains(t, err, "unsupported mutable map key type")
}
