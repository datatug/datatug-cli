---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Unified terminal workspace and API observability

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/unified-tui?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/unified-tui?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/unified-tui?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/unified-tui?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

A single terminal workspace supports Explore, chat, result grids, record inspection and live API observability. This specification records accepted TUI behavior and the remaining implementation work. It does not claim that the interrupted source implementation or the browser prototype delivers these requirements.

## Problem

Separate default-navigation and chat programs make the composer, selected result, inspector and local server state difficult to follow. Unmodified action keys conflict with instant search. Result exploration needs clear distinctions between opening data, attaching context, related-record previews, JOINs, and local versus server operations.

## Behavior

The [implementation plan](../../plans/unified-tui/README.md) maps every acceptance criterion to work and evidence.

The [implementation checklist](implementation-checklist.md) records the preserved source audit, partial coverage and remaining delivery work.

### Workspace, focus and identity

Bare `datatug`, `datatug ui` and `datatug chat` enter the same workspace/program. Default Explore retains Projects, Viewers and Settings above an always-available editable composer; `chat` is the convenient composer-focused entry. Empty projects and missing providers expose useful setup/navigation without preventing exploration or editing a draft.

Explorer / Chat-or-Explore / Inspect form three borderless, neutral columns. The header carries project identity and its F3 switch affordance, without repeated Project headings; keep the bold gold DataTug title. A visible, clickable session selector and `/switch` remain available. F4 has one meaning: focus composer.

Exactly one control owns keyboard focus, including while a dialog or overlay is open. Composer and grid cannot be active together. Focused messages/results/composer have a blue left marker. Selected messages use contrasting neutral surfaces, never blue fill. A focused side panel uses a lighter neutral surface (prototype reference `#252d39`), blue title and only the content-facing blue divider: Explorer RIGHT, Inspect LEFT. No full perimeter outline. Neutral row selection survives focus changes; the selected column has a distinct foreground highlight independent of row selection and keyboard focus. Color fallback must keep these distinctions legible in supported terminal profiles.

Outside a modal, Shift+Up/Down navigates chat history and Shift+Left/Right moves between workspace panels, including while the composer, query editor or workspace search owns focus. These accepted chords do not silently become text-selection shortcuts; the routing contract below defines alternatives and modal behavior. F6 and F7 independently toggle Explorer and Inspect. Hiding the focus owner transfers focus to a visible control. Narrow layouts preserve access to both side panels, composer and help without invisible active controls.

F9 and visible Expand enlarge the selected grid across the covered side panels using the SAME block/grid and RecordSet identity. Save panel layout, chat position, row identity, selected column, sort and scroll; do not clone or rerun. Esc restores them after any active search/editing layer has been dismissed, one layer per press as specified below. Shift+Left/Right from expansion first restores the saved layout, then reveals and focuses the requested side even if it was hidden before expansion. F4 transfers input to the composer without losing the result. Inspect retains an explicit selected-result/row target when focus leaves the grid or when an older result is selected; keyboard focus alone is not the inspection identity.

### Input, discovery and Explorer actions

Global bindings and visible controls are:

| Key | Action |
| --- | --- |
| F1 | Contextual hotkeys dialog, always visibly advertised (header when toolbar hidden) |
| Alt+F1 / Ctrl+F1 | Show/hide bottom hotkey toolbar outside dialogs; recover content height, retain operational feedback and visible header F1 |
| F3 | Project switcher |
| F4 | Composer |
| F5 | General web workspace |
| F6 / F7 | Independently toggle Explorer / Inspect |
| F8 | Existing API Monitor overview |
| F9 | Expand selected result |
| F10 | Reveal Inspect and focus Related |
| Shift+Up / Down | Chat history navigation |
| Shift+Left / Right | Panel navigation, including expanded-layout restoration |

