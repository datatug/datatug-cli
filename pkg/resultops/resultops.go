// Package resultops applies deterministic local operations to captured result
// snapshots. It has no executor or network dependency; indexes in its output
// always refer to the original captured row order.
package resultops

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrNoGroupColumns = errors.New("grouping requires at least one column")

const (
	AggregateSum   = "SUM"
	AggregateCount = "COUNT"
	AggregateMin   = "MIN"
	AggregateMax   = "MAX"
	AggregateAvg   = "AVG"

	maxNumericDigits      = 1000
	maxNumericBits        = 3322
	maxDecimalValueDigits = maxNumericDigits * 2
	maxDecimalValueBits   = maxNumericBits * 2
	maxDecimalTextLength  = 2048
	maxDecimalExponent    = 1000
)

// Column is metadata supplied by the result's authoritative declared type.
type Column struct {
	Name string
	Type string
}

// Cell distinguishes a missing value from SQL/JSON null. A present cell with
// Value == nil is null; Present == false is unavailable/missing.
type Cell struct {
	Present bool
	Value   any
}

// Row is input data for Capture. Its captured identity is its original zero-
// based row index, which remains stable through grouping and sorting.
type Row struct {
	Cells []Cell
}

// Snapshot owns deep copies of its columns and cells. Its fields are private,
// and every result mapping points into this immutable captured row order.
type Snapshot struct {
	columns []Column
	rows    []Row
}

// Group is one first-occurrence ordered group. Keys retain missing/null/value
// distinctions and RowIndices map to the source snapshot, in original order.
// Count is the number of source rows in the group, independent of whether the
// caller requests COUNT(*) as an output column.
type Group struct {
	Keys       []Cell
	RowIndices []int
	Count      int
	Aggregates []Cell
}

// AggregateTerm requests one explicitly named aggregate over a captured
// column. Function is one of AggregateSum, AggregateCount, AggregateMin, or
// AggregateMax. AVG is intentionally unsupported because this package does
// not infer an output precision or rounding policy.
type AggregateTerm struct {
	Column   string
	Function string
	Alias    string
}

// SortKey declares one sort priority. Earlier entries have higher priority.
type SortKey struct {
	Column     string
	Descending bool
}

// Capture validates the row shape and deep-copies all data so later page loads
// or caller mutation cannot change the local operation's scope.
func Capture(columns []Column, rows []Row) (Snapshot, error) {
	copyColumns := append([]Column(nil), columns...)
	columnIndex := make(map[string]struct{}, len(copyColumns))
	for i, column := range copyColumns {
		if strings.TrimSpace(column.Name) == "" {
			return Snapshot{}, fmt.Errorf("column %d has an empty name", i)
		}
		if _, exists := columnIndex[column.Name]; exists {
			return Snapshot{}, fmt.Errorf("duplicate column %q", column.Name)
		}
		columnIndex[column.Name] = struct{}{}
	}
	copyRows := make([]Row, len(rows))
	for i, row := range rows {
		if len(row.Cells) != len(copyColumns) {
			return Snapshot{}, fmt.Errorf("row %d has %d cells; want %d", i, len(row.Cells), len(copyColumns))
		}
		copyRows[i].Cells = make([]Cell, len(row.Cells))
		for j, cell := range row.Cells {
			copyRows[i].Cells[j] = Cell{Present: cell.Present}
			if !cell.Present || cell.Value == nil {
				continue
			}
			cloned, err := cloneValue(cell.Value)
			if err != nil {
				return Snapshot{}, fmt.Errorf("row %d column %q: %w", i, copyColumns[j].Name, err)
			}
			copyRows[i].Cells[j].Value = cloned
		}
	}
	return Snapshot{columns: copyColumns, rows: copyRows}, nil
}

// Columns returns a copy of the captured metadata.
func (s Snapshot) Columns() []Column { return append([]Column(nil), s.columns...) }

// RowCount returns the number of rows in the captured loaded scope.
func (s Snapshot) RowCount() int { return len(s.rows) }

// OriginalOrder returns the mapping that restores the captured row order.
func (s Snapshot) OriginalOrder() []int {
	order := make([]int, len(s.rows))
	for i := range order {
		order[i] = i
	}
	return order
}

