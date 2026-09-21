# DataTug Chat --- Recordset Charts and Adaptive Views

## Task

Work autonomously on the existing **DataTug `datatug chat` TUI** to
specify, plan, implement, test, review, and land recordset charts plus
adaptive recordset views.

First inspect the repository, specs, current Bubble Tea models,
recordset/grid implementation, DTQL integration, keybindings, and
conventions. Reuse existing abstractions. Do not stop after planning:
continue through implementation, tests, review, CI, and merge unless
genuinely blocked.

## UX goal

A recordset has three conceptual views:

``` text
[ <Recordset title>: <N> rows | Charts | Current row ]
```

Example:

``` text
[ Customers: 59 rows | Charts | Current row ]
```

The table is the default view. `Charts` and `Current row` are secondary
views.

When enough horizontal space exists, the selected secondary view appears
**to the right of the table**. When there is insufficient space, it
occupies the main content area. Resize must switch layouts correctly.

Use Bubble Tea + Lip Gloss + the existing table implementation +
**NTCharts**. Do not introduce another TUI framework.

## Recordset statistics

Collect statistics incrementally **while rows load**, not via a second
full scan just for charts.

At minimum:

-   row count
-   null/non-null count
-   distinct count/cardinality
-   value frequencies

Cardinality = number of distinct values.

Exact counting is fine for MVP. Avoid over-engineering approximate
algorithms, but avoid obviously pathological memory use.

Statistics belong to the recordset/data-analysis layer, not chart
rendering or layout.

## Renderer-independent chart model

Keep this separation:

``` text
recordset
  ↓
column statistics
  ↓
chart candidate inference/ranking
  ↓
renderer-independent ChartSpec
  ↓
NTCharts adapter
  ↓
Bubble Tea presentation
```

ChartSpec/equivalent should represent chart type, title, dimensions/X,
measures/Y, aggregation, ordering, limit, source columns, labels, and
grouping/bucketing.

Design it to represent future chart types such as `pie` and `donut` even
if NTCharts cannot natively render them now.

## Deterministic chart inference

Chart selection must be deterministic, explainable, stable, and
testable. **Do not use an LLM to choose charts.**

Use signals such as:

-   physical/logical/semantic type
-   cardinality and cardinality ratio
-   null ratio
-   categorical/numeric/date-time nature
-   natural ordering/continuity
-   dimensions/measures
-   recordset size
-   ModelSpec/entity metadata when already available

Generate multiple candidates and rank them deterministically. Prefer a
scoring abstraction over a single monolithic conditional.

The highest-ranked candidate is initially selected.

## Categorical charts

Categorical/groupable fields such as Country, Status, Type, City should
normally produce a frequency **bar chart**:

``` text
GROUP BY <column>
COUNT(*)
ORDER BY count DESC
TOP 10
```

Use already-collected value frequencies when possible instead of
executing another query.

Default to **Top 10**. A column may have much higher cardinality and
still be useful. Do not reject Country merely because it has 186
distinct values.

Use deterministic tie ordering. `Other` is not required for MVP.

Near-unique IDs/keys should receive a strong ranking penalty.

## Line charts

Prefer line charts when X has meaningful natural continuity/order,
especially date/time:

``` text
InvoiceDate + COUNT(*)    → line
InvoiceDate + SUM(Total)  → line
Timestamp + numeric value → line
```

Do not choose line merely because X is numeric. If date/time bucketing
is required, make it deterministic and sensible for the
range/cardinality.

## Other chart types

Where straightforward, infer additional NTCharts-native candidates:

``` text
numeric X + numeric Y                    → scatter
two categorical dimensions + count/value → heatmap
OHLC-shaped data                          → candlestick
```

Do not force every NTCharts feature into MVP.

Core MVP: bar + line + deterministic ranking. Scatter is desirable if
straightforward.

Investigate current NTCharts capabilities. Do not make Kitty/image
rendering a dependency. Pie/donut may exist in ChartSpec for future
renderers, while terminal categorical composition defaults to bar.

