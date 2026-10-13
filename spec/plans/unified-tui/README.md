---
format: https://specscore.md/plan-specification
status: Implemented
---

# Plan: Deliver the unified terminal workspace

**Status:** Implemented
**Reconciled:** 2026-10-13
**Source Feature:** unified-tui
**Date:** 2026-10-12
**Owner:** alex
**Supersedes:** —

## Summary

“I open DataTug and Explore plus the composer appear without another action. I browse a table, open its data, attach its context, or create a query. I ask a question, select an older result, search and expand it, and move to Inspect without losing my row. I preview a related record or explicitly choose a JOIN. I group or sort loaded rows immediately, or inspect a derived query and run it on the server. I open API Monitor, see actual incoming requests, and return to my unchanged workspace.”

| Journey stage | Observable good result | Task |
| --- | --- | --- |
| Start without input | One program, Explore and composer visible; empty setup useful | 3 |
| Select table/view | Open, Attach and New query perform distinct actions | 4 |
| Chat and select older result | Single input owner, retained grid/row/column target | 2–3 |
| Search, expand, move sideways | Same result, reversible layout, requested side reachable | 2, 4 |
| Inspect and follow relationship | Stable identity, parent/child preview and return preserve origin | 5 |
| Explicit JOIN | Authoritative choice produces derived lineage; Enter never joins accidentally | 6 |
| Group/sort loaded snapshot | Scope/count and reversible typed operation; zero network requests | 7–8 |
| Preview and run server operation | Exact valid query, secure execution, new result, original retained | 7–8 |
| Observe requests, including waiting stream | True listener state and request metrics update without extra user action | 9 |
| Return and use help/session controls | Context retained; visible shortcuts match terminal delivery | 10–11 |

The feature and plan originally landed in PR #367 at `57d9279`. Implementation and all five follow-up additions shipped in [PR #368](https://github.com/datatug/datatug-cli/pull/368), DataTug CLI v0.73.0; [PR #370](https://github.com/datatug/datatug-cli/pull/370), CLI v0.73.1, delivered the subsequent Actions/Trail mouse-boundary repair. The reconciled baseline is `da34f4e29e346c91d69f8ac30226bb9cd82ba0da`. The [implementation inventory](../../features/unified-tui/implementation-checklist.md) maps all 16 tasks to source, retained tests, private terminal observations, independent reviews and remote delivery. Selected-result browser handoff remains deferred.

## Approach

Shared opt-in components shipped first, followed by DataTug composition through the existing secure data/query paths and API Monitor module. Result identity remains separate from focus, and local presentation remains separate from derived execution. Independent reviews, source-bound terminal observations and exact-target CI accompany the published dependency chain. The coordinator owns landing/release/cleanup; implementation owners supplied scoped changes and evidence.

### Verified released architecture

Audited against CLI v0.73.1 on 2026-10-13:

| Repository / release | Delivered seams |
| --- | --- |
| datatug/datatug-cli `da34f4e29e346c91d69f8ac30226bb9cd82ba0da`, v0.73.1 | `apps/datatugapp/commands/workspace.go` mounts Projects/Viewers/Settings into the same workspace as chat. `pkg/chat` retains Inspect/Related/JOIN, local operations, reviewed secure derived execution, scope/running/trail/actions UX and the existing API Monitor. |
| strongo/aichat `4d793180360c470c63da86d7db6fc5c70d317f60`, v0.13.1 | Compatible opt-in `tui/chatshell` left/right/center composition, exclusive focus, expansion, modal routing, Select text and bounded overlay mouse dispatch. Full coverage and raw zero-count-block CI guard passed. |
| tuigoff/tuigoff `f766251af479ef8eaf5c6f58f8a55f9d392bad23`, v0.6.0 | Shared theme, focus, transcript, grid/gridblock, navigation and widgets provide neutral presentation and workspace search while preserving non-workspace defaults. Full coverage CI passed. |

CLI go.mod pins these published provider releases with no active replacements; consumer validation used `GOWORK=off`. Both CLI changes are merged and released, and related delivery branches/worktrees/claims were cleaned, including the separately approved telemetry capture. The inventory distinguishes superseded local publication-pending receipts from final target CI/releases. The browser prototype remains visual reference only; no web implementation is claimed.

### Input and state design constraints

