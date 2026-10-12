# Chat Phase 4 implementation design

The source specification is [chat-phase4-original-prompt.md](chat-phase4-original-prompt.md).
This design records the Phase 3 boundary and the reviewed Phase 4 cut.

## User journey and observable results

1. In Session A, ask for real Chinook customers and select five Prague rows. The
   `Selected` workspace identifies those five source rows. Cell/range
   selections retain an exact selected-cell mask, not a rows-by-columns guess.
2. Bookmark the selection, give it a title, and add two tags. The Bookmarks tab
   lists one durable project item with that title, tags, target kind, row count,
   source, snapshot time, and DTQL provenance. No query is rerun.
3. Open Session B. Search/filter the bookmark by both tags, open its frozen grid,
   attach it, and dock it. The attached context exposes column/parameter names
   to the agent; DataTug binds typed values locally for a follow-up DTQL query.
4. Clear and delete Session A. Its messages, session RecordSets, Views, Selections,
   and docks disappear. The bookmark still opens with precisely its saved data.
5. Quit and restart DataTug. The bookmark, tags, attachment/dock in Session B,
   and follow-up query path all restore without rerunning the old query.
6. Delete a bookmark. Deletion is refused while any persisted session attaches
   or docks it; detaching/undocking those references permits deletion. Session-
   local RecordSets remain independently owned by their sessions.

## Current Phase 3 ownership

The private SQLite store is selected by canonical project path. Sessions are
partitioned by a hash of environment, database, source configuration, and
access-policy/principal fingerprint. `recordsets` and `queries` have cascading
session foreign keys. Views, Selections, attachments, and docks are serialized
in `session_workspace`. A bookmark cannot safely retain only their IDs.

## Persistence and reachability

Schema v3 adds a `bookmarks` table to that same SQLite file. Each row has a
stable ID, explicit project ID, exact access scope, title, normalized/display
tags, timestamps, target kind, and one self-contained immutable snapshot. The
snapshot stores the typed RecordSet result using the existing result codec,
query/source/provenance strings, and a frozen effective View/Selection
definition when applicable. Original session/query IDs are historical
provenance, never live foreign keys. Organisational metadata is outside the
snapshot and may be edited without changing result bytes.

The copied View/Selection IDs are internal to that bookmark snapshot and are
never resolved through an originating session. For a row selection (no
`Ranges`), its ordered `Rows` and `Columns` define the effective projection.
For a cell/range selection, the union of saved `CellRange` coordinates defines
the selected cells; rows and columns only determine their order. The bookmark
grid renders unselected cross-cells as blank, and local query-parameter
binding excludes them. This avoids exposing extra cells from a disjoint range
as if they had been selected. Tests assert the exact selected-cell set and
typed bound values after restart.

This is deliberately copy-on-bookmark instead of a new shared-object/GC layer:
it costs one bounded result copy (query limit is 1000) but makes session
clear/delete safe and lets SQLite cascade session-only data normally. A
bookmark owns its copy; deleting an unreferenced bookmark deletes that copy.
All creation, metadata updates, and deletion use transactions. Creation reads
the authoritative session snapshot inside its transaction. Deletion checks
all persisted session attachments/docks in the same scope inside its
transaction. Corrupt/missing snapshots fail closed with a clear error.
Attaching or docking a bookmark validates the scoped bookmark inside the same
`SaveWorkspace` transaction as its JSON write. Together with deletion's
transactional reverse scan, this serializes attach/delete even across two
`OpenSessionStore` instances and leaves no stale reference race.

Bookmark visibility remains within the *exact current ChatScope* as well as
the project path/ID. This is intentionally narrower than all databases or
policy identities in a project; it prevents cached, policy-redacted data from
being reopened under another access configuration. Broader authorization is
future work, not a Phase 4 shortcut.

## Application and UI boundary

`SessionChat` is the store-aware action boundary. Bubble Tea and ADK's
`workspace_action` both call it; neither mutates bookmark rows independently.
The session snapshot carries only bookmarks visible in its exact scope. A
`bookmark` context reference resolves through that scoped map for validation,
grid rendering, local parameter binding, and concise model context. The model
sees identities, column names, counts, safe catalog source/database IDs and
parameter names, not row values. Raw source URLs (which may contain
credentials), query parameters, and arbitrary provenance text remain in the
private snapshot only; they are never emitted to model context, UI status, or
normal errors. Bookmark references are session attachments/docks; they never promote
those session objects themselves.

The Bookmarks tab is keyboard-first: tab navigation, arrows to browse, Enter
to open a frozen grid, `a` attach/detach, `d` dock, `r` rename, `t` add tag,
`T` remove tag, `/` search/filter, and `x` delete with a confirmation. A
contextual status/help line documents these actions. The same operations are
available to the agent through structured actions.

Tags trim whitespace, reject blank/control values, compare case-insensitively,
preserve first display spelling, and deduplicate. Multiple tag filters use
AND. Search matches title/tags case-insensitively. Results sort by update time
descending, then ID, for deterministic browsing.

## Acceptance gates

Deterministic tests cover v2-to-v3 migration, malformed snapshots, exact
RecordSet/View/row/cell/range selections, typed values after restart, tag
normalization and AND filtering, project/scope isolation, a credential-bearing
source URL never appearing in model/UI output, cross-session
attach/dock/query context, clear/delete of the origin session, deletion with
live references, and UI/agent action parity. The manual gate is the real
Chinook + inexpensive-model journey above, including a process restart after
deleting Session A. Review and merge only after the gates pass.
