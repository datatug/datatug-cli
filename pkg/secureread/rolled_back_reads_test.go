package secureread

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2postgres"
	"github.com/dal-go/dalgo2sql"
	"github.com/dal-go/record"
	"github.com/datatug/datatug-cli/internal/pgstandin"
	"github.com/datatug/datatug-cli/pkg/dbcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// txDB is a dal.DB whose read-only transactions run on tx and report what their worker returned.
type txDB struct {
	dal.DB
	tx        dal.ReadTransaction
	beginErr  error
	gotWorker error
}

func (d *txDB) RunReadonlyTransaction(ctx context.Context, f dal.ROTxWorker, _ ...dal.TransactionOption) error {
	if d.beginErr != nil {
		return d.beginErr
	}
	d.gotWorker = f(ctx, d.tx)
	return d.gotWorker
}

// readTx is a read transaction whose query answers reader, or err.
type readTx struct {
	dal.ReadTransaction
	reader dal.RecordsReader
	err    error
}

func (t readTx) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	return t.reader, t.err
}

// erringReader is a records reader that fails on its first record.
type erringReader struct{ dal.RecordsReader }

func (erringReader) Next() (record.Record, error) { return nil, errors.New("the row is lost") }
func (erringReader) Close() error                 { return nil }

func TestRolledBackReads(t *testing.T) {
	query := dal.NewTextQuery("SELECT 1", nil)
	rows := []record.Record{record.NewRecordWithData(record.NewKeyWithID("t", "1"), map[string]any{"id": 1})}

	db := &txDB{tx: readTx{reader: dal.NewRecordsReader(rows)}}
	reader, err := rolledBackReads{DB: db}.ExecuteQueryToRecordsReader(context.Background(), query)
	require.NoError(t, err)
	assert.ErrorIs(t, db.gotWorker, errReadRolledBack, "the worker ends the transaction with a rollback")
	got, err := dal.ReadAllToRecords(context.Background(), reader)
	require.NoError(t, err)
	assert.Len(t, got, 1, "the rows are handed over from memory after the rollback")

	queryErr := errors.New("syntax error")
	_, err = rolledBackReads{DB: &txDB{tx: readTx{err: queryErr}}}.ExecuteQueryToRecordsReader(context.Background(), query)
	assert.ErrorIs(t, err, queryErr)

	_, err = rolledBackReads{DB: &txDB{tx: readTx{reader: erringReader{}}}}.ExecuteQueryToRecordsReader(context.Background(), query)
	assert.EqualError(t, err, "the row is lost")

	beginErr := errors.New("the server could not be reached")
	_, err = rolledBackReads{DB: &txDB{beginErr: beginErr}}.ExecuteQueryToRecordsReader(context.Background(), query)
	assert.ErrorIs(t, err, beginErr)
}

// PostgreSQL SQL text runs in a read-only transaction that is rolled back, whatever the query: what a function it
// calls changes in the session goes with the rollback.
func TestRunNativeSQL_PostgresSQLTextIsRolledBack(t *testing.T) {
	db, recorder := pgstandin.Recording(t, true)
	t.Cleanup(dbcopy.SetPostgresOpenerForTest(func(string, dal.Schema, dalgo2sql.DbOptions, ...dalgo2postgres.Option) (*dalgo2postgres.Database, error) {
		return db, nil
	}))
	unrestricted, err := NewSession(SessionOptions{NoPolicies: true})
	require.NoError(t, err)
	// The stand-in speaks the simple protocol only, so it refuses the query, which goes in the extended one: what it
	// records is the transaction around it, which begins read-only and is rolled back.
	_, err = NewExecutor(unrestricted).RunNativeSQL(context.Background(), pgSource, "SELECT 1")
	require.Error(t, err)
	statements := recorder.Statements()
	require.Len(t, statements, 2, "%q", statements)
	assert.Contains(t, strings.ToUpper(statements[0]), "BEGIN")
	assert.Contains(t, strings.ToUpper(statements[0]), "READ ONLY")
	assert.Equal(t, "ROLLBACK", strings.ToUpper(statements[1]))
}