Use action IDs with one context dispatcher, a single focus owner, explicit modal/search modes, and visible bindings generated from the feature routing table. Dialogs capture input; F1 can open contextual help above them, while F4 and other workspace globals cannot act on their underlay. Outside modals, Shift arrows retain workspace navigation even in editors/search; ordinary typing and drafts survive focus transfer. Provide the explicit Select text mode described in the feature instead of silently claiming Shift arrows for editing. Esc consumes one layer per press: modal close, or search clear, search exit, then expansion restore; leaving editor focus retains text. Explorer uses Enter/Alt+A/Ctrl+N; JOIN uses Alt+J with Ctrl+J disabled; loaded quick sort uses Alt+Shift+Up/Down and server preview uses Ctrl+Shift+Up/Down. Tests and actual PTY modifier/LF observations bind these choices, with broader terminal portability limits recorded in the inventory. Shared opt-in APIs preserve old consumers. Store inspection/expansion targets as session + immutable RecordSet + stable row/column identities, distinct from focus. Version async operations and refuse stale responses. Snapshot local transforms and retain originals; server transforms create immutable child lineage.

The shipped scope defaults are Matching source rows for unaggregated queries and Original query output for grouped/aggregate/HAVING originals; a default choice still refuses unsupported shapes. Both dialogs identify scope, LIMIT/OFFSET placement, evaluator and aggregate treatment before execution. Exact explicit measures require a direct single-source original without joins/inner aggregation/HAVING and enforce row/byte budgets; recursive original-output evaluation refuses unsafe arithmetic/numeric shapes. API Monitor bounds are 60 five-second buckets, 500 retained requests per instance and 32 registered instances, verified at capacity/eviction boundaries. The feature records the full support/refusal contract. Local typing/null/tie and preview semantics remain unchanged requirements.

## Tasks

### Task 1: Audit partial work and define compatible component seams

**Id:** task-1
**Verifies:** unified-tui#ac:preserved-flows, unified-tui#ac:deferred-boundary
**Status:** complete
**Owner:** coordinator with shared-TUI and CLI implementation owners
**Depends on:** none

The source audit retained useful interrupted changes and established compatible provider seams, input precedence, secure execution and result identity. Published source SHAs, the all-task AC inventory and exact-target CI now replace the partial-work chronology. Web source remains outside scope, F5 retains the general workspace, and grid Shift+Enter explains the deferred selected-result handoff.

**Evidence:** diff-to-AC inventory; dependency graph; old-consumer compatibility fixtures; exact current CI policy references; explicit missing implementation list. Confirm general F5 works and no selected-result target is falsely advertised.

### Task 2: Shared neutral presentation, workspace layout and exclusive focus

**Id:** task-2
**Verifies:** unified-tui#ac:visual-focus, unified-tui#ac:panel-navigation, unified-tui#ac:result-expansion, unified-tui#ac:preserved-flows
**Status:** complete
**Owner:** shared-TUI implementation owner in tuigoff, then aichat
**Depends on:** Task 1

Shared opt-in neutral selection, column foreground highlighting, content-facing dividers, independent sides and a center surface are delivered. One dispatcher owns composer/grid/panel/overlay input. Expansion snapshots layout around the same block identity and restores it before revealing a requested side. Existing shared consumers retain their defaults and keymaps.

**Evidence:** behavior tests for every side-visibility combination, hidden-owner transfer, modal ownership, composer/grid exclusion, older-result expansion, unchanged block identity, row/column/sort/scroll/chat retention, and zero execution callbacks. Combined-state tests cover modal over searched expanded grid; successive Esc closes only the modal, clears search, exits search, then restores layout. Test F1 above modal and return, blocked F4/globals under modal, and F4 from searched/expanded state with retained context. Character-cell render captures at wide/narrow sizes show neutral selections, blue edge geometry, title and column distinction; resizing leaves no offscreen input owner.

### Task 3: Integrate default/ui/chat into one workspace

**Id:** task-3
**Verifies:** unified-tui#ac:workspace-entry, unified-tui#ac:inspector-identity, unified-tui#ac:preserved-flows
**Status:** complete
**Owner:** CLI workspace implementation owner
**Depends on:** Task 2

