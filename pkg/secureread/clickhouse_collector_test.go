package secureread

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/dalgo2clickhouse"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type clickHouseResultReader struct {
	set              recordset.Recordset
	position, closes int
	closeError       error
	next             func() (recordset.Row, recordset.Recordset, error)
}

func (r *clickHouseResultReader) Recordset() recordset.Recordset { return r.set }
func (*clickHouseResultReader) Cursor() (string, error)          { return "", dal.ErrNotSupported }
func (r *clickHouseResultReader) Close() error                   { r.closes++; return r.closeError }
func (r *clickHouseResultReader) Next() (recordset.Row, recordset.Recordset, error) {
	if r.next != nil {
		return r.next()
	}
	if r.position >= r.set.RowsCount() {
		return nil, r.set, dal.ErrNoMoreRecords
	}
	row := r.set.GetRow(r.position)
	r.position++
	return row, r.set, nil
}

func clickHouseResultSet(t *testing.T, names, types []string, rows ...[]any) recordset.Recordset {
	t.Helper()
	require.Len(t, types, len(names))
	columns := make([]recordset.Column[any], len(names))
	for i, name := range names {
		columns[i] = recordset.NewTypedColumn[any](name, nil, recordset.ColDbType(types[i]))
	}
	rs := recordset.NewColumnarRecordset("anonymous-clickhouse", columns...)
	for _, values := range rows {
		require.Len(t, values, len(names))
		row := rs.NewRow()
		for i, value := range values {
			require.NoError(t, row.SetValueByIndex(i, value, rs))
		}
	}
	return rs
}

type clickHouseBrokenRow struct {
	recordset.Row
	values []any
	err    error
}

func (r clickHouseBrokenRow) Data(recordset.Recordset) ([]any, error) { return r.values, r.err }

func TestClickHouseCollectorKeepsExactOrdinalNativeEvidence(t *testing.T) {
	amount := decimal.RequireFromString("12345678901234567890.123456789")
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	var nullable *string
	values := []any{uint64(math.MaxUint64), amount, day, nullable, int64(math.MinInt64), []any{uint64(math.MaxUint64), nil}, map[string]decimal.Decimal{"z": amount, "a": decimal.RequireFromString("1.000000001")}}
	rs := clickHouseResultSet(t, []string{"same", "same", "day", "nullable", "signed", "items", "attrs"}, []string{"UInt64", "Decimal(38,9)", "Date", "Nullable(String)", "Int64", "Array(Nullable(UInt64))", "Map(String,Decimal(38,9))"}, values, values)
	reader := &clickHouseResultReader{set: rs}
	result, err := collectClickHouse(context.Background(), reader)
	require.NoError(t, err)
	require.Equal(t, 1, reader.closes)
	require.Equal(t, []string{"same", "same", "day", "nullable", "signed", "items", "attrs"}, result.Columns)
	require.Equal(t, result.Columns, result.ColumnOrder)
	require.Equal(t, 2, result.Statistics.RowCount)
	require.Empty(t, result.Rows, "anonymous rows must never be mapped to record identities")
	require.Equal(t, result.AnonymousRecordset.Rows[0], result.AnonymousRecordset.Rows[1], "duplicate rows survive")
	row := result.AnonymousRecordset.Rows[0]
	require.Equal(t, apicontract.NewIntegerValue("18446744073709551615"), row[0])
	require.Equal(t, apicontract.NewDecimalValue(amount.String()), row[1])
	require.Equal(t, apicontract.NewDateValue("2026-01-02"), row[2])
	require.Equal(t, apicontract.NewNullValue(), row[3])
	require.Equal(t, apicontract.NewIntegerValue("-9223372036854775808"), row[4])
	var items []apicontract.TypedValue
	require.NoError(t, json.Unmarshal([]byte(row[5].Str), &items))
	require.Equal(t, apicontract.NewIntegerValue("18446744073709551615"), items[0])
	require.Equal(t, apicontract.NewNullValue(), items[1])
	var attrs []struct {
		Key   apicontract.TypedValue `json:"key"`
		Value apicontract.TypedValue `json:"value"`
	}
	require.NoError(t, json.Unmarshal([]byte(row[6].Str), &attrs))
	require.Len(t, attrs, 2)
	require.Equal(t, "a", attrs[0].Key.Str)
	require.Equal(t, "z", attrs[1].Key.Str)
	require.Equal(t, apicontract.NewDecimalValue(amount.String()), attrs[1].Value)
	require.Equal(t, "Decimal(38,9)", result.OrdinalColumns[1].NativeType)
	require.Equal(t, "json-text", result.OrdinalColumns[5].Encoding)
	require.Equal(t, "json-text", result.OrdinalColumns[6].Encoding)
}