// GroupBy groups selected columns by typed ordered tuples. Group order follows
// first occurrence, and rows within each group preserve their original order.
func (s Snapshot) GroupBy(columnNames []string) ([]Group, error) {
	if len(columnNames) == 0 {
		return nil, ErrNoGroupColumns
	}
	indexes, types, err := s.resolveColumns(columnNames)
	if err != nil {
		return nil, err
	}
	groups := make([]Group, 0)
	lookup := make(map[string]int)
	for rowIndex, row := range s.rows {
		var key bytes.Buffer
		values := make([]Cell, len(indexes))
		for i, columnIndex := range indexes {
			column := s.columns[columnIndex]
			encoded, cellValue, err := encodeGroupCell(types[i], column.Type, row.Cells[columnIndex])
			if err != nil {
				return nil, fmt.Errorf("row %d column %q: %w", rowIndex, column.Name, err)
			}
			writeFramed(&key, []byte(normalizeTypeName(column.Type)))
			writeFramed(&key, encoded)
			cloned, err := cloneCell(cellValue)
			if err != nil {
				return nil, fmt.Errorf("row %d column %q: %w", rowIndex, column.Name, err)
			}
			values[i] = cloned
		}
		keyString := key.String()
		groupIndex, ok := lookup[keyString]
		if !ok {
			groupIndex = len(groups)
			lookup[keyString] = groupIndex
			groups = append(groups, Group{Keys: values})
		}
		groups[groupIndex].RowIndices = append(groups[groupIndex].RowIndices, rowIndex)
		groups[groupIndex].Count++
	}
	return groups, nil
}

