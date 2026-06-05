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

## Notes

- A nil-check guard is missing before the `defer rows.Close()` in `sqlinfoschema.getTables`;
  this prevents testing the `Query` error path at the `GetDatabase` level (production bug).
  Documented gap — do not try to write that test until the production code is fixed.
- `pkg/schemers/sqliteschema` tests are in `package sqliteschema` (white-box), giving full
  access to unexported types (`collectionsReader`, `columnsReader`, `foreignKeysReader`, etc.).
  All new schemer tests should follow the same white-box pattern.
