---
format: https://specscore.md/plan-specification
status: Implemented
---

# Plan: Deliver terminal workspace usability and visual refinements

**Status:** Implemented
**Source Feature:** tui-refinements
**Date:** 2026-10-13
**Owner:** alex
**Supersedes:** —

## Summary

Deliver all twenty requested terminal refinements plus the topbar-edge and session-lifecycle follow-ups while preserving the released unified workspace flows. Build shared opt-in chrome/tab/state APIs first, then DataTug Explorer/Inspect/input/session integration and secure useful-column planning, followed by full real-terminal journeys and Astra UX/UI review. This plan adds a new feature contract; it does not rewrite the stable unified-tui history.

## Approach

The whole user journey comes first: open the project → move through Explorer and automatically read metadata → search Queries and descendants → recover from no matches with Esc → ask for customer invoice counts → inspect actual Query and compact fields → select two omitted name fields, preview and execute a secure immutable child → switch chats and recover separate drafts/results → close/archive/restore/delete sessions safely → resize and use every control. Each stage's observable good result is specified in the source feature's journey and mapped below. TUI-only scope includes terminal AI guidance and domain derivation needed for point 8; web source is untouched. Shared changes are additive and opt-in. No requested item is deferred; unknown metadata produces an honest disabled choice, never a claimed completed enrichment.

### Request coverage

| Request | Requirement / task |
| --- | --- |
| 1 prominent Explorer selection | explorer-selection / 1, 2 |
| 2 immediate selected-object metadata | explorer-live-metadata / 2 |
| 3 remove Alt-letter dependency | portable-input / 3 |
| 4 topbar/sidebar distinction and gap | surface-hierarchy / 1 |
| 5 header surfaces and bold muted titles | surface-hierarchy / 1, 4 |
| 6 result tabs, Query, Tab parity | result-tabs-query / 1, 4 |
| 7 Inspect tabs | inspect-tabs / 1, 4 |
| 8 meaningful AI columns, multi-add | useful-ai-projection, omitted-columns, column-preview-execution, aggregate-name-enrichment / 5, 6 |
| 9 top F1 always | permanent-help / 1, 3 |
| 10 compact returned/omitted fields | omitted-columns / 4, 6 |
| 11 Esc clears Explorer filter | explorer-clear-filter / 2 |
| 12 formatted no-match state | explorer-empty-layout / 2 |
| 13 quiet shortcut hints | surface-hierarchy / 1, 4 |
| 14 tree expand/collapse | explorer-tree-navigation / 2 |
| 15 Queries group usable in search | explorer-tree-navigation / 2 |
| 16 bottom tree hints | explorer-empty-layout / 2 |
| 17 separate right sections | right-panel-stack / 4 |
| 18 composer gap/cap | composer-separation / 1 |
| 19 Project first, Server right, no context top items | top-navigation / 1, 3 |
| 20 chat tabs and session retention | chat-session-tabs / 1, 7 |
| Follow-up top half-height edge | topbar-edge / 1 |
| Follow-up close/archive/delete shortcuts | chat-session-close, chat-session-direct-close / 3, 7 |
| Follow-up parallel query-column implementation | dedicated query-column lane / 5, 6 |
| Existing flows and whole journey | retained-workspace-flows, terminal-journey-evidence / 8 |

### Source-informed implementation seams

Baseline: datatug-cli commit `24b3e431e74f228145c18f2a3a2dde63fb0801d3`. Revalidate exact heads before implementation/publication. CodeGrapher indexed canonical checkout was used read-only for symbol definitions and relationships; implementation belongs in the task worktree.