// GroupByAggregates groups captured rows and computes only the explicitly
// requested aggregates. count marks whether the caller will emit the reserved
// COUNT(*) output; Group.Count always retains the actual row count.
// COUNT(column) ignores null values. Missing cells are refused because they do
// not have SQL aggregate semantics. Integer and decimal sums remain exact;
// floating sums use sequential IEEE-754 addition and reject non-finite results.
// Group order and RowIndices retain GroupBy's first-occurrence and source-order
// behavior.
func (s Snapshot) GroupByAggregates(columnNames []string, count bool, terms []AggregateTerm) ([]Group, error) {
	type resolvedTerm struct {
		column int
		kind   valueType
		fn     string
	}

	resolved := make([]resolvedTerm, len(terms))
	aliases := make(map[string]struct{}, len(terms))
	reserved := make(map[string]struct{}, len(columnNames)+1)
	for _, name := range columnNames {
		reserved[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	if count {
		reserved["count"] = struct{}{}
	}
	for i, term := range terms {
		alias := strings.TrimSpace(term.Alias)
		if alias == "" {
			return nil, fmt.Errorf("aggregate term %d has an empty alias", i)
		}
		aliasKey := strings.ToLower(alias)
		if _, exists := aliases[aliasKey]; exists {
			return nil, fmt.Errorf("duplicate aggregate alias %q", alias)
		}
		if _, exists := reserved[aliasKey]; exists {
			return nil, fmt.Errorf("aggregate alias %q collides with a group or COUNT(*) output", alias)
		}
		aliases[aliasKey] = struct{}{}

		function := strings.ToUpper(strings.TrimSpace(term.Function))
		switch function {
		case AggregateSum, AggregateCount, AggregateMin, AggregateMax:
		case AggregateAvg:
			return nil, fmt.Errorf("aggregate %s is unsupported: output precision and rounding are not defined", function)
		default:
			return nil, fmt.Errorf("unsupported aggregate function %q", term.Function)
		}
		columnIndex, ok := s.columnIndex(term.Column)
		if !ok {
			return nil, fmt.Errorf("unknown aggregate column %q", term.Column)
		}
		kind, err := declaredType(s.columns[columnIndex].Type)
		if err != nil {
			return nil, fmt.Errorf("aggregate column %q: %w", term.Column, err)
		}
		if function == AggregateSum && kind != typeInteger && kind != typeDecimal && kind != typeFloat {
			return nil, fmt.Errorf("aggregate %s requires a numeric column, got %q", function, s.columns[columnIndex].Type)
		}
		resolved[i] = resolvedTerm{column: columnIndex, kind: kind, fn: function}
	}

	groups, err := s.GroupBy(columnNames)
	if err != nil {
		return nil, err
	}
	for groupIndex := range groups {
		group := &groups[groupIndex]
		group.Aggregates = make([]Cell, len(resolved))
		for termIndex, term := range resolved {
			cell, err := s.aggregateGroup(group.RowIndices, term)
			if err != nil {
				return nil, fmt.Errorf("aggregate %s(%s) AS %s: %w", term.fn, terms[termIndex].Column, terms[termIndex].Alias, err)
			}
			group.Aggregates[termIndex] = cell
		}
	}
	return groups, nil
}

func (s Snapshot) aggregateGroup(rowIndexes []int, term struct {
	column int
	kind   valueType
	fn     string
}) (Cell, error) {
	var integerSum big.Int
	var decimalSum big.Rat
	var floatSum float64
	var selected normalizedCell
	var nonNull int

	for _, rowIndex := range rowIndexes {
		cell := s.rows[rowIndex].Cells[term.column]
		if !cell.Present {
			return Cell{}, fmt.Errorf("row %d is missing", rowIndex)
		}
		if cell.Value == nil {
			continue
		}
		value, err := normalizeValue(term.kind, cell.Value)
		if err != nil {
			return Cell{}, fmt.Errorf("row %d: %w", rowIndex, err)
		}
		nonNull++
		switch term.fn {
		case AggregateCount:
		case AggregateSum:
			switch term.kind {
			case typeInteger:
				integerSum.Add(&integerSum, value.(*big.Int))
			case typeDecimal:
				decimalSum.Add(&decimalSum, value.(*big.Rat))
			case typeFloat:
				floatSum += value.(float64)
				if math.IsInf(floatSum, 0) || math.IsNaN(floatSum) {
					return Cell{}, fmt.Errorf("row %d causes floating-point SUM overflow", rowIndex)
				}
			}
		case AggregateMin, AggregateMax:
			current := normalizedCell{state: 2, value: value}
			if nonNull == 1 || (term.fn == AggregateMin && compareNormalized(current, selected, term.kind) < 0) || (term.fn == AggregateMax && compareNormalized(current, selected, term.kind) > 0) {
				selected = current
			}
		}
	}

	switch term.fn {
	case AggregateCount:
		return Cell{Present: true, Value: nonNull}, nil
	case AggregateSum:
		if nonNull == 0 {
			return Cell{Present: true}, nil
		}
		switch term.kind {
		case typeInteger:
			return Cell{Present: true, Value: new(big.Int).Set(&integerSum)}, nil
		case typeDecimal:
			return Cell{Present: true, Value: new(big.Rat).Set(&decimalSum)}, nil
		case typeFloat:
			return Cell{Present: true, Value: floatSum}, nil
		}
	case AggregateMin, AggregateMax:
		if nonNull == 0 {
			return Cell{Present: true}, nil
		}
		cloned, err := cloneValueHook(selected.value)
		if err != nil {
			return Cell{}, err
		}
		return Cell{Present: true, Value: cloned}, nil
	}
	return Cell{}, fmt.Errorf("unsupported aggregate function %q", term.fn)
}

// Sort returns a stable display-to-original row-index mapping. Missing and
// null values precede ordinary values ascending; descending reverses each
// selected key's complete order. Equal keys retain original row order.
func (s Snapshot) Sort(keys []SortKey) ([]int, error) {
	indexes := make([]int, len(keys))
	types := make([]valueType, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for i, key := range keys {
		if _, exists := seen[key.Column]; exists {
			return nil, fmt.Errorf("sort column %q is selected more than once", key.Column)
		}
		seen[key.Column] = struct{}{}
		columnIndex, ok := s.columnIndex(key.Column)
		if !ok {
			return nil, fmt.Errorf("unknown sort column %q", key.Column)
		}
		kind, err := declaredType(s.columns[columnIndex].Type)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", key.Column, err)
		}
		indexes[i], types[i] = columnIndex, kind
	}
	values := make([][]normalizedCell, len(keys))
	for keyIndex, columnIndex := range indexes {
		values[keyIndex] = make([]normalizedCell, len(s.rows))
		for rowIndex, row := range s.rows {
			normalized, err := normalizeCell(types[keyIndex], row.Cells[columnIndex])
			if err != nil {
				return nil, fmt.Errorf("row %d column %q: %w", rowIndex, s.columns[columnIndex].Name, err)
			}
			values[keyIndex][rowIndex] = normalized
		}
	}
	order := s.OriginalOrder()
	sort.SliceStable(order, func(i, j int) bool {
		left, right := order[i], order[j]
		for keyIndex, key := range keys {
			cmp := compareNormalized(values[keyIndex][left], values[keyIndex][right], types[keyIndex])
			if cmp == 0 {
				continue
			}
			if key.Descending {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	return order, nil
}

func (s Snapshot) resolveColumns(names []string) ([]int, []valueType, error) {
	indexes := make([]int, len(names))
	types := make([]valueType, len(names))
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		if _, exists := seen[name]; exists {
			return nil, nil, fmt.Errorf("group column %q is selected more than once", name)
		}
		seen[name] = struct{}{}
		index, ok := s.columnIndex(name)
		if !ok {
			return nil, nil, fmt.Errorf("unknown group column %q", name)
		}
		kind, err := declaredType(s.columns[index].Type)
		if err != nil {
			return nil, nil, fmt.Errorf("column %q: %w", name, err)
		}
		indexes[i], types[i] = index, kind
	}
	return indexes, types, nil
}

func (s Snapshot) columnIndex(name string) (int, bool) {
	for i, column := range s.columns {
		if column.Name == name {
			return i, true
		}
	}
	return 0, false
}

type valueType uint8

const (
	typeText valueType = iota + 1
	typeInteger
	typeDecimal
	typeFloat
	typeBoolean
	typeTemporal
	typeBinary
)

func normalizeTypeName(raw string) string {
	return strings.ToUpper(strings.Join(strings.Fields(strings.TrimSpace(raw)), " "))
}

func declaredType(raw string) (valueType, error) {
	typ := normalizeTypeName(raw)
	base, args, err := parseDeclaredType(typ)
	if err != nil {
		return 0, fmt.Errorf("unsupported declared type %q: %w", raw, err)
	}
	parameterized := len(args) > 0
	switch base {
	case "TEXT", "STRING", "CHAR", "CHARACTER", "VARCHAR", "NVARCHAR", "NCHAR", "CLOB", "UUID":
		if parameterized && !isLengthType(base) {
			return 0, fmt.Errorf("type %s does not accept parameters", base)
		}
		if parameterized && (len(args) != 1 || args[0] < 1 || args[0] > 1_000_000) {
			return 0, fmt.Errorf("invalid length parameters for %s", base)
		}
		return typeText, nil
	case "INT", "INTEGER", "INT2", "INT4", "INT8", "SMALLINT", "BIGINT", "TINYINT", "MEDIUMINT", "SERIAL", "BIGSERIAL", "UNSIGNED":
		if parameterized {
			return 0, fmt.Errorf("type %s does not accept parameters", base)
		}
		return typeInteger, nil
	case "DECIMAL", "NUMERIC", "NUMBER":
		if parameterized && !validPrecisionScale(args) {
			return 0, fmt.Errorf("invalid precision/scale parameters for %s", base)
		}
		return typeDecimal, nil
	case "MONEY":
		if parameterized {
			return 0, fmt.Errorf("type %s does not accept parameters", base)
		}
		return typeDecimal, nil
	case "REAL", "FLOAT", "FLOAT4", "FLOAT8", "DOUBLE", "DOUBLE PRECISION":
		if parameterized && (base != "FLOAT" || len(args) != 1 || args[0] < 1 || args[0] > 53) {
			return 0, fmt.Errorf("invalid floating precision parameters for %s", base)
		}
		return typeFloat, nil
	case "BOOL", "BOOLEAN":
		if parameterized {
			return 0, fmt.Errorf("type %s does not accept parameters", base)
		}
		return typeBoolean, nil
	case "DATE", "TIME", "DATETIME", "TIMESTAMP", "TIMESTAMPTZ", "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITHOUT TIME ZONE":
		if parameterized {
			if (base != "TIME" && base != "DATETIME" && base != "TIMESTAMP" && base != "TIMESTAMP WITH TIME ZONE" && base != "TIMESTAMP WITHOUT TIME ZONE") || len(args) != 1 || args[0] < 0 || args[0] > 9 {
				return 0, fmt.Errorf("invalid temporal precision parameters for %s", base)
			}
		}
		return typeTemporal, nil
	case "BINARY", "VARBINARY", "BLOB", "BYTEA", "IMAGE":
		if parameterized && !isLengthType(base) {
			return 0, fmt.Errorf("type %s does not accept parameters", base)
		}
		if parameterized && (len(args) != 1 || args[0] < 1 || args[0] > 1_000_000) {
			return 0, fmt.Errorf("invalid length parameters for %s", base)
		}
		return typeBinary, nil
	default:
		return 0, fmt.Errorf("unknown base type %q", base)
	}
}

func isLengthType(base string) bool {
	switch base {
	case "CHAR", "CHARACTER", "VARCHAR", "NVARCHAR", "NCHAR", "BINARY", "VARBINARY":
		return true
	default:
		return false
	}
}

func validPrecisionScale(args []int64) bool {
	if len(args) != 1 && len(args) != 2 {
		return false
	}
	if args[0] < 1 || args[0] > 1000 {
		return false
	}
	return len(args) == 1 || (args[1] >= 0 && args[1] <= args[0])
}

// parseDeclaredType accepts only a base type and an explicitly supported,
// fully closed parameter list. It rejects suffixes such as SQL array markers
// instead of silently classifying their scalar prefix.
func parseDeclaredType(typ string) (string, []int64, error) {
	if typ == "" || strings.ContainsAny(typ, "[]") {
		return "", nil, fmt.Errorf("malformed or unsupported suffix")
	}
	open := strings.IndexByte(typ, '(')
	if open < 0 {
		return typ, nil, nil
	}
	closeRelative := strings.IndexByte(typ[open+1:], ')')
	if closeRelative < 0 {
		return "", nil, fmt.Errorf("unterminated type parameters")
	}
	closeAt := open + 1 + closeRelative
	if strings.ContainsAny(typ[open+1:closeAt], "()") || strings.ContainsAny(typ[closeAt+1:], "()") {
		return "", nil, fmt.Errorf("malformed type parameters")
	}
	prefix := strings.TrimSpace(typ[:open])
	params := strings.TrimSpace(typ[open+1 : closeAt])
	suffix := strings.TrimSpace(typ[closeAt+1:])
	if prefix == "" {
		return "", nil, fmt.Errorf("missing base type")
	}
	base := prefix
	if suffix != "" {
		if suffix != "WITH TIME ZONE" && suffix != "WITHOUT TIME ZONE" {
			return "", nil, fmt.Errorf("unsupported suffix %q", suffix)
		}
		base += " " + suffix
	}
	parts := strings.Split(params, ",")
	if params == "" {
		return "", nil, fmt.Errorf("empty type parameters")
	}
	args := make([]int64, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", nil, fmt.Errorf("empty type parameter")
		}
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("invalid type parameter %q", part)
		}
		args[i] = value
	}
	return base, args, nil
}

var encodeNormalizedCell = func(n normalizedCell, kind valueType) ([]byte, error) {
	return n.encoding(kind)
}

func encodeGroupCell(kind valueType, declared string, cell Cell) ([]byte, Cell, error) {
	var out []byte
	if !cell.Present {
		out = []byte{0}
		return out, Cell{}, nil
	}
	if cell.Value == nil {
		out = []byte{1}
		return out, Cell{Present: true}, nil
	}
	normalized, err := normalizeCell(kind, cell)
	if err != nil {
		return nil, Cell{}, fmt.Errorf("declared %s value: %w", declared, err)
	}
	payload, err := encodeNormalizedCell(normalized, kind)
	if err != nil {
		return nil, Cell{}, err
	}
	out = append([]byte{2}, payload...)
	return out, cell, nil
}

func writeFramed(dst *bytes.Buffer, value []byte) {
	var length [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(length[:], uint64(len(value)))
	dst.Write(length[:n])
	dst.Write(value)
}

type normalizedCell struct {
	state uint8 // 0 missing, 1 null, 2 value
	value any
}

func normalizeCell(kind valueType, cell Cell) (normalizedCell, error) {
	if !cell.Present {
		return normalizedCell{state: 0}, nil
	}
	if cell.Value == nil {
		return normalizedCell{state: 1}, nil
	}
	value, err := normalizeValue(kind, cell.Value)
	if err != nil {
		return normalizedCell{}, err
	}
	return normalizedCell{state: 2, value: value}, nil
}

func normalizeValue(kind valueType, value any) (any, error) {
	switch kind {
	case typeText:
		v, ok := value.(string)
		if !ok || !utf8.ValidString(v) {
			return nil, fmt.Errorf("want valid UTF-8 text, got %T", value)
		}
		return v, nil
	case typeInteger:
		return exactInteger(value)
	case typeDecimal:
		return exactDecimal(value)
	case typeFloat:
		v, ok := numericFloat(value)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("want a finite floating-point value, got %T", value)
		}
		if v == 0 {
			v = 0 // Group negative and positive zero by numeric equality.
		}
		return v, nil
	case typeBoolean:
		v, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("want boolean, got %T", value)
		}
		return v, nil
	case typeTemporal:
		v, ok := value.(time.Time)
		if !ok {
			return nil, fmt.Errorf("want time.Time, got %T", value)
		}
		return v.Round(0).UTC(), nil
	case typeBinary:
		v, ok := value.([]byte)
		if !ok {
			return nil, fmt.Errorf("want []byte, got %T", value)
		}
		return append([]byte(nil), v...), nil
	default:
		return nil, fmt.Errorf("unsupported value type %d", kind)
	}
}

func exactInteger(value any) (*big.Int, error) {
	switch v := value.(type) {
	case int:
		return big.NewInt(int64(v)), nil
	case int8:
		return big.NewInt(int64(v)), nil
	case int16:
		return big.NewInt(int64(v)), nil
	case int32:
		return big.NewInt(int64(v)), nil
	case int64:
		return big.NewInt(v), nil
	case uint:
		return new(big.Int).SetUint64(uint64(v)), nil
	case uint8:
		return new(big.Int).SetUint64(uint64(v)), nil
	case uint16:
		return new(big.Int).SetUint64(uint64(v)), nil
	case uint32:
		return new(big.Int).SetUint64(uint64(v)), nil
	case uint64:
		return new(big.Int).SetUint64(v), nil
	case json.Number:
		return parseInteger(v.String())
	case big.Int:
		if err := validateBigIntBudget(&v); err != nil {
			return nil, err
		}
		return new(big.Int).Set(&v), nil
	case *big.Int:
		if v != nil {
			if err := validateBigIntBudget(v); err != nil {
				return nil, err
			}
			return new(big.Int).Set(v), nil
		}
	}
	return nil, fmt.Errorf("want exact integer, got %T", value)
}

func parseInteger(text string) (*big.Int, error) {
	if text == "" || strings.TrimSpace(text) != text {
		return nil, fmt.Errorf("invalid integer %q", text)
	}
	digits := len(text)
	if text[0] == '+' || text[0] == '-' {
		digits--
	}
	if digits > maxNumericDigits {
		return nil, fmt.Errorf("integer exceeds numeric precision budget of %d digits", maxNumericDigits)
	}
	v, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return nil, fmt.Errorf("invalid integer %q", text)
	}
	return v, nil
}

func exactDecimal(value any) (*big.Rat, error) {
	switch v := value.(type) {
	case json.Number:
		return parseDecimal(v.String())
	case big.Rat:
		if err := validateBigRatBudget(&v); err != nil {
			return nil, err
		}
		return new(big.Rat).Set(&v), nil
	case *big.Rat:
		if v != nil {
			if err := validateBigRatBudget(v); err != nil {
				return nil, err
			}
			return new(big.Rat).Set(v), nil
		}
	case big.Int:
		if err := validateBigIntBudget(&v); err != nil {
			return nil, err
		}
		return new(big.Rat).SetInt(&v), nil
	case *big.Int:
		if v != nil {
			if err := validateBigIntBudget(v); err != nil {
				return nil, err
			}
			return new(big.Rat).SetInt(v), nil
		}
	}
	if integer, err := exactInteger(value); err == nil {
		return new(big.Rat).SetInt(integer), nil
	}
	return nil, fmt.Errorf("want exact decimal value, got %T", value)
}

var ratSetString = func(r *big.Rat, s string) (*big.Rat, bool) {
	return r.SetString(s)
}

func parseDecimal(text string) (*big.Rat, error) {
	if err := validateDecimalBudget(text); err != nil {
		return nil, err
	}
	v, ok := ratSetString(new(big.Rat), text)
	if !ok {
		return nil, fmt.Errorf("invalid decimal %q", text)
	}
	return v, nil
}

// validateDecimalBudget checks syntax and resource bounds before big.Rat parses
// an exponent, which can otherwise request memory proportional to its value.
func validateDecimalBudget(text string) error {
	if len(text) == 0 || strings.TrimSpace(text) != text {
		return fmt.Errorf("invalid decimal")
	}
	if len(text) > maxDecimalTextLength {
		return fmt.Errorf("decimal exceeds numeric precision budget of %d input bytes", maxDecimalTextLength)
	}
	i := 0
	if text[i] == '+' || text[i] == '-' {
		i++
		if i == len(text) {
			return fmt.Errorf("invalid decimal")
		}
	}
	digits := 0
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		digits++
		i++
	}
	if i < len(text) && text[i] == '.' {
		i++
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			digits++
			i++
		}
	}
	if digits == 0 {
		return fmt.Errorf("invalid decimal")
	}
	if digits > maxNumericDigits {
		return fmt.Errorf("decimal exceeds numeric precision budget of %d digits", maxNumericDigits)
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		start := i
		exponent := 0
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			digit := int(text[i] - '0')
			if exponent <= maxDecimalExponent {
				exponent = exponent*10 + digit
			}
			i++
		}
		if i == start {
			return fmt.Errorf("invalid decimal exponent")
		}
		if exponent > maxDecimalExponent {
			return fmt.Errorf("decimal exponent exceeds numeric precision budget of %d", maxDecimalExponent)
		}
	}
	if i != len(text) {
		return fmt.Errorf("invalid decimal")
	}
	return nil
}

