---
format: https://specscore.md/idea-specification
status: Specified
---

# Idea: Private MCP Apps saved query explorer

**Status:** Specified
**Date:** 2026-10-23
**Owner:** alex
**Promotes To:** mcp-apps-adapter
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we let a person explore real authorized saved-query results inside an MCP Apps host, with the existing DataTug CLI and without silently sending those rows to the model?

## Context

Design stage only: independent adversarial review precedes formal specification and implementation. Evidence is pinned to datatug-cli 428847f1f962e56475e72ffb8cd6e79cbed5289d and ai-plugin 41a2c10252c490da4788edac075866c88bca0b43. Existing pkg/mcpserver/server.go uses generic AddTool output; tools.go runQuery returns typed recordset and applied bindings to model-visible output. Attaching UI metadata to that output would leak rows. docs/mcp.md establishes authenticated loopback transport, saved SQLite SQL only, daemon-selected principal/grants, 30-second deadlines, 64 KiB input and 2 MiB output, 100 default/500 maximum returned rows. Preserve these controls.

The Mod's hooks/register.tsx is a terminal renderer; lib/results.js validates private result contracts and lib/rows.js provides stable sorting, precise integers, copying and bounded history. Existing Go pkg/queryresults, pkg/resultops and pkg/sessionresultcache provide result storage and operation precedents; cache binding is not itself fresh authorization. Reuse the existing execution authority and result contracts rather than introducing SQL execution in a browser.

Protocol evidence: [official stable MCP Apps specification](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx), inspected 2026-10-23, defines ui resource metadata, capability negotiation, visibility and sandbox lifecycle. Local Go SDK v1.8.0 mcp/protocol.go exposes Tool.Meta, CallToolResult.Meta and ClientCapabilities.Extensions; no dependency upgrade is presumed necessary. The spec's structuredContent guidance is not a privacy guarantee across MCP hosts: DataTug treats both content and structuredContent as model-visible.

## Recommended Direction

Ship one offline HTML App resource embedded with go:embed in the standalone CLI. A small browser renderer uses the pinned official @modelcontextprotocol/ext-apps App SDK. Its host uses AppBridge. A build step bundles SDK, JS and CSS; end users install only the CLI binary and keep their separately owned datatug serve running. No external CDN, new daemon, Node runtime or cloud service is needed. This is an adapter over the existing authenticated MCP client and saved query endpoint.

Retain existing datatug_run_query exactly for ordinary clients, including its documented model-visible rows. On a compatible Apps connection, the same datatug_run_query instead uses the private explorer envelope and UI resource metadata; there is no additional raw-row tool on that connection. Its negotiated description states that results remain in the person-facing App. Do not claim legacy calls are private. Activate the private envelope and app-only continuation tools only when the initialized connection advertises io.modelcontextprotocol/ui with text/html;profile=mcp-app. Tool metadata links ui://datatug/query-explorer. Hosts without that capability retain existing plain tools and documented model-visible rows. A compatible host whose App fails to load receives a safe summary and retry guidance, never raw-row fallback. Session negotiation is immutable; do not globally mutate registered tools when a second client connects. The CLI setup instructions explain the privacy distinction before connection; no rows are explicitly sent to model from the App in this release.

Private tool content and structuredContent contain counts, status and safe identifiers only; no row cells, raw bindings, paths, secrets or result bearer tokens. The initial result _meta carries the bounded private payload and opaque continuation handle; App auto-renders it from ontoolresult after App.connect initialization. All continuation results use the same explicit envelope, bypassing the generic typed-output helper. App-only visibility reduces model discovery but is not authentication: the trusted host is part of the boundary and receives private data; neither protocol nor DataTug can conceal rows from a malicious host.

### Journey and interaction contract