Default/ui and chat now use one workspace/program with persistent composer and retained Projects/Viewers/Settings. Default enters Explore; chat focuses the composer. Setup, project/session/provider and file-opening flows remain available. Inspect retains selected RecordSet/row identity across focus changes and older-result selection; authoritative primary/composite keys are distinguished from unavailable identity.

**Evidence:** process/program-count assertions; empty/project/provider-unconfigured startup tests; setup and file-opening regressions; older-result → Inspect → composer → grid test proves target retention; absent/composite key render cases. Draft input and history survive navigation.

### Task 4: Instant search and explicit Explorer actions

**Id:** task-4
**Verifies:** unified-tui#ac:instant-search, unified-tui#ac:explorer-actions
**Status:** complete
**Owner:** CLI workspace owner with shared input owner
**Depends on:** Tasks 2–3

Explorer/grid first printable input and digits enter incremental search; editors retain literal text, with one-layer Esc clear/exit. Visible Open data, Attach to composer and New query use existing secure paths. Supported bindings are Enter, Explorer Alt+A and Ctrl+N respectively; Attach/New query do not execute, and draft/context/history are retained.

**Evidence:** table-driven keys include `z r j e q s 1 2 3 /`, Unicode text, editing and modal contexts; first-character inclusion and reversible filtering; non-workspace compatibility tests. Inspect/Related bare digits must not activate actions; verify any optional relation-list filtering stays local to that control. Executor spies and integration fixtures prove Attach/New query make no read/execute call, Open is authorized, and draft/context/history persist. Denied actions surface useful errors.

### Task 5: Retained Inspect, actions and related-record navigation

**Id:** task-5
**Verifies:** unified-tui#ac:inspector-identity, unified-tui#ac:related-navigation
**Status:** complete
**Owner:** CLI query/inspection implementation owner
**Depends on:** Tasks 3–4

F10 reveals Related; Alt+A exposes Inspect Actions according to focus. Authoritative outgoing/incoming relations retain stable candidate numbers with Alt+1..0 and arrows/Enter alternatives. Secure parent/child reads preserve the origin and return path without a JOIN shortcut. Captured session/result/row/relation identity, generation and cancellation reject stale responses.

**Evidence:** Invoice → Customer and Invoice → InvoiceLine fixtures; >10 relations, composite/missing/null keys, empty/denied/unsupported source; no JOIN invocation; return restores original result. Delayed-response tests change focus, selected older result, row and session to prove correct retention or stale rejection. Bare digits still search in Explorer/grid outside explicit picker mode. In Inspect/Related, bare digits never activate actions; optional relation-list filtering remains scoped to that control.

### Task 6: Explicit quick JOIN picker

**Id:** task-6
**Verifies:** unified-tui#ac:quick-join
**Status:** complete
**Owner:** CLI query implementation owner
**Depends on:** Tasks 4–5

The explicit authoritative JOIN picker uses stable numbered choices, arrows/Enter and Esc cancellation. Applying preserves source/parameters and immutable parent lineage through the secure apply path. Alt+J is the verified primary binding. Ctrl+J remains disabled because the pinned terminal decoder maps legacy LF to the same event; LF/Enter cannot invoke JOIN.

**Evidence:** actual terminal key captures and decoder-to-shell tests for Alt+J and LF/Ctrl+J/Enter; no accidental JOIN on Enter or bare j; ambiguous/denied/multiple candidates, parameter retention, original-result preservation and stale session/selection responses. Cancellation makes no apply call.

### Task 7: Group by dialog, local snapshots and server derivation

**Id:** task-7
**Verifies:** unified-tui#ac:group-dialog, unified-tui#ac:group-loaded, unified-tui#ac:group-server
**Status:** complete
**Owner:** CLI result-operation owner with shared chooser owner
**Depends on:** Tasks 3–4

The reusable numbered/filterable chooser retains group-key selection, editable COUNT(*)/eligible SUM defaults and explicit aggregate row controls. Loaded grouping uses typed immutable snapshots with constituent navigation and reset. Server grouping requires exact structural preview, source/parameter retention and immutable child lineage. Matching source rows is the unaggregated default; Original query output is the grouped/aggregate/HAVING default. Scope and LIMIT/OFFSET/aggregate/evaluator treatment are explicit, with unsupported reaggregation/wrapping refused. Exact explicit measures admit only authoritative typed direct-source originals, bounded to 10,000 input rows and 16 MiB input/output allocation; unsupported/unsafe shapes fail without partial aggregates.