## Charts navigation

Do **not** make every chart a top-level tab:

``` text
Recordset
├── Table
├── Charts
│   ├── Country — bar
│   ├── City — bar
│   └── InvoiceDate — line
└── Current row
```

Charts must provide compact navigation among candidates and remember the
selected chart.

## Adaptive split layout

Use approximately this rule:

> If at least **40 terminal character cells** can usefully remain to the
> right of the table, use a split layout.

This means terminal cells, not database columns.

Do not hard-code a total terminal width. Calculate from terminal width,
useful/preferred table width, borders/padding/gap, and secondary-pane
minimum width.

Conceptually:

``` text
if remainingWidthAfterUsefulTable >= 40:
    table | selected secondary pane
else:
    selected view uses main content area
```

### Table + chart

When Charts is selected and there is room:

``` text
[ Customers: 59 rows | Charts | Current row ]

┌──────────── Customers ────────────┬────── By country ──────┐
│ CustomerId  Name      Country     │ USA      ███████  13   │
│ 1           Luís      Brazil      │ Canada   █████     9   │
│ 2           Leonie    Germany     │ France   ████      7   │
│ ...                               │ ...                  │
└───────────────────────────────────┴────────────────────────┘
```

If narrow, Charts uses the main content area.

There must be one selected-chart state shared by split and full-width
presentations.

## Current row inspector

This is a key requirement.

When `Current row` is selected **and enough width exists**, keep the
table on the left and show a **scrollable vertical field/value
card/form** on the right:

``` text
[ Customers: 59 rows | Charts | Current row ]

┌──────────── Customers ────────────┬────── Current row ──────┐
│ CustomerId  Name      Country     │ CustomerID              │
│ 40          Alice     USA         │ 42                      │
│ 41          Maria     Spain       │                         │
│ >42         Robert    Canada      │ FirstName               │
│ 43          Anna      Germany     │ Robert                  │
│ ...                               │                         │
│                                   │ LastName                │
│                                   │ Brown                   │
│                                   │                         │
│                                   │ Country                 │
│                                   │ Canada                  │
│                                   │                         │
│                                   │ Email                   │
│                                   │ robert@example.com      │
│                                   │              ↓ more     │
└───────────────────────────────────┴─────────────────────────┘
```

Show fields vertically as **field name + value**. Optimise for
readability, not a horizontal table.

The pane must:

-   be vertically scrollable
-   handle many fields
-   handle/wrap long values sensibly
-   render nulls clearly
-   resize correctly
-   always reflect the table's selected row
-   preserve scroll state where sensible
-   reset/reconcile scroll sensibly when row changes

Use Bubble Tea viewport or an existing abstraction where appropriate. Do
not implement a static non-scrollable string.

When narrow, selecting Current row shows the same scrollable card
full-width. Returning to the table must preserve row selection.

There must be one coherent current-row state: the row selected in the
table is the row shown in the inspector.

## Focus and navigation

Inspect existing DataTug keybindings before choosing controls.

Make switching among Recordset, Charts, and Current row convenient.
`Tab`/`Shift+Tab` or arrows may be suitable only if they do not
conflict.

Explicitly design focus in split mode:

-   table row navigation remains possible
-   Current row can scroll
-   chart candidates can be navigated
-   the same key must not ambiguously move a row and scroll another pane
-   controls must be discoverable using existing help conventions

## Streaming behaviour

Rows may arrive progressively. Statistics must update incrementally.

Avoid disruptive chart candidate reordering on every incoming row.
Choose stable behaviour; for example, accumulate stats continuously and
stabilise/finalise ranking on recordset completion if there is a natural
completion event.

## Edge cases

Handle explicitly:

-   empty recordset
-   one row
-   cardinality 0/1
-   mostly-null fields
-   high-cardinality fields
-   near-unique IDs
-   booleans
-   long/wide records
-   no current row
-   resize across split/narrow threshold

