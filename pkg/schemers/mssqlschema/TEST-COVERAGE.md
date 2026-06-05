# Coverage metrics

| Metric | Value |
|--------|-------|
| Pre-run coverage | 13.3% |
| Post-run coverage | 96.7% |
| Uncovered statements remaining | 2 |

## Seams added

None. All production code is unchanged.

## Documented gaps

### whyType: defensive/unreachable

#### `NextCollection` — default branch (tables.go:96-97)

```go
default:
    collectionType = datatug.CollectionTypeUnknown
```

**Reason:** `strings.ToLower(dbType)` can yield a value that is neither `"table"` nor `"view"`, setting `collectionType = CollectionTypeUnknown`. However, the very next statement calls `datatug.NewCollectionKey(collectionType, ...)`, which panics unconditionally when `collectionType == CollectionTypeUnknown`. There is no way to exercise this `default` branch without triggering a downstream panic that aborts the test goroutine. The sqlmock library cannot help here because the panic occurs inside production code after `rows.Scan` succeeds.

**Refactor required:** `NewCollectionKey` would need to return an error instead of panicking on unknown types, allowing `NextCollection` to propagate the error rather than panic. Alternatively, the `default` branch could be removed and replaced with an explicit error return before calling `NewCollectionKey`.

### whyType: external-io (scan error path)

#### `NextCollection` — scan error branch (tables.go:106-108)

```go
if err != nil {
    return nil, fmt.Errorf("failed to scan table row into Table struct: %w", err)
}
```

**Reason:** This branch is reachable only when `rows.Scan(...)` returns an error. With `github.com/DATA-DOG/go-sqlmock v1.5.2`, values passed to `AddRow` are eagerly validated and converted via `driver.DefaultParameterConverter`; any value that would cause a scan-time error panics inside `AddRow` itself during test setup. There is no mechanism in this sqlmock version to inject a value that passes `AddRow` validation but fails `Scan`. Reaching this branch requires either a real database or a custom `database/sql/driver` implementation.

**Refactor required:** Upgrading to a newer sqlmock version (e.g., `github.com/DATA-DOG/go-sqlmock v1.5.2+` or v2) that supports lazy value injection, or switching to a hand-rolled `driver.Rows` stub that can return an error from `Scan`, would allow this branch to be covered without changing production code.