**Evidence:** chooser filtering keeps IDs/selection across hidden matches and literal-digit filter input; zero/one/many keys, null vs missing/empty/zero, composite delimiter collisions, precision and declared types. COUNT(*) and eligible SUM start enabled and editable; authoritative PK/FK integer exclusions and unknown-metadata refusals are tested. Row-scoped S/C/M commands preserve literal filter typing, valid overrides survive key changes, and grouping keys cannot retain SUM. Exact integer/decimal and finite float sums use the typed bounded kernel. Executor/network counters stay zero for local operations. Real fixtures must make scope observable: six filtered source rows with multiple keys, only two loaded, yield local counts over two versus matching-source counts over six before the output cap; original-query-output scope remains bounded as previewed. Cover parameters, aliases, LIMIT and pre-existing aggregation; compare preview to submitted query and prove original/child lineage, policy denials, cancellation and stale-response rejection. Unsupported derivations do not execute. Composer Shift+Enter remains newline.

### Task 8: Sort dialog, stable local order and server ORDER BY

**Id:** task-8
**Verifies:** unified-tui#ac:sort-dialog, unified-tui#ac:sort-loaded, unified-tui#ac:sort-server
**Status:** complete
**Owner:** CLI result-operation owner with shared chooser owner
**Depends on:** Task 7 for chooser/derivation reuse

Sort reuses the chooser with per-column direction and explicit priority. Local loaded-snapshot sorting is stable and reversible. Supported server ORDER BY derivation previews exact scope, LIMIT/OFFSET and aggregate treatment before secure execution; unsupported shapes explain their refusal. Alt+Shift+Up/Down sorts loaded selected-column data; Ctrl+Shift+Up/Down opens the corresponding server preview, which still requires explicit Shift+Enter. Local and native/generic server ordering are labelled honestly.

**Evidence:** equal-key stable ties in both directions, mixed directions/priorities, exact numerics, null/missing/type/text/temporal/binary cases, unsupported-type errors and reset to original order. No network for local sort. A server fixture includes a highest-valued row outside the loaded/original limited subset: Matching source rows descending sort returns it; local and Original query output sorts cannot. Real server cases prove the selected scope, aliases/parameters and previewed LIMIT/grouping treatment or explicit refusal, preview equivalence, new immutable lineage and stale/cancel handling. Native chord captures and selected-column visual evidence validate quick actions.

### Task 9: Actual API telemetry and existing Monitor integration

**Id:** task-9
**Verifies:** unified-tui#ac:api-observability, unified-tui#ac:api-security-compatibility
**Status:** complete
**Owner:** CLI server/telemetry implementation owner
**Depends on:** Tasks 1 and 3

Actual BrowserBridge and HTTP listener lifecycle/request instrumentation wraps authorization guards without weakening them and feeds the existing API Monitor. Live begins after successful bind, with per-instance ownership, real base URLs, active/volume/duration metrics and bounded logs. The writer facade preserves supported optional interfaces and distinguishes actual status from failure outcome. Sanitization precedes retention. Bounds are 60 five-second buckets, 500 request entries per monitor and 32 registered instances; no start/stop UI was added.

**Evidence:** actual loopback requests from the web bridge and normal API, denied requests, occupied-port bind failure, graceful stop, overlapping/old instances, slow/live stream and hijack completion. Assert bucket edge times, eviction and bounded memory at/over chosen capacities. Test Flush/Hijack/Unwrap and unavailable interfaces, panic before/after response headers, cancellation and exactly-once finalization. Secret sentinels in header/body/query/fragment/path must be absent from retained snapshots/rendered telemetry. F8 return preserves workspace identity/layout. No start/stop UI is added.

### Task 10: Contextual help, visible controls and terminal conflict matrix

**Id:** task-10
**Verifies:** unified-tui#ac:help-session-shortcuts, unified-tui#ac:deferred-boundary, unified-tui#ac:visual-focus, unified-tui#ac:panel-navigation
**Status:** complete
**Owner:** CLI interaction/QA owner
**Depends on:** Tasks 4–9