1. Person connects their installed CLI to a compatible host and existing authenticated agent. The host discovers tool metadata, reads the static ui resource and initializes the sandbox through official App/AppBridge. No credentials or local files enter the iframe.
2. A saved-query explorer call validates project/environment/source/query/typed parameters and the effective daemon security context, then invokes existing saved SQL execution once. Loading becomes Table automatically when the tool result arrives; no extra Load click is required. The model receives a summary only.
3. One compact single-line toolbar contains the truncated query title, view selector and compact actions. It never wraps at 320 px or wide widths; narrow widths use a select/menu and ellipsis actions with accessible names/tooltips preserving full labels. There is no second tabs or chrome row. A genuine parameter editor may open as content beneath the toolbar. Table, Record, Columns, Sources and Related share one flat surface without nested cards. Apply shared Sneat UX hierarchy, neutral dividers, spacing, semantic status colors and accessible actions; framework-specific Ionic elements are not a reason to bundle Angular into this sandbox.
4. Table sorts the captured bounded rows stably and paginates locally (25 per page); it labels truncation as captured rows rather than a total database count. Null, empty text, boolean and precise large integer/decimal values remain distinct. Preserve the existing core v0.77.0 apicontract integer/decimal string-tagged wire DTO through JSON parse, sorting, rendering and copy; never first coerce exact values to JavaScript Number. Record follows that sort order with previous/next controls and typed values. Columns shows declared metadata where supplied. Sources shows authorized provenance and native SQL limitation. Related shows declared, authorized relationships or an explicit unavailable reason; no guessed relationships or fabricated counts. Relationship exploration beyond the saved query requires another existing authorized saved-query selection; no arbitrary SQL/table-row endpoint is added.
5. Parameters uses declared scalar input types, required validation and a Run button. Edits call an app-only rerun continuation with handle plus declared parameter values only, not arbitrary query/scope IDs. Server re-resolves the original saved query and fresh daemon context; exact typed bindings and read-only authorization remain authoritative. Show loading without displaying stale rows as fresh; failures keep the previous result visibly labeled previous. New successful runs enter a bounded 10-result in-session history. Parameter values remain private metadata.
6. Empty, timeout, denied grant, disconnected agent, unsupported host and expired result each have a clear status and appropriate retry/rerun action. Missing metadata says unavailable. User can copy selected displayed cells/record locally through a host-supported clipboard path; denial has a visible fallback. Copy never updates model context. Any explicit future send-to-model action requires its own scoped user action and test; none is implicit here.
7. Host teardown disconnects App listeners and cancels outstanding UI requests. Closing MCP stdin releases the MCP session and all handles, leaving the separately owned agent running. Out-of-order results cannot overwrite a newer run. Keyboard tab order, focused sorting/navigation, semantic table headers, status announcements, keyboard parameter submission, host theme variables/light/dark and reduced motion are acceptance criteria.

### Authority, privacy and lifecycle

Use a server-owned in-memory registry per MCP connection, bound to SDK session identity plus daemon principal and securityContextId, project/environment/source/query and definition digest. Generate cryptographically random handles (at least 128-bit entropy); expose them only through _meta. Never accept client-supplied principal, security context, path, daemon URL or SQL. Handles are scoped bearer capabilities within that trusted connection, not proof that a caller is an iframe. Recheck current daemon context and query definition on every read/rerun; changes invalidate the handle rather than reusing old authorization. Wrong-session, missing, guessed, expired and replay-after-teardown handles all fail with the same safe unavailable response.

Enforce 10 retained results, 10 minute idle expiry, 30 minute absolute expiry, and 16 MiB aggregate retained private payload per connection, with eviction of the oldest inactive entry. Each payload remains within the existing 2 MiB envelope including metadata, and 500 rows. Reject oversized requests before execution and oversized results before retention; a safe smaller-limit message contains no underlying rows. Bound concurrent reruns to one per handle and prevent late completions from reviving revoked handles. Existing disk results remain server-side only; do not expose their locators. Prefer no new persistent cache for this short-lived App; reuse Go result normalization/validation and current result contracts, with memory-only session capabilities layered around them.

UI resource _meta.ui.csp declares empty connectDomains/resourceDomains/frameDomains and no permissions unless clipboard support requires a narrow host-approved permission. Bundle all dependencies; no direct iframe fetch/WebSocket to localhost, filesystem access, keys, telemetry, external images or fonts. Render every cell and metadata label as text; never evaluate SQL/HTML/URLs from results. Use official SDK transport source-window checks and the host AppBridge sandbox/origin checks, including the opaque-origin sandbox proxy where the official SDK requires it. Do not replace that handshake with a permissive wildcard listener. Test forged messages from sibling frames and invalid origins against the real SDK. The trusted host enforces the resource CSP; document that boundary rather than claiming the Go server can authenticate browser origin via stdio.

### Auditable Mod reuse

Recommended mechanism is a checked-in, version-pinned upstream source vendor of the small pure browser-compatible Mod helpers, with upstream repository/path/commit/license/SHA256 manifest, deterministic import/update command and parity tests. The generated bundle imports those exact vendored modules; changes are made upstream and refreshed, never patched divergently in the vendor. Keep lib/results.js filesystem/CLI locator logic server-side where applicable: browser imports only pure format/row helpers and receives a normalized validated DTO from Go. Use existing Go queryresults/resultops validation and contracts directly where they match, not an invented parallel persistence format. Current upstream lib/rows.js compares noninteger numeric strings through Number, which loses precision (for example 9007199254740992.1 versus 9007199254740992.2). During implementation, make a narrow exact-decimal comparator fix upstream in ai-plugin, test and independently review it, then refresh the vendor at that exact updated commit/hash. Do not change source before formal specification review; do not weaken the precision guarantee or hand-patch a divergent vendor. Renderer and transport are new because terminal components cannot run in an iframe. Implementation must demonstrate the selected imports and shared fixtures rather than merely cite Mod inspiration.