The footer combines global and focused-control shortcuts. Important actions are clickable where terminal mouse support is enabled and visible with keyboard alternatives. Section headings, chooser entries and dialog buttons show working hints. Disabled/unavailable actions explain their reason. Modal closure restores the prior valid focus owner.

Plain printable characters, including digits, immediately start or extend incremental search in the focused Explorer or grid. The first character is included. There are NO bare `z`, `r`, `j`, `e`, `q`, `s` or `1..3` workspace actions; these search in Explorer/grid. Inspect/Related has no implicit bare-digit activation: printable input may filter its visibly labelled relation list, or be ignored when no filter is offered, but must not invoke a relation/action or change focus to another control. This does not impose Explorer/grid instant-search behavior on Inspect. `/` explicitly enters search outside editable fields; composer/query editor retain normal text, including slash and digits. Esc first clears a nonempty search, then leaves search. Search/filtering does not mutate the underlying result, and clearing restores its view. The change is opt-in for this workspace: other shared-library consumers keep their existing bindings.

#### Keyboard routing and one-layer Esc

Resolve input using the following order. A key is consumed by one owner; it never falls through to both a dialog and the workspace. Workspace search means the nonmodal Explorer/grid search, distinct from a chooser's filter field.

| State, highest priority first | Shift arrows / Shift+Enter | Esc | F1, F4 and other global actions |
| --- | --- | --- | --- |
| Modal or dialog, including a chooser filter input | Shift arrows belong to the modal controls, never move workspace panels/history. In Group/Sort, Shift+Enter invokes only the enabled previewed server action; Enter invokes only the local action. Other dialogs use their advertised bindings. | Close/cancel only the top modal, discarding its pending choices. A modal's filter text does not intercept Cancel Esc. Help opened above a dialog closes back to that same dialog. | F1 opens contextual help over the modal; F1 in help closes that help. F4 and workspace globals F3/F5–F10 are blocked while a modal owns input, with the hint “Close dialog to return to workspace”; they neither dismiss silently nor act on the underlay. |
| Composer or query editor, with no modal | Shift arrows perform workspace panel/history navigation while preserving draft/caret. Composer Shift+Enter inserts newline; query editors keep their advertised newline behavior. | Leave editing focus for the previous valid workspace control, retaining draft/caret; do not clear text or also restore expansion. | F1 opens help; F4 focuses composer. Other global actions follow their visible workspace binding and preserve editable content. |
| Active Explorer/grid search, with no modal/editor | Shift arrows navigate workspace panels/history; preserve the search/filter state on its originating control while another owner has focus. Grid Shift+Enter reports unavailable. | Nonempty search: clear only. Empty active search: leave search only. Neither press restores expansion. | F1 opens help; F4 transfers to composer, preserving search/filter state on the originating control. Other workspace globals act once and preserve search unless their own action explicitly changes its source context. |
| Expanded grid with no higher input layer | Shift+Left/Right restores saved layout then reveals/focuses the side as one navigation action. Shift+Up/Down selects chat history and restores normal layout if selection leaves the expanded result. Grid Shift+Enter reports unavailable. | Restore saved layout only. | F1 opens help; F4 focuses the persistent composer and keeps result state. Other globals act on this workspace, with no hidden-grid input dispatch. |
| Ordinary workspace control | Shift arrows navigate panels/history. Grid Shift+Enter reports unavailable; ordinary arrows/Enter retain the focused control's advertised behavior. | Use the focused control's visible Back behavior if one exists; otherwise no-op. Never quit from a bare printable key. | F1 opens help; F4 focuses composer; other global actions follow the advertised map. |

Thus an expanded grid with a nonempty search requires three Esc presses: clear search, leave search, restore layout. A modal above it adds one earlier press to close that modal. Returning from a modal restores the exact prior search/edit/expansion state; a press never clears/cancels/restores two layers. F1 help is always reachable and returns to its originating context.

