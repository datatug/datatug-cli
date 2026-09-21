package secureread

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/datatug/datatug-core/pkg/apicontract"
)

// The caps keep result analysis bounded. An incomplete flag always accompanies
// a retained lower bound, so callers never mistake it for an exact result.
const (
	MaxStatisticDistinctValues = 256
	// 2,048 covers ordinary Chat result limits (including a full Chinook
	// invoice history) while still preventing an unbounded time-series map.
	MaxStatisticDateBuckets      = 2048
	MaxStatisticDateNumericPairs = 64
)

// ValueKind records the source value family observed by the analysis layer.
// It intentionally does not depend on a renderer's data model.
type ValueKind string

const (
	ValueKindNull     ValueKind = "null"
	ValueKindString   ValueKind = "string"
	ValueKindBoolean  ValueKind = "boolean"
	ValueKindNumber   ValueKind = "number"
	ValueKindDecimal  ValueKind = "decimal"
	ValueKindDate     ValueKind = "date"
	ValueKindDatetime ValueKind = "datetime"
	ValueKindOther    ValueKind = "other"
)

// RecordSetStatistics is the renderer-independent, immutable analysis of a
// Result. Slices are sorted deterministically before being exposed or stored.
type RecordSetStatistics struct {
	RowCount                  int                `json:"rowCount"`
	Columns                   []ColumnStatistics `json:"columns"`
	DateNumericSums           []DateNumericSum   `json:"dateNumericSums,omitempty"`
	DateNumericSumsIncomplete bool               `json:"dateNumericSumsIncomplete,omitempty"`
}

type ColumnStatistics struct {
	Name                  string           `json:"name"`
	NullCount             int              `json:"nullCount"`
	NonNullCount          int              `json:"nonNullCount"`
	Cardinality           int              `json:"cardinality"`
	CardinalityIncomplete bool             `json:"cardinalityIncomplete,omitempty"`
	Frequencies           []ValueFrequency `json:"frequencies,omitempty"`
	FrequenciesIncomplete bool             `json:"frequenciesIncomplete,omitempty"`
	Types                 TypeObservations `json:"types"`
	DateBuckets           []DateBucket     `json:"dateBuckets,omitempty"`
	DateBucketsIncomplete bool             `json:"dateBucketsIncomplete,omitempty"`
}

type ValueFrequency struct {
	Label string    `json:"label"`
	Type  ValueKind `json:"type"`
	Count int       `json:"count"`
}

type TypeObservations struct {
	Null     int `json:"null"`
	String   int `json:"string"`
	Boolean  int `json:"boolean"`
	Number   int `json:"number"`
	Decimal  int `json:"decimal"`
	Date     int `json:"date"`
	Datetime int `json:"datetime"`
	Other    int `json:"other"`
}

type DateBucket struct {
	Bucket string `json:"bucket"`
	Count  int    `json:"count"`
}

type DateNumericSum struct {
	DateColumn    string              `json:"dateColumn"`
	NumericColumn string              `json:"numericColumn"`
	Buckets       []DateNumericBucket `json:"buckets,omitempty"`
	Incomplete    bool                `json:"incomplete,omitempty"`
}

type DateNumericBucket struct {
	Bucket string  `json:"bucket"`
	Sum    float64 `json:"sum"`
	Count  int     `json:"count"`
}

// StatisticsForRows derives statistics from already materialized rows. It is
// used only for legacy result payloads; live reads update the same accumulator
// as they load records.
func StatisticsForRows(columns []string, rows []Row) RecordSetStatistics {
	collector := NewStatisticsCollector()
	for _, row := range rows {
		collector.AddRow(row)
	}
	return collector.Finalize(columns)
}

// StatisticsCollector lets a codec or reader update analysis as each row is
// already materialized, avoiding a separate chart-only row pass.
type StatisticsCollector struct {
	accumulator *statisticsAccumulator
}

func NewStatisticsCollector() *StatisticsCollector {
	return &StatisticsCollector{accumulator: newStatisticsAccumulator()}
}

func (c *StatisticsCollector) AddRow(row Row) {
	c.accumulator.addRow(row.Data)
}

