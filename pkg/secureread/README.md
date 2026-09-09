# pkg/secureread

Policy-enforced read executor for `datatug serve` (Phase 1 plan task 6,
executor half). Runs structured, DTQL and native-SQL queries against any
`pkg/dbcopy`-supported source through one fixed [`Session`](session.go)'s
DALgo access policies, so every read the web UI can trigger goes through the
same access-control path regardless of query shape
(`spec/features/core-investigation-loop/README.md`, REQ:server-acl-all-reads).

This package does not touch HTTP, `pkg/server`, `pkg/storage` or
`cmd_serve.go` — those are stream S1's lane. This README is the wiring
contract between the two: what S1 calls, with what, and what comes back.

## Quick shape

```go
session, err := secureread.NewSession(secureread.SessionOptions{
    As: asFlag, Roles: roleFlags, Groups: groupFlags, // same shape as `query run`
    PoliciesDir: policiesDirFlag, PolicyFiles: policyFlags, NoPolicies: noPoliciesFlag,
})
executor := secureread.NewExecutor(session) // build ONCE, for the server's whole life

result, err := executor.RunStructured(ctx, sourceURL, query, variables)
result, err := executor.RunDTQL(ctx, sourceURL, dtqlYAMLBytes, variables)
result, err := executor.RunNativeSQL(ctx, sourceURL, sqlText)
```

`sourceURL` is a `pkg/dbcopy` URL (`sqlite:///abs/path.db`, `ingitdb://./dir`)
— `datatug serve` already resolves a project's configured database connection
to a filesystem path; turning that into a `dbcopy` URL is S1's job, not
something this package can do without knowing the server's connection model.

`Result{Columns []string, Rows []Row{Key string, Data map[string]any},
Limitations []Limitation}` — see `result.go` for the full doc comments.
`Limitations` is what REQ:limitation-visible requires the web UI to show;
never drop it when shaping the HTTP response.

Errors: `errors.Is(err, secureread.ErrAccessDenied)` for a refusal (map to
HTTP 403, matching `cmd_query.go`'s `exitCodeAccessDenied` convention);
`errors.Is(err, secureread.ErrNativeSQLUnsupported)` for a source with no
SQL-text surface; `errors.Is(err, secureread.ErrNoPrincipal)` only ever
surfaces at `NewSession` time (server startup), never per-request.

## The three call sites

### 1. `GET /datatug/exec/select` (existing — `pkg/server/endpoints/execute_endpoints.go: executeSelectHandler`)

Today this handler builds an unsecured `api.SelectRequest{Project,
Environment, Database, From, SQL, Where, Limit, Parameters}` straight from
query-string parameters and never checks policy. It already has the right
shape to route to either executor method:

- `request.SQL != ""` → opaque SQL text → `Executor.RunNativeSQL(ctx,
  sourceURL, request.SQL)`.
- otherwise → structured, built from `request.From`/`request.Where` (a
  `dal.NewQueryBuilder(dal.From(dal.NewRootCollectionRef(request.From,
  ""))).Where(...).SelectColumns()`, mirroring
  `apps/datatugapp/commands/cmd_query.go`'s `buildQuery`) →
  `Executor.RunStructured(ctx, sourceURL, query, variablesFromParameters)`.

`request.Parameters []datatug.Parameter` becomes the `variables
map[string]any` argument (by `.ID`/`.Value`); `request.Limit` becomes
`dal.Query.Limit()` on the built query, same as `--from` already does in
`cmd_query.go`.

### 2. Saved-query run (by `QueryDef.ID`)

Not yet a separate handler; the nearest existing sibling is
`pkg/server/endpoints/query_endpoints.go`'s `getQueryHandler`, which loads a
project's `QueryDef` by ID from storage but does not execute it. Wiring this
means: load the `QueryDef`, branch on `QueryDef.Type`
(REQ:dtql-query-type — `DTQL` reads the `<id>.dtql.yaml` sidecar and calls
`Executor.RunDTQL`; the existing `SQL` type reads its query text and calls
`Executor.RunNativeSQL`), with the caller's bound parameter values as
`variables`.

### 3. `POST /datatug/exec/run_query` (new — named by AC
`restricted-rows-and-columns-server` and `dtql-query-runs`)

The HTTP surface for #2: request body names a `queryID` (or project-relative
query path) plus bound parameter values; the handler resolves the `QueryDef`
exactly as #2 describes and shapes the JSON response from the returned
`secureread.Result` — `columns`, `rows`, and `limitations` (with
`rowsFiltered`/`hiddenColumns`/`nativeSql` derived by filtering
`Result.Limitations` by `Kind`, per REQ:limitation-visible and AC
`restricted-rows-and-columns-server`'s exact field names
`limitations.rowsFiltered` / `limitations.hiddenColumns`).

## What this package deliberately does not decide

- **Source URL resolution.** How a project's configured database connection
  (environment + database name) becomes a `pkg/dbcopy` URL is S1's concern;
  this package only consumes the resulting URL string.
- **HTTP status/JSON shape.** `ErrAccessDenied` → 403, a `dbcopy.Parse`
  error → 400, everything else → 500 is a reasonable mapping (mirrors
  `cmd_query.go`'s exit codes) but is S1's call, not enforced here.
- **Related-record lookups (REQ:related-lookup-execution).** Those are
  server-built structured queries too and should go through
  `Executor.RunStructured` the same way, but building the FK-based lookup
  query itself is the semantic-resolution work of a different plan task —
  out of this stream's scope.
- **Non-sqlite native SQL.** `RunNativeSQL` supports `sqlite://` sources only
  today (see its doc comment for why); a future SQL-capable adapter for
  another scheme needs a new branch there, not a change at the call site.
