# TEST-COVERAGE.md — dtviewers

Package: `github.com/datatug/datatug-cli/apps/datatugapp/datatugui/dtviewers`

## Coverage result

`go test -cover` reports **97.7%** of statements.  
`go tool cover -func` reports **100%** across all named functions.

The 2.3% gap is two package-level `var` function-literal default bodies that act as seams (see below).

## Seams added to production code

Two package-level `var` overrides were introduced to make side-effecting calls testable without live infrastructure:

| File | Seam variable | Replaced call |
|------|---------------|---------------|
| `viewers_screen.go` | `var screenOpened = func(id, name string) { dtlog.ScreenOpened(id, name) }` | `dtlog.ScreenOpened(...)` |
| `viewers_screen.go` | `var saveCurrentScreenPath = func(path string) { dtstate.SaveCurrentScreePath(path) }` | `dtstate.SaveCurrentScreePath(...)` |
| `viewers_init.go` | `var registerMainMenuItem = datatugui.RegisterMainMenuItem` | `datatugui.RegisterMainMenuItem(...)` (panics on duplicate IDs) |

## Documented gaps

### 1. Seam var default bodies

**Functions:** `screenOpened` default body, `saveCurrentScreenPath` default body  
**Location:** `viewers_screen.go` lines 14–15  
**Why not covered:** These are the production default values for the seam variables. Tests replace them with no-op stubs. Covering the defaults would require calling real `dtlog` / `dtstate` side effects or removing the seams — both defeat the purpose of the seam pattern.  
**Refactor required:** None. This is an inherent property of the seam pattern: the default body exists to serve production; tests always override it.

## What the tests cover

- `DbContextBase`: all four methods (`Name`, `Driver`, `Schema`, `GetDB`) — success and error paths.
- `NewSqlDBContext`: constructor field mapping; both `getDB` and `GetSqlDB` closure bodies.
- `GetSQLiteDbContext`: name derivation, tilde expansion, all three closures (outer `getSqlDB`, `getDB` lambda inside `NewSqlDBContext`, schema provider lambda via `schema.GetCollections`).
- `RegisterViewer`: appends to package-level slice.
- `RegisterModule`: calls the `registerMainMenuItem` seam with correct arguments.
- `GetViewersBreadcrumbs`: creates breadcrumbs and pushes the "Viewers" item; the pushed item's callback (`GoViewersScreen`) is invoked via `(*sneatv.Breadcrumbs).InputHandler()` with a synthetic `KeyEnter` event.
- `GoViewersScreen`: full path including seam overrides for `screenOpened` and `saveCurrentScreenPath`.
- `GetViewersListPanel`: no-viewers case, with-description case, all four `SetInputCapture` branches (ESC/Backtab/Left, KeyDown-at-last-item, KeyDown-not-at-last-item, KeyUp-at-first-item, KeyUp-not-at-first-item).
- `NewCloudsMenu`: empty viewers list, non-empty list with active viewer ID match, all `SetInputCapture` branches, `AddItem` callback (via `list.GetItemSelectedFunc(0)()`), `SetChangedFunc` callback (via `list.SetCurrentItem` which triggers the changed func internally).
