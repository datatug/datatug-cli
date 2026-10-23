---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Private MCP Apps saved query explorer

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/mcp-apps-adapter?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/mcp-apps-adapter?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/mcp-apps-adapter?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/mcp-apps-adapter?op=request-change) |
**Status:** Implementing
**Date:** 2026-10-23
**Owner:** alex
**Source Ideas:** mcp-apps-adapter
**Supersedes:** —
**Grade:** A

## Summary

Adapt existing authenticated saved-SQL MCP execution to an offline private MCP Apps explorer. Task-scoped authority is the user's explicit request: "design, specify and implement mcp apps adapter. Adversarial review for each stage. Work autonomously, drive to completion". Independent design r2 passed; this is not a claim of human artifact review. Repository gate configuration remains unchanged. Approval records independent review /private/tmp/mcp-apps-review-spec-r3-20261023.md (SHA256 d32025364bd4f2f50e0d724bac677efcc98312074367e349b41d1edacb8d677d) and the task-scoped user autonomy authority, not human artifact inspection.

## Problem

People need to inspect real saved-query results inside compatible hosts without automatically disclosing cells to the model. Terminal Mod components cannot serve browser iframes, and adding UI metadata to generic typed MCP output leaks private rows.

## Behavior

#### REQ: negotiation

An immutable snapshot of the first real stdio connection/session initialization capability with the required MIME type switches datatug_run_query to the UI envelope and resource linkage; plain clients preserve documented model-visible results. No alternate raw-row route exists on Apps sessions and UI failure never falls back to raw rows.

#### REQ: privacy

Both content and structuredContent contain only safe counts/status/identifiers. Private rows, bindings and opaque capabilities exist only in _meta; adapter outputs never put paths, daemon keys, connection targets, cells or UI-entered continuation parameters in model context, logs or stdout diagnostics. Initial model-supplied tool arguments are already visible to that model; this adapter does not promise to conceal them. App-only visibility is host routing, not caller authentication; trusted hosts can see private data.

#### REQ: binding

Bind random handles of at least128-bit entropy to SDK session ID, daemon principal/securityContextId, scope and query definition digest. On continuation reauthorize against fresh daemon info and re-resolved query; changed bindings invalidate. Never accept caller principal/context/path/URL/SQL overrides.

#### REQ: bounds

Retain at most10 results and16MiB aggregate per session; enforce10-minute idle and30-minute absolute expiry, oldest inactive eviction and2MiB total per envelope. Preserve64KiB input,30-second request deadline,100 default/500 maximum rows. Allow one in-flight rerun per handle; revoked/older completions cannot republish rows. Teardown cancels work and drops handles without stopping separately owned serve.

#### REQ: execution

Use existing authenticated loopback mcp Client and saved-query validation/typed binding/exec endpoint. Preserve proxy/redirect denial, daemon auth and native SQL grant, read-only SQLite authorizer and opaque-privileged limitation. Parameters rerun only the same retained saved query and scope; credential/multivalue/unknown values fail. No arbitrary SQL or writes.

#### REQ: precision-reuse

Preserve corev0.77.0 integer/decimal string-tagged DTO through JSON parse/sort/display/copy. Fix current ai-plugin noninteger Number comparator narrowly upstream during implementation, independently review/test, then vendor exact updated pure modules with commit/license/path/hash manifest and deterministic refresh. Never patch vendor independently. Reuse Go result validation/contracts, with a new browser renderer rather than terminal hooks.

#### REQ: views

Automatically render first result after App initialization; provide Table, Record, Columns, Sources and Related on one flat surface. Stable sort and25-row local paging label captured/truncated bounds honestly; Record follows sort. Show only supplied declared columns/provenance/authorized relationships, unavailable reasons otherwise. Related row expansion requires another authorized saved query, never guessed metadata or new raw execution. Keep10-result history with previous-result label during failed rerun.

#### REQ: ux