Contextual F1, per-workspace Alt+F1/Ctrl+F1 toolbar visibility, clickable controls/session selection, `/switch`, dialog hints and the explicit Select text alternative are delivered. Modal-first routing, nested help, blocked underlay globals, Shift workspace navigation and one-layer Esc retain drafts and source state. Grid Shift+Enter reports unavailable, composer Shift+Enter inserts newline, and F5 remains the general workspace. The inventory names actual PTY/CSI/LF/SGR observations and avoids claiming universal terminal/OS/Option-key parity or native GUI screenshot verification.

**Evidence:** key matrix for Explorer, grid, composer/editor, Inspect/Related/Actions, search and each modal, including terminal Option/Alt settings and legacy Ctrl+J ambiguity. Combined-state cases include composer + Shift arrows preserving drafts, search + expansion + three Esc presses, modal + search + expansion + four Esc presses, F1 help above a modal, blocked modal F4, and Group/Sort filter focus + Shift+Enter executing only the previewed dialog action. Real terminal mouse and keyboard runs, F1 advertising at narrow widths, focus restore after cancel, session/project changes and unavailable-action explanations. Browser prototype key tests are supplementary only.

### Task 11: Whole journey, adversarial review, published dependencies and delivery

**Id:** task-11
**Verifies:** unified-tui#ac:terminal-journey, unified-tui#ac:preserved-flows, unified-tui#ac:deferred-boundary
**Status:** complete
**Owner:** QA/reviewer for evidence; parent/coordinator for landing and releases
**Depends on:** Tasks 2–10

Actual CLI PTY journeys and retained integration tests cover default/chat entry, secure queries/HTTP, navigation, local/server transforms and cancellation. Independent source-bound reviews closed the recorded findings, followed by exact-source/published-pin validation and authoritative target CI. The inventory distinguishes private captures from portable repository evidence.

The coordinator delivered **tuigoff v0.6.0 → aichat v0.13.0 → CLI v0.73.0**, then **aichat v0.13.1 → CLI v0.73.1** for mouse boundaries. Consumer verification used **GOWORK=off** with no active replacements. Final target checks completed with zero failures: tuigoff five (three success, two skipped), aichat four (four success, zero skipped), CLI eighteen (15 success, three skipped). Provider coverage guards and CLI release/artifact/Homebrew checks succeeded. WB delivery and related branch/worktree cleanup receipts are retained, including approved telemetry capture cleanup. Unrelated checkouts were preserved. SpecScore lifecycle reconciliation records this source/evidence audit; prose edits do not change lifecycle statuses.

**Evidence:** terminal recordings/captures and input matrix; actual SQL/HTTP counts proving no local network operation and no expansion rerun; original/derived RecordSet IDs; exact-SHA CI/coverage receipts, adversarial review verdicts and finding resolutions, published dependency versions, release/binary smoke tests, WB remote landing and cleanup receipts. Missing terminal or release evidence leaves the task open.

**Follow-up journey: make exploration state and alternatives visible**

Accepted 2026-10-13 and delivered in CLI v0.73.0: identify result scope → transform locally → review/run securely with elapsed/cancel → understand aggregate defaults → revisit a retained branch → find an alternative shortcut. Tasks 12–16 remain in the same Tasks section for SpecScore rollup. Their full-shell evidence, parent real-terminal QA and final independent review are mapped in the inventory; the v0.73.1 mouse-boundary continuation is recorded there as well.

### Task 12: Persistent scope on retained result surfaces

**Id:** task-12
**Verifies:** unified-tui#ac:persistent-result-scope
**Status:** complete
**Owner:** Sol CLI implementation owner; coordinator sole landing owner
**Depends on:** Tasks 7–8

Results and local group/member panels retain compact Loaded/Server counts and real execution time with truthful provenance. Matched persisted query ID/source/ExecutedAt or actual HTTP response evidence proves Server scope, including native SQL and empty results; imported snapshots without proof stay Loaded. Original scope survives local/server transitions, and filtered counts remain distinct.

**Evidence:** full-shell secure SQLite local→server journey, retained origin label/state, compact/wide render assertions and coordinator terminal receipt.

### Task 13: Identity-bound running operations and cancellation

**Id:** task-13
**Verifies:** unified-tui#ac:operation-feedback-cancel
**Status:** complete
**Owner:** Sol CLI implementation owner
**Depends on:** Tasks 4–9

