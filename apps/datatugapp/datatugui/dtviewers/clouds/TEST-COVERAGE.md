# TEST-COVERAGE.md — clouds

## Coverage metrics

| Metric | Value |
|--------|-------|
| Pre-run coverage | 0.0% |
| Post-run coverage | 100.0% |
| Uncovered statements remaining | 0 |

## Seams added

| File | Seam |
|------|------|
| `clouds_ui.go` | `var newTextViewFunc = tview.NewTextView` — lets tests intercept the created `*tview.TextView` so the `SetInputCapture` closure branches can be driven directly. |

## Documented gaps

None — all branches covered.
