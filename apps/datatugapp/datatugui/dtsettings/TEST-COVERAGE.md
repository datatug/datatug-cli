# TEST-COVERAGE.md — dtsettings

## Coverage metrics

| Run | Coverage % | Uncovered statements |
|-----|-----------|---------------------|
| Pre-run (baseline) | 0.0% | all |
| Post-run | 100.0% | 0 |

## Seams added

All seams are package-level `var` overrides in `settings_screen.go`:

| Seam var | Type | Purpose |
|----------|------|---------|
| `getLexerFn` | `func(string) chroma.Lexer` | Substitutes the chroma lexer; used to force `ColorizeYAMLForTview` to return an error |
| `getSettingsFn` | `func() (dtconfig.Settings, error)` | Wraps `dtconfig.GetSettings`; used to simulate config-read failures |
| `onTextViewReady` | `func(*tview.TextView)` | Called after `textView.SetInputCapture` is registered; used by tests to retrieve the `*tview.TextView` and drive the input-capture closure |
| `onBreadcrumbPushed` | `func(func() error)` | Called after the Settings breadcrumb is pushed; used by tests to capture and invoke the breadcrumb action closure |

## Documented gaps

None — 100% statement coverage achieved.
