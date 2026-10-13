---
format: https://specscore.md/plan-specification
status: Executing
---

# Plan: Deliver the unified terminal workspace

**Status:** Executing
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

The feature and plan passed independent adversarial review and landed in PR #367 at 57d9279. Implementation has begun with shared focus/input/layout and routing regressions (Tasks 2 and 10). Earlier partial source is retained and audited; no task is complete without its required evidence, and no source release or selected-result web handoff is claimed.

## Approach

Implement opt-in shared TUI components first, then one DataTug workspace composition using the existing secure data/query paths and API Monitor module. Keep result identity separate from focus and local presentation separate from server-derived results. Validate every critical stage plus the whole journey in real terminals with actual requests. Release in dependency order only after independent review and exact-head CI. The parent/coordinator is sole landing owner for each repository and owns review, release and WB cleanup; implementation owners own scoped changes and evidence.

### Verified architecture baseline

Fetched origin/main on 2026-10-12 before these assertions:

| Repository and commit | Evidence and implication |
| --- | --- |
| datatug/datatug-cli `a1a729e2bfab0a2c67ce7da2514376ecb8f3062f` | `apps/datatugapp/commands/cmd_ui.go` composes Projects/Viewers/Settings/API Monitor; `apps/datatugapp/datatugui/app.go` and `pkg/chat/chatui.go` run separate programs. Unify composition rather than nesting programs. |
| Same CLI commit | `pkg/chat/chatui_inspector.go` uses shell FocusedRef; retained inspection identity must survive focus moving away and older-result selection. `pkg/chat/cell_detail.go` PreviewRelated resolves authoritative outgoing keys through the policy executor; incoming child navigation needs extension. ApplyJoinCandidate is the JOIN path, not plain related navigation. |
| Same CLI commit | `pkg/dbcopy/url.go` contains the DALgo aggregation execution path. This establishes a server building block, not delivery of interactive local grouping or safe arbitrary query transformation. `apps/datatugapp/datatugui/dtapiservice/api_service_ui.go` is the existing API Monitor module to extend. |
| strongo/aichat `b61460e406bbb7c0193b34d67f457e81b81d149d` | `tui/chatshell/chatshell.go` exposes a single sidebar/SidePanel contract, transcript/input and focus ring. Introduce compatible opt-in left/right/center/focus composition; retain the old API behavior. `.github/workflows/ci.yml` requires 100% coverage plus a raw zero-statement guard. |
| tuigoff/tuigoff `53c4c2d3cafde7e6cd4b92f45ff5eba7cefe0881` | `pkg/theme`, `pkg/focus`, `pkg/transcript`, `pkg/grid`, `pkg/gridblock`, `pkg/nav` and `pkg/widgets` provide shared components. Extend reusable presentation/input behavior here rather than duplicating a private design system. |

Re-fetch and audit any source work before resuming implementation. Existing `datatug-unified-tui` worktrees were interrupted and remain in progress; retain their changes and verify each against this feature. Temporary external GOWORK integration runs do not prove published dependency delivery. The browser prototype is visual/interaction reference only; it does not prove terminal escape sequences, secure SQL execution or HTTP instrumentation.

### Input and state design constraints

Use action IDs with one context dispatcher, a single focus owner, explicit modal/search modes, and visible bindings generated from the feature routing table. Dialogs capture input; F1 can open contextual help above them, while F4 and other workspace globals cannot act on their underlay. Outside modals, Shift arrows retain workspace navigation even in editors/search; ordinary typing and drafts survive focus transfer. Provide the explicit Select text mode described in the feature instead of silently claiming Shift arrows for editing. Esc consumes one layer per press: modal close, or search clear, search exit, then expansion restore; leaving editor focus retains text. Proposed Explorer/JOIN/quick-sort keys are validated choices, not settled product mandates. Shared opt-in APIs preserve old consumers. Store inspection/expansion targets as session + immutable RecordSet + stable row/column identities, distinct from focus. Version async operations and refuse stale responses. Snapshot local transforms and retain originals; server transforms create immutable child lineage.

Implementation choices include the telemetry bucket/history sizes, exact shared API names, and the proposed server scope defaults/support matrix. Both operation dialogs must identify server input scope, LIMIT placement and existing aggregate treatment before execution; Matching source rows versus Original query output is an explicit choice, not an inference from a server round trip. Five-second buckets and 500 logs are candidate bounds; prove the chosen boundaries. Feature-defined local typing/null/tie semantics and preview/refusal behavior remain testable contracts regardless of representation.