Boolean cardinality=2 should not automatically outrank a semantically
richer dimension merely because it has lower cardinality.

## Architecture boundaries

Maintain these boundaries:

-   NTCharts does not decide the best chart
-   layout does not calculate cardinality
-   statistics do not know terminal dimensions
-   chart inference contains no terminal rendering logic
-   adaptive layout does not alter chart semantics
-   Current row rendering does not own table selection

## Tests

Add unit tests for:

### Statistics

-   row/null/non-null counts
-   distinct counts
-   value frequencies
-   incremental updates

### Chart inference/ranking

-   cardinality 0/1/2+
-   high-cardinality categories
-   IDs
-   booleans
-   null-heavy fields
-   dates
-   numerics
-   deterministic ranking

Verify at least:

``` text
Country + count       → bar
Date + measure        → line
Date + count          → line
numeric X + numeric Y → scatter candidate where supported
```

### Top 10

-   descending frequency
-   deterministic ties
-   \<10, =10, \>10 groups
-   null handling

### View state

-   Table → Charts → Table
-   Table → Current row → Table
-   selected row retained
-   selected chart retained
-   Current row follows table selection
-   empty recordset
-   resize

### Adaptive layout

Test boundaries around the \~40-cell secondary-pane rule for both
table+chart and table+Current-row.

### Current-row viewport

Test many fields, long values, scroll boundaries, row change, resize,
and empty/no-current-row.

Avoid fragile screenshot/golden tests unless already conventional.

## Specifications and docs

Before implementation, update/create appropriate DataTug specs covering:

-   recordset statistics/cardinality
-   chart candidates and deterministic ranking
-   Top-10 behaviour
-   renderer-independent ChartSpec
-   NTCharts capability boundary
-   top-level views
-   adaptive split behaviour
-   Current row inspector
-   focus/navigation
-   streaming behaviour

Follow existing SpecScore conventions if applicable.

## Autonomous implementation process

1.  Inspect repository/specifications.
2.  Understand `datatug chat` recordset/grid architecture.
3.  Inspect Bubble Tea models, layout, focus, and keybindings.
4.  Inspect current NTCharts APIs.
5.  Write/update specs.
6.  Critically review architecture.
7.  Produce incremental implementation plan.
8.  Use suitable adversarial/reviewer agents where supported.
9.  Resolve important findings.
10. Implement progressively.
11. Add tests alongside implementation.
12. Run formatting, linting, tests, and relevant checks.
13. Manually exercise `datatug chat` where practical.
14. Review UX/architecture and fix issues.
15. Update help/docs.
16. Commit using repository conventions.
17. Create/update PR if normal workflow.
18. Address CI/review failures.
19. Merge/land when requirements are satisfied.

Use faster/cheaper suitable models for mechanical work where supported;
reserve stronger reasoning for architecture/difficult issues.

## Suggested milestones

1.  **Recordset statistics** --- incremental stats available and tested.
2.  **Chart inference** --- renderer-independent candidates/ranking +
    Top 10.
3.  **NTCharts rendering** --- real bar/line charts render.
4.  **Recordset views** --- `[ title: N rows | Charts | Current row ]`.
5.  **Current row inspector** --- scrollable vertical field/value card.
6.  **Adaptive split** --- Charts or Current row appears right of table
    when \>= \~40 useful cells remain.
7.  **Polish and land** --- help, edge cases, docs, review, CI, merge.

## Definition of done

Complete when:

-   statistics are gathered incrementally
-   cardinality/value frequencies are available
-   chart candidates are deterministic and renderer-independent
-   Top-10 categorical bars work
-   ordered/time lines work
-   NTCharts renders supported specs
-   UI exposes Recordset / Charts / Current row
-   Charts can appear right of table when space permits
-   Current row can appear right as a scrollable field/value form under
    the same rule
-   narrow terminals show secondary views full-width
-   selected row/chart state persists
-   focus/navigation/resize behaviour is coherent
-   tests/docs/specs are updated
-   normal CI/review passes
-   work is landed