Workspace Shift arrows remain navigation even in editable controls. Text selection uses a visible editor **Select text** action/mode (keyboard reachable via Tab/Enter and mouse where supported); in that explicitly entered modal mode, ordinary arrows extend selection, Enter accepts the selection and returns to editing, and Esc cancels that selection mode without changing the draft. Existing nonconflicting select-all/word-selection shortcuts may remain after terminal verification and must be listed in editor help. No implementation may silently reclaim workspace Shift arrows as text-selection shortcuts.

Selecting a table/view exposes three distinct actions: Open data, Attach to composer, New query. Open uses the existing secure data path and displays a result without erasing chat/history; Attach adds table/view context to the existing composer draft without executing it; New query opens an editable query scoped to that object without silently running it. Current project/session context survives all three. Proposed bindings are Enter to Open, Alt+A to Attach while Explorer owns focus, and Ctrl+N to New query; validate conflicts and terminal delivery before adopting them. These key choices are proposals, while all three visible actions are required.

### Inspect, Related and quick JOIN

Inspect shows a compact path such as `Invoice / 409` using a stable primary identity when available. Composite primary keys remain distinguishable; a missing key is labelled as unavailable and never replaced by a misleading visual row number. Nested paths such as `Customer / 29 / Invoices / 409`, ancestor navigation and truncation are future work.

Related lists authoritative relationships with numbered hints, for example `1 Customer`, `2 InvoiceLine`; `0` denotes candidate ten. F10 reveals/focuses Related. Alt+1..0 from a selected grid or Inspect opens that corresponding related preview; arrows plus Enter provide the alternative, including candidates beyond ten. Bare digits search in Explorer/grid and never activate a relation in Inspect/Related outside an explicitly opened picker, following the scoped printable-input rule above. The Actions heading visibly advertises Alt+A and focuses the actions section when Inspect owns focus. The same chord's proposed Explorer attachment action is routed solely by focus.

A related preview/navigation reads related records through the secure policy executor and authoritative relationship metadata; it is distinct from JOIN. Both outgoing parent and incoming child relationships must be supported to deliver the Customer and InvoiceLine examples. Empty, denied, incomplete-key and unsupported-source cases are explicit. Preserve the originating result and row target, provide return navigation, and reject late responses after session/result/row/relationship changes or cancellation. Never guess foreign keys or silently apply a JOIN to simulate navigation.

Quick JOIN opens an explicit numbered candidate picker through a modified prefix. Alt+J is the verified primary binding. Ctrl+J is disabled because the current terminal decoder maps legacy LF to the same event; LF/Enter must never trigger JOIN. Bare `j` searches. Candidate IDs/numbers come from the authoritative join resolver, remain stable while choosing, and support arrows/Enter and Esc cancellation. Applying a chosen candidate uses the existing secure apply path, retains source and parameters, and produces immutable derived-result lineage while retaining the original. Context changes invalidate the picker and in-flight response.

### Grouping loaded rows or running a derived query

Alt+G opens Group by for the selected result. The dialog has stable numbered columns, text filtering, arrows, Space/digit toggles and a multi-column selection summary that stays visible while filtering. Numbers stay attached to column identity rather than being reassigned by the filter; `0` selects column ten, with arrows/Space for later columns. Printable digits toggle numbered choices in this explicit mode; text filtering can use the focused filter input, including literal digits there. Selection and text-input focus are visibly distinct.

Two explicit actions are always distinguished:

- **Group loaded ↵**: Enter groups only the displayed result's loaded snapshot locally, with no query or network activity. Show the loaded-row count and any partial/limited scope. Preserve the original RecordSet and make the presentation reversible.
- **Run on server ⇧↵**: Shift+Enter derives a grouping query and executes it through the policy executor. Show the exact derived query, selected keys and explicit aggregates BEFORE enabling this action; retain source/parameters and create a new immutable result with parent lineage. Unsupported derivation disables the action with a reason; no silent local/server fallback.

