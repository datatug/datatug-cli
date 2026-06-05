# Coverage metrics

| Run | Coverage % | Uncovered statements |
|-----|-----------|----------------------|
| Pre-run (baseline) | 60.3% | ~17 |
| Post-run | 99.2% | 1 |

## Seams added

All seams are package-level `var` overrides in `posthog.go`:

| Seam var | File | Replaces |
|----------|------|----------|
| `getPostHogApiKeyFromServerFunc` | `posthog.go` | direct call to `getPostHogApiKeyFromServer()` in `getPostHogClient` |
| `posthogNewWithConfig` | `posthog.go` | direct call to `posthog.NewWithConfig(...)` in `getPostHogClient` |
| `osCreate` | `posthog.go` | direct call to `os.Create(...)` in `writePostHogConfigToFile` |
| `httpDoRequest` | `posthog.go` | direct call to `http.DefaultClient.Do(req)` in `getPostHogApiKeyFromServer` |
| `posthogAPIKeyURL` | `posthog.go` | hard-coded const URL string in `getPostHogApiKeyFromServer` |
| `newYamlEncoder` | `posthog.go` | direct call to `yaml.NewEncoder(file)` in `writePostHogConfigToFile` |
| `postInitFlush` | `posthog.go` | inline goroutine body in `init()` that sets `ph`, `initialized`, drains `queue` |

## Documented gaps

### defensive/unreachable — error-path

**Function:** `getPostHogClient` (posthog.go ~line 107)

**Branch:** `logus.Errorf(ctx, "Failed to write PostHog config file: %v", err)` inside
`if err := writePostHogConfigToFile(ctx, config); err != nil { ... }`

**Reason:** `writePostHogConfigToFile` always returns `nil` — the function body ends with
`return nil` unconditionally. Therefore the `err != nil` guard in the caller is permanently
false and the log line is structurally dead code.

**Refactoring required:** `writePostHogConfigToFile` would need to be changed to propagate
errors from `os.Create` or `yaml.Encoder.Encode` instead of swallowing them and always
returning `nil`. That is a behaviour change, not a seam, and is outside the permitted scope
of this coverage pass.
