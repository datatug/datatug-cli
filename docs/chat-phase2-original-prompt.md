# DataTug Chat Phase 2 — Durable Sessions and RecordSets

The `datatug chat` PoC is implemented and working: natural-language requests are translated by an inexpensive AI model into DTQL, DataTug executes the query against real data, and structured results are rendered as inline interactive grids.

Now implement **Phase 2: durable chat sessions and RecordSets**.

The objective is to establish the persistent state model correctly before we build selections, docking, bookmarks, project navigation, and the larger workspace UI.

This is foundational. Do not treat persistence as something that can be retrofitted later.

---

## 1. Goal

A DataTug Chat session must survive process termination and restart without losing:

- conversation history;
- generated/executed DTQL;
- query results;
- inline grids;
- enough structured context to continue working with previous results.

Users must be able to maintain multiple independent sessions and switch between them.

Conceptually:

```text
DataTug Project
│
├── Session A
│   ├── messages
│   ├── queries
│   ├── RecordSets
│   └── session state
│
├── Session B
│   ├── messages
│   ├── queries
│   ├── RecordSets
│   └── session state
│
└── Session C
```

Session state is durable across DataTug restarts but is still **session-scoped**.

Clearing/deleting a session removes its session-scoped cached data.

---

## 2. Start by inspecting the implemented PoC

Do not design from this prompt alone.

First inspect the current repository and understand exactly what the PoC implemented:

- `datatug chat` command structure;
- ADK/agent integration;
- model/provider abstraction;
- DTQL generation;
- DTQL execution;
- current RecordSet/result representation;
- inline grid implementation;
- chat message representation;
- any pi-go-derived code;
- existing DataTug project/storage abstractions;
- existing persistence mechanisms that can be reused.

Preserve working PoC behaviour.

Avoid unnecessary rewrites.

Before implementation, write a concise design and implementation plan based on the actual code.

---

# 3. Sessions are first-class objects

Introduce an explicit durable Chat Session concept.

A session should have at minimum:

```text
Session
├── stable ID
├── title/name
├── created timestamp
├── updated timestamp
├── messages
├── executed queries
├── RecordSets
└── relevant chat state
```

Exact Go types/storage layout should follow existing DataTug conventions.

Do not use the LLM/ADK conversation context itself as the authoritative session store.

**DataTug owns session state.**

ADK/model context should be reconstructed from DataTug state when necessary.

---

# 4. Multiple sessions

Users must be able to have multiple chat sessions within a DataTug project.

Implement the minimal UX necessary to:

- create a session;
- list/switch sessions;
- rename a session;
- reopen an existing session;
- clear a session;
- delete a session.

Do not implement the final full-screen session selector/workspace UI yet.

Use a simple appropriate TUI mechanism consistent with the existing PoC.

A future top bar will likely contain:

```text
Project: Chinook ▾ | Session: Customer analysis ▾
```

but building the final shell is out of scope.

## Session naming

A new session can initially have a generic name.

Where straightforward, auto-generate a useful short title after the first meaningful user request, for example:

```text
"Show last 100 orders"
        ↓
"Recent orders"
```

Do not make session naming a complex AI workflow.

Users must be able to rename sessions manually.

---

# 5. Durable RecordSets

Every successfully executed data-producing DTQL query should result in a durable session-scoped RecordSet.

Conceptually:

```text
RecordSet
├── stable ID
├── session ID
├── originating DTQL
├── source/database reference
├── schema/columns
├── rows/result snapshot
├── execution metadata
├── created timestamp
└── provenance/lineage metadata
```

Reuse or evolve the PoC RecordSet abstraction rather than introducing a parallel representation.

## Important invariant

**RecordSets represent result snapshots.**

Once created, the contents of a RecordSet should be treated as immutable.

If a query is executed again and the underlying database has changed, that execution produces another RecordSet rather than silently mutating the old one.

This is important for future investigation reproducibility.

---

# 6. Persist query results, not merely queries

Restarting DataTug must **not require rerunning previous queries** to reconstruct the session.

Persist the actual structured result snapshot.

Example:

```text
> Show last 100 orders

DTQL
  ↓
execute
  ↓
RecordSet rs-123
  ↓
persist snapshot
```

Then:

```text
exit DataTug

$ datatug chat

restore session
  ↓
load rs-123
  ↓
render existing grid
```

The underlying database may have changed while DataTug was closed. The restored historical RecordSet should still represent what the user originally saw.

---

# 7. Session cache semantics

