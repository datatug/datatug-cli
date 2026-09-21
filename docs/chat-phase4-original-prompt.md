# DataTug Chat Phase 4 --- Bookmarks, Tags and Cross-Session RecordSets

Phases 1--3 of `datatug chat` are implemented: AI → DTQL → real
RecordSets/grids, durable sessions and snapshots, split Chat/Workspace
UI, project explorer, structured context attachments, Views/Selections,
and session-scoped docking.

Implement **Phase 4: durable bookmarks, tags, discovery, and
cross-session RecordSet reuse**.

The goal is to establish the boundary between temporary session
analytical state and explicitly retained project-level analytical
artifacts. Do not expand into collections, charts, incident workflows,
or unrelated analytics.

## 1. Core lifecycle

A RecordSet/View/Selection is session-scoped by default. Docking does
not change its lifetime. Bookmarking promotes the analytical object to
project/workspace lifetime.

``` text
Query
  ↓
RecordSet                         SESSION
  ↓
View / Selection                 SESSION
  ├── Dock ────────────────────► SESSION
  └── Bookmark ────────────────► PROJECT
                                  ├── title
                                  ├── tags
                                  ├── snapshot
                                  ├── schema
                                  ├── provenance
                                  └── required lineage
```

A bookmark must survive application restart, session switching, session
clearing, and deletion of its originating session.

## 2. Inspect Phase 3 first

Before implementation, inspect the actual merged code: RecordSet
persistence, session ownership/lifetime, Views/Selections, docking,
stable IDs, provenance/lineage, context references, project model,
workspace tabs, application actions, storage, and cleanup behaviour.

Write a concise design based on the real implementation. Prefer
extending the existing persistence model over introducing a second
store.

## 3. Bookmark as first-class object

Introduce a durable bookmark abstraction, conceptually:

``` text
Bookmark
├── stable ID
├── project ID
├── target: RecordSet | View | Selection
├── title
├── tags
├── created/updated timestamps
└── provenance/reference metadata
```

The bookmark owns organisational metadata. Do not mutate the immutable
RecordSet to store bookmark metadata.

## 4. Durable reachability

Do not create fragile references such as `session-A/recordset-17` that
become invalid when Session A is deleted.

Prefer stable objects with ownership/reachability:

``` text
RecordSet rs-17
   ↑
   ├── Session A
   └── Bookmark B

delete Session A

RecordSet rs-17
   ↑
   └── Bookmark B
```

The RecordSet remains because Bookmark B still requires it.

Use the simplest robust mechanism appropriate to the current store:
ownership records, reference tracking, reachability cleanup, or
equivalent. Do not build an elaborate GC. Conservative cleanup is
acceptable; deleting still-referenced data is not.

## 5. Bookmarkable targets

Support bookmarking:

-   complete RecordSet;
-   View over a RecordSet;
-   meaningful Selection over a View/RecordSet.

If the user queries 20 customers, selects five, then says
`Bookmark them`, the bookmark must represent exactly those five---not
the parent 20.

Retain enough View/Selection definition and snapshot data to reproduce
exactly what was bookmarked.

## 6. UI and agent parity

Bookmarking must use shared application/domain actions.

``` text
Agent: "Bookmark these" ─┐
                         ├── CreateBookmark(target)
UI Bookmark action ──────┘
```

Apply the same rule to rename, delete, tagging, attachment, docking, and
search/filter operations.

Do not put bookmark business logic independently into Bubble Tea
handlers and AI tools.

## 7. Automatic names

Generate useful default names deterministically from existing
query/View/Selection metadata where possible:

-   `Top 5 Prague customers`
-   `Recent 100 orders`
-   `Brazil customers`
-   `Largest invoices`

Do not call an expensive LLM solely for naming. Allow manual rename.

## 8. Tags

Bookmarks support multiple tags from the start.

Examples:

``` text
incident
2026-09-15
payments
production
shipping
customer-analysis
```

Tags belong to the bookmark, not the RecordSet.

Support:

-   add tag;
-   remove tag;
-   list tags;
-   filter bookmarks by tags.

Normalize tags consistently: trim whitespace, prevent accidental
duplicates, and define case handling. Preserve useful display form where
practical.

Multiple tag filters use **AND** semantics by default:

``` text
incident + 2026-09-15
```

means bookmarks containing both tags.

Do not implement a complex expression language.

## 9. No collections yet

Do not implement folders/collections in this phase.

Tags are intentionally the MVP organisation mechanism. We want to learn
whether combinations such as `incident`, `2026-09-15`, and `payments`
are sufficient before adding hierarchy.

Do not emulate collections with hidden/special tags.

## 10. Bookmarks Workspace tab

Add:

``` text
[Project] [Selected] [Docked] [Bookmarks]
```

The Bookmarks tab must support:

