# TEST-COVERAGE.md — azureui

## Coverage metrics

| Metric | Value |
|--------|-------|
| Pre-run coverage | 0.0% |
| Post-run coverage | 100.0% |
| Uncovered statements remaining | 0 |

## Seams added

| File | Seam |
|------|------|
| `home_ui.go` | `var registerViewer = dtviewers.RegisterViewer` — lets tests intercept viewer registration and capture the `Viewer.Action` closure so its body (which delegates to `GoAzureHome`) can be invoked directly. |

## Documented gaps

None — all branches covered.
