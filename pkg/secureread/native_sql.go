package secureread

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"

	_ "modernc.org/sqlite" // register the "sqlite" driver — the same one dalgo2sqlite uses
)

// nativeSQLLimitation is appended to every RunNativeSQL Result the session's
// policies govern (REQ:opaque-sql-limitation): row and column rules never
// apply to opaque SQL text — only the source-level allow/deny does — so a
// caller must always be told, not left to assume the usual row/column
// policies applied.
var nativeSQLLimitation = Limitation{
	Kind:   LimitationNativeSQL,
	Policy: "native-sql",
	Note:   "row/column policies not applied to native SQL",
}

// RunNativeSQL executes raw SQL text read-only against sourceURL and stamps
// the Result with LimitationNativeSQL. DALgo cannot rewrite text it does not
// parse, so per REQ:opaque-sql-limitation (assumption A2) only the
// source-level allow/deny is enforced: the query becomes a dal.TextQuery,
// whose access.Resource is access.OpaqueQuery(sqlText) — a policy needs an
// access.OpaqueQueryScope rule to allow it at all, and no policy's row
// condition or field allow-list is applied to the returned rows regardless
// of what accesspolicies.Explain reports for the collection-scoped rules.
//
// Before any of that, this process's own operator-level grant is checked
// first (Task 12, api-contract.md REQ:opaque-sql-limitation: "the support
// demo has no such grant"): unless the session is Unrestricted or was
// explicitly started with AllowOpaqueSQL (`datatug serve
// --allow-opaque-sql`), RunNativeSQL refuses with ErrOpaqueSQLNotGranted
// before doing anything else — this is what makes every native-SQL caller
// (exec/run_query and the legacy exec/select/exec/execute_commands routes,
// which all share one Executor/Session) obey the same boundary, rather than
// each endpoint needing its own check.
//
// args are the query's own bind values (dal.QueryArg{Name, Value}) — a named
// arg (Name != "") binds an "@name"/":name"/"$name" placeholder in sqlText;
// a positional arg (Name == "") binds an ordinary "?" placeholder, in order.
// They reach the database/sql driver exactly as given: dal-go/dalgo2sql
// v0.11.7+ converts a named dal.QueryArg to sql.Named(Name, Value) and a
// positional one to its bare Value (see dal-go/dalgo2sql#177 — a real bind,
// not string substitution into sqlText, so a value can never be mistaken
// for SQL syntax).
//
// The SQLite session is pinned read-only at the engine level with
// PRAGMA query_only on a dedicated, single-connection database handle, so
// even a multi-statement injection inside sqlText cannot write — this is
// stronger than the access-denied-by-default posture the ACL check alone
// gives a syntactically valid but policy-forbidden write attempt.
//
// Only sqlite:// sources support native SQL today: dalgo2ingitdb executes
// only dal.StructuredQuery ("only StructuredQuery is supported"), and
// postgres:// is not wired at all (dbcopy.ErrPostgresNotWired). A source
// without a SQL-text execution surface fails with ErrNativeSQLUnsupported.
// A future SQL-capable adapter for another scheme should add a read-only
// dal.DB.RunReadonlyTransaction branch alongside this PRAGMA one, per the
// brief's "PRAGMA query_only for SQLite; read-only tx elsewhere" design.
func (e *Executor) RunNativeSQL(ctx context.Context, sourceURL, sqlText string, args ...dal.QueryArg) (Result, error) {
	if !e.session.Unrestricted && !e.session.AllowOpaqueSQL {
		return Result{}, ErrOpaqueSQLNotGranted
	}
	ref, err := dbcopy.Parse(sourceURL)
	if err != nil {
		return Result{}, err
	}
	if ref.Scheme != "sqlite" {
		return Result{}, fmt.Errorf("%w: scheme %q", ErrNativeSQLUnsupported, ref.Scheme)
	}
	// openReadOnlySQLite opens its own dedicated connection rather than going
	// through BackendRef.Open (see its doc comment), so it needs its own
	// existence check to surface dbcopy.ErrSourceFileMissing the same way
	// Open's sqlite branch does — see pkg/server/endpoints/exec_run_query.go
	// and util_error_handling.go for where that maps to SOURCE_UNAVAILABLE.
	if err := dbcopy.CheckSourceFile(ref.Path); err != nil {
		return Result{}, err
	}
	db, closeDB, err := openReadOnlySQLite(ctx, ref.Path)
	if err != nil {
		return Result{}, err
	}
	defer closeDB()

	query := dal.NewTextQuery(sqlText, nil, args...)
	result, err := e.runThroughPolicies(ctx, db, query, nil)
	if err != nil {
		return Result{}, err
	}
	if !e.session.Unrestricted {
		result.Limitations = append(result.Limitations, nativeSQLLimitation)
	}
	return result, nil
}

// openReadOnlySQLite opens a dedicated, single-connection *sql.DB against
// path and pins it read-only with PRAGMA query_only before any caller SQL
// runs on it. A dedicated connection — not dalgo2sqlite's own pool — is
// required because query_only is a per-connection SQLite setting and
// dalgo2sqlite exposes no handle to set it on its pool from outside the
// package. Wrapping the raw *sql.DB with dalgo2sql.NewDatabase (the same
// adapter dalgo2sqlite itself composes) reuses its tested TextQuery
// execution and row-to-record conversion rather than re-implementing it.
func openReadOnlySQLite(ctx context.Context, path string) (dal.DB, func(), error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, fmt.Errorf("secureread: open sqlite %q: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1) // one physical connection: PRAGMA query_only must stick to the connection every query reuses
	if pingErr := sqlDB.PingContext(ctx); pingErr != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("secureread: open sqlite %q: %w", path, pingErr)
	}
	if _, execErr := sqlDB.ExecContext(ctx, "PRAGMA query_only = ON"); execErr != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("secureread: enable read-only session on %q: %w", path, execErr)
	}
	// DbOptions{} deliberately leaves StructuredQueryDialect unset:
	// dalgo2sql v0.12.0's "sqlite" dialect only changes how a
	// dal.StructuredQuery compiles (getReaderBaseWithDialect), and
	// RunNativeSQL — this connection's only caller — always executes a
	// dal.TextQuery (see RunNativeSQL above), so the option would be a
	// no-op here even if set. See dbcopy.BackendRef.OpenProtected's doc
	// comment for why datatug-cli does not opt structured reads into that
	// dialect anywhere yet.
	db := dalgo2sql.NewDatabase(sqlDB, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{})
	return db, func() { _ = sqlDB.Close() }, nil
}