Open/editor/saved-query/refresh/Related/JOIN/derived execution uses captured operation identity, real elapsed seconds and visible keyboard/mouse Cancel. Cancellation signals captured context without waiting on a held session mutex, remains visibly pending until completion and fences synchronous backend/session work. Context checks precede persistence; canceled/late completion cannot select canceled output or overwrite newer state. Safe late success reconciles retained instances/trail without destroying drafts. AI-stream Busy ownership remains independent.

**Evidence:** delayed executor full-shell keyboard/mouse cancellation, context-switch/late-response/no-persist assertions, path-specific integration and terminal receipts.

### Task 14: Explain automatic and manual aggregation choices

**Id:** task-14
**Verifies:** unified-tui#ac:aggregate-provenance
**Status:** complete
**Owner:** Sol CLI implementation owner
**Depends on:** Task 7

Automatic aggregate suggestions explain authoritative float/decimal typing and complete integer non-PK/non-FK proof. PK/FK/grouping-key/nonnumeric/unknown exclusions are explicit. Automatic and manual labels remain truthful, valid deliberate overrides survive key changes, and filter typing remains literal.

**Evidence:** full-shell float/decimal/integer PK/FK/unknown cases, manual overrides, literal filter, compact explanation rendering.

### Task 15: Navigable retained exploration trail

**Id:** task-15
**Verifies:** unified-tui#ac:retained-exploration-trail
**Status:** complete
**Owner:** Sol CLI implementation owner
**Depends on:** Tasks 5–8

A compact lineage path and Trail/Back/Forward expose durable parents/children and session-local group/member nodes. Ctrl+T and Alt+Left/Right navigate retained instances without queries. Same-session additions reconcile without reconstructing old grids; missing/cyclic ancestry is bounded, repeated local return remains usable, and explicit branching replaces forward navigation.

**Evidence:** multi-generation/branching full-shell navigation, repeated local visits, unchanged row/column/filter/order/scroll and original instance across reconciliation; terminal receipt.

### Task 16: Searchable actions and explicit keyboard diagnostic

**Id:** task-16
**Verifies:** unified-tui#ac:shortcut-alternatives-diagnostic
**Status:** complete
**Owner:** Sol CLI implementation owner
**Depends on:** Tasks 10, 12–15

A context-aware searchable Actions registry dispatches existing paths, with Ctrl+P, `/actions`, clickable F1 and enabled/disabled reasons. Explicit `/keys` retains at most 32 normalized received events, executes no product command, states LF/Ctrl+J ambiguity and returns safely through Esc/Close without losing draft/caret. Ordinary composer/editor typing is unaffected. Final provider/consumer mouse-boundary repairs restrict activation to actual painted dialog rows and preserve legacy overlay behavior.

**Evidence:** full-shell searchable action/disabled reason/help/mouse routes, Ctrl+C/function-key diagnostic isolation, draft/caret return, compact layouts, parent actual key receipts and final independent review.

## Open Questions

Shortcut choices, server-scope defaults and supported/refused query shapes are resolved for the released baseline in the feature and inventory. Broader terminal/OS/modifier compatibility remains unproven beyond the recorded configurations; no automatic support claim is made. Grid-specific browser handoff and nested ancestor paths remain deferred to separate specifications. Incoming user corrections require their own source/evidence reconciliation rather than weakening existing acceptance criteria.

---

## Resolution

**Reconciled Executing → Implemented outside the tracked `change-status` flow** (16 task(s) marked complete; this did not walk the legal-transition matrix).

Reconciled after out-of-band delivery: all 16 tasks shipped in CLI v0.73.0 and mouse-boundary follow-up v0.73.1. Audited released source, retained tests, real CLI PTY receipts, independent reviews, exact-head CI, published provider pins and approved cleanup. This corrects stale records; terminal portability and deferred browser/Inspect work remain explicit in the inventory.

Evidence: https://github.com/datatug/datatug-cli/pull/368, https://github.com/datatug/datatug-cli/pull/370, https://github.com/datatug/datatug-cli/releases/tag/v0.73.1, da34f4e29e346c91d69f8ac30226bb9cd82ba0da, spec/features/unified-tui/implementation-checklist.md
*This document follows the https://specscore.md/plan-specification*
