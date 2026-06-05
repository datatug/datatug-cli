# TEST-COVERAGE.md — sqliteschema

## Coverage metrics

| Run | Coverage | Uncovered statements |
|-----|----------|----------------------|
| Pre-run (baseline) | 26.4% | ~58 |
| Post-run | 92.0% | 21 |

## Seams added

None. No production-code seams were required or added.

## Documented gaps

### whyType: error-path — rows.Err() and mid-iteration scan errors

All remaining uncovered statements are error-handling branches inside `for rows.Next()` loops that check `rows.Err()` after the loop or scan column values into variables. With an in-process SQLite (`:memory:`) driver these errors are structurally unreachable: the `mattn/go-sqlite3` driver never sets `rows.Err()` to a non-nil value after successful iteration, and column scan errors from a correctly-typed PRAGMA result cannot be induced without changing the query itself.

**Refactoring required**: replace the direct `db.Query(...)` calls inside each function with an injectable `queryFunc` variable (package-level `var queryFn = db.Query`) so tests can substitute a fake that returns an `*sql.Rows`-like object whose `Err()` returns a sentinel error, or replace `*sql.DB` with a `dbQuerier` interface that can be mocked.

Affected lines and functions:

#### collections.go

| Lines | Function | Branch |
|-------|----------|--------|
| 59–61 | `NextCollection` | `rows.Err() != nil` after exhausted rows loop |
| 72–74 | `NextCollection` | scan error (wrong column count / type) |

#### columns.go

| Lines | Function | Branch |
|-------|----------|--------|
| 53–56 | `NextColumn` | `rows.Err() != nil` after exhausted rows loop |
| 73–75 | `NextColumn` | scan error |

#### constraints.go

| Lines | Function | Branch |
|-------|----------|--------|
| 36–39 | `GetConstraints` | FK scan error in `fkRows.Next()` loop |
| 52–55 | `GetConstraints` | `fkRows.Err() != nil` after FK loop |
| 60–62 | `GetConstraints` | `uniqueConstraintIndexes` returns error |
| 65–67 | `GetConstraints` | `indexColumnNames` returns error |
| 82–84 | `uniqueConstraintIndexes` | `db.Query` error (index_list) |
| 90–92 | `uniqueConstraintIndexes` | scan error inside index_list loop |
| 102–104 | `indexColumnNames` | `db.Query` error (index_info) |
| 110–112 | `indexColumnNames` | scan error inside index_info loop |

Note: `uniqueConstraintIndexes` and `indexColumnNames` query errors (lines 82–84, 102–104) are only reachable from `GetConstraints` after `getSqliteDB()` has already succeeded and returned a valid `*sql.DB`. A closed DB is tested for `GetConstraints` itself (which fails at the initial `db.Query`), but the helpers receive the already-obtained `*sql.DB` and their own Query calls would only fail if the DB closes between the outer and inner calls — not possible in a single-goroutine test.

#### foreign_keys.go

| Lines | Function | Branch |
|-------|----------|--------|
| 65–67 | `NextForeignKey` | scan error inside rows loop |
| 89–91 | `NextForeignKey` | `rows.Err() != nil` after rows loop |

#### index_columns.go

| Lines | Function | Branch |
|-------|----------|--------|
| 29–31 | `GetIndexColumns` | scan error inside rows loop |
| 38–40 | `GetIndexColumns` | `rows.Err() != nil` after rows loop |

#### indexes.go

| Lines | Function | Branch |
|-------|----------|--------|
| 29–31 | `GetIndexes` | scan error inside rows loop |
| 45–47 | `GetIndexes` | `rows.Err() != nil` after rows loop |

#### referrers.go

| Lines | Function | Branch |
|-------|----------|--------|
| 30–32 | `GetReferrers` | scan error in table-name rows loop |
| 35–37 | `GetReferrers` | `rows.Err() != nil` after table-name loop |
| 42–44 | `GetReferrers` | `GetForeignKeys` error inside per-table loop |

The `GetReferrers` lines 42–44 are reachable only if the DB closes between the initial `sqlite_schema` query and the per-table `PRAGMA foreign_key_list` queries — not possible with an in-process driver.