One toolbar contains title, view selector and actions at320px and wide widths; use select/menu/ellipsis when narrow, never second chrome/tab row or nested cards. Parameter editor opens as content with scalar type/required validation. Provide keyboard navigation, focus, semantic table headings, status announcements, host light/dark/reduced-motion and explicit loading/empty/denied/expired/disconnected/timeout states with retry.

#### REQ: sdk-offline

Bundle pinned published official ext-apps App SDK with renderer/css into go:embed HTML resource; use official App.connect lifecycle and AppBridge sandbox/source/origin validation. CSP permits no external connections/resources/frames and no broad permissions. No iframe direct daemon fetch, key/filesystem access, dynamic HTML evaluation or external runtime.

#### REQ: delivery

Deterministic asset build and provenance verify locked published SDK versions and exact upstream vendor hashes. Independent implementation review and exact-head CI precede parent-owned WB merge/release; exact public release binary must pass real child/official browser acceptance before website claims availability. Docs distinguish tested host/MIME, plain model-visible privacy, ownership/setup and SQLite limitations; unrelated scan/demo work remains untouched.

## Architecture

pkg/mcpserver keeps server registration immutable. An AddReceivingMiddleware wrapper obtains req.GetSession().(*mcp.ServerSession), ID() and InitializeParams().Capabilities.Extensions from GoSDK1.8.0. Snapshot mode once at the first real stdio connection/session initialization; subsequent per-request sessionless initialization metadata or client capability fields must neither flip mode in either direction nor override bound authority. Use InitializeParams only to establish that snapshot, not as mutable per-call authority. For tools/list it constructs a session-local result projection with UI metadata for negotiated run_query and filters app-only continuation tools from plain sessions; never mutate shared Tool objects. For tools/call it guards continuation capabilities and passes session mode/identity to the explicit run-query envelope builder; direct invocation cannot bypass capability checks. Resource reads are static, with no result data embedded in HTML. All private envelopes bypass generic AddTool typed-result serialization. Server shutdown/session lifecycle owns registry eviction; tests prove separation with concurrent sessions.

Continuation tools are datatug_apps_rerun(handle, parameters) and datatug_apps_result(handle); both visibility:[app], same-server only. Handle binds fixed scope/query, rerun revalidates and produces a fresh bounded result/handle, result reads retained state after fresh authorization. Initial _meta carries full bounded DTO for automatic render. Legacy metadata tools retain their existing safe behavior. app-only restrictions do not authenticate iframe callers beyond the trusted negotiated host connection.

Official reference basic-host is not assumed private as shipped: the acceptance harness must explicitly advertise the Apps extension and use privacy-safe logging rather than copying its verbose input/result logger. Pin an actually published exact SDK version verified against package registry metadata and lockfile, not the monorepo main version. Preserve official origin/source guards, including targeted wildcard inner transport when required by its opaque-origin sandbox; no blanket wildcard prohibition substitutes for those checks.

Go execution remains pkg/mcpserver/client.go and tools.go; shared queryresults/resultops validation applies where contract-compatible. Short-lived result capability registry is memory-only; disk locators never leave Go. Browser source and embedded generated assets belong in pkg/mcpserver's App subdirectory; build/update scripts and manifest are checked in, not a runtime server. Existing ai-plugin pure helpers are exact reviewed upstream imports; terminal hooks stay untouched except necessary upstream comparator integration.

Read the source Idea for interaction detail and threat boundary. Apply shared Sneat frontend UX hierarchy/spacing/dividers/accessibility without bundling Ionic/Angular into this small iframe. No source implementation before formal review.

## Acceptance Criteria

### AC: negotiation

Scenario: Negotiated private execution (verifies REQ:negotiation)

Given concurrent plain and Apps SDK clients
When each lists tools and runs the same saved query and the Apps resource fails to load
Then plain output retains existing rows while Apps metadata links ui://datatug/query-explorer and every Apps model-visible envelope remains row-free; subsequent forged per-request/sessionless capability metadata cannot change either session mode or bound scope.

### AC: privacy

Scenario: Explicit private result envelope (verifies REQ:privacy)

