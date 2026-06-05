# TEST-COVERAGE.md

## Coverage metrics

| Run | Coverage % | Uncovered statements |
|-----|-----------|----------------------|
| Pre-run (baseline) | 0.4% | ~115 |
| Post-run | 98.3% | 4 |

## Seams added

None. All coverage was achieved by in-package tests injecting a mock `*sql.DB`
directly into the unexported `db` field of `InformationSchema` (possible because
the test file is in the same package).

## Documented gaps

### whyType: external-io — nil-rows panic (production bug)

**Functions:** `getTables` (schemer.go:88-90), `GetDatabase` (schemer.go:31-33)

**Reason:** `getTables` executes `defer rows.Close()` (line 86) *before* checking
`err` (line 88). When `db.Query` returns an error, `rows` is nil and the deferred
`rows.Close()` call panics with a nil-pointer dereference. This means the
`failed to query INFORMATION_SCHEMA.TABLES` error branch (line 89) and the
corresponding `failed to retrieve tables metadata` wrap in `GetDatabase` (line 32)
are both unreachable from a test without causing a panic.

**Refactoring required:** Guard the defer with a nil-check — e.g.:
```go
defer func() {
    if rows != nil {
        _ = rows.Close()
    }
}()
```
or move the defer to after the error check.

### whyType: external-io — unreachable goto branch (dead code)

**Function:** `getConstraints` (schemer.go:210-212)

**Reason:** Lines 210-212 are the `goto fkAddedToRefByTable` branch, reached when
`fk2.Name == fk.Name` is found in `refByTable.ForeignKeys`. This can only fire if
the same FK name appears a second time via the *new-FK* else-branch (line 173),
which requires `table.ForeignKeys[last].Name != constraint.Name`. However,
`SortedTables.SequentialFind` advances its cursor and never re-finds the same
table, so any second row for the same source table is silently skipped (returns nil).
The condition is therefore structurally unreachable with the current `SortedTables`
finder.

**Refactoring required:** Replace `SortedTables` with a map-based finder that
allows re-lookup of the same table, or split the FK append-column logic so that
the `refByTable` FK lookup is also performed on the append path.