func validateBigIntBudget(value *big.Int) error {
	return validateBigIntLimit(value, maxNumericDigits, maxNumericBits, "integer")
}

func validateBigIntLimit(value *big.Int, maxDigits, maxBits int, label string) error {
	if value == nil {
		return nil
	}
	// The bit check rejects huge values before converting to decimal text. The
	// final decimal digit check handles values near the configured boundary.
	if value.BitLen() > maxBits {
		return fmt.Errorf("%s exceeds numeric precision budget of %d digits", label, maxDigits)
	}
	digits := len(value.String())
	if value.Sign() < 0 {
		digits--
	}
	if digits > maxDigits {
		return fmt.Errorf("%s exceeds numeric precision budget of %d digits", label, maxDigits)
	}
	return nil
}

func validateBigRatBudget(value *big.Rat) error {
	if value == nil {
		return nil
	}
	if err := validateBigIntLimit(value.Num(), maxDecimalValueDigits, maxDecimalValueBits, "decimal numerator"); err != nil {
		return fmt.Errorf("decimal numerator: %w", err)
	}
	if err := validateBigIntLimit(value.Denom(), maxDecimalValueDigits, maxDecimalValueBits, "decimal denominator"); err != nil {
		return fmt.Errorf("decimal denominator: %w", err)
	}
	return nil
}

func numericFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float32:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		text := v.String()
		if err := validateDecimalBudget(text); err != nil {
			return 0, false
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || math.IsInf(f, 0) {
			return 0, false
		}
		// json.Number preserves the original decimal text. Reject a conversion if
		// float64 cannot represent that exact rational value; otherwise distinct
		// source numbers can silently collapse to one ordering/group key.
		decimal, err := parseDecimal(text)
		if err != nil {
			return 0, false
		}
		binaryValue := new(big.Rat).SetFloat64(f)
		return f, binaryValue != nil && binaryValue.Cmp(decimal) == 0
	}
	return 0, false
}

func (n normalizedCell) encoding(kind valueType) ([]byte, error) {
	switch kind {
	case typeText:
		return []byte(n.value.(string)), nil
	case typeInteger:
		return []byte(n.value.(*big.Int).String()), nil
	case typeDecimal:
		return []byte(n.value.(*big.Rat).RatString()), nil
	case typeFloat:
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], math.Float64bits(n.value.(float64)))
		return encoded[:], nil
	case typeBoolean:
		if n.value.(bool) {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case typeTemporal:
		v := n.value.(time.Time)
		var encoded [12]byte
		binary.BigEndian.PutUint64(encoded[:8], uint64(v.Unix())^(uint64(1)<<63))
		binary.BigEndian.PutUint32(encoded[8:], uint32(v.Nanosecond()))
		return encoded[:], nil
	case typeBinary:
		return n.value.([]byte), nil
	default:
		return nil, fmt.Errorf("unsupported value type %d", kind)
	}
}