-   browse;
-   search/filter;
-   filter by one or more tags;
-   open;
-   attach to chat;
-   dock into current session;
-   rename;
-   add/remove tags;
-   delete.

Keep it keyboard-first and consistent with the rest of DataTug Chat.

A compact presentation might be:

``` text
Bookmarks

Filter tags: [incident] [2026-09-15]

Top Prague customers · 5
  incident  2026-09-15  customers

Failed orders · 37
  incident  2026-09-15  payments
```

Prioritise correct interaction/lifecycle semantics over visual polish.

## 11. Cross-session use

Bookmarks belong to the project, not a chat session.

Scenario:

``` text
Session A

> Show ...
> Select ...
> Bookmark these.
> Tag this incident and 2026-09-15.
```

Switch to Session B, find and attach the bookmark:

``` text
[bookmark: Top Prague customers · 5 ×]

> Show their largest orders.
```

The agent must use the bookmarked RecordSet/View/Selection as structured
context exactly as it uses session-local context.

Do not paste all bookmarked rows into the LLM prompt unnecessarily.
Reuse the Phase 3 structured-reference/tool architecture.

## 12. Bookmark → Dock

A project bookmark can be docked into the current session:

``` text
Bookmark                  PROJECT
   └── Dock in Session B  SESSION
```

Undocking or deleting Session B must not delete the bookmark.

Deleting a bookmark while it is docked must have deterministic semantics
and must not create dangling references. Either preserve the target
while Session B references it or cleanly detach according to the chosen
ownership model.

## 13. Agent operations

Support straightforward requests such as:

``` text
Bookmark these.
Bookmark this as "Prague customers".
Tag this incident, 2026-09-15.
Add tag payments.
Remove tag payments.
Show my bookmarks tagged incident.
Show bookmarks tagged incident and 2026-09-15.
Attach the Prague customers bookmark.
Delete this bookmark.
```

The model interprets intent. DataTug performs deterministic operations
and owns all state.

Conceptual shared operations may include:

``` text
CreateBookmark(target)
RenameBookmark(id, title)
DeleteBookmark(id)
AddBookmarkTag(id, tag)
RemoveBookmarkTag(id, tag)
FindBookmarks(tags)
AttachBookmark(id)
DockBookmark(id)
```

Adapt APIs to existing code rather than forcing these signatures.

## 14. Provenance and snapshot semantics

A bookmark must retain enough provenance to answer:

-   what is this data?
-   where did it come from?
-   when was the snapshot created?
-   which DTQL produced it?
-   is it a full RecordSet, View, or Selection?
-   what filter/order/selection produced the subset?

Do not build a sophisticated provenance UI yet.

Bookmarking never silently reruns a query. It bookmarks the exact
immutable snapshot/object the user is looking at.

If the underlying database changes later, the bookmark still represents
the original snapshot. A future explicit refresh/re-run operation may
create a new RecordSet; it is out of scope here.

## 15. Project boundary

Bookmarks are project-scoped:

``` text
Project A
├── Sessions
└── Bookmarks

Project B
├── Sessions
└── Bookmarks
```

Do not expose Project A bookmarks in Project B merely because both use
the same local store.

Cross-project sharing is out of scope.

## 16. Critical session-deletion acceptance test

This is the most important lifecycle test:

``` text
Session A
   ↓
query
   ↓
RecordSet
   ↓
selection
   ↓
bookmark selection
   ↓
tags: incident, 2026-09-15
```

Delete Session A.

Verify:

-   Session A/chat disappear;
-   session-only docks/state disappear;
-   unreferenced session objects become cleanup candidates;
-   bookmark remains;
-   exact bookmarked rows remain;
-   bookmark title/tags remain;
-   provenance remains;
-   bookmark opens correctly;
-   bookmark can be attached in another session;
-   the agent can issue new DTQL using it as context.

This must pass before Phase 4 is complete.

## 17. Restart acceptance test

Create several bookmarks and tags, terminate DataTug completely,
restart, and reopen the project.

Bookmarks/tags must restore independently of the originating chat
sessions.

## 18. Cleanup

After sessions, bookmarks, Views, or other references are deleted,
objects no longer reachable may be cleaned up.

Correctness is more important than aggressive cleanup. Conservative
retention is acceptable for MVP.

It is never acceptable to remove data still needed by a bookmark or
active session reference.

Add tests for shared reachability.

## 19. Keyboard usability

Bookmarks must be fully usable without a mouse.

Provide/document sensible actions for:

-   open Bookmarks tab;
-   navigate;
-   open;
-   attach;
-   dock;
-   add/remove tags;
-   filter tags;
-   rename;
-   delete.

Reuse existing application shortcut conventions.

## 20. Errors

Handle lifecycle failures clearly, e.g.:

``` text
Bookmark target is unavailable.
```

``` text
Cannot delete this data because it is still referenced.
```

``` text
No bookmarks match tags:
incident + 2026-09-15
```

