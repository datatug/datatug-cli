# Test Coverage Patterns — datatug-cli

## Repo-specific patterns

### SQLite schemer test DB (`pkg/schemers/sqliteschema`)

The canonical in-memory SQLite fixture lives in `test_db_test.go` (`createTestDB()`). It creates a
multi-table schema (Country, Currency, User, Shop, Order, OrderDetails) with:
- single-column and composite FKs
- UNIQUE constraints
- composite primary keys
- a reserved-word table name (`[Order]`)

Use `createTestDB()` + `NewSchemaProvider(func() (*sql.DB, error) { return db, nil })` for any happy-path test.

### Three-level DB failure helpers (`pkg/schemers/sqliteschema/coverage_test.go`)

Three reusable helpers that cover the full error-injection space without sqlmock:

```go
// level 1: getSqliteDB itself returns an error — tested via newProviderWithErrDB
NewSchemaProvider(func() (*sql.DB, error) { return nil, sql.ErrConnDone })

// level 2: getSqliteDB succeeds but queries fail — tested via newProviderWithClosedDB
closed, _ := sql.Open("sqlite3", ":memory:")
_ = closed.Close()
NewSchemaProvider(func() (*sql.DB, error) { return closed, nil })

// level 3: live DB + real data — newProviderWithTestDB (wraps createTestDB)
```

Use these helpers before reaching for sqlmock; sqlmock is only needed for exotic scan/RowError paths.

### sqlmock RowError trick for `rows.Err()` path

`rows.Err()` is only non-nil if Next() returned `false` due to a driver error (not EOF).
To trigger it you *must* add a real row first, then attach the error to index 0:

```go
rows := sqlmock.NewRows(cols).
    AddRow(/* valid data */).
    RowError(0, errTest)   // fires when row 0 is read, causing Next()→false + Err()→errTest
```

Without the `AddRow` the driver takes the EOF path and `Err()` stays nil.

### sqlmock column-count mismatch for Scan errors

To force a `Scan` error without a real DB failure: declare fewer columns in `sqlmock.NewRows`
than the production code's `Scan(...)` call expects. E.g. PRAGMA table_info needs 6 columns;
providing 3 causes Scan to fail immediately.

### Inject unsupported `dbType` into `collectionsReader`

SQLite's `sqlite_schema.type` contains `"trigger"` rows that `NextCollection` doesn't handle.
Rather than building a special DB state, create the trigger in the test DB, then query
`sqlite_schema WHERE type = 'trigger'` and wrap the result in a bare `&collectionsReader{rows: rows}`.

### End-to-end SQLite scan via `scanDbCatalog` (`pkg/api`)

Use `t.TempDir()` + `sql.Open("sqlite3", path)` to create a file DB, populate it, close it,
then call `scanDbCatalog(server, params)` with:
```go
params := dbconnection.NewSQLite3ConnectionParams(dbPath, "main", dbconnection.ModeReadOnly)
server := datatug.ServerRef{Driver: dbconnection.DriverSQLite3, Host: "localhost"}
```
This exercises the full wire-up: connection → sqlite schemer → catalog output with tables,
columns, FKs, indexes, and alternate keys.

### `schemaProvider` unexported struct as seam (`pkg/schemers/sqliteschema`)

`NewSchemaProvider` returns the `schemer.SchemaProvider` interface, but many methods
(GetColumnsReader, GetConstraints, GetIndexes, …) are on `schemaProvider` (unexported).
Cast with `s.(schemaProvider)` to call them directly in package-internal tests, avoiding
the need to go through the public API for every branch.

### Testing `assertForeignKeys` with a bare `*testing.T{}`

`assertForeignKeys` is a test-helper that calls `t.Errorf`. To test its own failure branches
without failing the outer test, pass a fresh `inner := &testing.T{}` and check `inner.Failed()`
after the call.

### sqlmock `QueryMatcherEqual` option for `sqlinfoschema` providers

`sqlinfoschema` providers store a pre-built SQL string in a struct field and pass it verbatim.
Use `sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))` so that only the exact
SQL string matches; fall back to the default regex matcher for PRAGMA queries where the exact
string varies.

### `mock.MatchExpectationsInOrder(false)` for parallel fan-out

`InformationSchema.GetDatabase` calls `getColumns`, `getConstraints`, and `getIndexes` in
parallel. Disable sqlmock ordering with `mock.MatchExpectationsInOrder(false)` so the
expectations match whichever goroutine queries first.

### `parallel.Run` error-propagation testing

`GetDatabase` wraps three parallel sub-queries. To test that a failure in one sub-query is
surfaced: set up one `WillReturnError` for the sub-query under test and empty-row results for
the other two; assert the returned error contains the expected keyword.

### `schemer.SortedTables` as test fixture for `get*` methods