func compareNormalized(left, right normalizedCell, kind valueType) int {
	if left.state != right.state {
		if left.state < right.state {
			return -1
		}
		return 1
	}
	if left.state != 2 {
		return 0
	}
	switch kind {
	case typeText:
		return compareRunes(left.value.(string), right.value.(string))
	case typeInteger:
		return left.value.(*big.Int).Cmp(right.value.(*big.Int))
	case typeDecimal:
		return left.value.(*big.Rat).Cmp(right.value.(*big.Rat))
	case typeFloat:
		l, r := left.value.(float64), right.value.(float64)
		if l < r {
			return -1
		}
		if l > r {
			return 1
		}
		return 0
	case typeBoolean:
		l, r := left.value.(bool), right.value.(bool)
		if l == r {
			return 0
		}
		if !l {
			return -1
		}
		return 1
	case typeTemporal:
		l, r := left.value.(time.Time), right.value.(time.Time)
		if l.Before(r) {
			return -1
		}
		if l.After(r) {
			return 1
		}
		return 0
	case typeBinary:
		return bytes.Compare(left.value.([]byte), right.value.([]byte))
	default:
		return 0
	}
}

func compareRunes(left, right string) int {
	li, ri := 0, 0
	for li < len(left) && ri < len(right) {
		lr, lsize := utf8.DecodeRuneInString(left[li:])
		rr, rsize := utf8.DecodeRuneInString(right[ri:])
		if lr < rr {
			return -1
		}
		if lr > rr {
			return 1
		}
		li += lsize
		ri += rsize
	}
	if li == len(left) && ri == len(right) {
		return 0
	}
	if li == len(left) {
		return -1
	}
	return 1
}

