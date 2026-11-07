---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Web UI

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/web-ui?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/web-ui?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/web-ui?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/web-ui?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug web-ui` installs and manages local copies of the compiled DataTug web UI, which [`datatug serve --web-ui`](../serve/README.md) serves from the agent's own origin so the web UI works on a machine with no internet. The contracts (archive format, manifest, compatibility, trust model, browser hardening, offline proof) are the cross-repository Feature [`local-frontend-distribution`](https://github.com/datatug/backstage/blob/main/spec/features/local-frontend-distribution/README.md) in datatug/backstage; this page is the CLI's command surface. Code: `pkg/webui`, `apps/datatugapp/commands/cmd_web_ui.go`, `serve_web_ui.go`.

## Synopsis

```
datatug web-ui install <datatug-web-ui-<version>.tar.gz> [--sha256 <digest>] [--allow-unsigned]   # offline
datatug web-ui install --latest                                               # downloads (internet)
datatug web-ui update                                                         # same as install --latest
datatug web-ui list
datatug web-ui use <version>       # pin
datatug web-ui unpin
datatug web-ui rollback            # pin the previously used version
datatug web-ui remove <version>
datatug serve --web-ui[=<version>] [--ui-host <name>] [--ui-connect-src <https-origin>]...
```

## Behavior

#### REQ: offline-install

`install <archive>` MUST NOT touch the network. It verifies the archive's SHA-256 against `--sha256`, else an adjacent `<archive>.sha256`, else none (and says so), then every file against `manifest.json`, then the release signature (REQ: release-signature), then publishes `~/datatug/web-ui/versions/<version>/` atomically. Output names where the expected digest came from and who signed the archive.

#### REQ: release-signature

An archive MUST carry `manifest.json.sig` (`datatug-web-ui-signature/1 ed25519 <key id> <base64>`): an Ed25519 signature over the exact bytes of `manifest.json` by a key compiled into this CLI (`pkg/webui/signing.go`; key id = first 16 hex characters of the SHA-256 of the raw public key). A signature that does not verify is always refused. A missing signature, or one from a key this CLI does not know, is refused unless `--allow-unsigned` (a local or test build); the output then says the archive is NOT signed by a DataTug release key. `install --latest` / `update` never allow unsigned archives.

#### REQ: explicit-download

Only `install --latest` and `update` download, from GitHub Releases of `datatug/web-ui`: `releases/latest/download/manifest.json` first (no API call), then the releases list when the latest is not servable by this CLI. The digest comes from the release's `.sha256` asset.

#### REQ: storage

Installed versions live in `~/datatug/web-ui` (`$DATATUG_WEB_UI_DIR` overrides): `versions/<version>/` and `state.json` (`pinned`, `active`, `previous`). Not the OS cache directory: a cache cleaner must never remove the only copy on a disconnected machine.

#### REQ: compatibility

`LocalAPIVersion` (`pkg/webui/compat.go`, currently `1.0`) is this CLI's local API contract version; `MinFrontendVersion` (`0.1.0`) the oldest web UI it serves. A web UI is served only when its `requires.localApi` range holds this CLI's version and its own version is at least the floor. `list` shows each version's status.

#### REQ: serve-web-ui

`serve --web-ui` selects `--web-ui=<version>`, else the pinned version, else the newest compatible one; a requested or pinned version that is missing or incompatible stops serve with the reason (no silent fallback); nothing installed stops serve with both install commands and no network call. The chosen version is re-verified before serving.

#### REQ: ui-host

The page is served on `http://datatug-app.localhost:<port>` (`--ui-host` or `server.uiHost` in `~/.datatug.yaml`; only `localhost`, `127.0.0.1` or a `*.localhost` name), and a `http://127.0.0.1:<port>` link is printed beside it. `--web-ui` refuses a non-loopback `--host`.

## Privacy

With `serve --web-ui`, the page carries a Content-Security-Policy whose `connect-src` is `'self'`: the browser can reach only the agent that served it. No telemetry (Sentry, Google Analytics, PostHog) starts in local mode. The CLI itself makes no network request for the web UI except `install --latest` / `update`. Database connection details, schema, query text, results, Perimeter policies and activity stay on the machine unless you add an origin with `--ui-connect-src` (for example a hosted AI provider) and use that feature. Features that need the cloud (sign-in, cloud projects, GitHub projects, public demo databases, hosted AI) are not available in local mode.

## Acceptance Criteria

### AC: offline-journey

**Requirements:** web-ui#req:offline-install, web-ui#req:serve-web-ui, web-ui#req:ui-host

Under OS-level network isolation (datatug-apps `apps/datatug-app/playwright.local-web-ui.config.ts`, run inside `tools/package-web-ui/offline.sb`), installing an archive and serving it lets a browser open a project, browse a SQLite schema and records under Perimeter, run TugQL, see a direct API bypass refused, and continue after a restart, with no non-loopback request and no CSP violation.

### AC: hostile-archives-and-requests

**Requirements:** web-ui#req:offline-install, web-ui#req:serve-web-ui

`pkg/webui` and `pkg/server` tests refuse traversal, link, device, duplicate, tampered, extra, missing and oversized archive entries with nothing installed, and refuse foreign Host headers and foreign origins.

## Open Questions

- Who holds the release-signing key (Feature `local-frontend-distribution` F1)?

---
*This document follows the https://specscore.md/feature-specification*
