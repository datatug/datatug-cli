//go:build cgo

package secureread

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2duckdb"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/datatug/datatug-cli/pkg/queryresults"
	"github.com/duckdb/duckdb-go/v2"
	"github.com/stretchr/testify/require"
)

func TestDuckDBNativeReadsAndFailures(t *testing.T) {
	ctx := context.Background()
	executor := NewExecutor(Session{Unrestricted: true})
	result, err := executor.RunNativeSQL(ctx, "duckdb://:memory:", "SELECT ? AS answer", dal.QueryArg{Value: 42})
	require.NoError(t, err)
	require.Len(t, result.Rows, 1)
	require.EqualValues(t, 42, result.Rows[0].Data["answer"])
	require.Equal(t, []string{"answer"}, result.ColumnOrder)

	for _, query := range []string{"SELECT 1; SELECT 2", "CREATE TABLE forbidden(id INTEGER)", "SELECT ? AS missing_argument"} {
		_, err = executor.RunNativeSQL(ctx, "duckdb://:memory:", query)
		require.Error(t, err, query)
	}
	missing := filepath.Join(t.TempDir(), "missing.duckdb")
	_, err = executor.RunNativeSQL(ctx, "duckdb://"+missing, "SELECT 1")
	require.ErrorIs(t, err, dbcopy.ErrSourceFileMissing)
	broken := filepath.Join(t.TempDir(), "broken.duckdb")
	require.NoError(t, os.WriteFile(broken, []byte("not a DuckDB database"), 0600))
	_, err = executor.RunNativeSQL(ctx, "duckdb://"+broken, "SELECT 1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "open duckdb source")
}

func TestDuckDBNativeRichParquetPagesPreserveLegacyValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typed.parquet")
	db, err := dalgo2duckdb.NewDatabase("")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.SQLDB().Exec("COPY (SELECT i, (9007199254740993 + i)::BIGINT AS exact, (12.0034)::DECIMAL(12,4) AS amount, CASE i WHEN 0 THEN false WHEN 1 THEN true ELSE NULL END AS flag, (-2+i)::TINYINT AS negative FROM range(3) t(i)) TO '" + strings.ReplaceAll(path, "'", "''") + "' (FORMAT PARQUET)")
	require.NoError(t, err)
	executor := NewExecutor(Session{Unrestricted: true})
	for i := 0; i < 3; i++ {
		result, err := executor.RunNativeSQL(context.Background(), "duckdb://:memory:", "SELECT i, exact, amount, flag, negative FROM read_parquet('"+strings.ReplaceAll(path, "'", "''")+"') ORDER BY i LIMIT 1 OFFSET "+strconv.Itoa(i))
		require.NoError(t, err)
		require.Len(t, result.Rows, 1)
		row := result.Rows[0].Data
		require.Equal(t, int64(9007199254740993+int64(i)), row["exact"])
		require.Equal(t, int8(-2+i), row["negative"])
		require.Equal(t, []any{false, true, nil}[i], row["flag"])
		// Preserve the existing decimal JSON object shape, not a new wire
		// codec or a float64 round trip. Scale and unscaled integer stay exact.
		require.Equal(t, map[string]any{"Width": json.Number("12"), "Scale": json.Number("4"), "Value": json.Number("120034")}, row["amount"])
		require.Equal(t, []string{"i", "exact", "amount", "flag", "negative"}, result.ColumnOrder)
	}
}

func TestDuckDBNativeOverflowFailsWholeAndReopens(t *testing.T) {
	executor := NewExecutor(Session{Unrestricted: true})
	for _, text := range []string{"SELECT i FROM range(10001) t(i)", "SELECT repeat('x', 4000001) AS huge"} {
		result, err := executor.RunNativeSQL(context.Background(), "duckdb://:memory:", text)
		require.ErrorIs(t, err, ErrDuckDBResultLimit)
		require.Equal(t, Result{}, result)
	}
	result, err := executor.RunNativeSQL(context.Background(), "duckdb://:memory:", "SELECT i FROM range(10000) t(i)")
	require.NoError(t, err)
	require.Len(t, result.Rows, queryresults.MaxRows)
	result, err = executor.RunNativeSQL(context.Background(), "duckdb://:memory:", "SELECT 42 AS answer")
	require.NoError(t, err)
	require.EqualValues(t, 42, result.Rows[0].Data["answer"])
}