## Tasks

### Task 1: Audit partial work and define compatible component seams

**Id:** task-1
**Verifies:** unified-tui#ac:preserved-flows, unified-tui#ac:deferred-boundary
**Status:** planning
**Owner:** coordinator with shared-TUI and CLI implementation owners
**Depends on:** none

Compare preserved worktree diffs to freshly fetched origin/main and the complete feature. Inventory API compatibility, old key handlers, secure executors, result identity, server instrumentation and coverage gaps; retain useful changes. Record source SHAs and an implementation checklist without declaring interrupted tasks complete. Agree shared component contracts and input precedence before dependent changes. Keep web source outside scope and reserve grid Shift+Enter as unavailable.

**Evidence:** diff-to-AC inventory; dependency graph; old-consumer compatibility fixtures; exact current CI policy references; explicit missing implementation list. Confirm general F5 works and no selected-result target is falsely advertised.

### Task 2: Shared neutral presentation, workspace layout and exclusive focus

**Id:** task-2
**Verifies:** unified-tui#ac:visual-focus, unified-tui#ac:panel-navigation, unified-tui#ac:result-expansion, unified-tui#ac:preserved-flows
**Status:** in_progress
**Owner:** shared-TUI implementation owner in tuigoff, then aichat
**Depends on:** Task 1

Add opt-in neutral selection, column foreground highlighting, content-facing dividers, independent side panels and a center composition surface. Implement one focus dispatcher across composer, grids, panels and overlays. Snapshot expansion layout around the same block identity; restore before revealing a requested side. Preserve old shared consumers' defaults and keymaps.

**Evidence:** behavior tests for every side-visibility combination, hidden-owner transfer, modal ownership, composer/grid exclusion, older-result expansion, unchanged block identity, row/column/sort/scroll/chat retention, and zero execution callbacks. Combined-state tests cover modal over searched expanded grid; successive Esc closes only the modal, clears search, exits search, then restores layout. Test F1 above modal and return, blocked F4/globals under modal, and F4 from searched/expanded state with retained context. Character-cell render captures at wide/narrow sizes show neutral selections, blue edge geometry, title and column distinction; resizing leaves no offscreen input owner.

### Task 3: Integrate default/ui/chat into one workspace

**Id:** task-3
**Verifies:** unified-tui#ac:workspace-entry, unified-tui#ac:inspector-identity, unified-tui#ac:preserved-flows
**Status:** planning
**Owner:** CLI workspace implementation owner
**Depends on:** Task 2

Host existing Projects/Viewers/Settings and ChatUI in one program with persistent composer; default enters Explore and chat enters composer focus. Retain project/session/provider setup and file-opening flows. Keep selected RecordSet/row state after input focus moves, including older result versions; expose stable/composite primary identity honestly.

**Evidence:** process/program-count assertions; empty/project/provider-unconfigured startup tests; setup and file-opening regressions; older-result → Inspect → composer → grid test proves target retention; absent/composite key render cases. Draft input and history survive navigation.

### Task 4: Instant search and explicit Explorer actions

**Id:** task-4
**Verifies:** unified-tui#ac:instant-search, unified-tui#ac:explorer-actions
**Status:** planning
**Owner:** CLI workspace owner with shared input owner
**Depends on:** Tasks 2–3

Route first printable input/digits to focused tree/grid search and remove bare workspace action bindings. Preserve slash/text in editors and Esc clear-then-exit. Expose Open data, Attach to composer and New query through visible controls using existing secure paths. Validate proposed Enter/Alt+A/Ctrl+N bindings before advertising them.

**Evidence:** table-driven keys include `z r j e q s 1 2 3 /`, Unicode text, editing and modal contexts; first-character inclusion and reversible filtering; non-workspace compatibility tests. Inspect/Related bare digits must not activate actions; verify any optional relation-list filtering stays local to that control. Executor spies and integration fixtures prove Attach/New query make no read/execute call, Open is authorized, and draft/context/history persist. Denied actions surface useful errors.

### Task 5: Retained Inspect, actions and related-record navigation

**Id:** task-5
**Verifies:** unified-tui#ac:inspector-identity, unified-tui#ac:related-navigation
**Status:** planning
**Owner:** CLI query/inspection implementation owner
**Depends on:** Tasks 3–4

Add F10 Related and Alt+A Actions heading focus with visible hints. Resolve authoritative outgoing and incoming relationships and stable candidate numbering; implement Alt+1..0 and arrows/Enter previews with return navigation. Extend the secure related executor for incoming child relationships rather than using JOIN as a shortcut. Bind requests to session/result/row/relation and cancellation generation.