var cloneValueHook = func(value any) (any, error) {
	return cloneValue(value)
}

func cloneCell(cell Cell) (Cell, error) {
	copy := Cell{Present: cell.Present}
	if !cell.Present || cell.Value == nil {
		return copy, nil
	}
	value, err := cloneValueHook(cell.Value)
	copy.Value = value
	return copy, err
}

func cloneValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case time.Time:
		return v.Round(0), nil
	case json.Number, string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return v, nil
	case []byte:
		return append([]byte(nil), v...), nil
	case big.Int:
		if err := validateBigIntBudget(&v); err != nil {
			return nil, err
		}
		return *new(big.Int).Set(&v), nil
	case *big.Int:
		if v == nil {
			return (*big.Int)(nil), nil
		}
		if err := validateBigIntBudget(v); err != nil {
			return nil, err
		}
		return new(big.Int).Set(v), nil
	case big.Rat:
		if err := validateBigRatBudget(&v); err != nil {
			return nil, err
		}
		return *new(big.Rat).Set(&v), nil
	case *big.Rat:
		if v == nil {
			return (*big.Rat)(nil), nil
		}
		if err := validateBigRatBudget(v); err != nil {
			return nil, err
		}
		return new(big.Rat).Set(v), nil
	}
	cloned, err := cloneReflect(reflect.ValueOf(value), make(map[cloneVisit]bool))
	if err != nil {
		return nil, err
	}
	return cloned.Interface(), nil
}