**Cancel Esc** discards pending choices. An empty group selection disables both apply actions. Composer Shift+Enter remains newline; the modified Enter action is local to the grouping/sorting dialog context. COUNT(*) starts enabled. Authoritative float/decimal measures and integer measures proven to be neither primary nor foreign keys start with SUM; unknown relationship metadata never proves that an integer is a non-key. Grouping keys are excluded from aggregates. Defaults remain visible and editable, and changing keys preserves deliberate overrides when they remain valid.

With a column row selected, S selects SUM for a numeric measure, C selects COUNT(column), M selects MIN then MAX, and X removes its aggregate. COUNT(*) is a separate row and cannot become SUM(*). Focused filter input accepts these letters literally. Unsupported SUM types and AVG show a reason. Visible row hints and the exact server preview show every operation before application.

Local semantics: group keys are ordered tuples of typed values, never joined display strings. Null equals null for grouping, is distinct from missing/unavailable values and from empty string, zero or false. Preserve declared type identity; any numeric normalization must be lossless within that declared type. Composite keys compare every component. Group order follows first occurrence in the loaded snapshot unless explicitly sorted; constituent rows preserve original order. Count counts rows; other explicitly selected aggregates document supported input types/null handling and reject unsupported values rather than guessing. Grouping is a derived presentation of a captured snapshot; incoming pages or changed result versions require an explicit refresh/reapply, never silently changing the grouped scope.

### Sorting loaded rows or running ORDER BY

Alt+S opens Sort with the same stable numbered/type-filtered chooser pattern, selected-column summary, visible ascending/descending direction and editable multi-column priority. Enter applies a stable local sort to loaded rows only and shows scope/count; equal keys retain original snapshot order. Shift+Enter shows and runs a derived ORDER BY query through the same secure executor and immutable-lineage contract. Provide reverse/reset to original order; do not mutate the original RecordSet. Empty selections disable apply. Buttons show **Sort loaded ↵**, **Run on server ⇧↵**, **Cancel Esc**.

Local ordering is explicit and deterministic: missing/unavailable values first, then nulls, then non-null values ascending; descending reverses that order. Compare values by declared column type (numeric by exact value, text by Unicode code-point order, booleans false before true, temporal values by normalized instant, binary values lexicographically). Unsupported/mixed types require a visible error rather than display-string coercion. Multiple columns use the displayed priority and per-column direction; ties use original row order regardless of direction. The UI labels this local ordering and does not promise server collation/null order equivalence.

Server derivation preserves source and bound parameters and resolves aliases structurally from the query model. Its input scope, treatment of existing aggregation and placement of LIMIT must be explicit, using the scope contract below. If the selected scope/transformation cannot be represented safely, explain the unsupported case and do not run. Never silently move LIMIT, discard an aggregation or substitute one scope for another. Server-native collation/null order is labelled; any explicit override appears in the preview. A loading/error/cancelled operation leaves the source result accessible, and stale completions cannot replace the selected context.

#### Explicit server input scope

Both Group and Sort dialogs show **Loaded snapshot: N rows** separately from **Server input scope**, including whether the server input count is unknown. Before enabling Run on server, show the selected scope, exact derived query, original LIMIT and its resulting placement, existing GROUP BY/aggregates/HAVING and their treatment, plus changed ordering/projection. Any scope or selection change invalidates the old preview; execution must match the current preview. A server round trip alone does not imply that it covers all source rows.

The proposed initial support/default decision is below. It is an implementation proposal to validate in Tasks 7–8, not a user mandate that the original LIMIT must stay in one position. The accepted requirement is explicit, reviewable query modification and re-execution with no silent scope change.

