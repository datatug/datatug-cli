package watch

import (
	"context"
	"errors"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
	"github.com/dal-go/record/update"
)

var errWatchInjected = errors.New("injected Watch adapter failure")

// Tests delegate every successful operation to the actual DALGO SQLite handle.
// Only one selected adapter boundary fails, so transaction rollback is exercised.
type watchFaultControl struct {
	Kind, Table   string
	FailAt, Calls int
	Trace         []string
	Triggered     bool
	Err           error
}

func (c *watchFaultControl) hit(kind, table string) error {
	c.Trace = append(c.Trace, kind+":"+table)
	if c.Kind != "" && c.Kind != kind || c.Table != "" && c.Table != table {
		return nil
	}
	c.Calls++
	if c.FailAt > 0 && c.Calls == c.FailAt {
		c.Triggered = true
		if c.Err != nil {
			return c.Err
		}
		return errWatchInjected
	}
	return nil
}
func watchWrapFault(s *Store, c *watchFaultControl) func() {
	original := s.db
	s.db = &watchFaultDB{stateDatabase: original, Control: c}
	return func() { s.db = original }
}

type watchFaultDB struct {
	stateDatabase
	Control *watchFaultControl
}

func (db *watchFaultDB) Get(ctx context.Context, rec record.Record) error {
	if err := db.Control.hit("get", rec.Key().Collection()); err != nil {
		return err
	}
	return db.stateDatabase.Get(ctx, rec)
}
func (db *watchFaultDB) Exists(ctx context.Context, key *record.Key) (bool, error) {
	if err := db.Control.hit("exists", key.Collection()); err != nil {
		return false, err
	}
	return db.stateDatabase.Exists(ctx, key)
}
func (db *watchFaultDB) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	return watchFaultQuery(ctx, db.stateDatabase, q, db.Control)
}
func (db *watchFaultDB) RunReadwriteTransaction(ctx context.Context, worker dal.RWTxWorker, options ...dal.TransactionOption) error {
	if err := db.Control.hit("transaction", ""); err != nil {
		return err
	}
	return db.stateDatabase.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return worker(ctx, &watchFaultTx{ReadwriteTransaction: tx, Control: db.Control})
	}, options...)
}

type watchFaultTx struct {
	dal.ReadwriteTransaction
	Control *watchFaultControl
}

func (tx *watchFaultTx) Get(ctx context.Context, rec record.Record) error {
	if err := tx.Control.hit("get", rec.Key().Collection()); err != nil {
		return err
	}
	return tx.ReadwriteTransaction.Get(ctx, rec)
}
func (tx *watchFaultTx) Exists(ctx context.Context, key *record.Key) (bool, error) {
	if err := tx.Control.hit("exists", key.Collection()); err != nil {
		return false, err
	}
	return tx.ReadwriteTransaction.Exists(ctx, key)
}
func (tx *watchFaultTx) Insert(ctx context.Context, rec record.Record, options ...dal.InsertOption) error {
	if err := tx.Control.hit("insert", rec.Key().Collection()); err != nil {
		return err
	}
	return tx.ReadwriteTransaction.Insert(ctx, rec, options...)
}
func (tx *watchFaultTx) Update(ctx context.Context, key *record.Key, changes []update.Update, conditions ...dal.Precondition) error {
	if err := tx.Control.hit("update", key.Collection()); err != nil {
		return err
	}
	return tx.ReadwriteTransaction.Update(ctx, key, changes, conditions...)
}
func (tx *watchFaultTx) Delete(ctx context.Context, key *record.Key) error {
	if err := tx.Control.hit("delete", key.Collection()); err != nil {
		return err
	}
	return tx.ReadwriteTransaction.Delete(ctx, key)
}
func (tx *watchFaultTx) ExecuteQueryToRecordsReader(ctx context.Context, q dal.Query) (dal.RecordsReader, error) {
	return watchFaultQuery(ctx, tx.ReadwriteTransaction, q, tx.Control)
}
func watchFaultQuery(ctx context.Context, executor dal.QueryExecutor, q dal.Query, c *watchFaultControl) (dal.RecordsReader, error) {
	table := ""
	if structured, ok := q.(dal.StructuredQuery); ok {
		table = structured.From().Base().Name()
	}
	if err := c.hit("query", table); err != nil {
		return nil, err
	}
	reader, err := executor.ExecuteQueryToRecordsReader(ctx, q)
	if err != nil {
		return nil, err
	}
	return &watchFaultReader{RecordsReader: reader, Control: c, Table: table}, nil
}

type watchFaultReader struct {
	dal.RecordsReader
	Control *watchFaultControl
	Table   string
}

func (r *watchFaultReader) Next() (record.Record, error) {
	if err := r.Control.hit("next", r.Table); err != nil {
		return nil, err
	}
	return r.RecordsReader.Next()
}
func (r *watchFaultReader) Close() error {
	closed := r.RecordsReader.Close()
	if err := r.Control.hit("close", r.Table); err != nil {
		return err
	}
	return closed
}

type watchBusyError struct{}

func (watchBusyError) Error() string { return "injected SQLite busy" }
func (watchBusyError) Code() int     { return 5 }
