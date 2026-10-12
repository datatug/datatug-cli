# DataTug Chat Phase 3 — Workspace, Context, Views, Selections and Docking

The following are already implemented and working:

- `datatug chat`;
- inexpensive AI model → DTQL;
- DTQL validation/execution through DataTug;
- real database results;
- interactive inline grids;
- multiple durable chat sessions;
- persistent chat history;
- immutable persisted RecordSet snapshots;
- RecordSet provenance/basic lineage;
- restart/recovery without rerunning previous queries.

Now implement **Phase 3: the interactive DataTug workspace**.

The objective is to make RecordSets and DataTug project objects first-class interactive context for conversations.

This phase introduces:

- the final basic split-screen application shell;
- project/database navigation;
- explicit chat context attachments;
- RecordSet views and selections;
- row/cell/range selection;
- agent-driven and UI-driven selection operations;
- selected-item details;
- session-scoped docking/tabs;
- follow-up queries using structured context.

Do **not** implement global bookmarks/tags yet.

---

# 1. Core scenario

The primary acceptance scenario is:

```text
> Show me 20 largest customers with number and total amount of orders.

[interactive inline grid backed by persisted RecordSet]

> Select top 5 from Prague.

[selection/view created over that RecordSet]

> Dock them.

[docked as a session-scoped tab, automatically named]

> Show their largest orders.

[new DTQL generated using the selected/docked customers as structured context]

[new RecordSet/grid]
```

The same selection and docking operations must also be possible directly through the UI.

The important architectural property is:

```text
natural-language action ──┐
                          ├──> DataTug application operation
UI action ────────────────┘
```

Do not implement two independent behaviours.

---

# 2. Inspect current implementation first

Before changing architecture, inspect the actual merged Phase 1/2 implementation.

Understand:

- current chat/TUI structure;
- session persistence;
- RecordSet model/storage;
- DTQL execution;
- ADK/tool architecture;
- artifact/grid rendering;
- model context reconstruction;
- provenance/lineage;
- DataTug project representation;
- database/schema discovery;
- existing query/view concepts;
- keyboard/mouse handling;
- current TUI libraries.

Write a concise implementation plan based on the actual repository.

Preserve working behaviour and existing persistence guarantees.

---

# 3. Application shell

Move DataTug Chat to the basic intended layout:

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ DataTug │ Project: Chinook ▾ │ Session: Customer analysis ▾    View │ Help │
├──────────────────────────────────┬───────────────────────────────────────────┤
│ CHAT                             │ WORKSPACE                                 │
│                                  │                                           │
│ scrollable history               │ [Project] [Selected] [Docked]             │
│                                  │                                           │
│ assistant text                   │                                           │
│                                  │                                           │
│ [inline grids]                   │                                           │
│                                  │                                           │
│                                  │                                           │
├──────────────────────────────────┤                                           │
│ [attached context ×]             │                                           │
│ > Ask about your data...         │                                           │
├──────────────────────────────────┴───────────────────────────────────────────┤
│ project │ session │ recordsets │ context │ selection │ status               │
└──────────────────────────────────────────────────────────────────────────────┘
```

The exact visual styling should follow the existing TUI conventions.

Important layout requirements:

- chat on the left;
- workspace on the right;
- resizable divider where reasonably practical;
- input at bottom of chat;
- scrollable chat history above input;
- top application/navigation bar;
- bottom status bar;
- sensible behaviour on smaller terminals.

Do not over-polish visual styling before interaction works correctly.

---

# 4. Project selector

The top-level selector represents the **DataTug project**, not merely a database.

Conceptually:

```text
Project: Chinook ▾
```

A DataTug project may contain multiple data sources/databases.

Use existing DataTug project abstractions.

Do not introduce a Chat-specific duplicate project model.

---

# 5. Session selector

Expose the Phase 2 sessions in the top bar:

```text
Session: Customer analysis ▾
```

Users should be able to:

- switch sessions;
- create a session;
- rename;
- clear;
- delete.

Switching sessions must restore that session's:

- chat;
- RecordSets;
- current context;
- views/selections;
- docked workspace state.

Session switching must not leak state between sessions.

---

# 6. Workspace tabs

For Phase 3 the right workspace should contain at least:

```text
[Project] [Selected] [Docked]
```

`Bookmarks` will be added in Phase 4.

Treat the right side as a general **workspace**, not as a collection of unrelated panels.

Future artifact types should be able to live there without redesigning the application shell.

---

# 7. Project/database explorer

The `Project` workspace tab should provide a navigable tree over the current DataTug project.

Conceptually:

```text
Project
│
├── Database A
│   ├── Tables
│   │   ├── Customer
│   │   ├── Invoice
│   │   └── InvoiceLine
│   ├── Views
│   └── Queries
│
└── Database B
    └── ...
