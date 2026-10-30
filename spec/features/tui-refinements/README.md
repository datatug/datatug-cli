---
format: https://specscore.md/feature-specification
status: Stable
---

# Feature: Polished terminal exploration workspace

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/tui-refinements?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/tui-refinements?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/tui-refinements?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/tui-refinements?op=request-change) |
**Status:** Stable
**Source Ideas:** —

## Summary

Refine the released terminal workspace through clearer selection, structured metadata, portable shortcuts, consistent tabs, session navigation and useful query columns.

## Problem

The released unified terminal workspace has the right exploration capabilities but weak visual hierarchy and inconsistent keyboard ownership. Explorer selection is easy to miss, its filter can hide useful groups, changing an object does not immediately target Inspect, and empty results become a wall of hints. Result and Inspect controls do not read as tabs. Top chrome contains contextual actions and session identity that belong inside the workspace. On macOS, Option-letter input can insert text instead of sending the advertised shortcut. A technically correct customer-count query can omit the names needed to understand its rows.

This feature refines the existing terminal journey and supersedes only the presentation and input behaviors explicitly named here. The stable `unified-tui` feature and its completed plan remain historical evidence; their secure execution, retained-result, focus, cancellation, browser handoff, setup and discovery guarantees continue to apply. Web source and product flows are outside this change.

## Behavior

### User journey and observable results

1. “I open DataTug and see my project, Chat, Server state and F1.” The title is legible, the actual listener state is truthful, and top chrome is visually separate from the content.
2. “I move through Explorer.” Selection is prominent, each object immediately supplies metadata to Inspect without Enter or a database query, and tree branches expand/collapse with arrows.
3. “I type Queries, browse its children, then type a term with no matches.” Group names and matching descendants remain navigable; the empty state is concise, with a clear Esc recovery and hints anchored to the bottom.
4. “I press Esc and ask for customers with their invoice count.” The filter clears from either Explorer focus state. The terminal AI receives guidance and supported metadata-derived tools to choose meaningful identity/display columns alongside numeric measures.
5. “I inspect the result, its query and omitted columns.” Underlined tabs cycle forward/back consistently; Query shows the immutable execution behind this exact card; returned fields are a compact table; eligible omitted fields have checkboxes and explanations.
6. “I choose FirstName and LastName, Apply, review the exact query and run it.” A secure server execution creates an immutable child result. Customer counts and group cardinality remain unchanged, including null or missing FK matches; the original remains accessible.
7. “I create another chat, type a different draft and switch back, then close a tab.” Chat tabs preserve each session's draft/caret, history, retained results and viewport. Ctrl+W offers Archive, Delete and Cancel; direct archive/delete shortcuts perform their named operation without another question. Archived sessions can be restored; deletion removes that session's cached data. No query reruns as a side effect of switching or viewing tabs.
8. “I use the same workspace at a short or narrow terminal size.” Headers, gaps, composer, active tabs and controls fit the viewport; the final tree/related/field item remains reachable; mouse targets agree with rendered coordinates.

### Visual contract

Use mint/green foreground and a neutral contrasting background plus a shape marker for the selected Explorer row. Reserve gold for the DataTug title and selected result column. Distinguish topbar, canvas, focused side surface, panel headers and footer/hint strips. Panel titles are bold and muted; shortcut hints are quieter. Preserve the focus-blue content-facing divider. Use true underlining for active tabs, with a plain-text marker fallback when styling is unavailable.

The top line reads `DataTug ¦ Project: {title} [F3] ¦ Chat` on the left, with `Server {actual state} [F8]` and `Help [F1]` on the right. The broken bar avoids a full-height divider. Session identity moves into a tab row inside Chat. Contextual actions, including Expand, remain in the focused content, footer or Actions. Operation status/cancel remains visibly discoverable in the workspace without displacing permanent top navigation. At narrow widths, shorten/truncate the project title and labels with priority for DataTug, project access, Server and F1; no horizontal overflow.

Right-side Inspect, Related and Actions are separate header/content sections with one empty row between sections. Inspect's Row/Column/Result tabs belong to its header. Related keeps its own F10 header and loading/empty/error/content state; switching focus does not replace the entire Inspect view. Docked and Bookmarks remain reachable through their retained section navigation and Actions. Height is allocated once across headers, gaps and bounded/scrollable content. In a short terminal prioritize the active section, keep every other section reachable and identify collapsed content explicitly.