## Alternatives Considered

- Publish a new shared npm package first: strongest independent consumer versioning, but adds release sequencing and package ownership for two tiny modules. Adopt later if multiple browser consumers need it; pinned auditable upstream reuse has a smaller complete delivery path today.
- Port the terminal Mod wholesale or copy helpers without provenance: terminal hooks do not render browser DOM, and silent copies drift. Keep its tested pure functions, contracts and interaction semantics with explicit source provenance instead.
- Remote hosted App or iframe calls to localhost: adds availability, trust, network and credential surfaces. Embedded offline resource and host-mediated tools reuse the installed binary boundary.

## MVP Scope

Complete the real saved-query exploration journey above for the SQLite execution already supported by datatug mcp: automatic first render, five honest views, stable local sorting/paging, record navigation, declared-parameter rerun, private summaries, local copy/history and full error/teardown handling. This is a complete protocol adapter and result explorer, not a new database engine.

## Not Doing (and Why)

- Arbitrary SQL, writes, TugQL or new database drivers — preserve existing saved-SQL authority and proven SQLite execution boundary.
- Direct iframe daemon access or remote runtime hosting — preserve offline binary installation and avoid exposing local credentials.
- Invented metadata, related rows or exact totals — display only authoritative available evidence.
- Mod terminal renderer replacement — existing users and plain MCP behavior retain their contracts.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Official App/AppBridge supports private _meta result delivery and app-only continuation without putting rows in model context | Browser integration with pinned official App and AppBridge, inspecting host model transcript and tool envelopes with unique secret sentinel rows and parameter values |
| Must-be-true | Go SDK session capability and identity are usable without global cross-session tool leakage | Two concurrent official SDK clients, different negotiated capabilities/principals, discovery and guessed/replayed handle negative tests |
| Must-be-true | Existing real execution survives the App adapter | Actual CLI child stdio + authenticated daemon + committed Chinook saved query; automatically see CustomerId-dependent results, edit parameter and verify changed rows through browser App |
| Must-be-true | Release contains all assets | Run exact downloaded release binary with network blocked and no Node installed; official host reads embedded resource and completes same journey |
| Should-be-true | Mod helper reuse remains exact and precision safe | Manifest hashes, reproducible update/build, existing upstream fixtures plus null/duplicate/large integer/decimal/sort/copy parity tests; real browser E2E sentinels above 2^53 and high precision decimals must preserve distinctions through sorting, display and clipboard |
| Should-be-true | UX stays usable and bounded | Browser screenshots and keyboard tests at 320 px and wide widths, light/dark, five views, one-line header, loading/error/empty and truncated data |
| Must-be-true | Existing security is preserved | Wrong/missing daemon auth; grant denied; read-only write/attach attempts; unknown and credential parameters; changed security context; cross-session/expired/evicted handles; oversized payload; forged postMessages; CSP external network denial; stdout/log sentinel absence |

## SpecScore Integration

- **New Features this would create:** MCP Apps private saved-query explorer, promoted only after independent design review.
- **Existing Features affected:** Existing MCP stdio saved-query behavior and result contract compatibility; plain tools must retain existing tests.
- **Dependencies:** Official MCP Go SDK v1.8.0, pinned official ext-apps browser SDK, existing authenticated serve endpoint and real Chinook fixture. Source reuse follows the manifest above.

## Open Questions

None requiring a founder decision. Technical integration assumptions above are stop gates: prove them with the official SDK/host before claiming support. Failed privacy or session isolation requires design revision, not a weaker test.

### Delivery evidence and stage gates

Design adversarial review first; fix findings and obtain an exact-artifact verdict before promoting to Feature/Plan. Review that formal specification independently before source implementation. Then adversarial implementation review, targeted tests, official-host real browser E2E, exact-head CI and parent-owned WB landing/release. Repeat child-process and official-host acceptance with the exact public release binary before documenting public availability. Website instructions must match tested host support, privacy distinction, installation and limitations; do not modify unrelated demo CTA flags. No unsupported database E2E, TugQL completion, universal host support or public release claim follows from unit tests. Parent owns publication and cleanup. This artifact remains Draft until reviewed.