```

Adapt this to actual DataTug project/data-source concepts rather than assuming SQL everywhere.

The important supported context objects for this phase are:

- database/data source;
- table;
- view;
- query.

If existing DataTug concepts distinguish these differently, use the existing model.

---

# 8. Navigation vs attachment

Do not make ordinary browsing silently mutate AI context.

Keep **focus/navigation** separate from **attachment**.

Suggested keyboard semantics:

```text
↑ / ↓     navigate
Enter     open/focus/details
Space     attach/detach from chat context
```

Adapt shortcuts if they conflict with existing TUI conventions.

Mouse actions may expose an attach affordance where supported.

Clicking/focusing an object should not automatically mean that every subsequent AI request refers to it.

---

# 9. Structured chat context attachments

The input area should visibly show attached context.

For example:

```text
┌───────────────────────────────────────────────┐
│ [Customer ×] [Invoice ×] [Prague · 5 ×]      │
│                                               │
│ > Show their largest orders                  │
└───────────────────────────────────────────────┘
```

Attachments must be structured references, not pasted text.

Introduce/evolve a general context-reference abstraction capable of representing at least:

```text
Project
DataSource/Database
Table
View
Query
RecordSet
RecordSetView
Selection
```

Use stable IDs/references from the existing project/session model.

Conceptually:

```text
ChatContextReference
├── kind
├── project
├── source
└── object identity
```

Do not force this exact type shape if the repository suggests a better one.

The important invariant:

> Anything addressable in DataTug should eventually be attachable to a conversation through a structured reference.

For this phase, implement only the object types actually required above.

---

# 10. Agent context must remain economical

Attaching a table or RecordSet must not mean dumping all of its contents into the LLM prompt.

The agent should receive identity + useful metadata and use DataTug tools to inspect what it needs.

Prefer operations conceptually similar to:

```text
describe_table(...)
get_recordset_schema(...)
get_recordset_rows(...)
get_selection(...)
```

Reuse existing DataTug/ADK tool patterns.

Do not create unnecessary model calls for deterministic local operations.

---

# 11. RecordSet Views

Introduce **View** as a first-class concept over an immutable RecordSet.

The RecordSet remains the immutable snapshot from Phase 2.

A View describes how some part of that RecordSet is being presented/worked with.

Conceptually:

```text
RecordSet
├── View: All
├── View: Prague
└── View: Top Prague customers
```

A view may eventually contain:

- filtering;
- ordering;
- projected/visible columns;
- selections.

For Phase 3, implement only what the actual scenarios need, but choose a representation that does not conflate the immutable RecordSet with UI state.

A View should reference the RecordSet rather than copying its rows unnecessarily.

---

# 12. Selection is not the RecordSet

Selections should be first-class structured objects over a RecordSet/View.

Support at least:

- one row;
- contiguous range of rows;
- multiple rows where practical;
- one cell;
- rectangular range of cells;
- selected columns where naturally supported by the grid.

Conceptually:

```text
RecordSet
└── View
    ├── current presentation/filter/order
    └── Selection
        ├── rows
        ├── columns
        └── cell ranges
