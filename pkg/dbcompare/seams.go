package dbcompare

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/big"
	"os"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/recordops"
)

var (
	osMkdirTemp = os.MkdirTemp
	osOpenFile  = os.OpenFile
	osRemoveAll = os.RemoveAll
	fileClose   = func(f *os.File) error { return f.Close() }
	sqlOpen     = sql.Open
	dbClose     = func(db *sql.DB) error { return db.Close() }
	dbExec      = func(db *sql.DB, query string) (sql.Result, error) { return db.Exec(query) }
	txBegin     = func(ctx context.Context, db *sql.DB) (*sql.Tx, error) { return db.BeginTx(ctx, nil) }
	txPrepare   = func(ctx context.Context, tx *sql.Tx, query string) (*sql.Stmt, error) {
		return tx.PrepareContext(ctx, query)
	}
	stmtExecContext = func(ctx context.Context, stmt *sql.Stmt, args ...any) (sql.Result, error) {
		return stmt.ExecContext(ctx, args...)
	}
	stmtClose     = func(stmt *sql.Stmt) error { return stmt.Close() }
	txCommit      = func(tx *sql.Tx) error { return tx.Commit() }
	dbExecContext = func(ctx context.Context, db *sql.DB, query string) (sql.Result, error) {
		return db.ExecContext(ctx, query)
	}
	dbschemaListCollections    = dbschema.ListCollections
	dbschemaDescribeCollection = dbschema.DescribeCollection
	dbschemaListIndexes        = dbschema.ListIndexes
	recordopsDiff              = recordops.Diff[string]
	queryContext               = func(ctx context.Context, db *sql.DB, query string, args ...any) (*sql.Rows, error) {
		return db.QueryContext(ctx, query, args...)
	}
	dbExecuteQueryToRecordsReader = func(ctx context.Context, db dal.DB, q dal.Query) (dal.RecordsReader, error) {
		return db.ExecuteQueryToRecordsReader(ctx, q)
	}
	diffRowsFn         = diffRows
	jsonMarshal        = json.Marshal
	ratSetString       = func(r *big.Rat, s string) (*big.Rat, bool) { return r.SetString(s) }
	canonicalDecimalFn = canonicalDecimal
	floatString        = func(r *big.Rat, prec int) string { return r.FloatString(prec) }
	bigIntSetString    = func(z *big.Int, s string, base int) (*big.Int, bool) { return z.SetString(s, base) }
)
