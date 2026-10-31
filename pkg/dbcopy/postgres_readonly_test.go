package dbcopy

import (
	"context"
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/record"
	"github.com/stretchr/testify/require"
)

type modeCoordinator struct {
	dal.ReadTransactionCoordinator
	tx    dal.ReadTransaction
	err   error
	calls int
}

func (c *modeCoordinator) RunReadonlyTransaction(ctx context.Context, worker dal.ROTxWorker, _ ...dal.TransactionOption) error {
	c.calls++
	if c.err != nil {
		return c.err
	}
	return worker(ctx, c.tx)
}

type modeTransaction struct {
	dal.ReadTransaction
	reader dal.RecordsReader
	err    error
	seen   dal.Query
}

func (tx *modeTransaction) ExecuteQueryToRecordsReader(_ context.Context, q dal.Query) (dal.RecordsReader, error) {
	tx.seen = q
	return tx.reader, tx.err
}

type modeReader struct {
	dal.RecordsReader
	rows      []record.Record
	nextErr   error
	closeErr  error
	closed    int
	closeHook func()
}

func (r *modeReader) Next() (record.Record, error) {
	if r.nextErr != nil {
		return nil, r.nextErr
	}
	if len(r.rows) == 0 {
		return nil, dal.ErrNoMoreRecords
	}
	row := r.rows[0]
	r.rows = r.rows[1:]
	return row, nil
}
func (r *modeReader) Close() error {
	r.closed++
	if r.closeHook != nil {
		r.closeHook()
	}
	return r.closeErr
}
func modeRow(value any) record.Record {
	return record.NewRecordWithData(record.NewKeyWithID("mode", "1"), value)
}
func TestVerifiedPostgresReadonlyUsesSameTransactionBeforeWorker(t *testing.T) {
	reader := &modeReader{rows: []record.Record{modeRow(map[string]any{"transaction_read_only": "on"})}}
	tx := &modeTransaction{reader: reader}
	coordinator := &modeCoordinator{tx: tx}
	called := false
	sentinel := errors.New("force rollback")
	err := RunVerifiedPostgresReadonly(context.Background(), coordinator, func(_ context.Context, actual dal.ReadTransaction) error {
		require.Same(t, tx, actual)
		require.Equal(t, 1, reader.closed)
		require.Contains(t, tx.seen.String(), "SHOW transaction_read_only")
		called = true
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	require.True(t, called)
	require.Equal(t, 1, coordinator.calls)
}
func TestVerifiedPostgresReadonlyRefusesUnverifiedModes(t *testing.T) {
	failed := modeRow(map[string]any{"transaction_read_only": "on"})
	failed.SetError(errors.New("failed row"))
	cases := map[string]*modeReader{
		"off":         {rows: []record.Record{modeRow(map[string]any{"transaction_read_only": "off"})}},
		"boolean":     {rows: []record.Record{modeRow(map[string]any{"transaction_read_only": true})}},
		"unknown":     {rows: []record.Record{modeRow(map[string]any{"transaction_read_only": "future"})}},
		"wrong field": {rows: []record.Record{modeRow(map[string]any{"default_transaction_read_only": "on"})}},
		"extra field": {rows: []record.Record{modeRow(map[string]any{"transaction_read_only": "on", "extra": true})}},
		"wrong data":  {rows: []record.Record{modeRow("on")}}, "no rows": {}, "nil row": {rows: []record.Record{nil}}, "failed row": {rows: []record.Record{failed}},
		"two rows":   {rows: []record.Record{modeRow(map[string]any{"transaction_read_only": "on"}), modeRow(map[string]any{"transaction_read_only": "on"})}},
		"next error": {nextErr: errors.New("SHOW failed")}, "close error": {rows: []record.Record{modeRow(map[string]any{"transaction_read_only": "on"})}, closeErr: errors.New("close failed")},
	}
	for name, reader := range cases {
		t.Run(name, func(t *testing.T) {
			coordinator := &modeCoordinator{tx: &modeTransaction{reader: reader}}
			err := RunVerifiedPostgresReadonly(context.Background(), coordinator, func(context.Context, dal.ReadTransaction) error { t.Fatal("unverified worker dispatched"); return nil })
			require.ErrorIs(t, err, ErrPostgresReadOnlyUnverified)
			require.Equal(t, 1, reader.closed)
		})
	}
}
func TestVerifiedPostgresReadonlyErrorsAndCancellation(t *testing.T) {
	worker := func(context.Context, dal.ReadTransaction) error { t.Fatal("worker dispatched"); return nil }
	for _, coordinator := range []dal.ReadTransactionCoordinator{nil, &modeCoordinator{}, &modeCoordinator{tx: &modeTransaction{}}, &modeCoordinator{tx: &modeTransaction{err: errors.New("SHOW failed")}}} {
		require.ErrorIs(t, RunVerifiedPostgresReadonly(context.Background(), coordinator, worker), ErrPostgresReadOnlyUnverified)
	}
	// Deliberately verify the public guard refuses a missing context.
	var missingContext context.Context
	require.Nil(t, missingContext)
	require.ErrorIs(t, RunVerifiedPostgresReadonly(missingContext, &modeCoordinator{}, worker), ErrPostgresReadOnlyUnverified)
	require.ErrorIs(t, RunVerifiedPostgresReadonly(context.Background(), &modeCoordinator{}, nil), ErrPostgresReadOnlyUnverified)
	beginErr := errors.New("begin failed")
	require.ErrorIs(t, RunVerifiedPostgresReadonly(context.Background(), &modeCoordinator{err: beginErr}, worker), beginErr)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	coordinator := &modeCoordinator{}
	require.ErrorIs(t, RunVerifiedPostgresReadonly(ctx, coordinator, worker), context.Canceled)
	require.Zero(t, coordinator.calls)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	reader := &modeReader{rows: []record.Record{modeRow(map[string]any{"transaction_read_only": "on"})}, closeHook: cancel}
	require.ErrorIs(t, RunVerifiedPostgresReadonly(ctx, &modeCoordinator{tx: &modeTransaction{reader: reader}}, worker), context.Canceled)
}
func TestVerifiedPostgresReadonlyRejectsLegacyReadWriteDowngrade(t *testing.T) {
	raw, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, raw.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin().WillReturnError(errors.New("sql: driver does not support read-only transactions"))
	mock.ExpectBegin()
	mock.ExpectQuery("SHOW transaction_read_only").WillReturnRows(sqlmock.NewRows([]string{"transaction_read_only"}).AddRow("off"))
	mock.ExpectRollback()
	db := dalgo2sql.NewDatabase(raw, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{})
	err = RunVerifiedPostgresReadonly(context.Background(), db, func(context.Context, dal.ReadTransaction) error { t.Fatal("downgraded worker dispatched"); return nil })
	require.ErrorIs(t, err, ErrPostgresReadOnlyUnverified)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestVerifiedPostgresReadonlyClosesPartialReaderOnQueryFailure(t *testing.T) {
	reader := &modeReader{closeErr: errors.New("cleanup failed")}
	coordinator := &modeCoordinator{tx: &modeTransaction{reader: reader, err: errors.New("query failed after allocation")}}
	err := RunVerifiedPostgresReadonly(context.Background(), coordinator, func(context.Context, dal.ReadTransaction) error { t.Fatal("worker dispatched"); return nil })
	require.ErrorIs(t, err, ErrPostgresReadOnlyUnverified)
	require.Equal(t, 1, reader.closed)
}