| Server input scope | Meaning and proposed supported derivation |
| --- | --- |
| **Matching source rows** | For a structurally supported unaggregated query, retain source, joins, WHERE and parameters, then group/sort matching rows before the original final LIMIT. Grouping derives its selected keys/explicit aggregates; sorting replaces the relevant ORDER BY. Show the original LIMIT as a final output cap after the new operation, so it no longer truncates its input. Proposed default for these simple query shapes because this makes server execution useful beyond the loaded subset. A partial client page is never a server input cap. |
| **Original query output** | Re-execute the original query as a subquery, including its existing LIMIT, aggregation and HAVING, then apply the new operation to that query's output. Label “Original query output, limited to L” where applicable; this is a server operation over that bounded result, not a claim to all matching source rows. Proposed default for already aggregated queries, and an explicit alternative for supported simple queries. |

Already aggregated queries under **Original query output** retain their existing grouping/aggregates/HAVING; an outer grouping groups those returned aggregate rows, not underlying records. The dialog states that distinction. **Matching source rows** is initially unsupported for already aggregated/HAVING queries unless the query owner implements and verifies an explicit reaggregation mapping; disable that choice with a reason, rather than dropping the existing aggregate/HAVING or silently switching scopes. A later supported reaggregation must preview every replaced grouping/aggregate/HAVING clause and require the user to choose that scope. Unavailable aliases/columns, nondeterministic limited inputs and unsupported wrapping likewise need explicit diagnostics. The original immutable query/result remains available whichever derived scope is chosen.

Verification must include data where a loaded subset and all matching source rows differ: for example, load two of six filtered rows containing multiple group keys, then show local grouping/counts over two rows versus Matching source rows grouping over six before the output cap. For sort, include a highest-valued row outside the original loaded/limited subset and prove that server matching-source descending sort can return it while local or Original query output sorting cannot. Preview and actual submitted query must agree about LIMIT and aggregate treatment.

Explicit SUM/COUNT(column)/MIN/MAX server measures use bounded typed aggregation over policy-admitted rows from a direct single-source original. INTEGER/DECIMAL sums remain exact; finite FLOAT sums use sequential IEEE754 arithmetic. Input row/byte budgets are hard errors rather than partial aggregates. Existing inner aggregates/HAVING, joins or unsupported types refuse this exact path; no opaque native SQL or generic float aggregation substitutes for it. The preview identifies this evaluator separately from native server ordering.

Separate quick ascending and quick descending actions are required for the selected column. Alt+Shift+Up / Alt+Shift+Down are the selected loaded-row bindings and follow the same local ordering contract. Ctrl+Shift+Up / Ctrl+Shift+Down opens the selected-column ascending/descending server-sort preview; execution still requires the preview’s explicit Shift+Enter. The full dialog retains server execution and multi-column priorities.

### API overview

Reuse the existing API Monitor screen/module for F8, returning to the retained workspace. A persistent live/stopped/error indicator reports actual listener state. Live begins only after successful bind; show actual base URLs, including the assigned port. Per-instance ownership prevents a stopped, failed or old listener from overwriting another instance's live status.

Show request-volume chart, total/error/active-request metrics and duration summary, plus a bounded scrolling log containing time, method, safe path, status/outcome and duration. Include web-to-CLI traffic, rejected/denied routes, live streams and WebSocket requests; active streams are visible before completion and finalize once. Observe outside authorization guards without weakening authorization. Bind failures and stop/error transitions are truthful. Five-second buckets and 500 retained entries are implementation choices to validate, not product mandates.

Retain no headers, bodies, query strings, fragments or capability tokens. Sanitize paths before retention, including token-like path segments; base URLs contain no capabilities. Logs/metrics are bounded process-local telemetry, without exporting sensitive payloads. Preserve Flush, Hijack and ResponseController Unwrap behavior and optional-interface availability. Panics must be recorded as failed outcomes without inventing a successful status or pretending an already-written status changed. Distinguish an error outcome from the HTTP status actually sent. Server start/stop controls are outside this request.

### Scope and deferred web behavior

