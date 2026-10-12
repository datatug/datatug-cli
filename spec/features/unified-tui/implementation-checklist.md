# Unified TUI implementation inventory

Updated 2026-10-12. In-progress source, focused tests, and separately identified
coordinator terminal receipts. This is not a delivery verdict. The contract is
[README.md](README.md), with execution in the [plan](../../plans/unified-tui/README.md).
No task is declared complete by this inventory.

## Preserved state and ownership

The original three `datatug-unified-tui` worktrees preserve the initial uncommitted implementation. The coordinator mechanically transferred the reviewed CLI paths to the admitted `datatug-tui-aggregates` CLI worktree; final review fixes are authored there, with the original CLI left read-only. Shared edits remain in the original aichat checkout. Sol is the production coding owner; the
coordinator is the sole landing owner. Astra performs final review only. The
user requires integrated readiness and that review before any PR, push or merge.
The reviewed feature/plan baseline is `57d9279`; later accepted user changes
include separate loaded/server quick-sort chords, optional toolbar visibility,
and automatic editable Group aggregates.

The dependency order is tuigoff grid/theme/transcript → aichat chatshell → CLI
chat/execution → entry adapters. Shared behavior is opt-in. The external
`/private/tmp/datatug-aggregates.work` and shared-only `/private/tmp/datatug-shared-r9.work` are validation harnesses, never final dependency delivery. Published versions and `GOWORK=off` validation remain.

WB guard admission passes. The coordinator diagnosed a stale collaboration
checkout snapshot referring to a removed worktree; the supported rebind refuses
without corroborated ownership. No state bypass was used. The coordinator resolved the publication checkout through an admitted worktree and verified the transferred path hashes. The original stale checkout is preserved; no raw collaboration state edits were used.

## Task / AC evidence

