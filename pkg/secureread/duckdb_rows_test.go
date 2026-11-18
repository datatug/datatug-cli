package secureread

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/stretchr/testify/require"
)

type duckDBBudgetReader struct {
	dal.RecordsReader
	count, at, calls, closes int
	data                     map[string]any
	next                     func() (record.Record, error)
	close                    func() error
}

func (r *duckDBBudgetReader) Next() (record.Record, error) {
	r.calls++
	if r.next != nil {
		return r.next()
	}
	if r.at == r.count {
		return nil, dal.ErrNoMoreRecords
	}
	r.at++
	return record.NewRecordWithData(record.NewKeyWithID("native", ""), r.data), nil
}

func (r *duckDBBudgetReader) Close() error {
	r.closes++
	if r.close != nil {
		return r.close()
	}
	return nil
}

func TestDuckDBCollectorRowBoundaryAndOverflowDiscardWholeResult(t *testing.T) {
	for _, count := range []int{0, queryresults.MaxRows, queryresults.MaxRows + 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			reader := &duckDBBudgetReader{count: count, data: map[string]any{"v": 1}}
			rows, statistics, err := collectDuckDBRows(context.Background(), reader)
			require.Equal(t, 1, reader.closes)
			if count > queryresults.MaxRows {
				require.ErrorIs(t, err, ErrDuckDBResultLimit)
				require.Nil(t, rows)
				require.Nil(t, statistics)
				require.Equal(t, queryresults.MaxRows+1, reader.calls)
			} else {
				require.NoError(t, err)
				require.Len(t, rows, count)
				require.Equal(t, count+1, reader.calls, "the exact row cap still requires EOF")
				require.Equal(t, count, statistics.finalize([]string{"v"}).RowCount)
			}
		})
	}
}

func TestDuckDBCollectorByteBoundaryAndEscaping(t *testing.T) {
	empty, err := json.Marshal([]Row{{Key: "", Data: map[string]any{"v": ""}}})
	require.NoError(t, err)
	for _, delta := range []int{0, 1} {
		reader := &duckDBBudgetReader{count: 1, data: map[string]any{"v": strings.Repeat("a", queryresults.MaxDataBytes-len(empty)+delta)}}
		rows, statistics, err := collectDuckDBRows(context.Background(), reader)
		require.Equal(t, 1, reader.closes)
		if delta == 0 {
			require.NoError(t, err)
			encoded, err := json.Marshal(rows)
			require.NoError(t, err)
			require.Len(t, encoded, queryresults.MaxDataBytes)
		} else {
			require.ErrorIs(t, err, ErrDuckDBResultLimit)
			require.Nil(t, rows)
			require.Nil(t, statistics)
		}
	}
	for _, value := range []any{strings.Repeat("\"", queryresults.MaxDataBytes/2), strings.Repeat("x", queryresults.MaxDataBytes+1), make([]byte, queryresults.MaxDataBytes+1)} {
		reader := &duckDBBudgetReader{count: 1, data: map[string]any{"v": value}}
		rows, statistics, err := collectDuckDBRows(context.Background(), reader)
		require.ErrorIs(t, err, ErrDuckDBResultLimit)
		require.Nil(t, rows)
		require.Nil(t, statistics)
		require.Equal(t, 1, reader.closes)
	}
}

func TestDuckDBFinalBudgetIncludesMetadataAndStatistics(t *testing.T) {
	result := Result{Rows: []Row{{Data: map[string]any{"v": ""}}}}
	baseline, err := json.Marshal(result)
	require.NoError(t, err)
	result.Rows[0].Data["v"] = strings.Repeat("a", queryresults.MaxDataBytes-len(baseline))
	require.NoError(t, checkDuckDBResultBudget(result))
	result.ColumnOrder = []string{"v"}
	require.ErrorIs(t, checkDuckDBResultBudget(result), ErrDuckDBResultLimit)
	result.ColumnOrder = nil
	result.Statistics = StatisticsForRows([]string{"v"}, result.Rows)
	require.ErrorIs(t, checkDuckDBResultBudget(result), ErrDuckDBResultLimit)
}

func TestDuckDBCollectorCancellationAndCloseCannotDeliverLateRows(t *testing.T) {
	for _, stage := range []string{"next-row", "next-eof", "close"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader := &duckDBBudgetReader{count: 1, data: map[string]any{"v": 1}}
			if stage == "close" {
				reader.close = func() error { cancel(); return nil }
			} else {
				reader.next = func() (record.Record, error) {
					cancel()
					if stage == "next-eof" {
						return nil, dal.ErrNoMoreRecords
					}
					return record.NewRecordWithData(record.NewKeyWithID("native", ""), reader.data), nil
				}
			}
			rows, statistics, err := collectDuckDBRows(ctx, reader)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, rows)
			require.Nil(t, statistics)
			require.Equal(t, 1, reader.closes)
		})
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	reader := &duckDBBudgetReader{count: 1}
	_, _, err := collectDuckDBRows(ctx, reader)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, reader.calls)
	require.Equal(t, 1, reader.closes)
}