**Evidence:** Invoice → Customer and Invoice → InvoiceLine fixtures; >10 relations, composite/missing/null keys, empty/denied/unsupported source; no JOIN invocation; return restores original result. Delayed-response tests change focus, selected older result, row and session to prove correct retention or stale rejection. Bare digits still search in Explorer/grid outside explicit picker mode. In Inspect/Related, bare digits never activate actions; optional relation-list filtering remains scoped to that control.

### Task 6: Explicit quick JOIN picker

**Id:** task-6
**Verifies:** unified-tui#ac:quick-join
**Status:** planning
**Owner:** CLI query implementation owner
**Depends on:** Tasks 4–5

Use the authoritative candidate resolver and existing secure apply path. Provide stable numbered choices, arrows/Enter and Esc cancellation; retain source/parameters and immutable parent lineage. Alt+J is the verified primary. Ctrl+J is disabled: the pinned terminal decoder maps legacy LF to the same Ctrl+J event.

**Evidence:** actual terminal key captures and decoder-to-shell tests for Alt+J and LF/Ctrl+J/Enter; no accidental JOIN on Enter or bare j; ambiguous/denied/multiple candidates, parameter retention, original-result preservation and stale session/selection responses. Cancellation makes no apply call.

### Task 7: Group by dialog, local snapshots and server derivation

**Id:** task-7
**Verifies:** unified-tui#ac:group-dialog, unified-tui#ac:group-loaded, unified-tui#ac:group-server
**Status:** planning
**Owner:** CLI result-operation owner with shared chooser owner
**Depends on:** Tasks 3–4

Build the reusable numbered/filterable multi-select chooser. Alt+G exposes persistent selected keys, explicit aggregates and hinted Group loaded / Run on server / Cancel buttons. Implement typed/null/composite snapshot grouping and reset; implement structural server derivation with preview and source/parameter retention. Capture loaded scope before transformation; changed/paged results require reapply. Implement the feature’s explicit server-scope selector and query preview: proposed Matching source rows default for simple queries moves the original LIMIT to the derived output; Original query output retains it inside a subquery and is the proposed default for existing aggregated queries. Label input counts as unknown when not measured, LIMIT placement and retained/replaced grouping/aggregates/HAVING. Reject unsupported reaggregation/wrapping and invalidate preview after any scope change.

**Evidence:** chooser filtering keeps IDs/selection across hidden matches and literal-digit filter input; zero/one/many keys, null vs missing/empty/zero, composite delimiter collisions, precision and declared types. COUNT(*) and eligible SUM start enabled and editable; authoritative PK/FK integer exclusions and unknown-metadata refusals are tested. Row-scoped S/C/M commands preserve literal filter typing, valid overrides survive key changes, and grouping keys cannot retain SUM. Exact integer/decimal and finite float sums use the typed bounded kernel. Executor/network counters stay zero for local operations. Real fixtures must make scope observable: six filtered source rows with multiple keys, only two loaded, yield local counts over two versus matching-source counts over six before the output cap; original-query-output scope remains bounded as previewed. Cover parameters, aliases, LIMIT and pre-existing aggregation; compare preview to submitted query and prove original/child lineage, policy denials, cancellation and stale-response rejection. Unsupported derivations do not execute. Composer Shift+Enter remains newline.

### Task 8: Sort dialog, stable local order and server ORDER BY

**Id:** task-8
**Verifies:** unified-tui#ac:sort-dialog, unified-tui#ac:sort-loaded, unified-tui#ac:sort-server
**Status:** planning
**Owner:** CLI result-operation owner with shared chooser owner
**Depends on:** Task 7 for chooser/derivation reuse

Reuse chooser structure with per-column direction and explicit priority controls. Implement reversible loaded-snapshot stable sorting; preview and securely execute supported ORDER BY derivation with the same explicit server scope, LIMIT placement and aggregate-treatment contract. Validate the proposed defaults/support matrix rather than treating one LIMIT placement as mandated. Add distinct quick ascending/descending actions on the selected column; validate Alt+Shift+Up/Down for loaded sorting and Ctrl+Shift+Up/Down for server-sort preview. The preview never executes until its explicit Shift+Enter. Label local and server-native ordering honestly.