This feature changes the TUI and shared TUI libraries only. Preserve existing session/provider/project/setup, secure query/HTTP execution, grids/charts, joins, bookmarks and general F5 workspace-browser flows. Shared extensions are compatible and opt-in for consumers outside this workspace.

Selected-grid Shift+Enter browser handoff is deferred. Reserve the gesture with an explicit unavailable explanation; do not open a different result. F5 still opens the general web workspace. A later web change needs an exact sessionID + RecordSetID contract, older-result versions, explicit missing/changed-session errors, and no silent latest-result fallback or rerun. Current host/capability links are not proof of selected-result addressing. Cross-platform hotkey parity and browser/OS conflicts belong to that follow-up; no web source changes are included here.

## Acceptance Criteria

### AC: workspace-entry

Default/ui and chat launches use one workspace/program; Explore retains Projects/Viewers/Settings and composer, chat focuses composer, and empty/provider-unconfigured starts remain useful without extra input.

### AC: visual-focus

Every transition has exactly one input owner, correct neutral surfaces and content-facing side dividers, blue message/composer marker, persistent neutral row selection, independent column foreground highlight, bold gold title and nonduplicated project identity.

### AC: panel-navigation

F6/F7 independently toggle sides; the routing table governs Shift navigation, editing/search/modal combinations and one-owner dispatch; hidden-panel focus transfer and narrow layouts leave all controls reachable without hidden active targets.

### AC: result-expansion

F9/click Expand preserves the same block, RecordSet, row/column, sort, scroll and chat position without execution; Esc restores layout; expanded Shift arrows restore then reveal/focus the requested side, even previously hidden.

### AC: help-session-shortcuts

F1 is advertised and context-aware, the footer combines global/local hints, F3/F4/F5/F8/F9/F10 act as specified, the session picker is visible/clickable with /switch, and controls/headings/dialog buttons have accurate hints and focus restoration.

### AC: instant-search

The first printable character/digit starts Explorer/grid filtering, bare former action keys search, slash preserves editable text, Esc clears then leaves search before a later press restores expansion; Inspect bare digits never activate actions, and non-workspace library consumers keep compatible bindings.

### AC: explorer-actions

Open, Attach and New query are visible, distinct and secure; they respectively display data, attach context without execution, or open editable query without execution, retaining draft/session/history. Proposed keys pass conflict/terminal tests before adoption.

### AC: inspector-identity

Inspect follows retained selected-result/row identity across focus changes and older-result selection, displays stable/composite keys honestly, and never mistakes display row position for primary identity.

### AC: related-navigation

F10/Alt+digits/arrows+Enter expose authoritative numbered parent/child relationships; Alt+A focuses Inspect Actions; secure previews preserve origin/return path and reject stale responses, including empty/denied/incomplete-key cases, without JOIN side effects.

### AC: quick-join

A validated modified prefix opens stable numbered authoritative candidates; Esc cancels, bare j searches, Enter is never interpreted as Ctrl+J, and applying retains source/parameters/original result and immutable lineage through the secure executor.

### AC: group-dialog

Alt+G supports stable column numbers, filtering, multi-selection and persistent summary, explicit aggregates, visible local/server/cancel buttons, disabled invalid choices and preview-before-server semantics; composer Shift+Enter remains newline.

### AC: group-loaded

Enter groups only the captured loaded rows with typed/null/composite semantics, visible scope/count and editable COUNT/SUM defaults, no network, deterministic order and reversible original result; later pages require explicit reapply. Integer and decimal SUM remain exact, finite floats use documented floating-point semantics, and unsupported values fail explicitly. Automatic integer SUM excludes authoritative primary and foreign keys; grouping keys never silently retain SUM. Row-scoped S/C/M controls do not intercept filter text.

### AC: group-server

Shift+Enter runs only the previewed valid derived query through policy enforcement, retaining source/parameters and immutable lineage; the preview explicitly states server input scope, LIMIT placement and aggregate treatment under the proposed support matrix, with unsupported transformations rejected; stale/failure/cancel outcomes retain the original.