func TestDuckDBNativeStringAndBlobInlineBoundaries(t *testing.T) {
	// Official duckdb-go PR148 fixes an unaligned pointer read for strings
	// and blobs above the 12-byte inline boundary. Keep these reads under
	// normal -race/checkptr instrumentation, including non-inline BLOBs.
	ctx := context.Background()
	db, err := dalgo2duckdb.NewReadOnlyDatabase("")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	executor := NewExecutor(Session{Unrestricted: true})
	for _, length := range []int{0, 1, 11, 12, 13, 14, 4096} {
		t.Run(strconv.Itoa(length), func(t *testing.T) {
			text := strings.Repeat("s", length)
			binary := make([]byte, length)
			for i := range binary {
				binary[i] = byte(i)
			}
			var rawText string
			var rawBinary []byte
			err := db.SQLDB().QueryRowContext(ctx, "SELECT ?::VARCHAR, ?::BLOB", text, binary).Scan(&rawText, &rawBinary)
			require.NoError(t, err)
			require.Equal(t, text, rawText)
			require.Equal(t, binary, rawBinary, "raw driver BLOB must preserve zero and invalid UTF-8 bytes")
			result, err := executor.RunNativeSQL(ctx, "duckdb://:memory:", "SELECT ?::VARCHAR AS s, ?::BLOB AS b", dal.QueryArg{Value: text}, dal.QueryArg{Value: []byte(text)})
			require.NoError(t, err)
			require.Len(t, result.Rows, 1)
			require.Equal(t, text, result.Rows[0].Data["s"])
			// DALgo's existing legacy SQL records path converts []byte to
			// text. This regression preserves that existing output contract;
			// it does not assert a new binary-safe CLI wire representation.
			require.Equal(t, text, result.Rows[0].Data["b"])
		})
	}
	// Exercise later row offsets and another native vector batch, not only
	// element zero, while remaining below the materialized result bounds.
	result, err := executor.RunNativeSQL(ctx, "duckdb://:memory:", "SELECT repeat('s',13) AS s, encode(repeat('b',13)) AS b FROM range(4097)")
	require.NoError(t, err)
	require.Len(t, result.Rows, 4097)
	for _, row := range result.Rows {
		require.Equal(t, strings.Repeat("s", 13), row.Data["s"])
		require.Equal(t, strings.Repeat("b", 13), row.Data["b"])
	}
}

func TestDuckDBNativeNonFiniteAndExpandedEnvelopeFailWhole(t *testing.T) {
	executor := NewExecutor(Session{Unrestricted: true})
	for _, text := range []string{"SELECT 'NaN'::DOUBLE AS value", "SELECT 'Infinity'::DOUBLE AS value", "SELECT '-Infinity'::DOUBLE AS value"} {
		result, err := executor.RunNativeSQL(context.Background(), "duckdb://:memory:", text)
		require.ErrorContains(t, err, "non-finite result number")
		require.Equal(t, Result{}, result)
	}
	// This row alone fits the running row budget. Repeating its value in
	// the normal frequency statistics makes the complete envelope exceed
	// four million bytes, which must still fail the entire native result.
	result, err := executor.RunNativeSQL(context.Background(), "duckdb://:memory:", "SELECT repeat('x',2000000) AS value")
	require.ErrorIs(t, err, ErrDuckDBResultLimit)
	require.Equal(t, Result{}, result)
	result, err = executor.RunNativeSQL(context.Background(), "duckdb://:memory:", "SELECT 42 AS answer")
	require.NoError(t, err)
	require.EqualValues(t, 42, result.Rows[0].Data["answer"])
}

