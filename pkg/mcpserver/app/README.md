# Embedded query explorer

This browser resource is embedded by `pkg/mcpserver/apps.go`. The release binary serves all HTML, CSS and JavaScript without Node or network assets. It uses the published official `@modelcontextprotocol/ext-apps` App lifecycle and transport; application code does not implement a postMessage bridge. Real official-host/browser and released-binary acceptance are separate delivery gates in the adapter Plan.

The App renders only the versioned `_meta.datatug` DTO. Public summaries and legacy raw rows cannot populate the explorer. Integer/decimal tags remain strings from the core wire contract through display, exact sorting and local copy. Original per-cell tags are retained independently of column declarations; JSON copy formats each row with its actual cell types through the upstream helper. String cells are quoted and boolean cells labeled so mixed values remain distinct. Each continuation sends only the retained handle, plus declared text parameters for rerun. The host is trusted; app-only routing does not authenticate a caller independently.

Table and Record share a stable local ordering; paging holds 25 captured rows. Sources displays supplied provenance and limitations. Columns displays supplied result metadata. Related reports that relationship metadata is unavailable. The browser does not infer joins, execute arbitrary SQL, count database totals, fetch daemon URLs, update model context, or send model messages. Result retries reauthorize the same capability; reruns receive a fresh one. A failed rerun leaves clearly labeled previous rows. History holds at most ten distinct retained results. A request epoch, concurrent-call guard and initial-notification fence prevent late responses from replacing accepted user runs.

The single toolbar uses a truncated accessible query title, native view selector and compact Actions menu at narrow widths. Parameters open as content. Copy record JSON is available only in Record view and targets the displayed record. Copy is an explicit local gesture using clipboard-write permission; denial reveals a focusable manual copy selection. System/host theme and reduced motion are respected. The native flat surface follows the [shared UI bible](https://github.com/sneat-co/sneat-specs/blob/main/standards/frontend-ux/README.md) without importing Angular or Ionic.

## Build and verify

From this directory, with Node 20+ and npm installed on the build host:

```sh
wb run -- npm ci --ignore-scripts
wb run -- npm test
wb run -- npm run build
wb run -- npm run build:check
```

These commands were executed for this implementation. `build:check` builds in memory and compares the HTML, build manifest, embedded notices and archive notices byte for byte. A clean cache-backed offline install and check at a second absolute path also passed. The lockfile pins exact published dependency artifacts; direct dependencies are exact versions. Build output has no timestamps, source maps or absolute paths, and is capped at 1.5 MB. `build-manifest.json` records input hashes, lock hash, SDK registry integrity/git head, bundled package versions/integrities and final HTML hash/size. The root archive `THIRD_PARTY_NOTICES.md` and embedded HTML reproduce the full bundled dependency license texts, including published MCP relicensing notices.

The upstream helper is the exact reviewed `datatug/ai-plugin` `lib/rows.js` at the commit/hash in `vendor/upstream.json`. Only that pure module is imported; terminal/filesystem/result-locator modules are excluded. Refresh and parity commands, also executed for this implementation, read the pinned commit from an existing local clone without checking it out or contacting a network:

```sh
wb run -- node scripts/refresh-vendor.mjs /Users/alex/projects/datatug/ai-plugin
wb run -- node scripts/parity-upstream.mjs /Users/alex/projects/datatug/ai-plugin
wb run -- npm run build:check
```

Use the appropriate clone path on another machine. Refresh rejects a source hash mismatch; never edit the vendor independently. The parity command executes both Git-pinned upstream and vendored helpers against shared typed DTO and decimal/exponent/tie/null/copy vectors. `test/typed-result.json` is also decoded and validated by the actual Go core contract in `apps_assets_test.go`. State tests cover no-public-fallback behavior, exact values, stable paging, required/scalar parameter validation, initialization/rerun races, cancellation, safe failures, timeout, retry and history bounds. They do not substitute for actual official-host browser acceptance.
