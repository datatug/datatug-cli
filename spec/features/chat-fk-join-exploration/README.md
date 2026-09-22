---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Chat FK JOIN exploration

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/chat-fk-join-exploration?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/chat-fk-join-exploration?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/chat-fk-join-exploration?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/chat-fk-join-exploration?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Discover and apply deterministic foreign-key joins from DataTug Chat RecordSets.

## Problem

Chat currently displays an immutable query result but gives the user no schema-aware path to follow a related table. Asking the model to invent an ON clause is unreliable and bypasses the project database's relationship evidence.

## Behavior

### FK evidence and identity

#### REQ: fk-edges

For each exact relation instance in a parsed DTQL source tree, DataTug MUST discover outgoing and incoming foreign-key edges from the configured source's schema metadata. A composite FK is one edge. An edge carries source-instance path and alias, stable source constraint ID, ordered field pairs, target relation, direction, cardinality, and `foreign-key` evidence. Candidate order is deterministic. Missing FK capability yields a compact empty state, never an inferred relationship.

Candidate exposure and application MUST fail closed for a policy-secured session when the target relation cannot be proven readable without row or field restrictions. DALgo's secure-read authorization is the execution authority; a no-row-read preflight using the same policy set may filter candidates. Nested joins MUST be refused before execution if the DALgo policy path does not authorize every nested source. No JOIN may bypass a target relation's policy by being attached to an allowed base relation.

#### REQ: distinct-candidates

DataTug MUST distinguish two FKs to the same target, repeated aliases of one physical table, and self-FKs. It MUST suppress only an edge already active for that exact instance and normalized ON field-pair path, accepting equality operands in either order; legacy joins without stored FK provenance are matched conservatively by exact complete field-pair set. Candidate discovery MUST be bounded and non-recursive; adding a JOIN triggers a new pass.

### Query derivation and execution

#### REQ: deterministic-join

Both the terminal and agent MUST invoke one DataTug-owned ApplyJoinCandidate operation identified by a RecordSet and exact candidate identity. The operation MUST revalidate current FK metadata, derive a JOIN with qualified FK equality predicates in real DTQL, validate it, and execute through the existing DALgo-backed secure-read executor. The model MUST NOT supply ON predicates or SQL for this operation. The Chat `run_dtql` model tool MUST refuse JOIN-shaped DTQL so it cannot bypass this operation; other DataTug DTQL execution routes retain normal JOIN support.

#### REQ: projection-and-aliases

JOIN derivation MUST preserve the parent query's filters, grouping, ordering, limits and existing projections where valid, provide collision-safe relation aliases and distinct displayed target columns, and refuse transformations it cannot make valid without guessing. Aliases use the target relation name with a deterministic numeric suffix on collision; unqualified references in a previously single-relation query are bound to that original relation, while an already multi-relation query must already be qualified. Wildcard output is expanded from known schema columns before a JOIN to avoid duplicate map keys. JOIN removal is excluded unless dependency-aware validation is implemented; Space may add a candidate and must not silently delete a dependent JOIN.

### Presentation and lifecycle

#### REQ: inline-navigation

The candidate area MUST appear directly below its grid, grouped by source instance, with keyboard navigation, exact relationship details, cardinality indication, and a Space-to-add action. Esc returns to the grid. Existing chat, grid, workspace, and mouse text-selection behavior MUST remain usable.

#### REQ: immutable-lineage

An applied JOIN MUST create a new immutable RecordSet through the normal query path, retaining parent RecordSet ID, exact FK edge identity, and derived DTQL. Chat storage MUST migrate existing v1–v3 databases transactionally to a new lineage-aware schema; bookmark snapshot encoding MUST preserve lineage while decoding old snapshots without it. Restart MUST restore the result and lineage without re-executing it; candidate lists may be recomputed from current metadata. Existing Views, Selections, attachments, docks, bookmarks, and tags must continue to reference the new RecordSet normally.

#### REQ: safe-agent-selection

The agent MUST see bounded candidate identities and metadata, not row values. If more than one candidate matches a requested target and no exact edge is selected, DataTug MUST ask for clarification rather than choose by table name.

## Acceptance Criteria

### AC: schema-edge-cases

Given synthetic schemas with forward, reverse, composite, two-to-one-target, self and cyclic FKs, and a nested DTQL query with repeated table aliases
When DataTug discovers candidates for every relation instance
Then each edge has stable distinct identity and correct ordered ON field pairs/cardinality, exact active edges are suppressed, and discovery terminates without recursively traversing the schema graph.

### AC: apply-and-chain

Given a real Chinook Invoice RecordSet and its FK candidates
When the user selects Customer and presses Space, then applies another candidate from the resulting query
Then each action produces valid derived DTQL, runs through the normal executor, displays real joined rows, and offers candidates for all participating instances.

### AC: policy-safe-joins

Given policy-secured access where a joined target is denied, row-restricted, or field-restricted, including through a nested source
When candidates are exposed or a model/user tries to execute the JOIN
Then DataTug does not execute a query that can read that target, and reports a concise refusal; an unrestricted or fully permitted target remains joinable.

### AC: agent-parity-and-ambiguity

Given the same RecordSet and candidate catalog
When the agent asks DataTug to apply an exact Customer edge
Then the same application operation and DTQL derivation run as for Space; when two Address FKs match only a target name, DataTug returns a clarification without executing a query.

### AC: restart-and-context

Given a JOIN-derived result that is selected, docked and bookmarked
When DataTug exits and restarts, and the origin session is switched or deleted
Then the immutable result, lineage and dependent workspace/bookmark snapshots restore without rerunning the historical JOIN query, and current FK candidates can be recomputed.

### AC: keyboard-and-error-ux

Given a chat with multiple result grids and FK candidates
When the user navigates candidate source rows and edges, opens details, applies a JOIN, or returns with Esc
Then focus and history scrolling remain predictable; a stale FK or invalid derivation produces a concise actionable message with no raw database or model payload.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
