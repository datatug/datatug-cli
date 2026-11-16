package secureread

import (
	"context"
	"database/sql"
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

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2clickhouse"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-core/pkg/apicontract"
	"github.com/shopspring/decimal"
)

// OrdinalColumn describes a column by position, never by its possibly duplicate
// label. Compound cells use JSON text of recursively typed cells (maps use typed
// key/value pairs); this preserves nested UInt64 and Decimal without floats.
type OrdinalColumn struct {
	NativeType string `json:"nativeType"`
	Encoding   string `json:"encoding,omitempty"`
}

func (e *Executor) runClickHouseStructured(ctx context.Context, db dal.DB, query dal.Query, variables map[string]any, validator dalgo2clickhouse.StructuredQueryValidator) (Result, error) {
	prepared, err := accesspolicies.PrepareRecordsetWithVariables(ctx, query, accesspolicies.Options{Principal: e.session.Principal, Variables: variables, Policies: e.session.Policies, Unrestricted: e.session.DatabaseProfile.PolicyFreeReads()}, validator.ValidateStructuredQuery, clickHouseVariables)
	if err != nil {
		return Result{}, clickHousePreparationError(err)
	}
	reader, err := db.ExecuteQueryToRecordsetReader(ctx, prepared.Query)
	if err != nil {
		return Result{}, err
	}
	result, err := collectClickHouse(ctx, reader)
	if err != nil {
		return Result{}, err
	}
	result.Limitations = limitationsFromLines(prepared.Lines)
	if structured, ok := prepared.Query.(dal.StructuredQuery); ok {
		if ref, ok := baseCollectionRef(structured); ok {
			result.Collection = ref.Name()
		}
	}
	return result, nil
}

var openClickHouseNativeFn = func(ctx context.Context, ref dbcopy.BackendRef) (dal.DB, error) {
	return ref.OpenClickHouse(ctx, true)
}

func clickHouseNativeQueryGuard(text string, args []dal.QueryArg) func(dal.Query) error {
	return func(q dal.Query) error {
		t, ok := q.(dal.TextQuery)
		if !ok || t.Text() != text || !reflect.DeepEqual(t.Args(), args) {
			return fmt.Errorf("%w: native ClickHouse SQL and bindings must remain opaque", dal.ErrNotSupported)
		}
		return nil
	}
}

func (e *Executor) runClickHouseNative(ctx context.Context, ref dbcopy.BackendRef, text string, args []dal.QueryArg) (Result, error) {
	db, err := openClickHouseNativeFn(ctx, ref)
	if err != nil {
		return Result{}, err
	}
	defer dbcopy.CloseSource(db)
	query := dal.NewTextQuery(text, nil, args...)
	guard := clickHouseNativeQueryGuard(text, args)
	_, err = accesspolicies.PrepareRecordset(ctx, query, accesspolicies.Options{Principal: e.session.Principal, Policies: e.session.Policies, Unrestricted: e.session.DatabaseProfile.PolicyFreeReads()}, guard)
	if err != nil {
		return Result{}, clickHousePreparationError(err)
	}
	native, ok := dal.As[dalgo2clickhouse.NativeQuerier](db)
	if !ok {
		return Result{}, ErrNativeSQLUnsupported
	}
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg.Value
		if arg.Name != "" {
			values[i] = sql.Named(arg.Name, arg.Value)
		}
	}
	reader, err := native.QueryNative(ctx, text, values...)
	if err != nil {
		return Result{}, err
	}
	result, err := collectClickHouse(ctx, reader)
	if err == nil {
		result.Limitations = append(result.Limitations, nativeSQLLimitation)
	}
	return result, err
}

func clickHousePreparationError(err error) error {
	// Opaque access resources contain caller SQL. Keep the policy verdict's
	// identity, never its textual resource or an untrusted wrapper message.
	if errors.Is(err, ErrAccessDenied) {
		return fmt.Errorf("%w: ClickHouse query is not authorized", ErrAccessDenied)
	}
	if errors.Is(err, dalgo2clickhouse.ErrLimit) {
		return dalgo2clickhouse.ErrLimit
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return fmt.Errorf("%w: ClickHouse query preparation refused", dal.ErrNotSupported)
}

func collectClickHouse(ctx context.Context, reader dal.RecordsetReader) (Result, error) {
	if reader == nil {
		return Result{}, dalgo2clickhouse.ErrBackend
	}
	defer func() { _ = reader.Close() }()
	rs := reader.Recordset()
	if rs == nil || len(rs.Columns()) == 0 || len(rs.Columns()) > 256 {
		return Result{}, dalgo2clickhouse.ErrUnsupportedValue
	}
	carrier := &apicontract.Recordset{Columns: make([]apicontract.Column, len(rs.Columns())), Rows: [][]apicontract.TypedValue{}}
	result := Result{AnonymousRecordset: carrier, OrdinalColumns: make([]OrdinalColumn, len(rs.Columns()))}
	for i, col := range rs.Columns() {
		if !utf8.ValidString(col.Name()) || !utf8.ValidString(col.DbType()) {
			return Result{}, dalgo2clickhouse.ErrUnsupportedValue
		}
		result.Columns = append(result.Columns, col.Name())
		result.OrdinalColumns[i].NativeType = col.DbType()
		carrier.Columns[i] = apicontract.Column{Name: col.Name(), Type: clickHouseContractType(col.DbType())}
	}
	budget := 4_000_000
	for {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		row, rowset, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) {
			break
		}
		if err != nil {
			return Result{}, err
		}
		if row == nil || rowset != rs || len(carrier.Rows) >= 1000 {
			return Result{}, dalgo2clickhouse.ErrLimit
		}
		data, err := row.Data(rowset)
		if err != nil {
			return Result{}, dalgo2clickhouse.ErrUnsupportedValue
		}
		if len(data) != len(carrier.Columns) {
			return Result{}, dalgo2clickhouse.ErrUnsupportedValue
		}
		cells := make([]apicontract.TypedValue, len(data))
		for i, value := range data {
			cell, compound, err := clickHouseCell(reflect.ValueOf(value), 0)
			if err != nil {
				return Result{}, err
			}
			if carrier.Columns[i].Type == string(apicontract.ValueTypeDate) && cell.Type == apicontract.ValueTypeDatetime {
				cell = apicontract.NewDateValue(cell.Str[:10])
			}
			encoded, err := json.Marshal(cell)
			if err != nil || len(encoded) > 64<<10 {
				return Result{}, dalgo2clickhouse.ErrLimit
			}
			budget -= len(encoded)
			if budget < 0 {
				return Result{}, dalgo2clickhouse.ErrLimit
			}
			if compound {
				result.OrdinalColumns[i].Encoding = "json-text"
			}
			cells[i] = cell
		}
		carrier.Rows = append(carrier.Rows, cells)
	}
	result.ColumnOrder = append([]string(nil), result.Columns...)
	encoded, err := json.Marshal(struct {
		Recordset *apicontract.Recordset `json:"recordset"`
		Columns   []OrdinalColumn        `json:"ordinalColumns"`
	}{carrier, result.OrdinalColumns})
	if err != nil || len(encoded) > 4_000_000 {
		return Result{}, dalgo2clickhouse.ErrLimit
	}
	result.Statistics.RowCount = len(carrier.Rows)
	return result, nil
}