func TestDuckDBCollectorCloseAndReadFailuresDiscardWholeResult(t *testing.T) {
	failure := errors.New("owned fixture error")
	for _, stage := range []string{"close", "next", "conversion"} {
		reader := &duckDBBudgetReader{count: 1, data: map[string]any{"v": int64(9007199254740993)}}
		switch stage {
		case "close":
			reader.close = func() error { return failure }
		case "next":
			reader.next = func() (record.Record, error) { return nil, failure }
		case "conversion":
			reader.data["v"] = make(chan int)
		}
		rows, statistics, err := collectDuckDBRows(context.Background(), reader)
		require.Error(t, err)
		if stage != "conversion" {
			require.ErrorIs(t, err, failure)
		}
		require.Nil(t, rows)
		require.Nil(t, statistics)
		require.Equal(t, 1, reader.closes)
	}
}

func TestDuckDBCollectorPreservesDetachedNativeValues(t *testing.T) {
	buffer := []byte("a")
	nested := map[string]any{"integer": int64(0)}
	reader := &reusedMutableReader{bytes: buffer, nested: nested, data: map[string]any{"binary": buffer, "object": nested, "array": []any{nested}}}
	rows, _, err := collectDuckDBRows(context.Background(), reader)
	require.NoError(t, err)
	for i, row := range rows {
		require.Equal(t, []byte{byte('a' + i)}, row.Data["binary"])
		require.Equal(t, int64(9007199254740993+int64(i)), row.Data["object"].(map[string]any)["integer"])
	}
}

func TestDuckDBCollectorRetainsLegacyNilReaderAndNilDataContract(t *testing.T) {
	rows, statistics, err := collectDuckDBRows(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, rows)
	require.Zero(t, statistics.finalize(nil).RowCount)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows, statistics, err = collectDuckDBRows(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, rows)
	require.Nil(t, statistics)
	reader := &duckDBBudgetReader{count: 1}
	reader.next = func() (record.Record, error) {
		if reader.calls > 1 {
			return nil, dal.ErrNoMoreRecords
		}
		return record.NewRecordWithData(record.NewKeyWithID("native", ""), nil), nil
	}
	rows, statistics, err = collectDuckDBRows(context.Background(), reader)
	require.NoError(t, err)
	require.Equal(t, []Row{{Key: "", Data: nil}}, rows)
	require.Equal(t, 1, statistics.finalize(nil).RowCount)
	require.Equal(t, 1, reader.closes)
}

func TestDuckDBCollectorRejectsCyclicAndNestedOversizedValues(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for name, value := range map[string]any{
		"cycle":  cycle,
		"array":  []any{strings.Repeat("x", queryresults.MaxDataBytes)},
		"struct": struct{ Payload string }{Payload: strings.Repeat("x", queryresults.MaxDataBytes)},
	} {
		t.Run(name, func(t *testing.T) {
			reader := &duckDBBudgetReader{count: 1, data: map[string]any{"nested": value}}
			rows, statistics, err := collectDuckDBRows(context.Background(), reader)
			require.ErrorIs(t, err, ErrDuckDBResultLimit)
			require.Nil(t, rows)
			require.Nil(t, statistics)
			require.Equal(t, 1, reader.calls, "malformed first row must not trigger further reads")
			require.Equal(t, 1, reader.closes)
		})
	}
}

func TestDuckDBCollectorRemainingBudgetCannotBeBypassedByContainerShape(t *testing.T) {
	first := Row{Key: "", Data: map[string]any{"v": ""}}
	empty, err := json.Marshal([]Row{first})
	require.NoError(t, err)
	for _, test := range []struct {
		name      string
		remaining int
		second    map[string]any
	}{
		{"many-fields", 4, map[string]any{"a": nil, "b": nil, "c": nil, "d": nil}},
		{"long-field-name", 64, map[string]any{strings.Repeat("k", 128): nil}},
		{"many-array-elements", 64, map[string]any{"v": make([]int, 100)}},
		{"nested-envelope-overhead", 3, map[string]any{"v": nil}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &duckDBBudgetReader{}
			reader.next = func() (record.Record, error) {
				switch reader.calls {
				case 1:
					data := map[string]any{"v": strings.Repeat("a", queryresults.MaxDataBytes-len(empty)-test.remaining)}
					return record.NewRecordWithData(record.NewKeyWithID("native", ""), data), nil
				case 2:
					return record.NewRecordWithData(record.NewKeyWithID("native", ""), test.second), nil
				default:
					return nil, dal.ErrNoMoreRecords
				}
			}
			rows, statistics, err := collectDuckDBRows(context.Background(), reader)
			require.ErrorIs(t, err, ErrDuckDBResultLimit)
			require.Nil(t, rows, "the accepted first row must not escape as a partial result")
			require.Nil(t, statistics)
			require.Equal(t, 2, reader.calls)
			require.Equal(t, 1, reader.closes)
		})
	}
}