For this product, these persisted RecordSets are part of the **durable session cache**.

"Cache" here means:

- survives application restart;
- belongs to a particular session;
- exists so conversation/investigation context is not lost;
- may be removed when the owning session is cleared/deleted.

It does **not** mean that DataTug should freely evict it while the session exists.

For Phase 2:

```text
exit application
→ keep session cache

restart application
→ restore session cache

switch session
→ keep both sessions

clear session
→ remove its conversation + session cache

delete session
→ remove session and its session cache
```

Future bookmarked RecordSets will have workspace/global lifetime and survive session deletion, but **bookmarks are not part of this phase**.

Design storage so that future promotion to longer-lived ownership is possible, but do not implement it now.

---

# 8. Query provenance and basic lineage

Get provenance right from the start.

Every RecordSet should know how it was produced.

At minimum preserve:

- exact DTQL executed;
- relevant parameters;
- data source/database identity;
- execution timestamp;
- schema/result metadata;
- originating user/agent turn where practical.

Design the model so future derived queries can express relationships such as:

```text
rs-001
Customers ranked by total spend
     │
     └── future view/selection
             │
             └── rs-002
                 Orders for those customers
```

Do not build the complete lineage UI or graph now.

We only need enough durable provenance that future phases don't require changing every RecordSet.

---

# 9. Stable references

Sessions, queries, messages, and especially RecordSets need stable identities.

Do not rely on:

- screen position;
- array indexes;
- model conversation position;
- transient Bubble Tea component state.

The agent and future UI must eventually be able to reference something like:

```text
session: ...
recordset: rs-...
```

without copying the complete result into model context.

Choose an appropriate ID strategy consistent with the repository.

---

# 10. Continuing after restart

This is the most important behavioural requirement.

Example:

```text
$ datatug chat

> Show last 100 orders

[real inline grid]

# terminate DataTug
```

Later:

```text
$ datatug chat

# previous session restored

User can scroll/view the previous conversation
and the previous grid is restored from persisted data.
```

The user should then be able to continue the conversation.

For example:

```text
> Show the customers associated with those orders.
```

The system should have enough structured state to understand/recover relevant prior context.

Do not solve this by serialising opaque model-provider state as the only source of truth.

DataTug should be capable of reconstructing the model context from its own session/message/RecordSet state.

For Phase 2, use the simplest reliable context strategy. Do not prematurely build sophisticated context compression or retrieval.

---

# 11. Chat history

Persist enough message structure to faithfully restore the conversation.

Do not reduce everything to rendered strings if the PoC already has structured messages/artifacts.

Preserve distinctions where relevant between:

- user messages;
- assistant text;
- tool/query operations;
- DTQL;
- RecordSet/grid artifacts;
- errors/status.

After restart, restored grids must still be backed by their persisted RecordSets rather than by cached terminal rendering.

---

# 12. Storage

Choose the simplest robust local persistence appropriate to the existing DataTug architecture.

Requirements:

- transactional enough to avoid easily corrupting sessions;
- reasonably efficient for RecordSet snapshots;
- simple to inspect/debug during development;
- supports multiple sessions;
- supports future bookmarks/global RecordSet references without forcing a redesign;
- works across process restart.

Before introducing a new persistence dependency, inspect whether DataTug already has a suitable local storage mechanism.

Avoid building a distributed/general-purpose storage subsystem.

This is local DataTug workspace/session persistence.

Document the chosen storage structure and lifecycle.

---

# 13. Agent context

Keep the runtime model inexpensive as in the PoC.

Do not start sending all historical RecordSet rows to the model.

The model should receive enough conversation and structured metadata to reason about previous results, while DataTug remains authoritative for actual data.

Design toward future tools such as:

```text
get_recordset(...)
get_recordset_schema(...)
get_recordset_rows(...)
```

if appropriate.

Implement only what is necessary for Phase 2 continuation scenarios.

The important architectural principle is:

> LLM context is temporary. DataTug session state is durable.

---

# 14. UI scope

Preserve the current PoC chat UI:

```text
┌──────────────────────────────────────────────┐
│                                              │
│ chat history                                 │
│                                              │
│ [inline persisted grid]                      │
│                                              │
├──────────────────────────────────────────────┤
│ >                                            │
└──────────────────────────────────────────────┘
```

Add only enough UI for session management.

Do **not** implement yet:

- final split left/right layout;
- workspace panel;
- Project explorer;
- Selected tab;
- Docked tab;
- Bookmarks tab;
- draggable pane divider;
- final top navigation;
- full status bar.