Internal `getColumns` / `getIndexes` / `getConstraints` methods take `schemer.SortedTables`.
Build a minimal fixture with:
```go
func makeTable(catalog, schema, name string, tableType datatug.CollectionType) *datatug.CollectionInfo {
    return &datatug.CollectionInfo{
        DBCollectionKey: datatug.NewCollectionKey(tableType, name, schema, catalog, nil),
    }
}
tables := []*datatug.CollectionInfo{makeTable("testdb", "dbo", "users", datatug.CollectionTypeTable)}
is := InformationSchema{db: db}
err = is.getColumns("testdb", schemer.SortedTables{Tables: tables})
```

### Seam: `schemaProvider` literal for MSSQL sub-provider tests

In `pkg/schemers/mssqlschema`, the internal `columnsProvider`, `constraintsProvider`, etc.
embed the corresponding `sqlinfoschema.*Provider` structs. Instantiate them directly with
the sqlmock `*sql.DB`:
```go
p := columnsProvider{ColumnsProvider: sqlinfoschema.ColumnsProvider{DB: db}}
```

### Headless TUI pattern for tview/tcell packages (`apps/datatugapp/...`, `pkg/sneatview/sneatnav`)

All TUI packages use `tcell.NewSimulationScreen` backed by `tview.NewApplication`. The canonical
helper — repeated across `dtapiservice`, `dtsettings`, `awsui`, `azureui`, `clouds_ui`,
`dtviewers`, `gcloudui`, etc. — is:

```go
func newSafeTUI(t *testing.T) *sneatnav.TUI {
    screen := tcell.NewSimulationScreen("UTF-8")
    if err := screen.Init(); err != nil { t.Fatalf(...) }
    app := tview.NewApplication().SetScreen(screen)
    root := sneatv.NewBreadcrumb("test", func() error { return nil })
    tui := sneatnav.NewTUI(app, root)
    t.Cleanup(func() { app.Stop() }) // app.Stop() calls screen.Fini internally; do NOT call screen.Fini again
    return tui
}
```

`pkg/sneatview/sneatnav/testing.go` exports `InvokeInputCapture(p, key, ch, mod)` for
driving widget key-handlers without importing tview/tcell directly from external test packages.

### `registerViewer` seam for cloud viewer packages (`awsui`, `azureui`, `gcloudui`)

Each viewer package exposes a `var registerViewer = dtviewers.RegisterViewer` seam. In tests,
override it to capture the registered `Viewer` struct, then invoke its `Action` closure to cover
the registration body without side-effects:

```go
orig := registerViewer
t.Cleanup(func() { registerViewer = orig })
var captured dtviewers.Viewer
registerViewer = func(v dtviewers.Viewer) { captured = v }
RegisterAsViewer()
// now drive: captured.Action(tui, sneatnav.FocusToMenu)
```

### `sync.Once` guard for `RegisterModule` in test binaries

Packages that call `RegisterModule()` (or any function that panics on duplicate registration)
must wrap the first call in a `var registerOnce sync.Once` and use `registerOnce.Do(...)` in
every test that needs it. This is required in `dtapiservice`, `dtsettings`, and `dtviewers`.

### `newTextViewFunc` / `newXxxFunc` seams for widget capture

Production code that constructs widgets via a package-level `var newTextViewFunc = tview.NewTextView`
seam lets tests intercept construction and grab the concrete widget:

```go
orig := newTextViewFunc
t.Cleanup(func() { newTextViewFunc = orig })
var captured *tview.TextView
newTextViewFunc = func() *tview.TextView {
    tv := tview.NewTextView()
    captured = tv
    return tv
}
```
Use `captured` to call `GetInputCapture()` and drive every branch of the installed handler.

### `panelList` reflect trick for `*tview.List` inside sneatv panels (`dtviewers`)

`sneatv.WithDefaultBorders` wraps a `*tview.List` inside a `PrimitiveWithBox` interface field.
To extract the list for direct manipulation (e.g. `SetCurrentItem`, `GetItemSelectedFunc`):

```go
func panelList(p sneatnav.Panel) *tview.List {
    panelElem := reflect.ValueOf(p).Elem()
    pwbField := panelElem.FieldByName("PrimitiveWithBox")
    // ... unwrap interface → struct → Primitive field → *tview.List
}
```
See `pkg/dtviewers/dtviewers_test.go` for the full implementation.

### `gcloudcmds` CLI seam pattern

`gcloudcmds` exports two seam vars (`getGCloudProjects`, `openGCloudProjectsScreen`). Build a
root `*cli.Command` wrapping `GoogleCloudCommand()`, override both seams, then call
`root.Run(context.Background(), argv)` to drive the full subcommand dispatch in-process.

### `keyring.MockInit()` for OAuth token storage tests (`pkg/auth/gauth`, `pkg/auth/ghauth`)