Use one empty separator row above the composer and a subtle upper-half-block cap in its background color. Give the topbar a similarly subtle half-block lower edge or plain bottom-rule fallback. Both decorations count in the layout budget and remain consistent with content and mouse coordinates.

### Input contract

F1 always remains advertised at the top. F3 chooses project; F4 focuses composer; F6/F7 control Explorer/Inspect; F8 opens Server; F9 expands the focused result; F10 focuses Related; Ctrl+P opens searchable context Actions. Existing nonconflicting shortcuts remain. Remove Alt-letter primary bindings and their executable aliases everywhere in the workspace, dialogs, help, action registry and fallback strings. Use context Actions/buttons for attach, JOIN, group, sort, style and bookmark actions when a conflict-free dedicated chord is unavailable. Do not replace them with Ctrl+J or Ctrl+I: legacy terminals can decode those as Enter/LF and Tab. Ctrl+W opens the close-session chooser. F12 then A archives the current session immediately; F12 then D deletes it immediately. This function-key prefix works from normal workspace controls, including the composer, without taking Ctrl+K away from text editing. The prefix shows its choices, consumes only the explicit next chord, and Esc cancels it; this is shortcut discovery, not another confirmation. Prefix handling is outside modal/input completion ownership, uses a bounded pending state and never reinterprets ordinary text outside the prefix. These sequences remain distinct in legacy terminals, unlike Ctrl+Shift-letter variants. Ctrl+Tab/Ctrl+Shift+Tab switch chat sessions only when the workspace owns that event; ordinary Tab/Shift+Tab cycle the focused result or Inspect tabs and preserve widget/modal ownership.

Printable text, including macOS Option+A yielding `å`, stays literal in composer, query editor, Explorer search and other text inputs. Existing Alt-arrow/digit chords may remain only where they do not consume ordinary typing and have a searchable/clickable alternative. Esc in Explorer clears a nonempty filter immediately even when the tree, rather than its search box, owns focus. Empty-filter Esc retains the existing leave-search/return behavior. Tree Left collapses a branch or selects its parent; Right expands a branch or enters its first child. A filtered view searches the complete model independently of collapsed presentation; group-name matches reveal usable descendant context. Restoring the unfiltered tree uses stable node identity.

### Useful columns and secure derivation

Apply identity/display guidance to the actual terminal AI system instruction and supported tools, including narrowed-schema turns. Prefer meaningful catalog-described names/labels beside keys, respect explicit user projections, avoid gratuitous wide output, and explain absent/ambiguous metadata. Do not tell the model to author forbidden JOINs or infer identity from a field-name suffix. Browser instructions and web routes remain unchanged.

Build omitted-column candidates from the query's parsed relation/projection provenance and source-scoped authoritative schema/permissions. Separate selectable direct fields and proven outbound FK display fields from unavailable, ambiguous, denied or semantically unsafe fields. A list labelled complete requires complete admitted metadata; otherwise say coverage is unavailable/partial. Never invent a checkbox for a field the planner cannot execute. Show source qualification, type and a specific disabled reason where disclosure is allowed. Selecting multiple checkboxes produces one reviewed execution, not one query per field.

Use a dedicated column planner; do not weaken `DeriveJoinTugQL`'s existing aggregate guard. For the required Invoice `CustomerId + COUNT(*) AS InvoiceCount` example, prove that the complete projected group key maps through an authoritative outbound FK to an enforced unique/primary Customer target key. Derive a LEFT JOIN before aggregation, keep the original group key and aggregates, and add the selected target display fields to projection and GROUP BY. Require authoritative compatible equality semantics across the target unique constraint, generated JOIN and original GROUP BY: collation and type-affinity evidence must prove both at-most-one target match and one target display tuple per original group. A unique declaration alone is insufficient. Refuse when this compatibility cannot be proven; no guessed collation or flexible SQLite type coercion establishes it. This complete proof preserves groups and COUNT semantics, including null/missing targets. Qualify expressions and use collision-safe aliases. Preserve source, filters, parameters, HAVING, ordering, offset and final limit. Grouped output remains aggregate output without entity identity even after adding names. Reject unsupported numeric/evaluator/query shapes instead of coercing values or removing clauses. Composite keys require every paired key and complete uniqueness evidence; duplicate/nonunique targets refuse.