```

Avoid encoding selection only as transient grid cursor state.

If the user can subsequently ask the agent about something, it needs a structured identity/representation.

---

# 13. Multiple selections/views

Design the model so a RecordSet can have multiple meaningful selections/views.

For example, future workflows may contain:

```text
RecordSet
├── good
├── bad
├── unknown
└── investigate
```

Do not build a complex classification UI now.

But avoid a design where a RecordSet has exactly one global mutable `selection`.

At minimum, separate:

- current UI selection;
- durable session-level selection/view objects;
- attached context.

These are different concepts.

---

# 14. Selected workspace tab

The `Selected` tab should show useful details about the currently focused selection.

For a single row, display fields vertically, e.g.:

```text
Customer

CustomerId   42
Name         Example Customer
City         Prague
Country      Czech Republic
...
```

For a cell, show:

- column;
- value;
- row identity/context.

For multiple rows/cells, show an appropriate summary and, where useful, a compact grid/details view.

Do not attempt sophisticated statistics in this phase.

The purpose is to inspect precisely what is selected.

---

# 15. Agent-driven deterministic operations

The agent should be able to interpret requests such as:

```text
Select top 5 from Prague.
```

But the model should **not manually enumerate/manipulate rows**.

The agent should translate the intent into a structured DataTug operation.

For example, conceptually:

```text
select(
    recordset = rs-123,
    filter = City == "Prague",
    order = inherited,
    limit = 5
)
```

DataTug executes that operation deterministically over the persisted RecordSet/View.

Similarly:

```text
Select the first 3.
Select rows from Brazil.
Select these columns.
Clear the selection.
```

Where an operation can be performed locally over an existing RecordSet, do not rerun the original database query unnecessarily.

---

# 16. UI and agent commands share application actions

This is an important architectural invariant.

For example:

```text
Agent:
"Dock them"
      │
      ▼
DockView(selectionID)
```

and:

```text
User presses Dock
      │
      ▼
DockView(selectionID)
```

must invoke the same application/domain operation.

Apply this principle to:

- attach/detach;
- selection operations;
- docking;
- undocking;
- relevant view operations.

Avoid embedding business logic directly in Bubble Tea event handlers.

---

# 17. Docking

A RecordSet, View, or Selection should be dockable into the workspace.

Docking is **session-scoped presentation/work state**.

Conceptually:

```text
RecordSet/View/Selection
          │
          └── Dock
                 ↓
       session workspace tab
```

Example:

```text
[Project] [Selected] [Docked]

Docked:
  [Prague customers · 5]
  [Recent invoices]
```

A docked item should have an automatically generated useful title.

Allow rename if inexpensive, but do not make naming a major subsystem.

---

# 18. Docked lifetime

Docked state belongs to the session.

It must:

- survive process restart;
- restore when the session is reopened;
- disappear when the owning session is cleared/deleted.

It must **not** become global/workspace-persistent merely because it is docked.

Phase 4 bookmarks will provide that longer lifetime.

Important distinction:

```text
Dock
→ session-scoped

Bookmark
→ future project/workspace-scoped durable object
```

Do not implement bookmarks in this phase.

---

# 19. Inline and docked views refer to the same logical data

Docking must not create another database query or unnecessarily duplicate a RecordSet.

Conceptually:

```text
RecordSet rs-123
       │
       ├── InlineGridView
       │
       └── DockedGridView
```

Both are presentations of the same underlying RecordSet/View/Selection.

Selections/filtering/order should behave consistently across presentations according to the chosen View model.

---

# 20. Auto naming

Generate useful names for selections/views/docks without requiring the user to name everything.

Examples:

```text
Prague customers · 5
Recent invoices
Top 5 Prague customers
Customers from Brazil
```

Prefer deterministic naming from operation/context metadata where possible.

Do not call an expensive LLM merely to generate these names.

Names can be improved later.

---

# 21. Follow-up questions over selected context

This is the most important end-to-end capability in Phase 3.

Example:

```text
> Show me 20 largest customers with number and total amount of orders.