**Evidence:** equal-key stable ties in both directions, mixed directions/priorities, exact numerics, null/missing/type/text/temporal/binary cases, unsupported-type errors and reset to original order. No network for local sort. A server fixture includes a highest-valued row outside the loaded/original limited subset: Matching source rows descending sort returns it; local and Original query output sorts cannot. Real server cases prove the selected scope, aliases/parameters and previewed LIMIT/grouping treatment or explicit refusal, preview equivalence, new immutable lineage and stale/cancel handling. Native chord captures and selected-column visual evidence validate quick actions.

### Task 9: Actual API telemetry and existing Monitor integration

**Id:** task-9
**Verifies:** unified-tui#ac:api-observability, unified-tui#ac:api-security-compatibility
**Status:** planning
**Owner:** CLI server/telemetry implementation owner
**Depends on:** Tasks 1 and 3

Instrument BrowserBridge and HTTP listeners outside authorization guards and render the existing API Monitor module. Publish live only on successful bind, track each instance independently, and show truthful base URLs/active requests/volume/durations/bounded logs. Preserve optional response-writer capabilities and accurate actual-status versus failed-outcome distinction. Sanitize before retention and keep only allowed metadata.

**Evidence:** actual loopback requests from the web bridge and normal API, denied requests, occupied-port bind failure, graceful stop, overlapping/old instances, slow/live stream and hijack completion. Assert bucket edge times, eviction and bounded memory at/over chosen capacities. Test Flush/Hijack/Unwrap and unavailable interfaces, panic before/after response headers, cancellation and exactly-once finalization. Secret sentinels in header/body/query/fragment/path must be absent from retained snapshots/rendered telemetry. F8 return preserves workspace identity/layout. No start/stop UI is added.

### Task 10: Contextual help, visible controls and terminal conflict matrix

**Id:** task-10
**Verifies:** unified-tui#ac:help-session-shortcuts, unified-tui#ac:deferred-boundary, unified-tui#ac:visual-focus, unified-tui#ac:panel-navigation
**Status:** in_progress
**Owner:** CLI interaction/QA owner
**Depends on:** Tasks 4–9

Implement per-workspace Alt+F1 toolbar visibility (Ctrl+F1 alias), reclaim its rows while retaining operational alerts and header F1; dialogs keep modal priority. Generate hints/help from context bindings and expose important clickable actions, session selection and /switch. Verify all accepted function keys, Shift navigation and dialog button hints. Resolve proposed bindings with terminal measurements; record supported terminal/OS/modifier configuration and visible alternatives for intercepted keys. Preserve editable text and the feature’s explicit selection alternative; Shift arrows navigate workspace panels/history outside modals. Verify modal-first dispatch, F1 nesting, blocked modal F4/workspace globals, search clear/exit before expansion restore, and one action per key. Grid Shift+Enter remains unavailable; composer Shift+Enter is newline; F5 remains general workspace.

**Evidence:** key matrix for Explorer, grid, composer/editor, Inspect/Related/Actions, search and each modal, including terminal Option/Alt settings and legacy Ctrl+J ambiguity. Combined-state cases include composer + Shift arrows preserving drafts, search + expansion + three Esc presses, modal + search + expansion + four Esc presses, F1 help above a modal, blocked modal F4, and Group/Sort filter focus + Shift+Enter executing only the previewed dialog action. Real terminal mouse and keyboard runs, F1 advertising at narrow widths, focus restore after cancel, session/project changes and unavailable-action explanations. Browser prototype key tests are supplementary only.

### Task 11: Whole journey, adversarial review, published dependencies and delivery

**Id:** task-11
**Verifies:** unified-tui#ac:terminal-journey, unified-tui#ac:preserved-flows, unified-tui#ac:deferred-boundary
**Status:** planning
**Owner:** QA/reviewer for evidence; parent/coordinator for landing and releases
**Depends on:** Tasks 2–10

Run the complete journey in a real terminal against deterministic data and actual HTTP/query execution, including null-action startup, waiting live stream updates, default/chat entries, empty setup, older results and failure/cancel routes. Use component tests for isolation and a whole-journey test without manual reload/rerun crutches. Obtain a separate adversarial review of each implementation diff against this feature; rank findings and fix or explicitly decline them with rationale.

Satisfy full repository CI at exact source SHAs, including 100% shared-library coverage and aichat's raw zero-statement guard; focused/temp-workspace success is insufficient. The sole landing owner lands and releases **tuigoff → aichat → datatug-cli**, updating consumers to published versions and validating with **GOWORK=off** and no local replacements before each consumer's delivery. Use WB landing verbs per repository, retain remote landing/release receipts and authoritative CI conclusions, and complete branch/worktree cleanup. Preserve unrelated/in-progress work. Mark lifecycle completion through SpecScore only after the relevant evidence and remote delivery exist.

