# Coverage metrics

| Metric | Value |
|---|---|
| Pre-run coverage | 0.0% |
| Post-run coverage | 100.0% |
| Uncovered statements remaining | 0 |

## Seams added

All seams are package-level `var` overrides in `ghauth.go`:

| Seam var | Type | Purpose |
|---|---|---|
| `httpClient` | `*http.Client` | Replaces inline `&http.Client{}` in `postJSON` so tests can inject a custom transport |
| `deviceCodeURL` | `string` | Replaces the hardcoded GitHub device-code URL so tests can redirect to an `httptest.Server` |
| `accessTokenURL` | `string` | Replaces the hardcoded GitHub access-token URL so tests can redirect to an `httptest.Server` |
| `newTicker` | `func(time.Duration) tickerIface` | Replaces `time.NewTicker` in `PollForToken` so tests can inject a fake ticker with a pre-buffered channel |
| `jsonMarshal` | `func(any) ([]byte, error)` | Replaces `json.Marshal` in `SaveToken` so tests can inject a forced marshal error |

`tickerIface` and `realTicker` were also added to `ghauth.go` to support the `newTicker` seam:
- `tickerIface`: interface with `C() <-chan time.Time`, `Stop()`, `Reset(time.Duration)`
- `realTicker`: wraps `*time.Ticker` to implement `tickerIface`

## Documented gaps

None — all statements are covered at 100%.
