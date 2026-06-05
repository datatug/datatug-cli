# Coverage metrics

| Metric | Value |
|--------|-------|
| Pre-run coverage | 20.8% |
| Post-run coverage | 94.4% |
| Uncovered statements remaining | 4 |

## Seams added

All seams are package-level `var` overrides in `datatug_state.go`:

| Seam var | File | Replaces |
|----------|------|---------|
| `filePathFn` | `datatug_state.go` | direct `getFilePath()` calls in `GetDatatugState` and `SaveState` |
| `osOpen` | `datatug_state.go` | `os.Open(filePath)` in `GetDatatugState` |
| `goAsync` | `datatug_state.go` | `go func()` launch in `BumpRecentProject` |
| `appStop` | `datatug_state.go` | `global.App.Stop()` calls in `SaveCurrentScreePathSync` and `SaveState` panic guards |

## Documented gaps

### os/env-dependent

**Function:** `SaveState` (lines 155–160) — `f.Close()` error body

**Reason:** The deferred `f.Close()` in `SaveState` must return an error to reach the `logus.Errorf` call and the `err = errClose` assignment. Go's `*os.File.Close()` never returns a synthesizable error through a simple package-level seam because `os.Create` returns a concrete `*os.File` — there is no injectable interface at the call site.

**Refactoring required:** Replace `os.Create` with an `osCreate` seam whose return type is an `io.WriteCloser` interface instead of `*os.File`. This requires changing `var f *os.File` to `var f io.WriteCloser` which is a type change to production logic beyond the permitted seam rule (it also affects `json.NewEncoder(f)` and the defer closure).

**whyType:** `os/env-dependent`
