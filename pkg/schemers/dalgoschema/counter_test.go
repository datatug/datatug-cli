package dalgoschema

import (
	"context"
	"errors"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRecordsReader serves the records it was given, then ErrNoMoreRecords.
type fakeRecordsReader struct {
	records  []record.Record
	nextErr  error
	closeErr error
	closed   bool
}

func (r *fakeRecordsReader) Cursor() (string, error) { return "", dal.ErrNotSupported }
func (r *fakeRecordsReader) Close() error            { r.closed = true; return r.closeErr }
func (r *fakeRecordsReader) Next() (record.Record, error) {
	if r.nextErr != nil {
		return nil, r.nextErr
	}
	if len(r.records) == 0 {
		return nil, dal.ErrNoMoreRecords
	}
	next := r.records[0]
	r.records = r.records[1:]
	return next, nil
}

// fakeExecutor is a dal.QueryExecutor that records the queries it is asked to run.
type fakeExecutor struct {
	queries []dal.Query
	reader  *fakeRecordsReader
	err     error
}

func (e *fakeExecutor) ExecuteQueryToRecordsReader(_ context.Context, query dal.Query) (dal.RecordsReader, error) {
	e.queries = append(e.queries, query)
	if e.err != nil {
		return nil, e.err
	}
	return e.reader, nil
}

func (e *fakeExecutor) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, dal.ErrNotSupported
}

func collectionOf(t *testing.T, query dal.StructuredQuery) dal.CollectionRef {
	t.Helper()
	collection, ok := query.From().Base().(dal.CollectionRef)
	require.True(t, ok, "the query reads one collection")
	return collection
}

func countRecord(value any) record.Record {
	return record.NewRecordWithData(record.NewKeyWithID("Customer", "0"), map[string]any{countAlias: value})
}

var nativeCount = dal.QueryCapabilities{Aggregate: dal.AggregateCapabilities{Count: true}}

func TestNativeCounter_CountsWithOneCountStarQuery(t *testing.T) {
	for name, value := range map[string]any{"int64": int64(42), "int": 42, "float64": float64(42)} {
		t.Run(name, func(t *testing.T) {
			reader := &fakeRecordsReader{records: []record.Record{countRecord(value)}}
			executor := &fakeExecutor{reader: reader}
			counter := newNativeCounter(executor, nativeCount)

			count, err := counter.CountRecords(context.Background(), "public", "Order")
			require.NoError(t, err)
			require.NotNil(t, count)
			assert.Equal(t, 42, *count)
			assert.True(t, reader.closed)

			require.Len(t, executor.queries, 1)
			query, ok := executor.queries[0].(dal.StructuredQuery)
			require.True(t, ok)
			from := collectionOf(t, query)
			assert.Equal(t, "Order", from.Name(), "the name is passed as a name, never as SQL text")
			assert.Equal(t, "public", from.Schema())
			require.Len(t, query.Columns(), 1)
			assert.True(t, dal.HasAggregation(query))
			assert.Equal(t, countAlias, query.Columns()[0].Alias)
		})
	}
}

func TestNativeCounter_AnUnqualifiedTableIsCountedUnqualified(t *testing.T) {
	executor := &fakeExecutor{reader: &fakeRecordsReader{records: []record.Record{countRecord(int64(1))}}}
	_, err := newNativeCounter(executor, nativeCount).CountRecords(context.Background(), "", "Order")
	require.NoError(t, err)
	assert.Empty(t, collectionOf(t, executor.queries[0].(dal.StructuredQuery)).Schema())
}

func TestNativeCounter_NeverReadsATableToCountIt(t *testing.T) {
	// Without a native COUNT, DALgo would pull every row of the table through
	// this process to count it: a schema scan must not do that to a database.
	for name, capabilities := range map[string]dal.QueryCapabilities{
		"no capabilities":          {},
		"aggregates without count": {Aggregate: dal.AggregateCapabilities{Sum: true, Min: true, Max: true}},
	} {
		t.Run(name, func(t *testing.T) {
			executor := &fakeExecutor{reader: &fakeRecordsReader{}}
			counter := newNativeCounter(executor, capabilities)
			for range 2 {
				count, err := counter.CountRecords(context.Background(), "public", "Order")
				assert.NoError(t, err)
				assert.Nil(t, count, "no count, not a wrong one")
			}
			assert.Empty(t, executor.queries)
		})
	}
}