`github.com/zalando/go-keyring` has `keyring.MockInit()` and `keyring.MockInitWithError(err)`.
Call once per test (not per package) to redirect keyring operations to an in-memory store.
Use `MockInitWithError` to verify that saveRefreshToken errors are only logged, not returned.

### `fakeTicker` / `tickerIface` for polling loops (`pkg/auth/ghauth`)

`ghauth.PollForToken` uses a `var newTicker func(d) tickerIface` seam. Implement:

```go
type fakeTicker struct{ ch chan time.Time }
func newFakeTicker(ticks int) *fakeTicker {
    ft := &fakeTicker{ch: make(chan time.Time, ticks)}
    for i := 0; i < ticks; i++ { ft.ch <- time.Now() }
    return ft
}
func (f *fakeTicker) C() <-chan time.Time { return f.ch }
func (f *fakeTicker) Stop()               {}
func (f *fakeTicker) Reset(_ time.Duration) {}
```

Pre-fill the channel for success paths; use an unbuffered channel + cancelled context for the
timeout/cancel path.

### `fakeTypedDriver` for `ColumnTypeDatabaseTypeName` (`pkg/sqlexecute`)

`database/sql` does not expose `ColumnType.DatabaseTypeName` through sqlmock. Register a custom
`database/sql/driver` that implements `driver.RowsColumnTypeDatabaseTypeName`:

```go
type fakeTypedRows struct { rows []fakeRow; pos int }
func (r *fakeTypedRows) ColumnTypeDatabaseTypeName(index int) string { return r.rows[index].colDbType }
// ... Columns(), Close(), Next() boilerplate
```
Register named drivers once in `TestMain` via `sql.Register("fakename", &fakeTypedDriver{...})`.
This is the only way to test the `UNIQUEIDENTIFIER` → UUID conversion paths and the
sqlserver byte-swap branch in `executor.go`.

### `withFakeHandle` seam for `pkg/server/endpoints` handlers

`endpoints` uses a `var handle` function pointer and `var getContextFromRequest`. Override both
in tests to invoke the worker directly without a real HTTP pipeline:

```go
getContextFromRequest = func(r *http.Request) (context.Context, error) { return r.Context(), nil }
handle = func(w, r, dto, opts, status, getCtx, worker) {
    ctx, _ := getCtx(r)
    resp, err := worker(ctx)
    if err != nil { handleError(err, w, r); return }
    w.WriteHeader(status)
}
```

### `posthog` mock client and global-state backup pattern (`pkg/dtlog`)

`dtlog` stores global state in `mu`-protected vars (`ph`, `initialized`, `queue`,
`posthogDistinctID`). Tests that mutate these must backup-and-restore under `mu.Lock()`.
Implement `mockPosthogClient` satisfying `posthog.Client` with an `enqueued []posthog.Message`
slice to verify what gets queued during `postInitFlush`.

### Firestore iteration seams (`pkg/schemers/firestoreschema`)

`firestoreschema` injects four seam vars for Firestore client operations:
`firestoreDoc`, `firestoreCollections`, `iterCollectionNext`, `closeFirestoreClient`.
Override all four in `withSeams(t, refs, customDocSeam)` helper and restore with the returned
cleanup function. This avoids any live Firestore connection.

### `github.Client` backed by `httptest.Server` (`pkg/dtgithub`)

```go
func setupGHClient(t *testing.T) (*github.Client, *http.ServeMux) {
    mux := http.NewServeMux()
    server := httptest.NewServer(mux)
    t.Cleanup(server.Close)
    client := github.NewClient(nil)
    u, _ := url.Parse(server.URL + "/")
    client.BaseURL = u
    client.UploadURL = u
    return client, mux
}
```
Register route handlers on `mux` for the exact GitHub API paths under test.

### `stateSeams` backup/restore struct (`pkg/dtstate`)

`dtstate` has multiple package-level function vars (`getState`, `saveState`, `filePathFn`,
`osOpen`, `goAsync`, `appStop`). Create a `stateSeams` struct with a `backupSeams()` constructor
and a `restore()` method to save/restore all of them atomically per test. Also use `tempStateDir`
helper that sets `filePathFn` to a `t.TempDir()` path for isolation.

## Notes

- A nil-check guard is missing before the `defer rows.Close()` in `sqlinfoschema.getTables`;
  this prevents testing the `Query` error path at the `GetDatabase` level (production bug).
  Documented gap — do not try to write that test until the production code is fixed.
- `pkg/schemers/sqliteschema` tests are in `package sqliteschema` (white-box), giving full
  access to unexported types (`collectionsReader`, `columnsReader`, `foreignKeysReader`, etc.).
  All new schemer tests should follow the same white-box pattern.
- `app.Stop()` in tview calls `screen.Fini()` internally. Never call `screen.Fini()` separately
  in cleanup or the test will panic with a double-Fini. The `newSafeTUI` pattern above is the
  correct approach across all TUI packages.
