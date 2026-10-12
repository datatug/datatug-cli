# Chat Phase 2: durable sessions and result snapshots

The [original Phase 2 request](chat-phase2-original-prompt.md) is preserved
verbatim for historical context.

## Journey and observable results

1. Open `datatug chat`: the most recently updated session in this project,
   environment, database, and access-policy scope opens, or a new one is
   created. Its prior messages and grids are visible without database reads.
2. Ask a question: DataTug saves the user's message, executes model-produced
   DTQL through the existing secure executor, and atomically saves the query,
   result snapshot, and artifact message. The visible grid comes from that
   structured result.
3. Use `/new`, `/sessions`, `/switch`, `/rename`, `/clear`, and `/delete` in the
   composer. Switching restores the selected session; clearing removes only
   that session's conversation and snapshots; deleting removes that session.
4. Exit and reopen: both sessions and their snapshots remain. A follow-up
   uses a compact context rebuilt from DataTug-owned messages and RecordSet
   metadata, never an opaque provider session or all result rows.

## State and storage

Use the already-depended-on SQLite driver, not another persistence service.
Store one private database per canonical project path in
`~/.datatug/chat/<project-path-hash>.sqlite` (directory mode 0700, file mode
0600). Rows are segregated by environment, database, and the existing
principal-plus-policy fingerprint. A changed principal or policy set cannot
reopen an older scope's cached data. The database has four tables: sessions,
ordered messages, executed queries, and immutable RecordSets. Foreign keys
cascade session clear/delete to its session-scoped data. Each successful DTQL
tool execution immediately commits its query and immutable snapshot together;
the model's final text is saved separately. SQLite JSON payloads retain typed
cell values and remain inspectable.

Each RecordSet has a stable UUID, source/environment/database, exact DTQL,
query/message IDs, creation time, columns, rows, and limitations. Re-execution
creates a new ID and never updates the old snapshot. A future longer-lived
bookmark could copy or reference one snapshot under a different owner, but
this phase introduces no bookmark model.

The ADK runner uses an ephemeral provider session per turn. DataTug persists
the chat and rebuilds prompt context from recent messages, exact query text,
result schema, row count, and bounded distinct identifier values from recent
RecordSets. Result rows themselves are never blindly replayed into prompts.
The existing `secureread.Executor`, `secureread.Result`, and Bubble Table grid
remain the execution and UI boundaries.

## Failure semantics

An in-flight user message is durable before the model call. A successful
query is durable as soon as execution ends, even if the process ends while the
model is composing its final response. An interrupted turn may reopen as an
unanswered request or with its committed grid, but it cannot leave a
half-written RecordSet. Missing or corrupt metadata/snapshots produce a clear
load error instead of silently rerunning queries. Clear/delete use SQLite
transactions and do not affect other sessions.