func TestNativeCounter_Errors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	t.Run("the planner refuses the query", func(t *testing.T) {
		original := planAggregation
		planAggregation = func(dal.StructuredQuery, dal.QueryCapabilities) (dal.AggregationPlan, error) {
			return dal.AggregationPlan{}, boom
		}
		t.Cleanup(func() { planAggregation = original })
		executor := &fakeExecutor{reader: &fakeRecordsReader{}}
		count, err := newNativeCounter(executor, nativeCount).CountRecords(ctx, "public", "Order")
		assert.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "count records of Order")
		assert.Nil(t, count)
		assert.Empty(t, executor.queries)
	})

	t.Run("the query fails", func(t *testing.T) {
		_, err := newNativeCounter(&fakeExecutor{err: boom}, nativeCount).CountRecords(ctx, "public", "Order")
		assert.ErrorIs(t, err, boom)
		assert.ErrorContains(t, err, "count records of Order")
	})
	t.Run("no row comes back", func(t *testing.T) {
		_, err := newNativeCounter(&fakeExecutor{reader: &fakeRecordsReader{}}, nativeCount).CountRecords(ctx, "public", "Order")
		assert.ErrorContains(t, err, "count records of Order")
		assert.ErrorIs(t, err, dal.ErrNoMoreRecords)
	})
	t.Run("the row is not a map", func(t *testing.T) {
		row := record.NewRecordWithData(record.NewKeyWithID("Order", "0"), []int{1})
		_, err := newNativeCounter(&fakeExecutor{reader: &fakeRecordsReader{records: []record.Record{row}}}, nativeCount).CountRecords(ctx, "public", "Order")
		assert.ErrorContains(t, err, "unexpected COUNT(*) row")
	})
	t.Run("the value is not a number", func(t *testing.T) {
		_, err := newNativeCounter(&fakeExecutor{reader: &fakeRecordsReader{records: []record.Record{countRecord("many")}}}, nativeCount).CountRecords(ctx, "public", "Order")
		assert.ErrorContains(t, err, "unexpected COUNT(*) value of type string")
	})
	t.Run("closing the reader fails", func(t *testing.T) {
		reader := &fakeRecordsReader{records: []record.Record{countRecord(int64(3))}, closeErr: boom}
		count, err := newNativeCounter(&fakeExecutor{reader: reader}, nativeCount).CountRecords(ctx, "public", "Order")
		assert.ErrorIs(t, err, boom)
		assert.Nil(t, count)
	})
	t.Run("a close error does not hide the first error", func(t *testing.T) {
		reader := &fakeRecordsReader{nextErr: boom, closeErr: errors.New("close failed")}
		_, err := newNativeCounter(&fakeExecutor{reader: reader}, nativeCount).CountRecords(ctx, "public", "Order")
		assert.ErrorIs(t, err, boom)
	})
}

// capableDB is a dal.DB whose backend advertises its aggregation capabilities.
type capableDB struct {
	dal.DB
	executor *fakeExecutor
}

func (d capableDB) QueryCapabilities() dal.QueryCapabilities { return nativeCount }
func (d capableDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	return d.executor.ExecuteQueryToRecordsReader(ctx, query)
}

// plainDB is a dal.DB that advertises nothing.
type plainDB struct {
	dal.DB
	executor *fakeExecutor
}

func (d plainDB) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	return d.executor.ExecuteQueryToRecordsReader(ctx, query)
}

func TestNewNativeCounter_ReadsTheCapabilitiesTheDatabaseAdvertises(t *testing.T) {
	ctx := context.Background()

	executor := &fakeExecutor{reader: &fakeRecordsReader{records: []record.Record{countRecord(int64(7))}}}
	count, err := NewNativeCounter(capableDB{executor: executor}).CountRecords(ctx, "public", "Order")
	require.NoError(t, err)
	require.NotNil(t, count)
	assert.Equal(t, 7, *count)

	// A database that says nothing is not asked to count.
	silent := &fakeExecutor{reader: &fakeRecordsReader{}}
	count, err = NewNativeCounter(plainDB{executor: silent}).CountRecords(ctx, "public", "Order")
	assert.NoError(t, err)
	assert.Nil(t, count)
	assert.Empty(t, silent.queries)
}
