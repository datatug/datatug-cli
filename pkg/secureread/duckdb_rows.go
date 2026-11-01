package secureread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/dal-go/dalgo/dal"
	"github.com/datatug/datatug-cli/pkg/accesspolicies"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/queryresults"
)

// ErrDuckDBResultLimit refuses the entire native result, never a truncated
// success. These are materialized-result bounds, not an engine memory limit.
var ErrDuckDBResultLimit = errors.New("secureread: DuckDB native result exceeds row or byte budget")

var (
	openDuckDBNativeFn  = dbcopy.OpenReadOnlyDuckDB
	duckDBColumnOrderFn = selectListOrder
)

// Keep this tail DuckDB-specific. Other providers retain their existing
// collectors, and the result-file Collector deliberately truncates cells and
// rows, which is not the contract of native DuckDB execution.
func (e *Executor) runDuckDBThroughPolicies(ctx context.Context, db dal.DB, query dal.Query) (Result, error) {
	secured, err := accesspolicies.Run(ctx, db, query, accesspolicies.Options{
		Principal: e.session.Principal, Policies: e.session.Policies,
		Unrestricted: e.session.DatabaseProfile.PolicyFreeReads(),
	})
	if err != nil {
		return Result{}, err
	}
	rows, statistics, err := collectDuckDBRows(ctx, secured.Reader)
	if err != nil {
		return Result{}, err
	}
	columns := columnsFor(secured.Query, rows)
	return Result{Columns: columns, Rows: rows, Statistics: statistics.finalize(columns), Limitations: limitationsFromLines(secured.Lines)}, nil
}

func collectDuckDBRows(ctx context.Context, reader dal.RecordsReader) (rows []Row, statistics *statisticsAccumulator, err error) {
	if reader == nil {
		if err = ctx.Err(); err != nil {
			return nil, nil, err
		}
		return nil, newStatisticsAccumulator(), nil
	}
	defer func() {
		closeErr := reader.Close()
		if contextErr := ctx.Err(); contextErr != nil {
			rows, statistics, err = nil, nil, contextErr
		} else if err == nil && closeErr != nil {
			rows, statistics, err = nil, nil, closeErr
		}
	}()
	statistics = newStatisticsAccumulator()
	// Count the encoded Rows array, including separators. The final Result
	// check also includes column names, metadata and statistics.
	retained := 2
	for {
		if err = ctx.Err(); err != nil {
			return nil, nil, err
		}
		item, nextErr := reader.Next()
		if err = ctx.Err(); err != nil {
			return nil, nil, err
		}
		if errors.Is(nextErr, dal.ErrNoMoreRecords) || (nextErr == nil && item == nil) {
			return rows, statistics, nil
		}
		if nextErr != nil {
			return nil, nil, nextErr
		}
		// Read one sentinel past the exact row cap to distinguish a complete
		// result at the bound from an incomplete result above it.
		if len(rows) >= queryresults.MaxRows {
			return nil, nil, ErrDuckDBResultLimit
		}
		row := Row{Key: fmt.Sprint(item.Key().ID)}
		if item.Exists() {
			data := item.Data()
			remaining := queryresults.MaxDataBytes - retained
			if err = duckDBValueBudget(reflect.ValueOf(data), &remaining, 0); err != nil {
				return nil, nil, err
			}
			row.Data, err = resultDataMap(data)
			if err != nil {
				return nil, nil, err
			}
		}
		encoded, encodeErr := json.Marshal(row)
		if encodeErr != nil {
			return nil, nil, encodeErr
		}
		size := len(encoded)
		if len(rows) > 0 {
			size++
		}
		if size > queryresults.MaxDataBytes-retained {
			return nil, nil, ErrDuckDBResultLimit
		}
		if err = ctx.Err(); err != nil {
			return nil, nil, err
		}
		retained += size
		statistics.addRow(row.Data)
		rows = append(rows, row)
	}
}

// Refuse obviously oversized driver values before normalization copies their
// buffers or JSON escaping expands them. This conservative lower-bound walk
// is not an engine/driver allocation sandbox; exact JSON size is checked next.
func duckDBValueBudget(value reflect.Value, remaining *int, depth int) error {
	// Reflection exposes interface wrappers separately; leave room for the
	// existing normalization helper's 64 nested value levels.
	if depth > 132 {
		return ErrDuckDBResultLimit
	}
	if !value.IsValid() {
		return nil
	}
	*remaining -= 1
	if *remaining < 0 {
		return ErrDuckDBResultLimit
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if !value.IsNil() {
			return duckDBValueBudget(value.Elem(), remaining, depth+1)
		}
	case reflect.String:
		*remaining -= value.Len()
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			*remaining -= value.Len()
			break
		}
		if value.Len() > *remaining {
			return ErrDuckDBResultLimit
		}
		for i := 0; i < value.Len(); i++ {
			if err := duckDBValueBudget(value.Index(i), remaining, depth+1); err != nil {
				return err
			}
		}
	case reflect.Map:
		if value.Len() > *remaining {
			return ErrDuckDBResultLimit
		}
		iter := value.MapRange()
		for iter.Next() {
			if err := duckDBValueBudget(iter.Key(), remaining, depth+1); err != nil {
				return err
			}
			if err := duckDBValueBudget(iter.Value(), remaining, depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).PkgPath == "" {
				if err := duckDBValueBudget(value.Field(i), remaining, depth+1); err != nil {
					return err
				}
			}
		}
	}
	if *remaining < 0 {
		return ErrDuckDBResultLimit
	}
	return nil
}

func checkDuckDBResultBudget(result Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(encoded) > queryresults.MaxDataBytes {
		return ErrDuckDBResultLimit
	}
	return nil
}