type cloneVisit struct {
	typ reflect.Type
	ptr uintptr
}

func cloneReflect(value reflect.Value, seen map[cloneVisit]bool) (reflect.Value, error) {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		inner, err := cloneReflect(value.Elem(), seen)
		if err != nil {
			return reflect.Value{}, err
		}
		copy := reflect.New(value.Type()).Elem()
		copy.Set(inner)
		return copy, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		visit := cloneVisit{typ: value.Type(), ptr: value.Pointer()}
		if seen[visit] {
			return reflect.Value{}, fmt.Errorf("unsupported cyclic value %s", value.Type())
		}
		seen[visit] = true
		defer delete(seen, visit)
		copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			item, err := cloneReflect(value.Index(i), seen)
			if err != nil {
				return reflect.Value{}, err
			}
			copy.Index(i).Set(item)
		}
		return copy, nil
	case reflect.Array:
		copy := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			item, err := cloneReflect(value.Index(i), seen)
			if err != nil {
				return reflect.Value{}, err
			}
			copy.Index(i).Set(item)
		}
		return copy, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("unsupported mutable map key type %s", value.Type().Key())
		}
		visit := cloneVisit{typ: value.Type(), ptr: value.Pointer()}
		if seen[visit] {
			return reflect.Value{}, fmt.Errorf("unsupported cyclic value %s", value.Type())
		}
		seen[visit] = true
		defer delete(seen, visit)
		copy := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			item, err := cloneReflect(iter.Value(), seen)
			if err != nil {
				return reflect.Value{}, err
			}
			copy.SetMapIndex(iter.Key(), item)
		}
		return copy, nil
	case reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Func:
		return reflect.Value{}, fmt.Errorf("unsupported mutable value %T", value.Interface())
	case reflect.Struct:
		return reflect.Value{}, fmt.Errorf("unsupported struct value %T", value.Interface())
	default:
		return value, nil
	}
}