- `pkg/chat/chatui.go`: `topBarInfo`, construction options, `appendGridResult` and `loadSession` integrate shell chrome, retained query views and chat tabs. `RecordSet.DTQL`, `QueryID` and `ChatSession.Queries` provide exact immutable query provenance. Local operations need their actual operation chain, not invented SQL.
- `pkg/chat/chatui_sidepanel.go`: `allExplorerNodes` currently collapses before filtering, `explorerNodes` matches labels only, `projectExplorer` appends four action lines and `updateKey` clears only search-owned Esc. Build a complete tree model with stable parent/node IDs; derive filtered visible rows separately and notify explicit Inspect target on selection.
- `currentRecordsetDetails` currently renders a blank row per field. Replace with aligned compact rows and a planner-backed omitted list. `inspectorView`/`setTab` currently mix internal tab cycling and outer Selected/Docked/Bookmarks navigation; define focus ownership before changing rendering. `inspector_related_workspace.go` provides existing Related request/cancel/stale ownership to retain.
- `pkg/chat/chatui_pickers.go`, `workspace_actions.go`, `unified_workspace.go`, help/footer strings, `join_block.go`, result/HTTP/save/export/query overlays contain input and hint surfaces; audit them together. Do not merely rename Alt hints while leaving dispatch aliases or unreachable actions.
- `pkg/chat/grid_state.go` already offers opt-in extra views; add Query once per exact result identity and retain normal Table/Charts/Current row. Rebinding a restored result must not append duplicate extra views.
- `pkg/chat/agent.go`: `buildInstruction` is the actual terminal instruction; narrowed turns rebuild it through `instructionFor`. Browser instruction selection is distinct. Model-authored JOINs are prohibited, so useful-column guidance must invoke a metadata-derived planner/tool instead. Respect explicit user projection.
- `pkg/chat/join.go`: `deriveJoinDTQL` explicitly rejects grouped/aggregate/HAVING queries. Keep that guard. Reuse its expression qualification, alias/edge source checks, existing LEFT JOIN semantics and secure executor routing in a dedicated planner with complete group-key and enforced target uniqueness proof. `ForeignKeySnapshot` currently carries FK pairs and ordered physical columns but no unique-key proof; extend CLI-local metadata/candidate structures additively, load actual primary/unique constraints and refresh them on Apply. Do not call the existing many-to-one label uniqueness evidence.
- Shared libraries: `tuigoff` owns theme/tab rendering and grid event ownership; `aichat` owns workspace layout, header/composer budgets and transcript/session-tab rendering. Proposed additive APIs: `WorkspaceTopBar`, `WorkspacePanelHeader`, `WorkspaceSelectedRow`, `WorkspaceTabs`; grid `SetTabViewCycling(bool)`; shell `WithWorkspaceTopBarProvider`, optional panel `WorkspaceHeader()`, `WithWorkspaceSpacing`, `WithWorkspaceChatTabs` and pinned `WithWorkspaceChatTabActions`. Shared shell captures/restores `ComposerState{Text,Cursor,Chips}` and `WorkspaceViewport{EntryID,ScrollOffset}`; CLI maps these by exact session ID. Other consumers retain defaults.

### Execution and dependency order

The root agent owns publication, merge and cleanup. This CLI lane authors these two documents and, after explicit phase release, owns CLI layout/input/Explorer/Inspect/session implementation and store lifecycle. The shared-library lane owns provider repositories. A separately isolated query-column lane owns new `pkg/chat/result_columns*.go` backend/dialog/tests plus `agent.go` terminal guidance; it does not edit `grid_state.go`, `chatui_sidepanel.go`, `inspector_ui.go` or `workspace_actions.go`. CLI wires the column overlay/metadata display/action hooks after root integrates the query-column candidate. No two lanes edit the same file concurrently. No lane starts servers, alters unrelated Detective/Checks worktrees, commits, pushes or merges independently. Root's plan review is a phase boundary, not a request for another user approval. Root advances lifecycle using the official SpecScore CLI; authoring does not hand-edit statuses.

## Tasks

### Task 1: Add opt-in shared chrome, tabs and retained state contracts