func TestClickHouseCollectorClosesAndReturnsNoPartialResultOnFailure(t *testing.T) {
	marker := errors.New("reader failure")
	base := func() recordset.Recordset {
		return clickHouseResultSet(t, []string{"value"}, []string{"String"}, []any{"first"})
	}
	for _, tc := range []struct {
		name  string
		build func() (*clickHouseResultReader, context.Context)
		want  error
	}{
		{"missing recordset", func() (*clickHouseResultReader, context.Context) {
			return &clickHouseResultReader{}, context.Background()
		}, dalgo2clickhouse.ErrUnsupportedValue},
		{"empty columns", func() (*clickHouseResultReader, context.Context) {
			return &clickHouseResultReader{set: recordset.NewColumnarRecordset("empty")}, context.Background()
		}, dalgo2clickhouse.ErrUnsupportedValue},
		{"cancelled", func() (*clickHouseResultReader, context.Context) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return &clickHouseResultReader{set: base()}, ctx
		}, context.Canceled},
		{"reader error after row", func() (*clickHouseResultReader, context.Context) {
			r := &clickHouseResultReader{set: base(), closeError: errors.New("secondary close failure")}
			calls := 0
			r.next = func() (recordset.Row, recordset.Recordset, error) {
				calls++
				if calls == 1 {
					return r.set.GetRow(0), r.set, nil
				}
				return nil, r.set, marker
			}
			return r, context.Background()
		}, marker},
		{"cancel after admitted row", func() (*clickHouseResultReader, context.Context) {
			ctx, cancel := context.WithCancel(context.Background())
			r := &clickHouseResultReader{set: base()}
			r.next = func() (recordset.Row, recordset.Recordset, error) { cancel(); return r.set.GetRow(0), r.set, nil }
			return r, ctx
		}, context.Canceled},
		{"nil row", func() (*clickHouseResultReader, context.Context) {
			r := &clickHouseResultReader{set: base()}
			r.next = func() (recordset.Row, recordset.Recordset, error) { return nil, r.set, nil }
			return r, context.Background()
		}, dalgo2clickhouse.ErrLimit},
		{"changed recordset", func() (*clickHouseResultReader, context.Context) {
			r := &clickHouseResultReader{set: base()}
			other := base()
			r.next = func() (recordset.Row, recordset.Recordset, error) { return r.set.GetRow(0), other, nil }
			return r, context.Background()
		}, dalgo2clickhouse.ErrLimit},
		{"row decode error", func() (*clickHouseResultReader, context.Context) {
			r := &clickHouseResultReader{set: base()}
			r.next = func() (recordset.Row, recordset.Recordset, error) {
				return clickHouseBrokenRow{err: marker}, r.set, nil
			}
			return r, context.Background()
		}, dalgo2clickhouse.ErrUnsupportedValue},
		{"row width changed", func() (*clickHouseResultReader, context.Context) {
			r := &clickHouseResultReader{set: base()}
			r.next = func() (recordset.Row, recordset.Recordset, error) {
				return clickHouseBrokenRow{values: []any{"one", "two"}}, r.set, nil
			}
			return r, context.Background()
		}, dalgo2clickhouse.ErrUnsupportedValue},
		{"unsupported value", func() (*clickHouseResultReader, context.Context) {
			return &clickHouseResultReader{set: clickHouseResultSet(t, []string{"value"}, []string{"String"}, []any{make(chan int)})}, context.Background()
		}, dalgo2clickhouse.ErrUnsupportedValue},
		{"invalid UTF8 label", func() (*clickHouseResultReader, context.Context) {
			return &clickHouseResultReader{set: clickHouseResultSet(t, []string{string([]byte{0xff})}, []string{"String"})}, context.Background()
		}, dalgo2clickhouse.ErrUnsupportedValue},
		{"cell budget", func() (*clickHouseResultReader, context.Context) {
			return &clickHouseResultReader{set: clickHouseResultSet(t, []string{"value"}, []string{"String"}, []any{strings.Repeat("x", 65<<10)})}, context.Background()
		}, dalgo2clickhouse.ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, ctx := tc.build()
			result, err := collectClickHouse(ctx, reader)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, result.AnonymousRecordset, "failure must not expose the first admitted row")
			require.Equal(t, 1, reader.closes)
		})
	}
	_, err := collectClickHouse(context.Background(), nil)
	require.ErrorIs(t, err, dalgo2clickhouse.ErrBackend)
}

func TestClickHouseCollectorEnforcesWholeResultBudgets(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		columns, rows, cellBytes int
		want                     error
	}{
		{"column count", 257, 0, 0, dalgo2clickhouse.ErrUnsupportedValue},
		{"row count", 1, 1001, 1, dalgo2clickhouse.ErrLimit},
		{"cumulative cells", 1, 65, 63000, dalgo2clickhouse.ErrLimit},
		// Each cell is below its own bound and their sum is below 4 MiB;
		// column metadata alone pushes the final encoded envelope over budget.
		{"metadata envelope", 256, 1, 15000, dalgo2clickhouse.ErrLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			names, types := make([]string, tc.columns), make([]string, tc.columns)
			for i := range names {
				names[i] = "value"
				types[i] = "String"
				if tc.name == "metadata envelope" {
					names[i] = strings.Repeat("n", 1000)
					types[i] = "String"
				}
			}
			rows := make([][]any, tc.rows)
			for i := range rows {
				rows[i] = make([]any, tc.columns)
				for j := range rows[i] {
					rows[i][j] = strings.Repeat("x", tc.cellBytes)
				}
			}
			reader := &clickHouseResultReader{set: clickHouseResultSet(t, names, types, rows...)}
			result, err := collectClickHouse(context.Background(), reader)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, result.AnonymousRecordset)
			require.Equal(t, 1, reader.closes)
		})
	}
}

func TestClickHouseCompoundEncodingRejectsUnrepresentableValues(t *testing.T) {
	value, compound, err := clickHouseCell(reflect.Value{}, 0)
	require.NoError(t, err)
	require.False(t, compound)
	require.Equal(t, apicontract.NewNullValue(), value)
	for _, value := range []any{[]string{strings.Repeat("x", 65<<10)}, make(chan int)} {
		_, _, err := clickHouseJSONCell(value)
		require.ErrorIs(t, err, dalgo2clickhouse.ErrLimit)
	}
}