**Evidence:** terminal recordings/captures and input matrix; actual SQL/HTTP counts proving no local network operation and no expansion rerun; original/derived RecordSet IDs; exact-SHA CI/coverage receipts, adversarial review verdicts and finding resolutions, published dependency versions, release/binary smoke tests, WB remote landing and cleanup receipts. Missing terminal or release evidence leaves the task open.

## Follow-up journey: make exploration state and alternatives visible

Accepted 2026-10-13 after the initial implementation review. The journey is: identify result scope → transform locally → review/run securely with elapsed/cancel → understand aggregate defaults → revisit a retained branch → find an alternative shortcut. Existing feature/plan lifecycle statuses remain unchanged; each task needs focused full-shell evidence, parent real-terminal QA and final Astra review before publication.

### Task 12: Persistent scope on retained result surfaces

**Id:** task-12
**Verifies:** unified-tui#ac:persistent-result-scope
**Status:** in_progress
**Owner:** Sol CLI implementation owner; coordinator sole landing owner
**Depends on:** Tasks 7–8

Show compact Loaded/Server counts and actual execution times with truthful provenance on results and local group/member panels. Preserve original scope through local/server transitions and make filtered counts distinct.

**Evidence:** full-shell secure SQLite local→server journey, retained origin label/state, compact/wide render assertions and coordinator terminal receipt.

### Task 13: Identity-bound running operations and cancellation

**Id:** task-13
**Verifies:** unified-tui#ac:operation-feedback-cancel
**Status:** in_progress
**Owner:** Sol CLI implementation owner
**Depends on:** Tasks 4–9

Track existing async Open/editor/saved query/refresh/Related/JOIN/derived paths. Render real elapsed seconds and visible keyboard/mouse Cancel. Preserve secure context checks before persistence and fence canceled/late completions; cancellation must not synchronously acquire a held session mutex. Retain independent AI-stream Busy ownership.

**Evidence:** delayed executor full-shell keyboard/mouse cancellation, context-switch/late-response/no-persist assertions, path-specific integration and terminal receipts.

### Task 14: Explain automatic and manual aggregation choices

**Id:** task-14
**Verifies:** unified-tui#ac:aggregate-provenance
**Status:** in_progress
**Owner:** Sol CLI implementation owner
**Depends on:** Task 7

Explain authoritative numeric type suggestions, complete integer non-key proof and exclusions. Render automatic labels only for suggestions; manual row changes and grouping-key edits retain truthful explanations.

**Evidence:** full-shell float/decimal/integer PK/FK/unknown cases, manual overrides, literal filter, compact explanation rendering.

### Task 15: Navigable retained exploration trail

**Id:** task-15
**Verifies:** unified-tui#ac:retained-exploration-trail
**Status:** in_progress
**Owner:** Sol CLI implementation owner
**Depends on:** Tasks 5–8

Expose compact durable lineage and session-local group/member nodes with Trail/Back/Forward. Reconcile same-session additions using retained instances; bound missing/cyclic ancestry and make explicit branching replace forward navigation without a query.

**Evidence:** multi-generation/branching full-shell navigation, repeated local visits, unchanged row/column/filter/order/scroll and original instance across reconciliation; terminal receipt.

### Task 16: Searchable actions and explicit keyboard diagnostic

**Id:** task-16
**Verifies:** unified-tui#ac:shortcut-alternatives-diagnostic
**Status:** in_progress
**Owner:** Sol CLI implementation owner
**Depends on:** Tasks 10, 12–15

Use a context-aware searchable registry dispatching existing actions. Expose Ctrl+P, command alternatives and clickable F1 discovery. Add opt-in bounded received-key diagnostics with safe Esc/Close, known LF ambiguity, no automatic-support claim and no ordinary editor/composer interception.

**Evidence:** full-shell searchable action/disabled reason/help/mouse routes, Ctrl+C/function-key diagnostic isolation, draft/caret return, compact layouts, parent actual key receipts and final independent review.

## Open Questions

The feature's proposed shortcut, server-scope default and backend-support questions are owned by Tasks 6–8 and 10; resolve them with recorded terminal/query evidence before claiming their behaviors delivered. Grid-specific browser handoff and nested ancestor paths remain deferred to separate specifications. No unfinished source implementation is promoted to complete by this plan.

---
*This document follows the https://specscore.md/plan-specification*