func TestDuckDBCollectorAndFinalEnvelopeRefuseMalformedNumericJSON(t *testing.T) {
	data := map[string]any{"number": json.Number("not-a-number")}
	reader := &duckDBBudgetReader{count: 1, data: data}
	rows, statistics, err := collectDuckDBRows(context.Background(), reader)
	require.Error(t, err, "malformed numeric JSON must fail serialization")
	require.Nil(t, rows)
	require.Nil(t, statistics)
	require.Equal(t, 1, reader.closes)
	// The complete-envelope guard also fails closed if a future metadata or
	// result producer introduces a value JSON cannot faithfully encode.
	require.Error(t, checkDuckDBResultBudget(Result{Rows: []Row{{Data: data}}}), "malformed numeric JSON must fail complete-envelope serialization")
}

type duckDBCancelDuringJSON struct{ cancel context.CancelFunc }

func (value duckDBCancelDuringJSON) MarshalJSON() ([]byte, error) {
	value.cancel()
	return []byte(`"converted"`), nil
}

func TestDuckDBCollectorRejectsCancellationDuringValueSerialization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &duckDBBudgetReader{count: 1, data: map[string]any{"value": duckDBCancelDuringJSON{cancel: cancel}}}
	rows, statistics, err := collectDuckDBRows(ctx, reader)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, rows)
	require.Nil(t, statistics)
	require.Equal(t, 1, reader.closes)
}

func TestDuckDBNativePreAbortDoesNotOpenEngine(t *testing.T) {
	original := openDuckDBNativeFn
	t.Cleanup(func() { openDuckDBNativeFn = original })
	openDuckDBNativeFn = func(string) (dbcopy.ReadOnlyDuckDB, error) {
		t.Fatal("pre-aborted native execution must not open a DuckDB engine")
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewExecutor(Session{Unrestricted: true}).RunNativeSQL(ctx, "duckdb://:memory:", "SELECT 42 AS answer")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, Result{}, result)
}

type duckDBDeliveryFixture struct {
	dal.DB
	reader *duckDBBudgetReader
	closes int
	close  func() error
}

func (d *duckDBDeliveryFixture) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return d.reader, nil
}
func (d *duckDBDeliveryFixture) ValidateReadQuery(context.Context, string) error { return nil }
func (d *duckDBDeliveryFixture) SQLDB() *sql.DB                                  { return nil }
func (d *duckDBDeliveryFixture) Close() error {
	d.closes++
	if d.close != nil {
		return d.close()
	}
	return nil
}

func TestDuckDBNativeMetadataAndDatabaseCloseCannotDeliverLateSuccess(t *testing.T) {
	for _, stage := range []string{"metadata", "database-close", "database-close-error"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("owned close failure")
			db := &duckDBDeliveryFixture{reader: &duckDBBudgetReader{count: 1, data: map[string]any{"v": 1}}}
			originalOpen, originalColumns := openDuckDBNativeFn, duckDBColumnOrderFn
			t.Cleanup(func() { openDuckDBNativeFn, duckDBColumnOrderFn = originalOpen, originalColumns })
			openDuckDBNativeFn = func(string) (dbcopy.ReadOnlyDuckDB, error) { return db, nil }
			duckDBColumnOrderFn = func(context.Context, *sql.DB, string, []dal.QueryArg) []string {
				if stage == "metadata" {
					cancel()
				}
				return []string{"v"}
			}
			db.close = func() error {
				if stage == "database-close" {
					cancel()
				}
				if stage == "database-close-error" {
					return failure
				}
				return nil
			}
			result, err := NewExecutor(Session{Unrestricted: true}).RunNativeSQL(ctx, "duckdb://:memory:", "SELECT 1 AS v")
			if stage == "database-close-error" {
				require.ErrorIs(t, err, failure)
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			require.Equal(t, Result{}, result)
			require.Equal(t, 1, db.closes)
			require.Equal(t, 1, db.reader.closes)
		})
	}
}