**Id:** task-1
**Verifies:** tui-refinements#ac:surface-hierarchy, tui-refinements#ac:permanent-help, tui-refinements#ac:top-navigation, tui-refinements#ac:composer-separation, tui-refinements#ac:topbar-edge, tui-refinements#ac:explorer-selection, tui-refinements#ac:result-tabs-query, tui-refinements#ac:inspect-tabs, tui-refinements#ac:chat-session-tabs
**Status:** complete
**Implemented-by:** strongo/aichat@f5e0d4102186c4d8fb06d524d5286fed5be2140e
**Evidence:** Released providers [tuigoff v0.7.0, PR #11](https://github.com/tuigoff/tuigoff/pull/11) and [aichat v0.14.0, PR #28](https://github.com/strongo/aichat/pull/28); exact merge-target checks passed, published source matched the reviewed files, and both provider worktrees were retired through WB. Completed CLI validation against these released pins is recorded in task 8.
**Depends-On:** —
**Owner:** shared-library lane; root publishes dependencies

Implement the additive seams above. Reserve right top items before truncating left context. Add one budgeted chat-tab row with pinned New/overflow controls; active tab stays visible. Give grid local Tab ownership only in opt-in mode and after text/modal ownership. Capture/restore composer caret/chips and transcript viewport without creating source execution. Use one geometry calculation for top gap/edge, session row, side headers, composer gap/cap and mouse/history coordinates. Prove default consumers unchanged and 24-row layouts bounded. Root releases provider versions before final consumer validation; temporary workspace linking is diagnostic only.

### Task 2: Make Explorer selection, search and metadata a coherent journey

**Id:** task-2
**Verifies:** tui-refinements#ac:explorer-selection, tui-refinements#ac:explorer-live-metadata, tui-refinements#ac:explorer-clear-filter, tui-refinements#ac:explorer-empty-layout, tui-refinements#ac:explorer-tree-navigation
**Status:** complete
**Implemented-by:** 547cf596c8739ec7ca85525249804aa0a44ddcde
**Depends-On:** 1
**Owner:** CLI lane

Separate complete catalog tree, collapse state and filtered visible rows. Preserve stable identity and clamp viewport after changes. Arrow navigation/click selection updates explicit object/group Inspect target immediately; derive admitted metadata only, with a spy asserting zero executor calls. Search group labels includes usable descendant context; matching descendants retain their ancestors and are searchable even under collapsed branches. Esc clears from tree/search focus. Bottom hint strip is anchored with concise no-match state and disabled/unavailable actions. Test branches, Queries, no matches, literal h/j/k/l/å, long labels, nested/last items and focus transitions.

### Task 3: Reconcile keyboard ownership and all discoverability surfaces

**Id:** task-3
**Verifies:** tui-refinements#ac:portable-input, tui-refinements#ac:permanent-help, tui-refinements#ac:top-navigation, tui-refinements#ac:chat-session-direct-close, tui-refinements#ac:retained-workspace-flows
**Status:** complete
**Implemented-by:** 547cf596c8739ec7ca85525249804aa0a44ddcde
**Note:** Implemented in CLI PR #372. Ordinary typing remains literal; context Actions replace unsafe Alt-letter navigation.
**Depends-On:** 1
**Owner:** CLI lane

Write one conflict matrix by focus zone and modal/input owner. Remove Alt-letter dispatch aliases and hints in every workspace/dialog/action/help/fallback surface; add context Actions for attach, JOIN, group/sort/style and bookmark management rather than assigning unsafe Ctrl-letter replacements. Preserve Ctrl+P/F1/F3/F4/F6/F7/F8/F9/F10, existing safe shortcuts and actions. Keep literal input literal. F1 is always top-advertised; top content only has requested navigation and truthful Server state. Add Ctrl+W chooser and terminal-distinguishable F12 → A archive / F12 → D delete prefixes (including composer focus, leaving Ctrl+K editing intact), bounded pending state and Esc cancel. Prefix hints are not confirmation; the direct gestures must not ask again. Keyboard diagnostic describes legacy ambiguities accurately. Assert each advertised key dispatches once to the intended owner and every retained operation has a reachable alternative.

### Task 4: Integrate result Query tabs and stacked right panels

**Id:** task-4
**Verifies:** tui-refinements#ac:result-tabs-query, tui-refinements#ac:inspect-tabs, tui-refinements#ac:right-panel-stack, tui-refinements#ac:omitted-columns, tui-refinements#ac:surface-hierarchy
**Status:** complete
**Implemented-by:** 547cf596c8739ec7ca85525249804aa0a44ddcde
**Depends-On:** 1, 2, 3
**Owner:** CLI lane

Bind Query content to exact immutable execution when appending/restoring cards, including empty/historical/refreshed/joined/local-derived/HTTP results and explicit unavailable/provenance states. Query viewing never executes. Use shared underlined tabs and local forward/reverse ownership. Split Inspect, Related and Actions into separate header/content sections with one gap and explicit active-section budget; preserve Docked/Bookmarks reachability. Render compact returned fields with qualification/type/role, reserving the omitted list seam for task 6. Test selection-to-object metadata and result return, missing metadata, long lists/last item, simultaneous Related status and Inspect content, scrolling and resizing.

### Task 5: Implement metadata-proven useful-column domain planning

**Id:** task-5
**Verifies:** tui-refinements#ac:aggregate-name-enrichment, tui-refinements#ac:column-preview-execution, tui-refinements#ac:useful-ai-projection, tui-refinements#ac:omitted-columns
**Status:** complete
**Implemented-by:** 85df2a8f2f14bd6477bc72bf5a729a952191604f
**Depends-On:** —
**Owner:** query-column lane in its own isolated CLI worktree; root integrates candidate

Add a dedicated candidate/preview/application seam with source/snapshot/candidate identity and exact projection provenance. Load authoritative columns, permissions, FK pairs, enforced target unique/primary constraints, and sufficient collation/type-affinity equality-semantics evidence. Prove both at-most-one target match and one display tuple per original GROUP BY class; uniqueness alone is not enough, and missing compatibility evidence refuses. Support direct projection additions and required full-FK-group-key aggregate enrichment through cardinality-preserving LEFT JOIN before grouping. Preserve all existing clauses/limits and exact numeric semantics; fail closed for unsupported shapes without relaxing legacy JOIN guards. Preview derives a complete document and immutable plan fingerprint; Apply revalidates source/session/metadata/policy and executes exactly that plan via secure executor. Preserve lineage and grouped-output nonidentity. Share this planner between UI and terminal model tool; models select opaque metadata candidates, never construct ON predicates. Start with secure SQLite fixture proof of supported serializer/evaluator routing, then implement UI.

Fixture: Customer has enforced unique CustomerId and distinct FirstName/LastName, Invoice has multiple rows per customer, a nullable CustomerId, an unmatched CustomerId, filters and enough rows beyond a displayed/limited subset. Compare every original group/count before and after adding two names; include binary-unique target a/A against a NOCASE source grouping key and verify refusal through the actual secure executor without changing the original count; test nonunique duplicate targets, composite/partial keys, ambiguous aliases, forbidden field, stale metadata, HAVING, order/offset/limit, exact numerics and unavailable source. No client lookup or unguarded SQL substitutes for the secure execution.

### Task 6: Wire terminal AI guidance and multi-column Apply journey

**Id:** task-6
**Verifies:** tui-refinements#ac:useful-ai-projection, tui-refinements#ac:omitted-columns, tui-refinements#ac:column-preview-execution, tui-refinements#ac:aggregate-name-enrichment
**Status:** complete
**Implemented-by:** 547cf596c8739ec7ca85525249804aa0a44ddcde
**Depends-On:** 3, 4, 5
**Owner:** query-column lane owns overlay/new files and agent.go; CLI lane owns post-integration sidepanel/action wiring

Expose planner-backed omitted fields below returned fields with multi-checkbox selection and Apply. Keep unavailable fields truthful and do not expose denied names beyond admitted catalog disclosure. Apply opens exact reviewed query/scope/proof, then explicit Run uses captured session/result/generation ownership, operation elapsed/cancel and new immutable child. Actual terminal AI instruction and tools prefer useful identity/display fields and can invoke the same safe planner on newly captured results; attach no guessed JOIN text. Deterministic provider tests inspect the actual terminal request and dispatch safe tool candidates through production SessionChat; assert a useful customer-count answer. Browser instruction/tool behavior is opt-out/unchanged. Integration tests assert one execution for two fields, parent unchanged, exact preview equals submission, persisted reload, denied/stale/cancel paths and newer draft preservation.

### Task 7: Move durable sessions into chat tabs with retained drafts

**Id:** task-7
**Verifies:** tui-refinements#ac:chat-session-tabs, tui-refinements#ac:chat-session-close, tui-refinements#ac:chat-session-direct-close, tui-refinements#ac:top-navigation, tui-refinements#ac:retained-workspace-flows
**Status:** complete
**Implemented-by:** 547cf596c8739ec7ca85525249804aa0a44ddcde
**Depends-On:** 1, 3, 4
**Owner:** CLI lane

Use SessionChat list/create/switch/rename/delete and existing store scope rather than inventing another session store. Add compatible persisted archive metadata and explicit terminal open-tab filtering/restoration; archived sessions remain available through a restore action without reopening automatically. Keep browser instruction/routes and existing API semantics stable. Capture draft/caret/chips and viewport before switching; restore after exact-session history rehydration. Retain per-session grids/selection/tab state in session-local maps so switching does not discard the earlier viewport or rerun queries. Keep open-tab order stable across activation independently of the recency-sorted picker; append new/restored sessions and remove archived/deleted tabs. Pin New and overflow picker, keep active tab visible at narrow width and handle long/duplicate titles through IDs. Busy fences and captured operation identities protect switching; stale responses cannot cross sessions. Implement Ctrl+W close chooser (Archive/Delete/Cancel) and direct prefix shortcuts from task 3. Archive preserves data; delete purges that session's cached rows/queries/HTTP/artifacts and every owned memory map/chip/draft/viewport, cancels/fences in-flight work and prevents late persistence. Remove dangling references without deleting independent data from other sessions. Closing the last tab selects a usable fresh chat. Verify two unsent drafts, result retention, switching via click/chords/slash picker, new/rename/close/cancel/archive/restore/delete/direct shortcuts, restart, store cascade/isolation and viewport fallback when a target is removed.

### Task 8: Verify all production journeys and obtain Astra UX/UI review

**Id:** task-8
**Verifies:** tui-refinements#ac:terminal-journey-evidence, tui-refinements#ac:retained-workspace-flows, tui-refinements#ac:result-tabs-query, tui-refinements#ac:aggregate-name-enrichment, tui-refinements#ac:chat-session-tabs
**Status:** complete
**Implemented-by:** 547cf596c8739ec7ca85525249804aa0a44ddcde
**Note:** Completed in CLI v0.74.0. Combined source passed full tests, vet, build, specification lint and golangci-lint against released providers without overlays. Astra final source/UX review passed. PR CI passed with 7 successful and 9 skipped checks; merged-target CI/release passed with 15 successful and 3 skipped checks. Published-artifact checksum/signature/notarization/runtime verification passed. WB retired both CLI source worktrees and their integration candidate; provider worktrees were already retired. The closure record changes no executable source.
**Depends-On:** 2, 3, 4, 5, 6, 7
**Owner:** CLI lane for tests/evidence; root for review/publication

Run meaningful focused model/domain tests through WB, then the existing required package/full-repo/coverage/lint/build checks once the final dependency heads are known. Add a full-shell secure SQLite journey with deterministic provider that walks steps 1–8 through actual key/mouse events; count executor calls to prove metadata selection, Query tabs and session switching do not rerun. Include zero/one/many results, empty/no-provider start, long tree/field/Related/session lists, forward/reverse tab traversal, hidden panes/footer, expanded results, loading/cancel/stale responses, resize and final-item visibility. Preserve the existing security/query/HTTP/JOIN/group/sort/bookmark/export/trail/setup suites.

Build the production executable using released provider versions (`GOWORK=off`, no local replace), then run it in a real terminal at 140x42, 100x30 and 80x24. Capture default, green selection+live metadata, Queries filter, no-match recovery, result Query, compact omitted-field multi-apply preview+child, stacked right panels, multiple chats with restored drafts, close chooser/archive/restore/direct deletion, composer cap/top edge and fallback states. Record exact CLI/provider versions, SHA, dimensions and terminal/key events; verify mouse hit targets, caret, Unicode/fallback and normal terminal restoration. Root dispatches Astra on real screenshots/journeys to break usability and aesthetics, with the original twenty points and follow-up, fixes or explicitly records every finding, and repeats affected real-terminal checks. CI green alone is not a UX acceptance receipt. Root alone commits/pushes/creates PRs, lands dependency-first, revalidates consumer against releases and cleans task branches/worktrees.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