Given unique row-cell sentinels and distinct UI-entered continuation parameter sentinels; initial model-supplied arguments are separately known and already model-visible
When initial execution, continuation, retry, error and copy complete through official AppBridge
Then model transcripts and captured diagnostics contain no sentinels or handles while the person-facing App displays authorized cells; copy changes only clipboard.

### AC: binding

Scenario: Session and authority binding (verifies REQ:binding)

Given two sessions and a retained handle
When the other session replays it, daemon context or query changes, handle is guessed, or original session closes
Then all further reads/reruns fail with the same safe unavailable response and no rows are returned.

### AC: bounds

Scenario: Bounded lifecycle and concurrency (verifies REQ:bounds)

Given a controlled clock and saturated session
When limits, expiry, concurrent reruns, cancellation and late completion occur
Then bounded retention and safe errors are observed, latest accepted result wins, and closing stdin ends CLI while owned daemon remains running.

### AC: execution

Scenario: Existing saved SQL authority (verifies REQ:execution)

Given real Chinook saved SQL through authenticated serve
When CustomerId changes
Then rows change according to database contents;
When auth/grants are absent, parameters invalid, or write/attach SQL is attempted
Then execution is denied without secret leakage.

### AC: precision-reuse

Scenario: Exact typed data and upstream reuse (verifies REQ:precision-reuse)

Given values above2^53, high precision decimals including9007199254740992.2 before9007199254740992.1, nulls and ties
When official-host App sorts/displays/copies
Then exact values remain distinct and correctly ordered; vendor hashes match reviewed upstream and parity fixtures pass.

### AC: views

Scenario: Five honest result views (verifies REQ:views)

Given real bounded results and absent optional metadata
When a person changes views, sorts, pages and navigates records
Then rows agree across views, truncation is explicit, unavailable metadata is labeled and no fabricated relationships/counts appear.

### AC: ux

Scenario: One-line accessible interaction (verifies REQ:ux)

Given narrow/wide light/dark browser states and keyboard-only input
When navigating all views and loading/error/empty/parameter states
Then header remains one line, controls remain named and usable, announcements/focus work and no stale result is presented as fresh.

### AC: sdk-offline

Scenario: Official sandbox and bundled runtime (verifies REQ:sdk-offline)

Given official App/AppBridge and real CLI child stdio/daemon/Chinook
When resource initialization, automatic load, rerun and teardown execute offline with forged sibling/origin messages and attempted external fetch
Then authorized interaction succeeds, forged messages/network fail, all assets load from release binary without Node.

### AC: delivery

Scenario: Reviewed reproducible public delivery (verifies REQ:delivery)

Given exact reviewed head and a downloaded public release binary
When clean asset rebuild, CI, real child/official-host browser tests and installation instructions execute
Then receipts identify SHA/version/assets, accurate supported host and offline privacy behavior; public availability is claimed only after all pass.

## Test Strategy

Each AC is exercised by its scenario above: SDK discovery/envelope negatives, controlled-clock registry tests, existing authority negatives, upstream exact-helper fixtures and real official-host browser tests. Use actual compiled CLI child stdio plus authenticated separately owned serve and committed demo-project-1 Chinook; official ext-apps App and AppBridge drive sandbox behavior, not only custom postMessage mocks. Record build SHA/SDK/package lock/vendor manifests/browser screenshots/transcript sentinel scans. Repeat with exact downloaded release artifact offline. Rehearse markdown stubs are omitted because concrete Go/browser executable scenarios are the observers and duplicate prose stubs would not execute this protocol; each implementation task must supply those tests. All source-Idea must-be-true assumptions are covered by negotiation/privacy/binding/execution/sdk-offline/delivery ACs; none deferred.

## Not Doing

No new SQL engine, TugQL, drivers, write APIs, cloud runtime, direct iframe daemon access, generic query builder or unsupported-host claims. No unrelated scan or website demo-CTA changes.

## Open Questions

None requiring founder input. Official SDK compatibility/privacy/session proof remains an implementation gate rather than an assumption waived by this document.

---
*This document follows the https://specscore.md/feature-specification*