### AC: sort-dialog

Alt+S supports numbered/filterable columns, visible direction and priorities, persistent summary and distinct hinted local/server actions; separate quick ascending/descending actions have validated bindings and selected-column foreground feedback.

### AC: sort-loaded

Local sort is stable, reversible and limited to loaded rows, follows declared type/null/collation/tie semantics, shows scope/count and executes no query/network operation.

### AC: sort-server

Server sort previews and securely executes structurally derived ORDER BY with source/parameters and new lineage, labelling input scope/LIMIT placement/aggregate treatment and resolving aliases or explicitly refusing unsupported derivation; server-native order is honestly labelled and stale results rejected.

### AC: api-observability

Actual bind/stop/failure and concurrent listener instances produce truthful persistent state/base URLs; F8 shows bounded charts/metrics/logs for real web-to-CLI, denied, completed and active streaming requests and returns to the retained workspace.

### AC: api-security-compatibility

Telemetry retains no secrets in any permitted field, bounds bucket/log storage, preserves optional response-writer interfaces, and records panic/stream/hijack outcomes accurately without changing authorization or execution behavior.

### AC: preserved-flows

Existing configuration, provider/session/project, secure execution, grid/chart/JOIN/bookmark/HTTP and F5 journeys pass; published shared dependencies work with GOWORK=off and consumers outside the workspace retain compatibility.

### AC: terminal-journey

A real supported terminal walks default Explore, composer/query, result search/expansion, related preview/JOIN, local/server grouping/sorting, API requests and return, preserving older results; a second run covers empty setup and chat entry. Recorded key delivery and actual HTTP/query counts prove the mechanism.

### AC: deferred-boundary

Grid Shift+Enter reports unavailable while composer Shift+Enter inserts newline and F5 opens the general workspace. No web source or unimplemented selected-result URL contract is presented as delivered.

## Accepted follow-up: scope, waiting, explanation, trail and keyboard alternatives

The 2026-10-13 user request “Do all 5” extends the existing workspace journey. “I open a result and can see whether it represents captured loaded rows or a server execution. I sort/group locally, then review and run a server operation. While it waits I see elapsed seconds and can cancel without losing my newer draft or selecting a canceled result. I understand suggested aggregates from schema evidence. I return through the exploration trail without reloading an earlier grid. If my terminal cannot send a preferred chord, I search for the same action or inspect received keys in an explicit diagnostic.”

Keep scope beside every grid, including retained and local group/member views. Loaded counts refer to the captured input, with grid filtering counts separately reported. Server labels require matched persisted ExecutedQuery ID/source/ExecutedAt (including native SQL and empty results), or actual HTTP response provenance, and use real execution time labelled `run`; `refreshed` is reserved for genuine refresh. Scope must remain visible after the dialog closes, with result identity readable in compact layouts.

Running queries (Open data/editor Run, saved query Run and refresh), Related, JOIN and derived server operations expose elapsed seconds and Cancel. Ctrl+X cancels only a tracked operation; mouse Cancel is visible and reachable. Cancellation signals the captured context without synchronously waiting on the session mutex. A cancellation request remains visibly pending until backend completion is observed; synchronous session actions stay fenced while the source may still be finishing. Late/canceled responses cannot persist or select canceled derived output, clear a newer operation, reset a newer row/editor/composer, or fabricate progress percentages. AI streaming retains its existing Busy/cancel ownership and is not mislabeled as supporting the tracked Ctrl+X action.

Suggested measures say `SUM · automatic`. Float/decimal suggestions explain authoritative numeric type; integer suggestions require complete authoritative non-PK/non-FK proof. PK, FK, grouping-key, nonnumeric and unknown-metadata exclusions are explained without guessing from column names. A deliberate override says `manual choice`; changing grouping keys preserves valid deliberate overrides.

