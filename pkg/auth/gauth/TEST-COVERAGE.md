# Coverage metrics

| Metric | Value |
|--------|-------|
| Pre-run coverage | 38.8% |
| Post-run coverage | 57.8% |
| Uncovered statements remaining | 37 |

# Seams added

| File | Seam var | Replaces |
|------|----------|---------|
| `authenticate.go` | `var getTokenFromWebFn = getTokenFromWeb` | direct `getTokenFromWeb(ctx, cfg)` call in `StartInteractiveLogin` |
| `file_store.go` | `var userConfigDir = os.UserConfigDir` | direct `os.UserConfigDir()` call in `DefaultFilepath` |
| `file_store.go` | `var osWriteFile = os.WriteFile` | direct `os.WriteFile(...)` call in `Save` |
| `file_store.go` | `var jsonMarshalIndent = json.MarshalIndent` | direct `json.MarshalIndent(...)` call in `Save` |

# Documented gaps

## external-io — real OAuth / browser flow

### `getGoogleCloudClient` (authenticate.go:26–78)
**Uncovered statements:** 23

**Reason:** Builds a real `oauth2.Config`, calls `GetRefreshToken` (covered separately via
`keyring.MockInit`), then drives a live token-source `ts.Token()` refresh against Google's
endpoint, and falls back to `getTokenFromWeb` (a browser-based flow).  The function cannot
be exercised in a unit test without either (a) injecting the whole `oauth2.Config` / token
source or (b) injecting both the "get refresh token" and "get token from web" steps as
package-level vars.

**Refactoring required:**
- Extract the `oauth2.Config` construction into a factory var or parameter.
- Replace `ts.Token()` with an injectable `tokenSourceFn` seam.
- Replace the `getTokenFromWeb` call with `getTokenFromWebFn` (seam already present for `StartInteractiveLogin`; `getGoogleCloudClient` still calls the raw function directly).
- Then table-test the refresh-success, refresh-failure, and nil-token paths.

---

### `getTokenFromWeb` (authenticate_with_browser.go:15–36)
**Uncovered statements:** 10

**Reason:** Calls `browser.OpenURL` (platform GUI) and then `waitForAuthCode` (spins up a
real `:8080` HTTP server).  Additionally, line 33 uses `log.Fatalf` on a token-exchange
error, which exits the process — a returned error cannot be asserted in a test.

**Refactoring required:**
- Replace `browser.OpenURL` with a `var openURLFn = browser.OpenURL` seam.
- Replace `waitForAuthCode()` with a `var waitForAuthCodeFn = waitForAuthCode` seam.
- Change `log.Fatalf` on exchange error to `return nil, err` so the error path is
  testable without process exit.
- Inject `config.Exchange` via a seam or interface.

---

### `waitForAuthCode` (authenticate_with_browser.go:39–61)
**Uncovered statements:** 11 (shared count with `getTokenFromWeb` above in the profile)

**Reason:** Binds `http.Server` to hard-coded `:8080`, registers a global
`http.HandleFunc`, and blocks on a channel until a real browser redirect arrives.

**Refactoring required:**
- Accept an `addr string` (or `net.Listener`) parameter so tests can bind to `:0`
  (ephemeral port) and send a synthetic `GET /oauth2callback?code=test` via
  `httptest` or `net/http` client.
- Stop registering on the global `http.DefaultServeMux`; use a per-call `http.ServeMux`
  to avoid cross-test pollution.

---

### `GetGCloudProjects` (get_projects.go:13–37)
**Uncovered statements:** 14 (includes `log.Fatalf` branch)

**Reason:** Calls `getGoogleCloudClient` (real OAuth) and then issues live Cloud Resource
Manager API calls.  Additionally, line 25 uses `log.Fatalf` on service-creation error,
preventing the error from being returned.

**Refactoring required:**
- Replace `getGoogleCloudClient` call with a `var getClientFn = getGoogleCloudClient` seam
  that accepts an injectable `*http.Client`.
- Change `log.Fatalf` to `return nil, err` so that path is testable.
- Inject `cloudresourcemanager.NewService` via a factory var so an `httptest.Server`
  returning canned `SearchProjectsResponse` JSON can be used.

## error-path — dead code

### `ensureDir` Validate branch (file_store.go:52–54)
**Uncovered statements:** 1

**Reason:** `ensureDir` is only called from `Save`, which itself calls `s.Validate()`
first and returns on error.  Therefore the `Validate` check inside `ensureDir` is
structurally unreachable without bypassing `Save`.

**Refactoring required:** Either remove the redundant `Validate` call from `ensureDir`
(making it a pure directory-creation helper) or make `ensureDir` exported/test-accessible
so a test can call it directly with `FileStore{Filepath: ""}`.
