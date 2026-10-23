# Embedded query explorer

This browser resource is embedded by `pkg/mcpserver/apps.go`. The release binary serves all HTML, CSS and JavaScript without Node or network assets. It uses the published official `@modelcontextprotocol/ext-apps` App lifecycle and transport; application code does not implement a postMessage bridge. Real official-host/browser and released-binary acceptance are separate delivery gates in the adapter Plan.

The build removes dependency console calls, including the official SDK transport debug logging, so private envelopes are not logged by bundled dependencies. Script/style insertion uses literal replacement callbacks to preserve SDK strings containing JavaScript replacement tokens.

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

## Maintained official browser acceptance

This is the contributor/developer-preview reference host; it does not establish compatibility with a named chat host. A stdio client configuration alone does not demonstrate iframe rendering. Node is build/test-host tooling only: the supplied CLI child runs with an empty tool PATH and all App resources come from that binary.

Use Go compatible with this checkout's go.mod (CI uses Go 1.27.3), Node 20+, npm, Git, access to this source checkout's private Core Go dependency, and the locked Playwright Chromium. Check out the public demo repository at the exact commit in `test/browser-fixture.json`; supply its clean `demo-project-1` directory explicitly. The committed Chinook database hash is also checked. The supplied absolute CLI binary's embedded HTML must match this checkout's reviewed build manifest. Build a source candidate separately using `CGO_ENABLED=0`, matching release packaging; this harness never builds or replaces the supplied binary.

From the repository root, the following maintained entrypoint was executed against the actual pure-Go candidate on the local Mac:

```sh
wb run -- npm --prefix pkg/mcpserver/app ci --ignore-scripts
# From pkg/mcpserver/app:
wb run -- npx --no-install playwright install chromium
# From the repository root:
DATATUG_TELEMETRY=0 DO_NOT_TRACK=1 \
DATATUG_MCP_TEST_BINARY=/private/tmp/mcp-apps-task5-datatug \
DATATUG_DEMO_PROJECT_DIR=/Users/alex/projects/datatug/datatug-demo-project/demo-project-1 \
wb run -- npm --prefix pkg/mcpserver/app run test:browser
```

Use equivalent absolute paths on another machine. Missing/mismatched inputs fail instead of skipping. Optional `DATATUG_MCP_APPS_ARTIFACTS=/absolute/output` saves screenshots and a safe JSON receipt. `DATATUG_MCP_APPS_LABEL` defaults to `source`; set a label such as `release-v0-111-0` when repeating with an exact downloaded release artifact, preserving separate evidence. The same entrypoint accepts that downloaded binary without rebuilding it. A successful candidate run is not released-binary acceptance.

The Go fixture owns port0 agent/host/sandbox listeners, an authenticated official SDK session and the actual CLI stdin/stdout child. The browser uses official AppBridge manual forwarding and exact hash-checked upstream double-iframe sandbox source. Its dedicated broker is test-only, bounded and CSRF/origin checked; no daemon credential is supplied to the iframe. The default Go test suite skips the optional browser fixture; the maintained entrypoint enables it and requires every prerequisite. The Go CI `MCP Apps browser` job runs locked JS tests/build verification and this full actual-binary journey on every workflow run, preserving existing Go/coverage jobs. That checked-in configuration is not itself a claim that hosted CI has passed.

The real database journey verifies CustomerId1/2 Rock counts14/17, database-only private cell and UI-only binding sentinels absent from model discovery/result projections and captured console argument JSON/stderr, ten-result history, failed rerun/previous state, actual SDK timeout/cancellation before a delayed request is released, and separately owned agent survival after child exit. Precision/mixed-cell/paging cases are explicitly separate core-valid renderer fixtures delivered through official AppBridge, not SQLite decimal-storage guarantees. Browser assertions exercise five views, sort/Record/copy targeting, 25-row paging, genuine clipboard permission grant/denial, 320px/wide host light/dark, keyboard validation/focus and actual dark error contrast. Native sibling/source rejection uses broker call counts; wrong-parent-origin guard dispatch is explicitly synthetic, and native target-origin mismatch is separately rejected. Inherited HTTP/meta CSP blocks attempted network and frame navigation independently of Playwright's extra network containment.

All processes, listeners, keys and copied data are owned by the single Go fixture and have finite deadlines. Cleanup closes browser/context, official sessions/child stdin, listeners and agent, then removes test keys/temp directories; the CLI does not stop an independently owned serve process. No persistent daemon, tunnel, shared browser or personal MCP configuration is created.