| Task / AC | Implemented evidence | Remaining |
| --- | --- | --- |
| 1 / preserved flows, boundary | Actual source audit; legacy regression triage distinguishes real lost journeys from replaced keys/wrapping. Web source untouched. | Full green regression and final independent review/delivery receipts. |
| 2 / visual focus, navigation, expansion | Neutral opt-in shared surfaces; one active edge; gold title; panel separation; stable entry cursor restores after earlier stop changes/removal; expansion retains the same block and result state. Selected column foreground overrides product PK gold, with visible header emphasis; borderless result keeps its title. Compact layout prioritizes readable center and explicit F7 Inspect. Shared journey tests cover modal/search/expansion and removed panel fallback. | Coordinator r7 wide/compact PTY review passed; native screenshot QA when Mac unlocked. |
| 3 / entry, identity | Default/ui/chat share shell; compact default module selector searches printable input and opens explicitly. Provider-unconfigured composer remains usable. Query editor retains drafts per session/object. Authoritative ordered PK/composite identities and wrapped Inspect values. `workspace_journey_test.go`, `workspace_retained_controls_test.go`. | Current integrated terminal run of latest default adapter; final entry/program-option compatibility gates. |
| 4 / search, Explorer actions | Visible Explorer owns search and actions. Open executes secure query and focuses result; Attach/New query execute nothing. Explicit known projection and read-only Alt+I catalog detail scroll/reset. Grid literal digits/slash/Unicode search and one-layer Esc. | Full regression reconciliation and terminal modifier matrix. |
| 5 / Related | Incoming/outgoing authoritative relationships, stable Alt+1..0 slots (0=10), secure reads, session/result/row/generation guard and cancellation, immutable children and Alt+Left origin. Current-context invalidation prevents stale parent relationships under child. | Final adversarial review; incomplete/denied edge receipts retained in focused tests. |
| 6 / JOIN | Explicit Alt+J picker with source/target FK pairs, captured session/result/row/entry/focus and interaction identity, `ApplyJoinCandidateActive`; actual ultraviolet LF decode cannot enter JOIN. Delayed persisted completions reconcile durable data without reloading a newer workspace context. Retained valid JOIN render/lineage and delayed result/row/editor/composer/modal regressions pass. Long JOIN/Related lists follow selection with stable slots and selected FK detail. | Final Astra follow-up and coordinator r9 terminal verification. |
| 7 / Group | Typed immutable loaded snapshot grouping and constituent navigation; default COUNT(*) and eligible SUM; authoritative integer PK/FK exclusion; editable row S/C/M controls with literal filter typing. Exact integer/decimal loaded sums and bounded policy-admitted server aggregation; readable exact preview and durable output types. Actual SQLite UI journey distinguishes loaded two rows, Matching source six rows, and Original output two rows, preserving parent lineage. Grouped output does not pretend entity identity or advertise Related/JOIN. | Coordinator r7 loaded/server aggregate PTY journey passed. Inline JOIN hints repaired with live/reload render regression; coordinator r8 terminal confirmation passed. Short grouped/member viewports now size the grid and retain selected rows/return controls; r9 regressions pass. Final review remains. |
| 8 / Sort | Multi-column typed local stable sorting/reset retains filter and row identity; secure server preview with explicit execution. Alt+Shift+Up/Down loaded; Ctrl+Shift+Up/Down server preview; Alt+S full dialog. Focused real-shell tests prove no execution before confirmation. Coordinator r6 real CSI modifier receipt passed. | Full regressions and final review. |
| 9 / API | Reviewed bounded privacy-preserving monitor/writer facade integrated with actual server/bridge. Full-width temporary API screen preserves layout/drafts. Coordinator actual compact loopback 403/path/chart/live receipt passed. Empty chart reports observed peak zero. | Final integrated review and lifecycle/compatibility gates. |
| 10 / controls | Contextual F1 including modal return, readable compact hints; explicit composer and query-editor selection/copy mode, Tab/Enter and mouse reachable, with bounded Unicode/multiline caret viewport and contextual help; mouse Group controls, editor Run/Select/Back and header Session/Expand; F4 composer. Alt+F1 toolbar (Ctrl+F1 alias) reclaims rows, keeps F1 in header and operational feedback. Focused shell tests and coordinator r6 actual toolbar/modifier journeys pass. Aggregate row hints are contextual. | Real terminal mouse and final viewport review. |
| 11 / delivery | Runnable real-entry private-fixture binaries r1–r9. Coordinator actual wide and 80×24 PTY journeys recorded privately. | All local gates, final Astra, CI gates, shared releases in dependency order, consumer validation, WB landing and cleanup. |

## Numeric/security compatibility

Secure collection now detaches reused mutable driver cells while preserving native
integer, REAL and exact JSON numeric types. Existing typed session codec remains;
parameter loaders use exact JSON decoding. Execution normalizes common exact
small numeric parameters and refuses unsupported unsafe values/typed containers
before evaluation. Focused tests cover adjacent large integers, mixed REAL data,
reused row buffers, durable reload, unsupported exact variables, and no child on
failed derived execution. Old already-rounded INTEGER snapshots need refresh;
the implementation does not guess lost precision.

Original output uses the recursive policy executor for every leaf. The generic
DALgo evaluator cannot guarantee exact large grouping keys or SUM/AVG arithmetic;
unsafe source values and arithmetic shapes explicitly refuse before child
persistence. New explicit measures instead use the bounded exact aggregator over
policy-admitted typed source rows, with hard input row/byte budgets. Direct
single-source originals are supported; existing inner aggregation/HAVING and
joins refuse this exact path. COUNT-only/native Matching source queries remain
separate. No direct database executor bypass is used. Exact preview labels
identify the actual evaluator and pagination, and limited nondeterministic
originals refuse the scope.

## Verification and unresolved gates