Do not silently lose data or silently reinterpret missing targets.

## 21. Automated tests

Add deterministic coverage for at least:

### Bookmark creation

``` text
RecordSet
→ bookmark
→ persist/reload
→ target + metadata intact
```

### View/Selection bookmark

Verify bookmarking a subset preserves exactly that subset.

### Tags

Test add, remove, deduplicate, persistence, reload.

### Multi-tag filtering

Given:

``` text
A: incident, 2026-09-15
B: incident, 2026-09-16
C: payments, 2026-09-15
```

filtering `incident + 2026-09-15` returns A only.

### Cross-session access

Create bookmark in Session A; open/attach in Session B; verify identical
data.

### Session deletion

Bookmark a target from Session A; delete A; verify bookmark remains
fully usable.

### Bookmark deletion

Delete a bookmark; retain underlying data if referenced elsewhere;
otherwise make it cleanup-eligible.

### Restart

Persist bookmarks/tags; recreate application; verify restoration.

### Project isolation

Project A bookmarks must not appear as Project B bookmarks.

Do not make deterministic tests depend on live model calls.

## 22. Manual end-to-end scenario

Use real Chinook data and the normal inexpensive runtime model.

``` text
$ datatug chat

> Show me 20 largest customers with number and total amount of orders.

[real RecordSet/grid]

> Select top 5 from Prague.

[selection]

> Bookmark them.

Bookmark created:
Top 5 Prague customers

> Tag this customer-analysis and 2026-09-21.
```

Switch to Session B:

``` text
# Open Bookmarks
# filter: customer-analysis + 2026-09-21
# select Top 5 Prague customers
# attach
```

Then:

``` text
> Show their largest orders.

[new DTQL based on bookmarked customer context]
[new real RecordSet/grid]
```

Delete Session A.

Restart DataTug completely.

Open Session B or create Session C, filter again by:

``` text
customer-analysis + 2026-09-21
```

Verify `Top 5 Prague customers` still exists, contains the exact
bookmarked snapshot, can be opened/docked/attached, and can participate
in further agent queries.

## 23. Explicitly out of scope

Do not implement:

-   collections/folders;
-   charts;
-   arbitrary new artifact types;
-   incident-specific workflows;
-   collaboration/sharing;
-   cloud sync;
-   scheduled bookmark refresh;
-   automatic re-querying;
-   sophisticated provenance visualisation;
-   semantic/full-text AI search over bookmarks;
-   AI-generated bookmark summaries;
-   complex tag expressions;
-   cross-project bookmarks.

Design around obvious future compatibility where cheap, but do not
expand scope.

## 24. Implementation approach

Work autonomously and incrementally:

1.  inspect merged Phase 3;
2.  document current ownership/lifetime semantics;
3.  propose the smallest bookmark/reachability design;
4.  critically review it for session deletion and shared references;
5.  implement bookmark domain/application operations;
6.  implement durable ownership/reachability;
7.  implement tags and AND filtering;
8.  add Bookmarks Workspace tab;
9.  add attach/dock operations;
10. add agent actions using the same domain operations as UI actions;
11. add deterministic lifecycle tests;
12. run the real Chinook scenario;
13. repeatedly test session deletion and process restart;
14. review for dangling references/data-loss risks;
15. remove unnecessary complexity;
16. run broader relevant tests;
17. merge to main only when acceptance criteria pass.

Do not proceed into speculative Phase 5 work.

## 25. Definition of success

Phase 4 is complete when:

-   RecordSets/Views/Selections can be bookmarked;
-   bookmarks are project-scoped rather than session-scoped;
-   bookmarks have stable IDs and useful default titles;
-   bookmarks can be renamed;
-   bookmarks support multiple tags;
-   tags can be added/removed;
-   multi-tag filtering uses AND semantics;
-   Bookmarks have a dedicated Workspace tab;
-   bookmarks can be opened, attached, and docked;
-   agent and UI actions share application/domain operations;
-   bookmarks can be used as structured agent context;
-   bookmarks work across sessions;
-   bookmarked snapshots remain immutable;
-   deleting the originating session does not destroy bookmarked data;
-   restarting DataTug does not lose bookmarks/tags;
-   project boundaries are respected;
-   no dangling references are introduced;
-   cleanup never removes still-referenced data;
-   automated tests cover ownership/lifecycle semantics;
-   the real Chinook end-to-end scenario works.

## Most important invariant

**A session is temporary working context; a bookmark is an explicit
decision to retain an analytical result beyond that session.**

Clearing or deleting a session must remove session-only state while
leaving every bookmarked analytical object fully intact and usable from
other sessions.

After this phase, stop and use DataTug Chat before defining another
major phase. Let actual usage determine whether the next priority is
richer grids, charts, lineage, saved queries, multi-source analysis,
agent capabilities, Incidentius workflows, or something else.