[rs-001]

> Select top 5 from Prague.

[selection/view v-002]

> Dock them.

[Prague customers · 5]

> Show their largest orders.
```

The agent must understand that `their` refers to the relevant structured context.

The next query should be based on stable IDs/data from that selection/view, not on the model remembering row text from earlier output.

Conceptually:

```text
attached/active context
        ↓
selection/view
        ↓
customer IDs
        ↓
agent produces DTQL
        ↓
DataTug executes
        ↓
new RecordSet
```

Use the existing Phase 2 lineage/provenance mechanism to connect derived results where appropriate.

---

# 22. Context semantics

Keep these concepts distinct:

### Focused

The object currently receiving keyboard/UI interaction.

### Selected

Rows/cells selected inside a RecordSet/View.

### Attached

Structured context explicitly supplied to the next agent turn.

### Docked

Session workspace presentation.

They may interact, but do not collapse them into one boolean/state variable.

For MVP convenience, an operation may attach a newly created selection automatically where that makes the conversational behaviour obvious, but attached context must remain visible and removable.

---

# 23. Persistence

All meaningful Phase 3 session state must survive restart.

Persist:

- RecordSet Views that are meaningful session objects;
- selections required for subsequent context;
- chat attachments;
- docked items;
- dock titles;
- enough workspace state to reopen the session coherently;
- relevant lineage/provenance relationships.

Do not persist transient cursor/hover state unless it is useful and trivial.

After restart:

```text
session
→ chat restored
→ RecordSets restored
→ selections/views restored
→ attached context restored
→ docks restored
```

No previous database queries should need to be rerun merely to restore the workspace.

---

# 24. Project explorer context scenario

Test explicit attachment from the project navigator.

For example:

```text
Project
└── Chinook
    └── Tables
        ├── Customer   [attach]
        └── Invoice
```

Attach `Customer`.

Input shows:

```text
[Customer ×]

> Show 50 from Prague.
```

The agent should receive an unambiguous structured reference to that table.

Likewise test attaching:

- database;
- table;
- view where available;
- saved/project query where available.

Do not implement artificial demo-only objects if the DataTug project does not currently have some of these.

---

# 25. Status bar

Add a useful but restrained bottom status bar.

Potential information:

```text
Chinook │ Customer analysis │ rs:3 │ context:2 │ selected:5 │ ready
```

Also use it for transient operational states such as:

```text
generating DTQL…
executing…
loading…
```

Do not turn the status bar into a debugging console.

---

# 26. Keyboard-first usability

DataTug CLI must remain efficient without a mouse.

Define/document shortcuts for at least:

- moving focus between chat/workspace;
- switching workspace tabs;
- navigating grids;
- selecting rows/cells;
- attaching/detaching context;
- docking;
- returning focus to chat input;
- switching sessions.

Avoid surprising global shortcuts that conflict with text input.

Mouse support is welcome where already practical, but keyboard usability is required.

---

# 27. Error handling

Structured operations should fail clearly.

Examples:

```text
Cannot select "City = Prague":
this RecordSet has no City column.
```

```text
The selected RecordSet is no longer available.
```

```text
Cannot attach this object because its data source is unavailable.
```

Do not silently fall back to asking the model to guess.

---

# 28. Testing

Add deterministic tests for the new domain/application operations.

At minimum:

## Views/selections

```text
RecordSet
→ create view
→ select rows
→ persist
→ reload
→ verify identical selection
```

## Multiple selections

Ensure creating a second selection does not silently destroy the first meaningful selection/view.

## Context attachments

```text
attach
→ persist
→ reload
→ detach
```

## Project object attachment

Verify table/database references remain structured and unambiguous.

## Docking

```text
dock
→ restart
→ restored

