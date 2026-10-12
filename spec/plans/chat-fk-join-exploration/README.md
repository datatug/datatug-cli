---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Phase 5 FK JOIN exploration

**Status:** Draft
**Source Feature:** chat-fk-join-exploration
**Date:** 2026-09-21
**Owner:** codex
**Supersedes:** —

## Summary

Implement Phase 5 in the Go CLI Chat without a second query engine: FK-backed candidate discovery, deterministic recursive DTQL derivation, shared UI/agent application, immutable lineage, and real Chinook acceptance. The original request is preserved at `docs/chat-phase5-original-prompt.md`.

## Approach

Journey: open an existing RecordSet → see grouped FK edges below its grid → inspect/select an edge → Space or agent action calls the same application service → service rechecks the edge, derives and validates DTQL → secure-read DALgo execution produces a new immutable RecordSet → chat restores it and offers its next edges. A stale or ambiguous edge fails before execution.

1. Upgrade CLI to `github.com/dal-go/dalgo v0.85.0` and `github.com/dal-go/dalgo2sql v0.18.0`. Compile and execute a recursive-JOIN Chinook fixture as the dependency contract gate before feature wiring. Keep query execution exclusively in `secureread.Executor`/DALgo.
2. At chat startup, read FK edges once from the configured source using DataTug's existing schema provider (SQLite/Chinook first); cache a source-scoped, deterministically ordered snapshot for cursor movement. Preserve SQLite PRAGMA FK id as a stable constraint name, group composite columns by id/sequence, qualify schema/table names, and resolve implicit target PK columns. A source with no supported FK metadata exposes no candidates, not inferred ones. Rescan/revalidate on apply.
3. Walk the real DTQL `FromSource` tree and identify each relation instance by its zero-based source-tree path (`root=[]`, child joins append index) plus effective alias. Key an edge by source instance, stable FK constraint name, direction and complete ordered field pairs; normalize equality operand order to match active ON clauses. A recursive query tree may contain repeated physical tables, but discovery itself never recursively walks FK targets.
4. Derive an inner JOIN in the source tree with collision-safe aliases (`Customer`, `Customer2`, ...) using case-insensitive existing-alias checks. Rebuild the query through DALgo's AST/serializer so filters, limit and other clauses survive; qualify previously unqualified expressions only when the parent has one relation. Expand source wildcard projection from the catalog and append qualified target columns with unique aliases (e.g. `Customer_CustomerId`) to avoid map-key collisions. Reject unsupported projection/aggregation combinations rather than guessing or deleting an existing JOIN.
5. Persist parent RecordSet and exact FK edge identity, including each applied relation-tree path, in optional lineage fields. Migrate v1–v4 stores transactionally to v5; update load/validation and bookmark snapshot copy, while decoding old bookmarks with empty lineage. Use one application operation for UI and agent; keep row/cell values local.
   DALgo's secure-read authorization evaluates flat JOIN sources and refuses joined-target residual row/field rules. For candidate exposure, use a no-row-read target policy probe with the same policy set; for execution, fail closed on nested sources until the DALgo authorization path covers them, and test denial/row/field restrictions plus multiple policies and schema-qualified relations.
6. Render compact candidate groups beneath each grid. Focus joins with a nonconflicting key, use arrows for rows/candidates, Enter for details, Space to add, Esc for grid. Preserve normal grid shortcuts and terminal mouse text selection.
7. Validate synthetic edge cases, a deterministic fake-model agent/UI parity path, Chinook real joins, policy negatives, restart with no historical re-execution, and existing workspace/bookmark lifecycle; inspect actual terminal layout and run broader tests. Finish with adversarial review, then WB-managed landing and exact CI/release/tag checks.

Review decisions: reverse edges carry opposite cardinality; one composite FK stays atomic; billing/shipping edges remain distinct; self-joins always receive a new alias; exact active-edge suppression is instance/field-pair-based; arbitrary schema cycles cannot trigger traversal; removal is deliberately deferred because current SELECT/WHERE/ORDER may depend on the joined alias.

## Tasks

### Task 1: Dependency and FK metadata boundary

**Id:** task-1
**Verifies:** chat-fk-join-exploration#ac:schema-edge-cases
**Depends-On:** —
**Status:** planning

Update the exact DALgo dependency pair and prove JOIN parse/serialize/SQLite execution. Expose a source-scoped FK snapshot through the existing DataTug schema path, preserving constraint ids, schema, ordered fields, and empty-capability behavior.

### Task 2: Candidate discovery and derivation

**Id:** task-2
**Verifies:** chat-fk-join-exploration#ac:schema-edge-cases, chat-fk-join-exploration#ac:apply-and-chain
**Depends-On:** 1
**Status:** planning

Build the pure candidate walker and exact-edge suppression over the parsed DTQL tree. Derive a validated JOIN using FK ON pairs and aliases, with deterministic tests for nested/repeated/self/cyclic cases and safe refusals.

### Task 3: Shared execution and lineage

**Id:** task-3
**Verifies:** chat-fk-join-exploration#ac:apply-and-chain, chat-fk-join-exploration#ac:restart-and-context
**Depends-On:** 2
**Status:** planning

Add one SessionChat application operation that revalidates the selected edge, executes through secure-read, and persists a new immutable result and parent/edge lineage via the v5 migration. Cover old bookmark decoding, restart without rerun, and existing view/dock/bookmark references. Add fail-closed policy protection for JOIN sources.

### Task 4: Agent action and ambiguity

**Id:** task-4
**Verifies:** chat-fk-join-exploration#ac:agent-parity-and-ambiguity
**Depends-On:** 3
**Status:** planning

Expose bounded candidate metadata in context and an exact-candidate tool that calls the shared operation. Refuse JOIN-shaped `run_dtql` model calls, reject target-only ambiguous requests before execution, and assert parity with the UI path using a fake model.

### Task 5: Inline terminal interaction

**Id:** task-5
**Verifies:** chat-fk-join-exploration#ac:keyboard-and-error-ux
**Depends-On:** 3
**Status:** planning

Place grouped candidates directly below each grid; add candidate focus, navigation, details, Space apply, Esc return, cardinality cues and concise stale-edge errors. Exercise narrow and long terminal histories.

### Task 6: Real acceptance and release

**Id:** task-6
**Verifies:** chat-fk-join-exploration#ac:apply-and-chain, chat-fk-join-exploration#ac:agent-parity-and-ambiguity, chat-fk-join-exploration#ac:restart-and-context, chat-fk-join-exploration#ac:keyboard-and-error-ux, chat-fk-join-exploration#ac:policy-safe-joins
**Depends-On:** 4, 5
**Status:** planning

Run focused and broader tests, a real Chinook FK/DTQL/row/restart journey with the inexpensive configured model, inspect the terminal UX, address review findings, then land and verify the exact remote/CI/release/cleanup receipts.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