The planner's preview includes the exact derived document, changed fields, FK/cardinality/equality-semantics proof, preserved clauses, scope and execution mechanism. The terminal model uses opaque metadata candidate IDs and the same exact preview fingerprint in a bounded enrichment tool, within the current user turn's query authorization. Automatic model execution is labelled as such and never described as human-reviewed. The interactive checkbox workflow retains explicit preview and Run; narrowed model turns retain this supported tool and guidance. Apply routes through the existing secure source-bound executor and operation ownership. Refresh/revalidate metadata and permissions before execution; changed candidates or scope invalidate the preview. Persist a new result and executed-query record with parent lineage; never patch original rows or perform a client-side name lookup. Errors, cancellation or stale session/selection/generation completions preserve the original result and newer draft. Query displays actual executable TugQL/SQL where retained; a composed operation without one equivalent executable document shows honest original query plus operation/provenance instead of fabricated SQL. Parameter names may be shown using existing safe rendering; secret/capability/bound selection values are not exposed.

## Dependencies

- unified-tui

## Acceptance Criteria

### AC: explorer-selection

Given a focused or unfocused Explorer with a selected node, when selection moves, then the current row uses mint/green, neutral contrast and a shape marker; gold remains independent for selected result columns and titles. Selection survives focus changes and is legible without color.

### AC: explorer-live-metadata

Given an Explorer table, source, project, view, query or group, when its selection changes, then Inspect immediately targets that object/group's admitted metadata with no Enter and no data query. Returning to a result restores explicit result inspection; object metadata never masquerades as result metadata. Missing metadata has a concise honest state.

### AC: portable-input

Given any workspace/input/dialog focus, when Option-letter produces literal text, then that text is preserved and no product action fires. No Alt-letter binding or misleading hint remains; every removed action stays reachable through an enabled context control or searchable Actions. Ctrl+J/Ctrl+I ambiguity is not used as a workaround.

### AC: surface-hierarchy

Given wide and compact layouts, when a side receives focus, then distinct topbar/canvas/side/header/footer surfaces remain visible with a budgeted top-content gap and focus-blue content divider. Bold muted panel titles and quiet hints remain independently styled.

### AC: result-tabs-query

Given any current or historical result card, when Tab/Shift+Tab cycle Table, Charts, Current row and Query, then the active tab is underlined, cycling wraps locally, and row/column/filter/scroll survive. Query shows this result's immutable execution/provenance without rerun, latest-result substitution or guessed SQL; missing query provenance is labelled.

### AC: inspect-tabs

Given focused Inspect, when Tab/Shift+Tab cycle Row, Column and Result, then the same underlined tab grammar and local cycling apply. Inspect tabs remain in its header; other right sections and grid tabs do not unexpectedly receive the event.

### AC: useful-ai-projection

Given a terminal AI data request including “customers with number of invoices per customer”, when the real instruction/tools construct the answer, then useful schema-supported customer display fields are requested through the safe column planner alongside CustomerId and InvoiceCount. A deterministic provider fixture asserts the actual system/tool request, not a copied prompt fragment; narrowed turns retain guidance and explicit projections remain respected.

### AC: omitted-columns

Given a result with admitted source metadata, when its fields are inspected, then returned fields form a compact aligned table and omitted fields are listed below with qualified name, type and eligibility. Several fields can be selected and deselected before one Apply; unknown/denied/ambiguous/unsafe metadata cannot become a false executable choice or false complete list.

### AC: column-preview-execution

Given at least two selected omitted fields, when Apply opens the reviewed preview and its Run is activated, then the exact current preview executes once through secure source-bound admission and creates a new immutable child with query/parent lineage. Original projection/results remain byte-for-byte unchanged. Editing candidates invalidates the preview; stale schema/permissions/session/cancel completions cannot execute/select an obsolete result.

### AC: aggregate-name-enrichment