undock
→ underlying RecordSet still exists
```

## Session isolation

Docks/selections/attachments from Session A must not appear in Session B.

## Session deletion

Delete Session A.

Verify its docks/selections/session-scoped state disappear.

## Agent vs UI operation

Where practical, test that an agent-issued application action and equivalent UI action reach the same domain operation/result.

Do not make deterministic tests depend on live model calls.

---

# 29. Manual end-to-end acceptance

Use real Chinook data and the normal inexpensive

---

<!-- Continuation supplied in a separate user message. Its section numbering
is retained as supplied; the first pasted file ended mid-sentence above. -->

## 23. Manual end-to-end acceptance

Use real Chinook data and the normal inexpensive runtime model.

```text
$ datatug chat

> Show me 20 largest customers with number and total amount of orders.

[real inline RecordSet/grid]

> Select top 5 from Prague.

[Selection/View]

> Dock them.

[docked: Top 5 Prague customers]

> Show their largest orders.

[new real DTQL]
[new RecordSet/grid]
```

Then test the equivalent UI path:

1. manually select rows/cells;
2. inspect them in `Selected`;
3. attach the Selection;
4. dock it;
5. ask a follow-up;
6. verify the agent uses exactly that structured context.

Also:

1. open Project explorer;
2. attach `Customer`;
3. ask `Show 50 from Prague`;
4. verify the attached table is used.

Terminate DataTug completely, restart, and verify chat, RecordSets, Views/Selections, attachments and docks restore without rerunning historical queries. Continue with a follow-up question successfully.

## 24. Explicitly out of scope

Do not implement:

- global/project bookmarks;
- bookmark tags;
- collections;
- cross-session bookmarked RecordSets;
- charts;
- arbitrary new artifact types;
- incident-specific workflows;
- cloud sync/collaboration;
- sophisticated lineage UI;
- speculative general frameworks not required by this phase.

Design cheaply for obvious future compatibility, but do not implement Phase 4 early.

## 25. Implementation approach

Work autonomously and incrementally:

1. inspect merged Phase 2;
2. document current state/domain boundaries;
3. propose the smallest View/Selection/context/docking design;
4. critically review it for persistence and future bookmark compatibility;
5. implement the application shell;
6. implement Project explorer and structured attachments;
7. implement Views/Selections;
8. implement shared deterministic application actions;
9. implement Selected workspace;
10. implement docking;
11. wire agent tools to the same application actions;
12. persist all meaningful Phase 3 state;
13. add deterministic tests;
14. exercise real Chinook scenarios;
15. repeatedly test restart/session switching/deletion;
16. review UX in the real terminal;
17. remove unnecessary abstractions;
18. run broader relevant tests;
19. merge to main only when acceptance criteria pass.

## 26. Definition of success

Phase 3 is complete when:

- the split Chat/Workspace shell works;
- Project and Session selectors work;
- Project explorer exposes real project/database objects;
- DB/table/view/query objects can be attached as structured chat context where available;
- attachments are visible and removable;
- immutable RecordSets have separate View/Selection state;
- row/cell/range selection works sufficiently for the core scenarios;
- multiple meaningful Views/Selections are supported by the model;
- `Selected` shows useful details;
- the agent can perform deterministic selection operations over existing RecordSets;
- UI and agent operations share domain/application actions;
- RecordSets/Views/Selections can be docked;
- docks are session-scoped and restore after restart;
- inline and docked presentations share the same logical data;
- follow-up questions use structured selected/docked context;
- session switching isolates and restores workspace state;
- all meaningful state survives restart without rerunning historical queries;
- deterministic tests cover the new state and lifecycle;
- the real Chinook scenario works end-to-end.

## Most important invariant

**The model interprets intent; DataTug owns data, context, selection, persistence and deterministic operations.**

A user must be able to move naturally between chat and direct grid interaction without creating two different systems. A Selection made by the user and a Selection requested through chat must become the same kind of DataTug object and be equally usable as structured context.

Once this phase is complete, merge it to main and proceed separately to Phase 4 for project-level bookmarks and tags.
