# TEST-COVERAGE.md — dtapiservice

## Coverage metrics

| | Statements covered | Coverage % |
|---|---|---|
| Pre-run | 0 | 0.0% |
| Post-run | all | 100.0% |

Uncovered statements remaining: 0

## Seams added

| File | Seam |
|---|---|
| `api_service_ui.go` | `var newTextViewFunc = func() *tview.TextView { return tview.NewTextView() }` — lets tests intercept the textView created by `GoApiServiceMonitor` to drive its `SetInputCapture` closure. |

## Documented gaps

None — 100% statement coverage achieved.