func clickHouseContractType(native string) string {
	for strings.HasPrefix(native, "Nullable(") || strings.HasPrefix(native, "LowCardinality(") {
		native = native[strings.IndexByte(native, '(')+1 : len(native)-1]
	}
	switch {
	case strings.HasPrefix(native, "UInt"), strings.HasPrefix(native, "Int"):
		return string(apicontract.ValueTypeInteger)
	case strings.HasPrefix(native, "Decimal"):
		return string(apicontract.ValueTypeDecimal)
	case strings.HasPrefix(native, "Float"):
		return string(apicontract.ValueTypeNumber)
	case strings.HasPrefix(native, "DateTime"):
		return string(apicontract.ValueTypeDatetime)
	case native == "Date" || native == "Date32":
		return string(apicontract.ValueTypeDate)
	case native == "Bool":
		return string(apicontract.ValueTypeBoolean)
	default:
		return string(apicontract.ValueTypeString)
	}
}

func clickHouseCell(v reflect.Value, depth int) (apicontract.TypedValue, bool, error) {
	bad := func() (apicontract.TypedValue, bool, error) {
		return apicontract.TypedValue{}, false, dalgo2clickhouse.ErrUnsupportedValue
	}
	if depth > 16 {
		return bad()
	}
	if !v.IsValid() {
		return apicontract.NewNullValue(), false, nil
	}
	if v.CanInterface() {
		switch x := v.Interface().(type) {
		case time.Time:
			return apicontract.NewDatetimeValue(x.UTC().Format(time.RFC3339Nano)), false, nil
		case decimal.Decimal:
			return apicontract.NewDecimalValue(x.String()), false, nil
		case big.Int:
			return apicontract.NewIntegerValue(x.String()), false, nil
		}
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return apicontract.NewNullValue(), false, nil
		}
		return clickHouseCell(v.Elem(), depth+1)
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return bad()
		}
		return apicontract.NewStringValue(v.String()), false, nil
	case reflect.Bool:
		return apicontract.NewBooleanValue(v.Bool()), false, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return apicontract.NewIntegerValue(strconv.FormatInt(v.Int(), 10)), false, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return apicontract.NewIntegerValue(strconv.FormatUint(v.Uint(), 10)), false, nil
	case reflect.Float32, reflect.Float64:
		if math.IsNaN(v.Float()) || math.IsInf(v.Float(), 0) {
			return bad()
		}
		return apicontract.NewNumberValue(v.Float()), false, nil
	case reflect.Slice, reflect.Array:
		cells := make([]apicontract.TypedValue, v.Len())
		for i := range cells {
			cell, _, err := clickHouseCell(v.Index(i), depth+1)
			if err != nil {
				return bad()
			}
			cells[i] = cell
		}
		return clickHouseJSONCell(cells)
	case reflect.Map:
		type entry struct {
			Key   apicontract.TypedValue `json:"key"`
			Value apicontract.TypedValue `json:"value"`
		}
		entries := make([]entry, 0, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			key, _, err := clickHouseCell(iter.Key(), depth+1)
			if err != nil {
				return bad()
			}
			value, _, err := clickHouseCell(iter.Value(), depth+1)
			if err != nil {
				return bad()
			}
			entries = append(entries, entry{key, value})
		}
		sort.Slice(entries, func(i, j int) bool {
			a, _ := json.Marshal(entries[i].Key)
			b, _ := json.Marshal(entries[j].Key)
			return string(a) < string(b)
		})
		return clickHouseJSONCell(entries)
	}
	return bad()
}

func clickHouseJSONCell(value any) (apicontract.TypedValue, bool, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 64<<10 {
		return apicontract.TypedValue{}, false, dalgo2clickhouse.ErrLimit
	}
	return apicontract.NewStringValue(string(encoded)), true, nil
}
