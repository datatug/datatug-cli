package secureread

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2sql"
	"github.com/datatug/datatug-cli/pkg/dbcopy"

	"modernc.org/sqlite"
)

const nativeReadOnlyDriver = "datatug-sqlite-readonly"

func init() {
	driver := &sqlite.Driver{}
	driver.RegisterConnectionHook(installReadOnlyAuthorizer)
	sql.Register(nativeReadOnlyDriver, driver)
}

// Every physical connection, including a replacement after cancellation, gets
// this policy. Unlike query_only alone, SQL cannot turn the authorizer off.
func installReadOnlyAuthorizer(conn sqlite.ExecQuerierContext, _ string) error {
	registerer, ok := conn.(sqlite.AuthorizerRegisterer)
	if !ok {
		return fmt.Errorf("SQLite driver has no read-only authorizer")
	}
	return registerer.RegisterAuthorizer(authorizeReadOnlySQL)
}

func authorizeReadOnlySQL(action sqlite.AuthorizerActionCode, arg1, arg2, _, _ string) sqlite.AuthorizerReturnCode {
	switch action {
	case sqlite.AuthRead, sqlite.AuthSelect, sqlite.AuthRecursive, sqlite.AuthTransaction, sqlite.AuthSavepoint:
		return sqlite.AuthorizerOK
	case sqlite.AuthFunction:
		switch strings.ToLower(arg2) {
		case "load_extension", "writefile":
			return sqlite.AuthorizerDeny
		default:
			return sqlite.AuthorizerOK
		}
	case sqlite.AuthPragma:
		switch strings.ToLower(arg1) {
		case "table_info", "table_xinfo", "index_list", "index_info", "index_xinfo", "foreign_key_list":
			return sqlite.AuthorizerOK
		case "query_only":
			if arg2 == "" || strings.EqualFold(arg2, "ON") || arg2 == "1" {
				return sqlite.AuthorizerOK
			}
		}
	}
	// Includes all writes, ATTACH, DETACH, VACUUM INTO's internal attachment,
	// schema changes and unknown future actions.
	return sqlite.AuthorizerDeny
}

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
// SQLite opens its main file with mode=ro, retains PRAGMA query_only, and
// installs a connection authorizer that rejects writes, attachments and
// disabling query_only. Multi-statement SQL cannot weaken this boundary.
//
// Only sqlite:// sources support native SQL today: dalgo2ingitdb executes
// only dal.StructuredQuery ("only StructuredQuery is supported"), and a
// postgres:// source takes structured queries only (its read-only session is
// for those, not for SQL text). A source without a SQL-text execution surface
// fails with ErrNativeSQLUnsupported.
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
	if ref.Scheme == "postgres" {
		return Result{}, fmt.Errorf("%w: PostgreSQL sources take structured queries only", ErrNativeSQLUnsupported)
	}
	if ref.Scheme != "sqlite" {
		return Result{}, fmt.Errorf("%w: scheme %q", ErrNativeSQLUnsupported, ref.Scheme)
	}
	// openReadOnlySQLite opens its own dedicated connection rather than going
	// through BackendRef.Open (see its doc comment), so it needs its own
	// existence check to surface dbcopy.ErrSourceFileMissing the same way
	// Open's sqlite branch does — see pkg/server/endpoints/exec_run_query.go
	// and util_error_handling.go for where that maps to SOURCE_UNAVAILABLE.
	if err := ref.CheckFile(); err != nil {
		return Result{}, err
	}
	db, sqlDB, closeDB, err := openReadOnlySQLiteDB(ctx, ref.Path)
	if err != nil {
		// The driver's own text can quote the DSN: say which source failed and
		// keep the cause for errors.Is and errors.As (see BackendRef.OpenFailure).
		return Result{}, ref.OpenFailure(err)
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
	result.ColumnOrder = selectListOrder(ctx, sqlDB, sqlText, args)
	return result, nil
}

// selectListOrder is the column names of a SELECT in the order its select list
// gives them. The reader hands each row over as a map, which has no order, so
// the statement is asked once for its columns with no rows (LIMIT 0 over it,
// on the same read-only connection). It answers nil when the text is not a
// single query that can be wrapped (a PRAGMA, several statements); Columns
// stays the sorted union either way.
func selectListOrder(ctx context.Context, db *sql.DB, sqlText string, args []dal.QueryArg) []string {
	text := strings.TrimRight(strings.TrimSpace(sqlText), "; \t\r\n")
	bound := make([]any, len(args))
	for i, arg := range args {
		bound[i] = arg.Value
		if arg.Name != "" {
			bound[i] = sql.Named(arg.Name, arg.Value)
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT * FROM ("+text+") LIMIT 0", bound...)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	names, _ := rows.Columns() // nil when the rows are unusable
	return names
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
	db, _, closeDB, err := openReadOnlySQLiteDB(ctx, path)
	return db, closeDB, err
}

// openReadOnlySQLiteDB is openReadOnlySQLite that also answers the raw
// connection the dal.DB wraps.
func openReadOnlySQLiteDB(ctx context.Context, path string) (dal.DB, *sql.DB, func(), error) {
	// The path is handed over as a "file:" URI: a bare path with a "?" in it is read by the
	// driver as the file before it and its parameters (see dbcopy.SQLiteFileURI).
	sqlDB, err := sqlOpenNative(nativeReadOnlyDriver, dbcopy.SQLiteFileURIMode(path, dbcopy.SQLiteReadOnly))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("secureread: open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // one physical connection: PRAGMA query_only must stick to the connection every query reuses
	if pingErr := sqlDB.PingContext(ctx); pingErr != nil {
		_ = sqlDB.Close()
		return nil, nil, nil, fmt.Errorf("secureread: open sqlite: %w", pingErr)
	}
	if execErr := pragmaQueryOnly(ctx, sqlDB); execErr != nil {
		_ = sqlDB.Close()
		return nil, nil, nil, fmt.Errorf("secureread: enable read-only session: %w", execErr)
	}
	// The sqlite structured-query dialect is set like on every other SQLite
	// open in this CLI, so no structured read can reach dalgo2sql's legacy
	// text emitter. RunNativeSQL — this connection's only caller — always
	// executes a dal.TextQuery, which the dialect does not touch.
	db := newNativeSQLDatabase(sqlDB, dal.NewSchema(nil, nil), dalgo2sql.DbOptions{StructuredQueryDialect: "sqlite"})
	return db, sqlDB, func() { _ = sqlDB.Close() }, nil
}