func (c *StatisticsCollector) Finalize(columns []string) RecordSetStatistics {
	return c.accumulator.finalize(columns)
}

// StatisticsFromRecordset preserves apicontract's tagged source types. It is
// deliberately used after RunSnapshot's SQLite policy replay instead of
// inferring date, decimal, or boolean semantics from SQLite driver values.
func StatisticsFromRecordset(recordset apicontract.Recordset) RecordSetStatistics {
	columns := make([]string, len(recordset.Columns))
	for i, column := range recordset.Columns {
		columns[i] = column.Name
	}
	a := newStatisticsAccumulator()
	for _, row := range recordset.Rows {
		values := make(map[string]observedValue, len(columns))
		for i, column := range columns {
			if i < len(row) {
				values[column] = observedFromTypedValue(row[i])
			}
		}
		a.addObservedRow(values)
	}
	return a.finalize(columns)
}

type observedValue struct {
	kind    ValueKind
	label   string
	date    string
	numeric float64
	isNum   bool
}

func observedFromValue(value any) observedValue {
	if value == nil {
		return observedValue{kind: ValueKindNull}
	}
	switch v := value.(type) {
	case time.Time:
		return observedValue{kind: ValueKindDatetime, label: v.UTC().Format(time.RFC3339Nano), date: v.UTC().Format("2006-01-02")}
	case string:
		if date, err := time.Parse("2006-01-02", v); err == nil && date.Format("2006-01-02") == v {
			return observedValue{kind: ValueKindDate, label: v, date: v}
		}
		if datetime, err := time.Parse(time.RFC3339, v); err == nil {
			return observedValue{kind: ValueKindDatetime, label: v, date: datetime.UTC().Format("2006-01-02")}
		}
		return observedValue{kind: ValueKindString, label: v}
	case bool:
		return observedValue{kind: ValueKindBoolean, label: strconv.FormatBool(v)}
	case json.Number:
		if n, err := v.Float64(); err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) {
			return observedValue{kind: ValueKindNumber, label: v.String(), numeric: n, isNum: true}
		}
		return observedValue{kind: ValueKindOther, label: v.String()}
	}
	if n, ok := numberValue(value); ok {
		return observedValue{kind: ValueKindNumber, label: strconv.FormatFloat(n, 'g', -1, 64), numeric: n, isNum: true}
	}
	return observedValue{kind: ValueKindOther, label: fmt.Sprint(value)}
}

func observedFromTypedValue(value apicontract.TypedValue) observedValue {
	switch value.Type {
	case apicontract.ValueTypeNull:
		return observedValue{kind: ValueKindNull}
	case apicontract.ValueTypeString:
		return observedValue{kind: ValueKindString, label: value.Str}
	case apicontract.ValueTypeBoolean:
		return observedValue{kind: ValueKindBoolean, label: strconv.FormatBool(value.Bool)}
	case apicontract.ValueTypeNumber:
		return observedValue{kind: ValueKindNumber, label: strconv.FormatFloat(value.Num, 'g', -1, 64), numeric: value.Num, isNum: true}
	case apicontract.ValueTypeInteger:
		n, err := strconv.ParseFloat(value.Str, 64)
		return observedValue{kind: ValueKindNumber, label: value.Str, numeric: n, isNum: err == nil}
	case apicontract.ValueTypeDecimal:
		n, err := strconv.ParseFloat(value.Str, 64)
		return observedValue{kind: ValueKindDecimal, label: value.Str, numeric: n, isNum: err == nil}
	case apicontract.ValueTypeDate:
		return observedValue{kind: ValueKindDate, label: value.Str, date: value.Str}
	case apicontract.ValueTypeDatetime:
		parsed, err := time.Parse(time.RFC3339, value.Str)
		if err != nil {
			return observedValue{kind: ValueKindDatetime, label: value.Str}
		}
		return observedValue{kind: ValueKindDatetime, label: value.Str, date: parsed.UTC().Format("2006-01-02")}
	default:
		return observedValue{kind: ValueKindOther}
	}
}

