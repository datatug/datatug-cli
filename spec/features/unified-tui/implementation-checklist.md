# Unified TUI implementation inventory

Audited 2026-10-13 against DataTug CLI **v0.73.1**, commit
[`da34f4e29e346c91d69f8ac30226bb9cd82ba0da`][cli-source]. The
[feature contract](README.md) retains the acceptance criteria; the
[plan](../../plans/unified-tui/README.md) contains all 16 tasks. Source/test
presence, terminal observations and remote delivery are separate evidence below.
Lifecycle status is maintained through SpecScore.

## Released architecture and delivery

| Repository | Delivered change | Release / merge | Validation |
| --- | --- | --- | --- |
| tuigoff/tuigoff | Opt-in neutral theme, grid/search, transcript and focus/layout primitives | [PR #10][tuigoff-pr], [v0.6.0][tuigoff-release], `f766251af479ef8eaf5c6f58f8a55f9d392bad23` | Five completed target checks: three success, two skipped, zero failures; shared statement coverage 100%. |
| strongo/aichat | Unified chatshell, modal routing, selection mode and attachment traversal; later bounded overlay mouse input | [PR #26][aichat-pr], [v0.13.0][aichat-release]; [PR #27][aichat-mouse-pr], [v0.13.1][aichat-mouse-release], `4d793180360c470c63da86d7db6fc5c70d317f60` | Four completed final target checks: four success, zero skipped, zero failures; 100% statement coverage and raw zero-count-block guard. |
| datatug/datatug-cli | Workspace and five UX additions; Actions/Trail mouse hitbox repair and deterministic retention-clock test fixture | [PR #368][cli-pr], [v0.73.0][cli-release]; [PR #370][cli-mouse-pr], [v0.73.1][cli-mouse-release], `da34f4e29e346c91d69f8ac30226bb9cd82ba0da` | [18 completed final target checks][cli-ci]: 15 success, three skipped, zero failures. Hermetic, PostgreSQL 17/18 journeys, lint/tidy, CGO-off and release/artifact/Homebrew smoke checks succeeded. |

The current [go.mod][cli-mod] uses aichat v0.13.1 and tuigoff v0.6.0 with no
active local replacements. Consumer validation used published versions with
`GOWORK=off`; temporary external workspaces were integration harnesses only.
The source proofs matched 1,063 CLI Go files and 119 aichat Go files to the
published final versions. Shared delivery preceded consumer delivery.

## All 16 tasks: acceptance, source and tests

Paths in this table refer to the pinned CLI release [source tree][cli-source],
unless a provider is named; bare `.go` filenames are under `pkg/chat`. The named tests are retained repository evidence,
covered by the delivered test suites; they are not a claim of a new test run
performed while editing this inventory. Every row was audited against its plan
AC references. Tasks 1–16 have implementation/test and delivery evidence; the
limits below remain explicit rather than being treated as delivered features.

| Task / acceptance criteria | Released implementation | Retained verification / delivery |
| --- | --- | --- |
| 1 — preserved-flows, deferred-boundary | Published shared seams in [tuigoff][tuigoff-source] and [aichat][aichat-source]; CLI composition in `apps/datatugapp/commands/workspace.go`, `pkg/chat/unified_workspace.go`; secure paths retained; web source outside change. | `apps/datatugapp/commands/cmd_ui_test.go`, `pkg/chat/chatui_workspace_routing_test.go`; three-repository release chain above supersedes the interrupted-worktree inventory. |
| 2 — visual-focus, panel-navigation, result-expansion, preserved-flows | Opt-in provider theme/grid/transcript and `tui/chatshell` workspace composition; `pkg/chat/unified_workspace.go` and `workspace_identity.go` retain result/row identity independently of focus. | `chatui_workspace_routing_test.go` verifies one-layer Esc/global modal ownership; `workspace_journeys_test.go`, `final_review_regression_test.go` cover title, compact lists and selection. Provider chatshell tests cover visibility, focus, expansion identity and old-consumer behavior; private wide/compact PTY captures supplement tests. |
| 3 — workspace-entry, inspector-identity, preserved-flows | `apps/datatugapp/commands/workspace.go` mounts existing modules into the same workspace; `cmd_ui.go`, `cmd_chat.go`; `pkg/chat/workspace_query_editor.go`, `chatui_inspector.go`. | `apps/datatugapp/commands/workspace_journey_test.go` (`TestUnifiedEntryCompactModulesSearchThenExplicitOpen`), `cmd_ui_test.go`; `workspace_journeys_test.go` verifies declared composite identity; query/editor and provider-unconfigured entry regressions retained. |
| 4 — instant-search, explorer-actions | `pkg/chat/chatui_sidepanel.go`, `workspace_query.go`, `workspace_catalog_details.go`: Enter Open, Explorer Alt+A Attach, Ctrl+N editable query; printable search and Alt+I read-only details. | `TestUnifiedWorkspaceExplorerActionsAndEditableQueryPreserveDraft`, `TestUnifiedWorkspaceExplorerSearchAndComposerDraft`, `TestUnifiedWorkspaceGridSearchEscKeepsExpansionUntilThirdPress`; no-execution Attach/New query and secure Open assertions. |
| 5 — inspector-identity, related-navigation | `inspector_related_workspace.go`, `inspector_ui.go`, `chatui_inspector.go`: authoritative incoming/outgoing relations, stable Alt+1..0 slots, F10, secure reads, retained origin/return and stale/cancel fences. | `workspace_journeys_test.go`: relationships/composite/policy slots, late-generation/cancel rejection, no previous-row relations, exact-origin return; `final_review_regression_test.go`: long relation lists. |
| 6 — quick-join | `workspace_query.go`, `chatui_pickers.go`, `join.go`: Alt+J explicit authoritative picker and secure `ApplyJoinCandidateActive`; immutable lineage and original retained. Ctrl+J is disabled. | `TestUnifiedWorkspaceDecodedLFNeverEntersInlineJoin`, `TestUnifiedWorkspaceJoinCtrlJAndStalePickerCannotExecute`, `TestUnifiedWorkspaceDelayedJoinRetainsNewerContext`; actual legacy LF and Alt+J PTY captures. |
| 7 — group-dialog, group-loaded, group-server | `result_operations.go`, `result_aggregate_controls.go`, `derived_execution.go`, `pkg/resultops`, `pkg/queryops`, `pkg/secureread/exact_aggregate.go`: typed snapshots, editable COUNT/SUM defaults, explicit scope/preview, policy execution and lineage. | `result_aggregate_controls_test.go`, `result_aggregate_execution_test.go`, `derived_execution_test.go`; resultops/queryops/exact-aggregate tests cover types, nulls, precision, aliases, budgets/refusal and scope. Secure SQLite shell journey distinguishes loaded two rows from matching six and original-output two; private PTY capture supplements it. |
| 8 — sort-dialog, sort-loaded, sort-server | `result_operations.go`, `derived_execution.go`, `pkg/resultops`, `pkg/queryops`: stable local order/reset, multi-column priority, explicit reviewed server derivation. | `workspace_retained_controls_test.go` quick loaded/server chord and modal tests; `TestUnifiedWorkspaceLocalOperationsStableSelectionFilteringAndReset`; queryops tests prove a server sort reaches rows outside original LIMIT; typed ordering/tie tests in `pkg/resultops`. |
| 9 — api-observability, api-security-compatibility | `pkg/apimonitor/{monitor,response,view}.go`, `pkg/server`, `pkg/chat/bridge.go`, `api_monitor_lifecycle_test.go`: real bind/request lifecycle, privacy bounds, actual-status/outcome distinction and F8 retained return. | `pkg/apimonitor/monitor_test.go` covers real loopback active/denied requests, occupied/admission/overlapping instances, eviction, all optional writer combinations, Unwrap/Flush/Hijack, panic and redaction; `empty_chart_test.go`; compact actual-loopback PTY observation. |
| 10 — help-session-shortcuts, deferred-boundary, visual-focus, panel-navigation | `chatui_pickers.go`, `workspace_actions.go`, `workspace_query_editor.go` and provider shell: contextual F1, toolbar toggle, selection/copy, session/Expand mouse controls and accurate hints. | `chatui_workspace_routing_test.go`, `workspace_retained_controls_test.go`, `final_review_regression_test.go`; provider forward/reverse attachment ring regression. Actual CSI modifier, toolbar, Unicode selection and SGR mouse receipts; portability limits below. |
| 11 — terminal-journey, preserved-flows, deferred-boundary | Reviewed provider → consumer publication, actual CLI journeys and released binary/source matching; no web implementation or selected-result handoff claimed. | Private real-process PTY QA, source-bound independent reviews, published-version consumer tests, exact-target CI and all three release receipts above. Final delivery and approved telemetry cleanup supersede old “publication pending” states. |
| 12 — persistent-result-scope | `workspace_trail.go`, `result_operations.go`, `chatui.go`: persistent Loaded/Server scope; matched execution/HTTP provenance, run time, retained/local group/member labels and separate filter counts. | `workspace_ux_five_test.go`: `TestWorkspaceUXScopeLocalServerAndRetainedTrail`, imported-unproven snapshot and native-SQL/empty-result scope tests; secure SQLite shell and compact render assertions. |
| 13 — operation-feedback-cancel | `workspace_operation.go`, `workspace_query.go`, `derived_execution.go`, `inspector_related_workspace.go`, `chatui.go`: captured operation context, elapsed/Stopping state, nonblocking Ctrl+X/mouse cancel, backend lifetime fences and retained late-success reconciliation. | `workspace_ux_five_test.go`: cancel/late context, query paths before persistence, newer-operation preservation, backend mutex fence, composer editing, contended bridge retry, mouse-only background HTTP/JOIN/result reconciliation. Private keyboard/SGR mouse cancellation receipt. |
| 14 — aggregate-provenance | `result_aggregate_controls.go`: automatic/manual explanations from authoritative numeric/PK/FK metadata, grouping-key exclusion and valid override retention. | `TestWorkspaceUXAggregateExplanationsFollowSchemaAndOverrides`, `TestUnifiedWorkspaceAutomaticAggregatesUseAuthoritativeKeysAndExactLoadedValues`; literal filter input, compact explanations and default/manual PTY captures. |
| 15 — retained-exploration-trail | `workspace_trail.go`, `chatui.go`, `result_operations.go`: durable lineage plus session-local group/member nodes, Ctrl+T and Alt+Left/Right navigation, stable retained instances, bounded ancestry and branching. | `TestWorkspaceUXTrailBranchPreservesGridSearchScrollAndReconcile`, scope/trail and repeated local Esc tests in `workspace_ux_five_test.go`; Actions/Trail rendered/clipped/blank/resize/frame tests in `workspace_dialog_hitboxes_test.go`; private repeated Trail/group visits. |
| 16 — shortcut-alternatives-diagnostic | `workspace_actions.go`, `chatui.go` and provider bounded overlay routing: searchable Ctrl+P or `/actions`, clickable F1 discovery, reasons, opt-in `/keys`, safe modal exit/draft retention. | `TestWorkspaceUXActionsDiagnosticAndEditorIsolation`; `workspace_dialog_hitboxes_test.go` verifies actual painted rows and recovery after no matches. Provider `TestWorkspaceBoundedMouse*` tests preserve legacy/unbounded overlay behavior; PR #370/v0.73.1 delivers the boundary repair. |

## Supported choices and explicit limitations

- Explorer bindings are Enter / Alt+A / Ctrl+N, routed by focus. JOIN uses Alt+J; legacy LF and Ctrl+J never open JOIN. Loaded quick sort uses Alt+Shift+Up/Down; Ctrl+Shift+Up/Down opens server preview, with a separate Shift+Enter confirmation. Searchable Actions and visible controls are alternatives.
- Matching source rows is the default for unaggregated direct-field DTQL. Original query output is selected for grouped/aggregate/HAVING originals, but unsupported shapes still refuse. Source/parameters, LIMIT/OFFSET placement and aggregate treatment appear in the exact preview. Structured derivation does not imply arbitrary native SQL transformation.
- Matching-source derivation refuses cursors, derived sources, wildcard/computed projections and reaggregation. Original-output recursive evaluation refuses unsafe numeric/arithmetic/SUM/AVG shapes and unsupported routing/wrapping; limited/offset originals need proven unique ordering. Explicit SUM/COUNT(column)/MIN/MAX require authoritative types and a direct single-source original without joins/inner aggregation/HAVING. Exact aggregation has 10,000-row and 16 MiB input bounds and 16 MiB output/allocation bounds; it never returns partial aggregates on budget failure. Old rounded INTEGER snapshots require refresh; lost precision is not reconstructed.
- API Monitor retains at most 500 requests per instance, 60 five-second buckets and 32 registered instances, with 160-byte safe paths and 16-byte methods. It sanitizes before retention, retains no headers/body/query/fragment/capabilities, and exposes no start/stop UI.
- Selected-result browser addressing and nested Inspect ancestor paths remain deferred. Grid Shift+Enter reports unavailable, composer Shift+Enter inserts newline, and F5 opens the general web workspace. No web source was changed.
- Terminal evidence is actual CLI PTY/ANSI rendering/input on macOS, at wide and 80×24 sizes, including CSI modifiers, legacy LF and SGR mouse. It does not establish every terminal/OS/Option-key configuration or native GUI screenshot fidelity. The explicit diagnostic makes no automatic support claim.

## Local observations and evidence provenance

The following are **private local receipts**, inspected during reconciliation;
they are not permanent repository links or publicly accessible artifacts:

- `/private/tmp/datatug-ux-five-root/qa-receipt-r3.json`: source-manifest/binary-bound real CLI 80×24 PTY, keyboard/SGR mouse cancellation, draft/diagnostic and group/trail repeats. Its historical “publication pending” state is superseded by the delivery receipts, not silently rewritten.
- `/private/tmp/datatug-ux-five-astra-r3.md`: final UX source review closes all eight prior findings; 57 packages passed, three skipped, zero failures, 93.7624% CLI statement coverage and zero lint findings. Its then-unresolved landing authorization/publication gate is superseded by subsequent authorized delivery.
- `/private/tmp/datatug-r7-*.ansi`, `/private/tmp/datatug-r9-*.ansi` and `/private/tmp/datatug-r9b-*.ansi`: earlier real-process wide/compact render, scope, modifier, LF/JOIN, Unicode-selection and mouse observations. These cover their recorded source revisions; published-source matching and later regressions bind the delivered continuation, not a claim that every older capture was rerun on v0.73.1.
- `/private/tmp/datatug-ux-five-root/released-preview-receipt.json` and `delivery-receipt.json`: published-pin consumer validation, PR #368, CLI v0.73.0 and provider release/source proofs. The older telemetry-cleanup exception in `delivery-receipt.json` is superseded by `telemetry-approved-verification.json`, which verifies capture `6ed7cf033ccd8800505081ce8300d619dff9db89`, absent branch/checkout, released claim and clean canonical CLI at the final release SHA.
- `/private/tmp/datatug-dialog-hitboxes/astra-review-r2.md` and `astra-review-r3.md`: clean independent mouse-boundary and clock-fixture reviews, each with zero blockers/majors/minors. `delivery-receipt.json`, `cli-source-proof.json`, `aichat-source-proof.json` and `cli-target-ci-final.json` bind the reviewed continuation to PR #370, the final releases and 18 completed CLI checks: 15 success, three skipped, zero failures. Related delivery worktrees/claims were cleaned; unrelated old checkouts were retained.

The portable source, PR, release and CI links below survive those local receipts.
No local file alone substitutes for remote delivery or proves a broader terminal
support matrix.

[cli-source]: https://github.com/datatug/datatug-cli/tree/da34f4e29e346c91d69f8ac30226bb9cd82ba0da
[cli-mod]: https://github.com/datatug/datatug-cli/blob/da34f4e29e346c91d69f8ac30226bb9cd82ba0da/go.mod
[cli-pr]: https://github.com/datatug/datatug-cli/pull/368
[cli-release]: https://github.com/datatug/datatug-cli/releases/tag/v0.73.0
[cli-mouse-pr]: https://github.com/datatug/datatug-cli/pull/370
[cli-mouse-release]: https://github.com/datatug/datatug-cli/releases/tag/v0.73.1
[cli-ci]: https://github.com/datatug/datatug-cli/actions/runs/38386753803
[aichat-source]: https://github.com/strongo/aichat/tree/4d793180360c470c63da86d7db6fc5c70d317f60
[aichat-pr]: https://github.com/strongo/aichat/pull/26
[aichat-release]: https://github.com/strongo/aichat/releases/tag/v0.13.0
[aichat-mouse-pr]: https://github.com/strongo/aichat/pull/27
[aichat-mouse-release]: https://github.com/strongo/aichat/releases/tag/v0.13.1
[tuigoff-source]: https://github.com/tuigoff/tuigoff/tree/f766251af479ef8eaf5c6f58f8a55f9d392bad23
[tuigoff-pr]: https://github.com/tuigoff/tuigoff/pull/10
[tuigoff-release]: https://github.com/tuigoff/tuigoff/releases/tag/v0.6.0