Given grouped Invoice counts with projected full CustomerId FK and enforced unique Customer key, when FirstName and LastName are added, then a metadata-derived LEFT JOIN/grouping plan executes and every original group, InvoiceCount, null group and missing-customer group is preserved. Filters, HAVING, parameters, order, offset and limit remain as previewed. Nonunique/duplicate target metadata, partial composite keys, incompatible or unknown collation/type-affinity semantics, denied names and unsupported shapes refuse before execution. A secure-executor regression uses binary-unique Customer keys a/A with a NOCASE Invoice grouping key and proves refusal before any enrichment execution; the original group/count remains unchanged. The enriched aggregate does not gain row entity identity.

### AC: permanent-help

Given visible or hidden bottom toolbar and any normal layout, when rendered, then top Help [F1] is visible/clickable and F1 reaches contextual help, including above dialogs. F1 and Actions do not rely on a bottom hint that can disappear.

### AC: explorer-clear-filter

Given a nonempty Explorer filter and focus on its search or tree, when Esc is pressed, then the filter clears on that first press, selection/offset clamp by stable identity and the unfiltered tree is usable. With an empty filter, Esc preserves existing leave/return ownership.

### AC: explorer-empty-layout

Given a no-match filter, when Explorer renders, then a brief heading and recovery instruction have deliberate spacing; no wall of per-action lines appears. Bottom-anchored tree hints use a distinct strip, muted descriptions and contrasting keys within width/height bounds.

### AC: explorer-tree-navigation

Given expanded/collapsed nested branches, when arrows or branch activation are used, then Left/Right collapse/expand/enter/select-parent coherently and the last visible child remains reachable. Printable h/j/k/l remain search input. Filtering Queries selects the group and exposes usable descendants even if the underlying group was collapsed; no search match is an unreachable ghost node.

### AC: right-panel-stack

Given inspected object/result and Related/Actions content, when rendered/focused, then separate title/hint/tab headers and content sections have exactly one gap row, independent focus/scroll and bounded height. F10 focuses Related without destroying Inspect. Docked/Bookmarks and existing actions remain accessible at short heights; loading/empty/error states and final list items are reachable.

### AC: composer-separation

Given transcript content ending in a result card, when rendered, then one empty row and a subtle composer-background upper-half-block cap separate the composer. Cursor, multiline editing, attachment controls and mouse coordinates remain correct after resize; plain fallback stays readable.

### AC: top-navigation

Given project and actual server state, when the topbar renders, then left order is DataTug, Project title [F3], Chat with short separator; right is Server actual-state [F8] and Help [F1]. Session and contextual Expand/actions are absent there. Long titles and compact widths fit without replacing truthful state or navigation reachability.

### AC: chat-session-tabs

Given multiple durable chats, when a tab or session-switch chord selects another or New creates one, then tabs live inside Chat and the exact session's history, result snapshots, draft/caret, chips and viewport are restored. Open tabs keep their positions when activated; new or restored chats append without reordering existing tabs. Active session and New remain reachable with overflow; switch/create honors existing busy fences and late responses cannot populate another session. Rename/delete/restart and slash-picker paths continue to work.

### AC: chat-session-close

Given a current chat tab, when Ctrl+W or its close control is activated, then a chooser offers Archive, Delete and Cancel with clear retained-data versus removed-data wording. Cancel changes nothing. Archive closes the tab while preserving a restorable durable session. Delete removes that session's cached rows, executed queries, HTTP responses and owned retained artifacts, plus its in-memory grids/chips/draft/viewport; independently saved project bookmarks are not session cache and retain their own ownership; it does not delete another session's independent data. Pending operations are canceled/fenced before removal and late completion cannot recreate deleted data. The last open tab closes to a usable fresh chat; restoring an archive does not rerun anything.

### AC: chat-session-direct-close

Given a current chat and no capturing modal, when F12 then A or F12 then D is pressed, then the session is respectively archived or deleted without a confirmation dialog. Prefix hints describe the consequence, Esc cancels a pending prefix, and ordinary text outside it stays literal. F1/Actions expose both shortcuts and destructive/non-destructive intent accurately; legacy terminal decoding distinguishes the commands.

### AC: topbar-edge

Given color/Unicode-capable and fallback terminals, when top chrome renders, then its lower half-block edge or plain rule subtly separates content without a heavy full-height separator, overflow or stolen content rows. Both topbar and composer decoration are part of the same layout/hit-test budget.