func numberValue(value any) (float64, bool) {
	var n float64
	switch v := value.(type) {
	case int:
		n = float64(v)
	case int8:
		n = float64(v)
	case int16:
		n = float64(v)
	case int32:
		n = float64(v)
	case int64:
		n = float64(v)
	case uint:
		n = float64(v)
	case uint8:
		n = float64(v)
	case uint16:
		n = float64(v)
	case uint32:
		n = float64(v)
	case uint64:
		n = float64(v)
	case float32:
		n = float64(v)
	case float64:
		n = v
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}

type columnAccumulator struct {
	nonNull    int
	types      TypeObservations
	values     map[string]*ValueFrequency
	incomplete bool
	buckets    map[string]int
	bucketFull bool
}

type pairAccumulator struct {
	dateColumn    string
	numericColumn string
	buckets       map[string]DateNumericBucket
	incomplete    bool
}

type statisticsAccumulator struct {
	rows            int
	columns         map[string]*columnAccumulator
	pairs           map[string]*pairAccumulator
	pairsIncomplete bool
}

func newStatisticsAccumulator() *statisticsAccumulator {
	return &statisticsAccumulator{columns: make(map[string]*columnAccumulator), pairs: make(map[string]*pairAccumulator)}
}

func (a *statisticsAccumulator) addRow(values map[string]any) {
	observed := make(map[string]observedValue, len(values))
	for name, value := range values {
		observed[name] = observedFromValue(value)
	}
	a.addObservedRow(observed)
}

func (a *statisticsAccumulator) addObservedRow(values map[string]observedValue) {
	a.rows++
	dates := make(map[string]string)
	numbers := make(map[string]observedValue)
	for name, value := range values {
		column := a.column(name)
		column.add(value)
		if value.date != "" {
			dates[name] = value.date
		}
		if value.isNum {
			numbers[name] = value
		}
	}
	// Retain the same first pairs when the bounded aggregate cap is reached.
	// Iterating the maps directly would make candidate availability depend on
	// Go's randomized map order for wide results.
	dateColumns := make([]string, 0, len(dates))
	for name := range dates {
		dateColumns = append(dateColumns, name)
	}
	sort.Strings(dateColumns)
	numericColumns := make([]string, 0, len(numbers))
	for name := range numbers {
		numericColumns = append(numericColumns, name)
	}
	sort.Strings(numericColumns)
	for _, dateColumn := range dateColumns {
		for _, numericColumn := range numericColumns {
			a.addPair(dateColumn, numericColumn, dates[dateColumn], numbers[numericColumn].numeric)
		}
	}
}

func (a *statisticsAccumulator) column(name string) *columnAccumulator {
	column := a.columns[name]
	if column == nil {
		column = &columnAccumulator{values: make(map[string]*ValueFrequency), buckets: make(map[string]int)}
		a.columns[name] = column
	}
	return column
}

func (column *columnAccumulator) add(value observedValue) {
	column.types.add(value.kind)
	if value.kind == ValueKindNull {
		return
	}
	column.nonNull++
	key := string(value.kind) + "\x00" + value.label
	frequency := column.values[key]
	if frequency == nil {
		if len(column.values) >= MaxStatisticDistinctValues {
			column.incomplete = true
		} else {
			frequency = &ValueFrequency{Label: value.label, Type: value.kind}
			column.values[key] = frequency
		}
	}
	if frequency != nil {
		frequency.Count++
	}
	if value.date != "" {
		if _, exists := column.buckets[value.date]; !exists && len(column.buckets) >= MaxStatisticDateBuckets {
			column.bucketFull = true
			return
		}
		column.buckets[value.date]++
	}
}

func (a *statisticsAccumulator) addPair(dateColumn, numericColumn, bucket string, value float64) {
	key := dateColumn + "\x00" + numericColumn
	pair := a.pairs[key]
	if pair == nil {
		if len(a.pairs) >= MaxStatisticDateNumericPairs {
			a.pairsIncomplete = true
			return
		}
		pair = &pairAccumulator{dateColumn: dateColumn, numericColumn: numericColumn, buckets: make(map[string]DateNumericBucket)}
		a.pairs[key] = pair
	}
	entry, exists := pair.buckets[bucket]
	if !exists && len(pair.buckets) >= MaxStatisticDateBuckets {
		pair.incomplete = true
		return
	}
	entry.Bucket = bucket
	entry.Sum += value
	entry.Count++
	pair.buckets[bucket] = entry
}

func (a *statisticsAccumulator) finalize(columns []string) RecordSetStatistics {
	stats := RecordSetStatistics{RowCount: a.rows, Columns: make([]ColumnStatistics, 0, len(columns)), DateNumericSumsIncomplete: a.pairsIncomplete}
	seen := make(map[string]bool, len(columns))
	for _, name := range columns {
		seen[name] = true
		stats.Columns = append(stats.Columns, a.finalizeColumn(name))
	}
	var extra []string
	for name := range a.columns {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		stats.Columns = append(stats.Columns, a.finalizeColumn(name))
	}
	for _, pair := range a.pairs {
		buckets := make([]DateNumericBucket, 0, len(pair.buckets))
		for _, bucket := range pair.buckets {
			buckets = append(buckets, bucket)
		}
		sort.Slice(buckets, func(i, j int) bool { return buckets[i].Bucket < buckets[j].Bucket })
		stats.DateNumericSums = append(stats.DateNumericSums, DateNumericSum{DateColumn: pair.dateColumn, NumericColumn: pair.numericColumn, Buckets: buckets, Incomplete: pair.incomplete})
	}
	sort.Slice(stats.DateNumericSums, func(i, j int) bool {
		if stats.DateNumericSums[i].DateColumn == stats.DateNumericSums[j].DateColumn {
			return stats.DateNumericSums[i].NumericColumn < stats.DateNumericSums[j].NumericColumn
		}
		return stats.DateNumericSums[i].DateColumn < stats.DateNumericSums[j].DateColumn
	})
	return stats
}

func (a *statisticsAccumulator) finalizeColumn(name string) ColumnStatistics {
	column := a.columns[name]
	if column == nil {
		if a.rows == 0 {
			return ColumnStatistics{Name: name}
		}
		return ColumnStatistics{
			Name:        name,
			NullCount:   a.rows,
			Cardinality: 1,
			Frequencies: []ValueFrequency{{Label: "(null)", Type: ValueKindNull, Count: a.rows}},
			Types:       TypeObservations{Null: a.rows},
		}
	}
	nulls := a.rows - column.nonNull
	var frequencies []ValueFrequency
	if nulls > 0 {
		frequencies = append(frequencies, ValueFrequency{Label: "(null)", Type: ValueKindNull, Count: nulls})
	}
	for _, frequency := range column.values {
		frequencies = append(frequencies, *frequency)
	}
	sort.Slice(frequencies, func(i, j int) bool {
		if frequencies[i].Label == frequencies[j].Label {
			return frequencies[i].Type < frequencies[j].Type
		}
		return frequencies[i].Label < frequencies[j].Label
	})
	var buckets []DateBucket
	for bucket, count := range column.buckets {
		buckets = append(buckets, DateBucket{Bucket: bucket, Count: count})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Bucket < buckets[j].Bucket })
	types := column.types
	types.Null = nulls
	return ColumnStatistics{
		Name:                  name,
		NullCount:             nulls,
		NonNullCount:          column.nonNull,
		Cardinality:           len(column.values) + boolToInt(nulls > 0),
		CardinalityIncomplete: column.incomplete,
		Frequencies:           frequencies,
		FrequenciesIncomplete: column.incomplete,
		Types:                 types,
		DateBuckets:           buckets,
		DateBucketsIncomplete: column.bucketFull,
	}
}

func (types *TypeObservations) add(kind ValueKind) {
	switch kind {
	case ValueKindNull:
		types.Null++
	case ValueKindString:
		types.String++
	case ValueKindBoolean:
		types.Boolean++
	case ValueKindNumber:
		types.Number++
	case ValueKindDecimal:
		types.Decimal++
	case ValueKindDate:
		types.Date++
	case ValueKindDatetime:
		types.Datetime++
	default:
		types.Other++
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