// A volatile native UDF proves execution reached DuckDB before cancellation;
// a timer alone could instead cancel while opening or validating the query.
type duckDBCancellationGate struct {
	resultType   duckdb.TypeInfo
	started      chan bool
	expectedDone <-chan struct{}
}

func (g *duckDBCancellationGate) Config() duckdb.ScalarFuncConfig {
	return duckdb.ScalarFuncConfig{ResultTypeInfo: g.resultType, Volatile: true}
}

func (g *duckDBCancellationGate) Executor() duckdb.ScalarFuncExecutor {
	return duckdb.ScalarFuncExecutor{RowContextExecutor: func(ctx context.Context, _ []driver.Value) (any, error) {
		active := ctx.Done() != nil && ctx.Done() == g.expectedDone && ctx.Err() == nil
		select {
		case g.started <- active:
		default:
		}
		if !active {
			return nil, context.Canceled
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
}

func TestDuckDBNativeCancellationAfterDispatchAndReopen(t *testing.T) {
	for _, mode := range []string{"caller", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			db, err := dalgo2duckdb.NewReadOnlyDatabase("")
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			conn, err := db.SQLDB().Conn(context.Background())
			require.NoError(t, err)
			resultType, err := duckdb.NewTypeInfo(duckdb.TYPE_BIGINT)
			require.NoError(t, err)
			gate := &duckDBCancellationGate{resultType: resultType, started: make(chan bool, 1)}
			err = duckdb.RegisterScalarUDF(conn, "fixture_cancellation_gate", gate)
			require.NoError(t, conn.Close())
			require.NoError(t, err)
			original := openDuckDBNativeFn
			openDuckDBNativeFn = func(string) (dbcopy.ReadOnlyDuckDB, error) { return db, nil }
			t.Cleanup(func() { openDuckDBNativeFn = original })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			defer cancel()
			gate.expectedDone = ctx.Done()
			observed := make(chan bool, 1)
			if mode == "caller" {
				go func() {
					select {
					case active := <-gate.started:
						observed <- active
					case <-ctx.Done():
						observed <- false
					}
					cancel()
				}()
			}
			result, err := NewExecutor(Session{Unrestricted: true}).RunNativeSQL(ctx, "duckdb://:memory:", "SELECT fixture_cancellation_gate() AS gate")
			if mode == "caller" {
				require.True(t, <-observed, "native callback must precede caller abort")
				require.ErrorIs(t, err, context.Canceled)
			} else {
				select {
				case active := <-gate.started:
					require.True(t, active, "native callback must precede deadline")
				default:
					t.Fatal("native callback was never reached")
				}
				require.ErrorIs(t, err, context.DeadlineExceeded)
			}
			require.Equal(t, Result{}, result)
			openDuckDBNativeFn = original
			result, err = NewExecutor(Session{Unrestricted: true}).RunNativeSQL(context.Background(), "duckdb://:memory:", "SELECT 7 AS answer")
			require.NoError(t, err)
			require.EqualValues(t, 7, result.Rows[0].Data["answer"])
		})
	}
}

func TestDuckDBNativeParquetAndFileDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "items.duckdb")
	parquet := filepath.Join(t.TempDir(), "items.parquet")
	db, err := dalgo2duckdb.NewDatabase(path)
	require.NoError(t, err)
	_, err = db.SQLDB().Exec("CREATE TABLE items(id INTEGER, name VARCHAR); INSERT INTO items VALUES (1, 'DuckDB')")
	require.NoError(t, err)
	_, err = db.SQLDB().Exec("COPY items TO '" + parquet + "' (FORMAT PARQUET)")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	executor := NewExecutor(Session{Unrestricted: true})
	for _, source := range []string{"duckdb://" + path, "duckdb://:memory:"} {
		query := "SELECT name FROM read_parquet('" + parquet + "') WHERE id = 1"
		result, err := executor.RunNativeSQL(ctx, source, query)
		require.NoError(t, err)
		require.Len(t, result.Rows, 1)
		require.Equal(t, "DuckDB", result.Rows[0].Data["name"])
		require.Equal(t, []string{"name"}, result.ColumnOrder)
	}
}
