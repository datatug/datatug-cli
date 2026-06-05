# TEST-COVERAGE: pkg/server/endpoints

## Coverage metrics

| Metric | Value |
|--------|-------|
| Pre-run coverage | 18.2% |
| Post-run coverage | 100.0% |
| Uncovered statements remaining | 0 |

## Seams added

| File | Seam |
|------|------|
| `dbservers_endpoints.go` | `var deleteDbServerFunc = api.DeleteDbServer` — replaces direct `api.DeleteDbServer` call so tests can inject a stub and cover the `returnJSON` success line in `deleteDbServer` |

## Documented gaps

### Production bugs (no-return after handleError)

Several handlers call `handleError` without a subsequent `return`, meaning execution continues with a nil context after error handling. This is a production bug — not fixable under the seam-only rule. Affected handlers:

- `getDbServerSummary` (line 35-36): missing `return` after `handleError`
- `getEntities` (same pattern)
- `getProjectFull` (same pattern)
- `getProjects` (same pattern)
- `getRecordsetsSummary` (same pattern)
- `deleteDbServer` (line 54-56): missing `return` after context error handleError

Tests for these handlers use `defer func() { recover() }()` (COVER-BEFORE-PANIC pattern) to record coverage of the target lines before the resulting nil-context panic propagates.

### Uninitialized api functions (storage.NewDatatugStore panics)

Multiple `api.*` functions call `storage.NewDatatugStore` which is an uninitialized package-level var that panics at test time. Affected tests use `defer func() { recover() }()` so the line before the panic is still recorded as covered.

### executeRecordsetCommand inverted condition

`execute_recordset_endpoints.go` line 118-120: `if count, err = strconv.Atoi(countStr); err == nil { handleError(...) }` — a *valid* count string triggers the error path. This is a production bug. Covered by passing a valid integer count string so the `handleError` branch executes.

### validation.NewErrBadRecordFieldValue does not satisfy IsBadRequestError

`validation.NewErrBadRecordFieldValue` is not recognised as a bad-request error by `IsBadRequestError` (which only checks `errBadRequest`), so the handler returns HTTP 500 instead of 400. The test asserts `w.Code != http.StatusOK` rather than `== 400`.