A compact lineage path and Trail control expose retained durable parents/children and session-local grouping/member branches. Ctrl+T opens the trail in result contexts; `/trail` and searchable Actions provide alternatives. Alt+Left/Right navigate Back/Forward, with visible controls. Returning uses the same grid/panel instances, retaining row, column, local order, filter, scroll and draft. Branching after Back replaces forward navigation; missing/cyclic lineage is bounded and visibly honest. Session-local transforms are never presented as durable server lineage.

Ctrl+P and `/actions` open a searchable, context-aware action list; F1 exposes a clickable Actions alternative. The registry dispatches existing secure actions and shows disabled reasons. Literal input remains literal in composer/editor/search. `/keys` or the searched Keyboard diagnostic opens an opt-in, bounded received-event display; keys such as Ctrl+C and function keys do not execute product actions while captured. Esc/mouse Close returns without draft/caret loss. It explains the known legacy LF/Ctrl+J ambiguity and does not claim automatic terminal support detection from environment variables. F1 remains the shell's contextual-help escape above dialogs.

### AC: persistent-result-scope

Given a server result, when I sort/group captured rows and then execute a reviewed server derivation, each resulting grid identifies its true Loaded/Server scope and count after closing dialogs, with real execution timestamps only. Filtering never inflates loaded/source counts, and the original scope/state remains retained at 80×24 and wide sizes.

### AC: operation-feedback-cancel

Given a slow policy-bound query, refresh, Related, JOIN or server derivation, elapsed seconds and keyboard/mouse Cancel are visible. Cancellation returns control immediately, signals the operation identity/context, and rejects late completion without canceled derived persistence or newer-context reset. Starting/switching contexts cannot let an older completion cancel or clear a newer operation.

### AC: aggregate-provenance

Given authoritative schema and suggested measures, the Group dialog identifies automatic SUM and its evidence, explains PK/FK/unknown/grouping-key exclusions, and changes to a truthful manual label after S/C/M overrides. Filter input remains literal and valid deliberate overrides survive grouping-key changes.

### AC: retained-exploration-trail

Given a result with JOIN/derived children and local grouping/member branches, visible Trail/Back/Forward actions navigate actual retained instances and restore row, column, filter, order and scroll without execution. Branching, repeated visits, reconciliation, missing parents and cyclic lineage do not corrupt return paths or fabricate durable ancestry.

### AC: shortcut-alternatives-diagnostic

Given a terminal with uncertain modified-arrow/function-key delivery, searchable Actions from Ctrl+P, `/actions` or clickable F1 discover the same existing action paths with enabled/disabled reasons. The opt-in diagnostic shows normalized received events without running product commands, preserves draft/caret on Esc/Close, states known LF/Ctrl+J ambiguity honestly, and remains usable at 80×24. Ordinary typing in composer/query editor never opens these surfaces.

## Open Questions

- **Terminal key compatibility:** implementation owner validates proposed Enter/Alt+A/Ctrl+N Explorer bindings, Alt+J primary JOIN (Ctrl+J disabled for the verified LF ambiguity), and Alt+Shift+Up/Down loaded sorting plus Ctrl+Shift+Up/Down server-sort preview before advertising them. Accepted behaviors and visible alternatives remain mandatory; unresolved chords cannot silently replace them or collide with editable input.
- **Server scope defaults and derivation support:** Tasks 7–8 validate the proposed Matching source rows default for simple queries and Original query output default for aggregated queries, with user-visible scope selection and exact LIMIT/aggregate preview. These are proposed implementation choices, not user-set LIMIT placement. Record supported source/query shapes and explicit refusals; reaggregation of existing aggregates/HAVING needs its own verified mapping.
- **Deferred follow-ups:** exact selected-result browser addressing and nested Inspect ancestor paths require separate specifications before implementation.

---
*This document follows the https://specscore.md/feature-specification*