### AC: retained-workspace-flows

Given the existing unified workspace journeys, when refinements ship, then provider-unconfigured/empty startup, projects/setup, composer selection/editing, streaming/cancel, query preview/run, saved queries, secure SQL/HTTP, charts, expansion, refresh/version retention, JOIN, Related, local/server sort/group, export, trail, attachments, docks, bookmarks, F5 browser handoff and help still work. Security, scope, provenance, no-rerun and stale-operation ownership remain unchanged except explicit behavior superseded above.

### AC: terminal-journey-evidence

Given production CLI built against released shared-library versions without local replaces, when the complete journey is exercised with a real secure SQLite fixture and a real terminal at 140x42, 100x30 and 80x24, then steps 1–8 run without hidden resets/reruns, final items/controls are reachable and terminal restoration succeeds. Retain screenshots/terminal captures and exact command/version/SHA evidence. Run adversarial Astra review of both usability and aesthetics on these real screens, fix or record every finding, then verify changed screens again.

## Implementation evidence

[CLI PR #372](https://github.com/datatug/datatug-cli/pull/372) landed at [`547cf596c8739ec7ca85525249804aa0a44ddcde`](https://github.com/datatug/datatug-cli/commit/547cf596c8739ec7ca85525249804aa0a44ddcde) after exact-head CI passed (7 successful checks, 9 skipped) and shipped as [CLI v0.74.0](https://github.com/datatug/datatug-cli/releases/tag/v0.74.0). Merged-target CI and release checks passed (15 successful, 3 skipped), including published-artifact verification. Shared dependencies are the released [tuigoff v0.7.0](https://github.com/tuigoff/tuigoff/tree/v0.7.0) and [aichat v0.14.0](https://github.com/strongo/aichat/tree/v0.14.0); no local replacement or dependency workspace is needed. WB completed cleanup of both CLI source worktrees, their integration candidate and the provider worktrees.

Astra independently reviewed clean candidate `af41fe569dd7f360b183a923d89cc81f02cfb897`, released module/build metadata, the final source delta and all thirteen final terminal captures. The final verdict was `blockers=0 majors=0 minors=0 land=yes` for source, usability and aesthetics. This verdict is separate from remote CI and release evidence. Full repository tests, vet, build, SpecScore lint and golangci-lint passed with `GOWORK=off`, no query overlay and no local replaces. An independently rerun focused suite exercised production result-column and full-shell session/metadata routing.

Actual PTY journeys verified mint Explorer selection and immediate metadata, filtered no-match recovery with Esc, exact Query provenance and reverse Tab navigation, one two-field preview/run retaining the original four invoice-count groups (including null and missing customers), independent drafts at stable tab positions, and the close/archive/delete chooser. The release-equivalent `CGO_ENABLED=0` binary additionally executed the real SQLite Invoice read and returned all six expected rows. Layouts were captured at 140×42, 100×30 and 80×24; both owned terminal processes restored and exited successfully.

The public darwin/arm64 v0.74.0 archive passed its published SHA-256 checksum (`9e2688b068e235ea471024ac1a8e5f4f60f116425eee8f4e8759576a3416f32f`). Its executable hash was `24b475996773f668fe28957a602b83facff319045e264a67cad106f456e6a77d`; strict code-signature verification and the CLI assessment `spctl --assess --type install` passed with `source=Notarized Developer ID`. The downloaded executable reported version 0.74.0 and exact merge commit `547cf596c8739ec7ca85525249804aa0a44ddcde`, opened the actual terminal workspace, executed another six-row Invoice read and exited 0. The existing direct-install updater independently resolved v0.74.0 in read-only dry-run mode; the user's installed binary was not replaced by this task.

Evidence limits: images are rendered actual ANSI terminal output, not native OS screenshots. Emulator-only background/partial-frame artifacts are excluded. Terminal QA disabled the external AI provider; deterministic provider tests cover the actual terminal instruction/tool execution path without claiming an external-model run. The earlier stable `unified-tui` delivery record remains unchanged. Selected-result browser addressing, nested Inspect ancestor paths and cross-platform web shortcut parity remain follow-ups in that feature's deferred-web contract; web source was not changed.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
