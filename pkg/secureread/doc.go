// Package secureread is the policy-enforced read executor for the DataTug
// agent server (`datatug serve`). It runs structured, DTQL and native-SQL
// queries against any pkg/dbcopy-supported source through one fixed
// [Session]'s DALgo access policies, so every read the web UI can trigger
// goes through the same access-control path regardless of the query shape
// (Feature core-investigation-loop, REQ:server-acl-all-reads).
//
// # Design
//
// A [Session] is built once, for the whole `datatug serve` process lifetime,
// from the same --as/--role/--group/policy flags `datatug query run` already
// accepts (REQ:principal-selection): the server never re-binds its principal
// per request. An [Executor] is bound to that Session and reused across
// requests; each call names only the source URL and the query.
//
//   - RunStructured executes a dal.Query (typically built with
//     dal.NewQueryBuilder) through pkg/accesspolicies.Run: row conditions are
//     AND-ed into the query, field allow-lists redact each row, and a query
//     that explicitly references a field no policy allows is refused with
//     ErrAccessDenied rather than silently emptied (AC hidden-column-refused).
//   - RunDTQL deserializes a DTQL-YAML document (dal-go/dalgo/dtql) and runs
//     it exactly like RunStructured (REQ:dtql-query-type).
//   - RunNativeSQL executes raw SQL text read-only. DALgo cannot rewrite text
//     it does not parse, so per REQ:opaque-sql-limitation (assumption A2 in
//     the hub Feature) only the source-level allow/deny — an
//     access.OpaqueQueryScope rule — is enforced, never row conditions or
//     field allow-lists; the session is pinned read-only at the SQLite engine
//     level (PRAGMA query_only) and the Result is always stamped with a
//     LimitationNativeSQL entry. Only sqlite:// sources support native SQL
//     today — see RunNativeSQL's doc comment.
//
// Every Result carries the Limitations a caller must show, never apply
// silently (REQ:limitation-visible): which policy narrowed the rows, whether
// rows were filtered, which columns an implicit select hid, and whether row/
// column policy was skipped for opaque SQL.
//
// See README.md for the exact HTTP call sites this package is meant to back.
package secureread
