---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Chat RecordSet charts and adaptive views

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/chat-recordset-adaptive-charts?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/chat-recordset-adaptive-charts?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/chat-recordset-adaptive-charts?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/chat-recordset-adaptive-charts?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

An immutable Chat RecordSet exposes its table, deterministic chart candidates, and a scrollable current-row inspector. The latter two views share space with the table when the terminal is wide enough and take the main pane when narrow.

## Problem

The terminal grid shows individual rows but not compact distributions or a readable vertical account of one wide row. The user has to infer patterns and inspect clipped values manually. Chart inference must not ask an AI model or issue an extra database query merely to choose a visualization.

## Behavior

### REQ: incremental-statistics

As policy-admitted rows are materialized, DataTug MUST update row count and per-column null, non-null, distinct and value-frequency statistics. A missing field counts as null. Type observations and the bounded date/numeric aggregates needed by line charts are accumulated in the same pass. Limits on retained distinct values and pair aggregates MUST be explicit; an exceeded limit marks the affected statistic incomplete rather than reporting a false exact cardinality. The renderer and layout MUST NOT calculate statistics. New snapshots retain statistics; legacy snapshots derive them while decoding the already-required row pass, without rerunning the query. A result with zero rows has zero counts and no invented values.

### REQ: deterministic-chart-model

DataTug MUST infer and rank chart candidates from finalized RecordSet statistics and available column semantics using stable, explainable scoring. The model MUST NOT choose a chart. A renderer-independent ChartSpec MUST express type, title, X/dimension, Y/measure, aggregation, order, limit, source columns, labels and bucket/group definition. It MAY represent future pie/donut kinds without rendering them in this phase. NTCharts receives supported ChartSpecs only; unsupported kinds produce a clear empty state.

### REQ: categorical-top-ten

Groupable dimensions SHOULD yield a count-frequency bar candidate using stored frequencies. Bar data MUST be ordered by count descending and then label ascending, limited to ten groups, with nulls labeled consistently and included only when present. High cardinality alone MUST NOT discard a useful dimension. Near-unique identifiers, booleans, constant and mostly-null columns MUST receive deterministic ranking penalties. Incomplete frequencies MUST NOT be presented as exact Top 10.

### REQ: ordered-lines

Date/time dimensions SHOULD yield chronological count and, when a numeric measure exists, sum line candidates from aggregates collected during row loading. Bucketing MUST be deterministic from observed precision/range, and labels MUST be ordered by bucket. A numeric X alone MUST NOT imply temporal continuity. When straightforward, two numeric columns MAY yield a scatter candidate; absent that capability, bar and line remain the required MVP.

### REQ: recordset-views

Each inline RecordSet MUST expose Table (default), Charts and Current row in one compact header. Charts is one top-level view with a compact candidate selector beneath it, not one tab per candidate. Returning to a view retains the selected chart and table row while the RecordSet remains on screen. Empty results and no-chart cases show informative messages, not blank or fabricated visuals. Views MUST use the existing Bubble Tea, Lip Gloss and bubble-table components; NTCharts renders supported charts without image/Kitty requirements.

### REQ: adaptive-presentation

When the current chat pane can leave at least approximately 40 useful terminal cells to the right of a useful table width after borders and gap, a selected secondary view MUST appear alongside the table. Otherwise it MUST occupy the RecordSet content area. Width is recalculated on every resize or workspace split change. Changing presentation MUST NOT change selected row, selected chart, ChartSpec or inspector scroll semantics.

### REQ: current-row-inspector

Current row MUST show the table-selected row as a vertical field-name/value card in column order. It MUST use a scrollable viewport, wrap long sanitized values, show SQL NULL distinctly, handle many fields and no row, and follow table selection. Scrolling is clamped after resize; changing row resets the inspector to its top. Returning to Table keeps row selection, including after sort and horizontal scroll. The inspector MUST NOT maintain a competing row selection.

### REQ: focus-and-streaming

Keyboard control MUST distinguish table navigation, chart-candidate navigation, inspector scrolling, switching views and returning to the composer. Focus and active styling MUST show which pane receives arrow keys. Existing Chat, JOIN, workspace and mouse-selection controls MUST remain available. Help/status copy MUST list the new controls. If rows arrive progressively, statistics MAY update per row, but chart candidates MUST stabilize at the query completion event rather than reorder under the user.

## Acceptance Criteria

### AC: statistics-and-snapshots

Given mixed typed, sparse, null-heavy and high-cardinality rows
When the secure-read collector materializes them and the result is saved/reopened
Then row/null/non-null counts, exact-or-explicitly-incomplete cardinality, frequencies and date/numeric aggregates are correct, with no second chart-only scan or historical query rerun.

### AC: candidate-ranking

Given synthetic Country, City, boolean, ID, date and numeric columns
When candidates are inferred twice from the same stats
Then ordering and scores are identical; Country+count yields a Top-10 bar even with many groups, date+count and date+sum yield lines, ID/constant/null-heavy fields rank lower, and ties/chronology are deterministic.

### AC: recordset-view-journey

Given a real Chinook RecordSet with a selected table row and more than one chart candidate
When the user opens Charts, changes candidate, returns to Table, opens Current row, scrolls, selects another row and returns
Then the same row and selected chart are retained as appropriate, the inspector follows the row, and normal chat/JOIN/workspace navigation still works.

### AC: adaptive-resize

Given a RecordSet in Charts or Current row
When the chat pane crosses the approximately 40-cell usable-secondary boundary in either direction
Then wide layout shows table plus secondary pane and narrow layout shows the secondary full-width, without selection loss, rendering overflow or a stale inspector viewport.

### AC: real-terminal

Given the configured inexpensive model and real Chinook database
When the user requests customers and invoices in `datatug chat`
Then true query rows appear in interactive grids, bar and chronological line views render through NTCharts where eligible, Current row exposes the selected row, and no model-generated table/chart data is used.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