Focused tests for current source pass for new keyboard/toolbar/modal/mouse,
query editor, native numeric reload, safe derivation, and buffer detachment
journeys. The latest complete pre-aggregate chat snapshot had one failure, an
obsolete F3 text assertion now replaced with actual focus/overlay assertions.
Current focused unified journeys and the r7 full chat suite pass. The latter
receipt is `/private/tmp/datatug-chat-r7-final.jsonl`. After the r8 inline JOIN and lint fixes, all affected packages (`pkg/chat`, `pkg/secureread`, `pkg/apimonitor`, `apps/datatugapp/commands`) pass in `/private/tmp/datatug-cli-r8-affected-tests.log`.
Failures were individually reconciled; no blanket waiver or deleted journey.

The coordinator independently ran all current frozen shared packages at 100%
statement coverage and both shared linters at zero findings. Manifest and logs
are recorded privately in `datatug-unified-r7-shared-gates.json`. Local toolchain
was Go 1.27.1 darwin/arm64 for those receipts. The r8 full CLI suite passed with 57 passing packages, three skipped packages, zero failures and 94% statement coverage; tidy, hermetic and CGO-off checks also passed. CLI lint passes at zero findings (`/private/tmp/datatug-cli-r9-lint-final.log`). The invalid-provider entry regression confirms an actionable notice and a running local workspace. Final source checks now also use Go 1.27.2 from temporary storage; authoritative CI and final Astra review remain open. Native screenshots are
pending Mac unlock; actual PTY captures are keyboard/render evidence, not native
screenshots. No PR, dependency release, merge or cleanup is claimed.

## Final review r1 repairs (local evidence)

Astra r1 found four major defects and one viewport defect; the coordinator independently reproduced LF entering legacy JOIN in a real terminal. The r9 fixes remove that dispatch, reject stale JOIN UI reloads, follow selected JOIN/Related entries, provide the composer selection alternative, and size local group/member grids. `pkg/chat/final_review_regression_test.go` exercises the actual ultraviolet LF decoder through the shell, a delayed real command/persistence round trip, long lists, Unicode copying without draft/caret loss, contextual help, mouse controls and compact views. The unchanged valid-current JOIN render/lineage regression also passes.

Current r9 full chat package passes in `/private/tmp/datatug-chat-r9-final.log`; CLI lint reports zero findings in `/private/tmp/datatug-cli-r9-lint-final.log`. Full aichat tests in the shared-only workspace pass at 100% statement coverage (3,854 raw blocks, zero uncovered) in `/private/tmp/datatug-aichat-r9-shared-only-cover.out`; shared lint is clean. Tuigoff source is unchanged from the coordinator's green r8 gates. The coordinator reported r8 full CLI tests (57 passing packages, three skipped, no failures), 94% coverage and hermetic/CGO-off gates before this bounded review repair. Coordinator r9 actual terminal receipts verify Unicode selection/copy without sending or losing caret, raw LF staying outside JOIN, and valid Alt+J native JOIN preserving the original and composer draft. The r9b follow-up corrects the focused Select text toolbar; its wide/narrow state tests pass. Final r9b integrated verification, Astra follow-up, dependency publication, `GOWORK=off` consumer validation, authoritative Go 1.27.2 CI and delivery/cleanup remain open. Native screenshot QA remains limited by the locked Mac; actual PTY receipts are separately identified.

## Final review r2 repair (local evidence)

Astra r2 independently verified the frozen source and binary hashes and closed all five r1 findings. One new minor finding remained: Shift+Tab from the second attachment skipped the first attachment and entered Select text. The bounded r10 shared-shell fix restores the complete forward and reverse text/attachments/action ring. A two-attachment full-shell regression checks both directions, Esc, Unicode draft/caret retention, no accidental submit, command-menu completion and opt-out behavior. The shared-only suite passes at 100% coverage (3,856 raw blocks, zero uncovered); lint is clean. The coordinator's compact real-terminal receipt separately verifies the action's contextual toolbar and mouse Cancel preserving the draft. Final follow-up review, published-dependency validation, authoritative CI and delivery/cleanup remain pending.
