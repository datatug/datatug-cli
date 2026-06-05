# TEST-COVERAGE.md — sqliteschema

## Coverage metrics

| Run | Coverage | Uncovered statements |
|-----|----------|----------------------|
| Pre-run (baseline) | 26.4% | ~58 |
| Previous run | 92.0% | 21 |
| Post-run | 100.0% | 0 |

## Seams added

None. No production-code seams were required or added.

## Approach

All remaining error-path branches were covered using `github.com/DATA-DOG/go-sqlmock v1.5.2`
to inject mock `*sql.DB` instances that return controlled errors.

Key technique: `RowError(row int, err error)` only fires when a row at the given
index exists in the mock rows set. For `rows.Err()` branches (which execute after
`for rows.Next()` exhausts), a real row must be `AddRow`-ed first, then
`RowError(0, errTest)` applied — this makes the driver return the error while
reading that row, causing `database/sql`'s `rows.Next()` to return `false` and
store the error so `rows.Err()` returns it.

Scan-error branches were covered by supplying fewer columns than the `Scan` call
expects (e.g. 2-column rows for an 8-destination `Scan`).

For `uniqueConstraintIndexes` and `indexColumnNames`, which accept `*sql.DB`
directly as a parameter, they were called directly with a sqlmock DB that
returns errors or mismatched rows.

## Documented gaps

None — all branches are now covered.