Those belong to subsequent phases.

---

# 15. Explicitly out of scope

Do not implement in Phase 2:

- row/cell selections;
- named selections;
- RecordSet views;
- local filtering model beyond whatever the PoC grid already has;
- docking;
- docked tabs;
- bookmarks;
- tags;
- collections;
- cross-session bookmarked RecordSets;
- project/database explorer;
- attaching DB/table/view/query to chat;
- charts;
- full workspace layout;
- elaborate lineage UI.

However, avoid persistence decisions that make these unnecessarily difficult later.

In particular, future requirements include:

```text
RecordSet
  ├── session-scoped by default
  ├── future Views/Selections
  ├── future Dock → session UI lifetime
  └── future Bookmark → workspace/global lifetime
```

No need to implement those abstractions yet.

---

# 16. Testing

Persistence must have strong deterministic tests.

At minimum cover:

### Session lifecycle

```text
create
save
reload
switch
rename
clear
delete
```

### RecordSet persistence

```text
execute query
→ persist RecordSet
→ terminate/recreate application state
→ reload RecordSet
→ verify schema + rows + DTQL + metadata
```

### Session isolation

Create A and B with different queries/results.

Restart.

Verify A and B restore independently.

### Clearing

Create a session with persisted RecordSets.

Clear it.

Verify its chat/session cache is gone.

### Snapshot semantics

Execute query → persist result.

Change underlying test data.

Reload old RecordSet.

Verify the old snapshot remains unchanged.

### Error/recovery cases

Handle reasonably:

- missing/corrupt session metadata;
- missing RecordSet data;
- interrupted/incomplete writes;
- empty RecordSets.

Do not make automated tests depend on live model API calls where deterministic stubs are sufficient.

---

# 17. Manual acceptance scenario

Use real Chinook data and the actual inexpensive runtime model.

Test something approximately like:

```text
$ datatug chat

> Show last 100 orders

[grid A]

> Show 50 customers from Prague

[grid B]
```

Create another session:

```text
New session

> Show 20 newest invoices

[grid C]
```

Switch back to the first session and verify grids A/B are intact.

Then exit DataTug completely.

Restart.

Verify:

- both sessions exist;
- names are restored;
- conversation histories are restored;
- grids A/B/C render from persisted RecordSets;
- previous queries were not rerun;
- session switching still works.

Continue in the restored first session:

```text
> Show the customers associated with those orders.
```

Verify that previous structured context can participate in the new turn.

Finally clear/delete the first session and verify its session-scoped state/cache disappears while the other session remains intact.

---

# 18. Implementation process

Work autonomously and incrementally.

1. Inspect the current PoC implementation.
2. Inspect relevant existing DataTug persistence/project abstractions.
3. Write a concise proposed state model and storage design.
4. Review it critically for future compatibility with selections, docking and bookmarks.
5. Implement the smallest coherent durable session model.
6. Add RecordSet persistence.
7. Add session management.
8. Add context restoration.
9. Add deterministic tests.
10. Exercise real Chinook scenarios.
11. Kill/restart the process repeatedly during manual testing.
12. Review storage/lifecycle semantics.
13. Remove unnecessary abstractions.
14. Run the broader relevant test suite.
15. Merge to main only when acceptance criteria pass.

Prefer working functionality over speculative generalisation.

---

# 19. Definition of success

Phase 2 is complete when:

- multiple DataTug Chat sessions can exist;
- users can create, switch, rename, clear and delete sessions;
- sessions survive application restart;
- chat history survives restart;
- every successful data query produces a stable, immutable RecordSet snapshot;
- RecordSets persist across restart;
- restored grids render from persisted RecordSets without rerunning queries;
- exact DTQL and basic provenance are retained;
- session state is isolated correctly;
- clearing/deleting a session removes its session-scoped cache;
- DataTug, not ADK/LLM state, is authoritative for durable context;
- a restored conversation can continue using previous structured context;
- the existing PoC AI → DTQL → execution → grid flow remains working;
- tests cover persistence and lifecycle semantics;
- implementation remains small enough to understand and evolve.

## Most important invariant

At any meaningful point during a DataTug Chat investigation, I should be able to terminate the process, start DataTug again, reopen the session, and continue from the same logical state without rerunning previous queries or asking the model to reconstruct lost data.

Do not proceed into selections, docking, bookmarks, tags, or the full workspace UI in this task. Those will be implemented after this foundation has been exercised and accepted.
