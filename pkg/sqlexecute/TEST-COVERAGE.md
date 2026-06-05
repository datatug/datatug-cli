# TEST-COVERAGE.md — pkg/sqlexecute

## Coverage metrics

| Run | Coverage | Uncovered statements |
|-----|----------|---------------------|
| Pre-run (baseline) | 0.7% | ~120 |
| Previous agent run | 94.9% | 7 |
| Post-run | 95.7% | 6 |

## Seams added

None. No production code was modified. The `db.Close()` error branch (executor.go:159-161) was
covered by registering a fake SQL driver (`dbclosetest`) whose `Conn.Close()` returns an error,
then routing `executeCommand` through that driver via `getDbByID`. No seam in production code
was required because the driver name is already injectable via the `getDbByID` callback.

## Documented gaps

All 6 remaining uncovered statements are structurally unreachable under the seam rule (would require refactoring production logic to inject failure points).

### error-path

**`executeQuery` — `rows.Close()` error in defer (executor.go:208-210)**
- `whyType`: error-path
- `reason`: By the time the defer runs, `sql.Rows` has already been auto-closed internally when `rows.Next()` returned `false` at EOF (the `database/sql` layer calls `rows.close()` on EOF). A subsequent `rows.Close()` call returns `nil` (guarded by `if rs.closed { return nil }`). Even a fake driver whose `Close()` returns an error cannot be reached from the defer because the rows object is already marked closed.
- `refactorRequired`: Inject an early-return path (e.g. via a `ColumnTypes` error) so the defer fires while rows are still open, or wrap `*sql.Rows` behind an interface so a test double can return a Close error.

**`executeQuery` — `rows.ColumnTypes()` error path (executor.go:213-215)**
- `whyType`: error-path
- `reason`: `rows.ColumnTypes()` only returns an error when `rs.closed == true` or `rs.rowsi == nil`. The rows are opened by `db.Query()` and `ColumnTypes()` is called immediately after with no intervening `Close()` call. A context-cancelled `QueryContext` errors at the Query call itself (not after), so rows are never returned in a closed state. No fake driver mechanism can trigger this path.
- `refactorRequired`: Accept a `context.Context` parameter in `executeQuery` and call `db.QueryContext`, then use a context that cancels asynchronously between Query return and ColumnTypes call — but this would be a race. Alternatively inject `*sql.Rows` behind an interface for test doubles.

**`executeQuery` — `rows.Scan` error path (executor.go:231-234)**
- `whyType`: error-path
- `reason`: `rows.Scan` is called with `*interface{}` destinations. `database/sql`'s `convertAssign` for `*any` destination always succeeds (`*d = src; return nil`). When a driver-level error occurs in `Next()`, `doClose=true` and `ok=false` so `rows.Next()` returns false before `Scan` is ever called. There is no reachable combination of inputs that causes `Scan` to fail with `*interface{}` destinations.
- `refactorRequired`: Check `rows.Err()` after the loop, or scan into typed destinations that can reject incompatible values.

### structural

**`RequestCommand.Validate` — `v.Port != 0` / "db & port" branch (models.go:77-79)**
- `whyType`: error-path
- `reason`: To reach the `if v.Port != 0` check, the code must have already passed `v.ServerRef.Validate()` (which requires `Driver != ""`), and `v.Host == ""` (otherwise the "db & host" check fires), and `v.Driver == ""` (otherwise the "db & driver" check fires). But `ServerRef.Validate()` fails when `Driver == ""`, making the combination `Driver==""` + pass impossible. No combination of valid inputs can reach line 77 without first triggering an earlier error.
- `refactorRequired`: Reorder the `RequestCommand.Validate` checks so the `DB`/`Port` conflict is tested before `ServerRef.Validate()`, or split `ServerRef.Validate()` into driver-present and connection-present sub-checks.
