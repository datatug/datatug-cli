# Coverage metrics

| Package | Pre-run | Post-run | Uncovered statements remaining |
|---------|---------|----------|-------------------------------|
| github.com/datatug/datatug-cli | 88.0% | 100.0% | 0 |

## Seams added

| File | Seam |
|------|------|
| main.go | `var logFatal = log.Fatal` — replaces the direct `log.Fatal(err)` call so tests can stub it without terminating the test binary |

## Previously uncovered branches and how they were covered

### `global.App != nil` branch (main.go:35-37)

**whyType:** defensive/unreachable

Set `global.App = tview.NewApplication()` before invoking `main()` with a panicking `getCommand` stub (returning nil triggers a nil-pointer panic in urfave/cli). `tview.Application.Stop()` is safe when `screen == nil` (returns early). Tested in `TestMainFunc/panic_with_app_non_nil`.

### `logFatal(err)` branch (main.go:67-69)

**whyType:** error-path

Added seam `var logFatal = log.Fatal` in production code and updated the call site to use it. In the test, `logFatal` is replaced with a capturing stub before calling `main()` with a `getCommand` whose Action returns an error. Tested in `TestMainFunc/cmd_run_error`.

### `getCommand` var body (main.go:86-88)

**whyType:** defensive/unreachable

Captured the original closure in `var defaultGetCommand = getCommand` at package init time (before any test reassigns the var), then called `defaultGetCommand()` directly. Tested in `TestMainFunc/default_getCommand_returns_non_nil`.

## Documented gaps

None — all branches are now covered.
