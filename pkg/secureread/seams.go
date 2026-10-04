package secureread

import (
	"context"
	"database/sql"
	"os"

	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-core/pkg/apicontract"
)

var (
	collectRowsFn          = collectRows
	collectRowsFederatedFn = collectRows
	streamFederatedDTQLFn  = func(e *Executor, ctx context.Context, document []byte, sourceURLs map[string]string, variables map[string]any) (*FederatedStream, error) {
		return e.StreamFederatedDTQL(ctx, document, sourceURLs, variables)
	}
	osMkdirTemp            = os.MkdirTemp
	osChmod                = os.Chmod
	sqlOpenSnapshot        = sql.Open
	dbCloseSnapshot        = func(db *sql.DB) error { return db.Close() }
	restoredValidate       = func(rs *apicontract.Recordset) error { return rs.Validate() }
	beginTxSnapshot        = func(ctx context.Context, db *sql.DB) (*sql.Tx, error) { return db.BeginTx(ctx, nil) }
	prepareContextSnapshot = func(ctx context.Context, tx *sql.Tx, query string) (*sql.Stmt, error) {
		return tx.PrepareContext(ctx, query)
	}
	stmtExecSnapshot = func(ctx context.Context, stmt *sql.Stmt, args ...any) (sql.Result, error) {
		return stmt.ExecContext(ctx, args...)
	}
	sqlOpenNative = sql.Open
	// newNativeSQLDatabase is a seam over dalgo2sql.NewDatabase so a test can
	// see the options the native-SQL connection is wrapped with.
	newNativeSQLDatabase = dalgo2sql.NewDatabase
	pragmaQueryOnly      = func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, "PRAGMA query_only = ON")
		return err
	}
)
