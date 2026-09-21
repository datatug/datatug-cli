# DataTug Chat Phase 5 — Interactive JOIN Discovery and Exploration

Implement **Phase 5: interactive FK-based JOIN discovery and exploration** on top of the merged Phases 1–4.

The goal: for every relation instance already participating in a DTQL query, show relations that can be joined through database foreign keys, let the user navigate/apply those joins directly from the grid, derive real DTQL, execute it through normal DataTug/DALGO infrastructure, and continue exploring.

For MVP, **foreign keys are the only JOIN-candidate evidence source**. Design for future DataTug project metadata / `entity.field` mappings, ModelSpec relationships and inferred relationships, but do not implement them now.

## 1. Inspect first

Before implementation inspect the actual repository: DTQL AST and JOIN/nested-JOIN support, aliases/relation instances, DALGO execution, schema/FK introspection, RecordSet lineage, Views, grids/focus, shared application actions, agent tools, persistence, bookmarks, and Chinook setup.

Do not create a parallel query model. Write a concise design and plan based on the real code, critically review it, then work autonomously through implementation, tests, UX review and merge.

## 2. Target UX

For:

```text
> Show recent invoices.

[interactive grid]

You can JOIN:
  Invoice: Customer, InvoiceLine
```

The JOIN area belongs to the query/grid and should normally be directly under the grid.

For a query already containing multiple relations:

```text
You can JOIN:
  Invoice:  InvoiceLine
  Customer: Employee
  City:     Country, Region
```

Each row corresponds to a **relation instance in the current query**, not merely a physical table.

Suggested keyboard interaction:

```text
↑ / ↓     navigate source-relation rows
← / →     navigate candidates
Space     change JOIN state / add JOIN
Enter     inspect relationship details
Esc       return focus to grid
```

Adapt shortcuts if they conflict with existing TUI conventions. Keyboard operation is required; mouse support is optional.

## 3. Relation-instance identity

JOIN candidates belong to exact query relation instances/aliases.

For:

```text
FROM Invoice AS purchase
JOIN Invoice AS refund ...
```

support:

```text
You can JOIN:
  purchase (Invoice): Customer, InvoiceLine
  refund (Invoice):   Customer, InvoiceLine
```

The UI may omit aliases when unambiguous, but internal identity must never rely only on the physical table name.

Nested/multi-relation DTQL queries must be walked using their actual structure. Do not flatten away aliases or JOIN semantics.

## 4. JoinCandidate is an edge

A candidate is a relationship edge, not a target-table string.

Conceptually:

```text
JoinCandidate
├── identity
├── sourceRelationInstance
├── sourceFields[]
├── targetRelation
├── targetFields[]
├── direction
├── cardinality
└── evidence
```

For Phase 5: `evidence = foreign-key`.

Reuse existing schema/DTQL types where appropriate.

## 5. Same target through multiple fields

If multiple FKs reach the same target, distinguish them.

```text
Order.BillingAddressId  → Address.Id
Order.ShippingAddressId → Address.Id
```

Display:

```text
Order: Address (BillingAddressId), Address (ShippingAddressId)
```

If only one relationship to a target exists, keep it compact:

```text
Invoice: Customer
```

Relationship identity must remain distinct internally regardless of display.

## 6. Composite FKs

A composite FK is one candidate:

```text
Order.(CountryCode, LocationCode)
  → Location.(CountryCode, LocationCode)
```

Display compactly, e.g.:

```text
Order: Location (CountryCode, LocationCode)
```

Never expose component columns as separate candidates. Add synthetic tests if Chinook lacks composite FKs.

## 7. Both directions and cardinality

Discover an FK relationship from either side where useful.

For:

```text
Invoice.CustomerId → Customer.CustomerId
```

Invoice can offer Customer, and Customer can offer Invoice.

Retain cardinality:

```text
Invoice → Customer   many-to-one
Customer → Invoice   one-to-many
```

One-to-many JOINs may multiply rows. Do not block them, but make cardinality inspectable. A compact `→1` / `→*` indication is welcome if readable.

## 8. Active JOIN suppression

Do not show the exact relationship edge already active in the query.

But do **not** suppress every relationship merely because its target physical table already appears. Multiple FKs, repeated aliases and self-joins must remain possible.

Suppress exact active edge/path identity.

## 9. Candidate details

`Enter` should show exact relationship details, preferably using the existing details/Selected workspace:

```text
Customer

Invoice.CustomerId
  → Customer.CustomerId

Evidence: Foreign key
Cardinality: many → one
```

For ambiguity:

```text
Address via ShippingAddressId

Order.ShippingAddressId
  → Address.Id
```

## 10. Applying a JOIN

Interactive JOINs must derive **real DTQL**:

```text
current DTQL + JoinCandidate
→ derived DTQL
→ validate
→ normal DataTug/DALGO execution
→ new immutable RecordSet
→ grid
```

Do not locally merge displayed rows, generate SQL directly from the UI, or bypass normal execution.

FK metadata deterministically defines the ON condition. The AI must not invent it.

Example:

```text
Invoice.CustomerId → Customer.CustomerId
```

produces the DTQL equivalent of:

```text
JOIN Customer
ON Invoice.CustomerId = Customer.CustomerId
```

Use existing/default DTQL JOIN-type semantics. Do not turn this phase into a full JOIN-type editor.

Follow existing DTQL projection/wildcard rules and keep duplicate column names distinguishable.

## 11. Space and removal

The intended interaction is that Space changes JOIN state.

Adding must work in Phase 5.

If dependency-aware removal is clean with the existing AST, implement full toggle. If removing a JOIN can invalidate SELECT/FILTER/ORDER/etc., do not implement unsafe deletion. Provide a controlled failure/removal path and keep the domain action model ready for proper toggling.

Never silently create invalid DTQL.

## 12. Chained exploration

After every JOIN, rediscover candidates from the resulting query.

Example:

```text
Invoice
  ↓ JOIN Customer

Invoice + Customer

You can JOIN:
  Invoice:  InvoiceLine
  Customer: Employee
```

Allow further JOINs without arbitrary depth-one limitations.

Do not automatically traverse the relationship graph; every JOIN requires explicit user/agent action.

## 13. Shared UI/agent operation

The agent should understand requests such as:

```text
Join customers.
Add Customer.
Join InvoiceLine.
Join the shipping address.
```

UI and agent must invoke the same application/domain operation:

```text
UI Space ───────────┐
                    ├── ApplyJoinCandidate(...)
Agent action ───────┘
```

When deterministic FK candidates exist, do not let the model synthesize an unrelated JOIN.

If ambiguous:

```text
Order has two joins to Address:
- BillingAddressId
- ShippingAddressId
```

require selection/clarification rather than guessing.

FK candidate discovery, ON construction, direction/cardinality and rendering require **no model call**.

## 14. Future evidence sources — architecture only

Keep evidence extensible:

```text
ForeignKey                  ← Phase 5
DataTug project metadata    ← future
entity.field mapping        ← future
ModelSpec relationship      ← future
inferred relationship       ← future
```

Future sources may carry confidence/priority/explanation. Do not implement them now.

## 15. Self joins, aliases and cycles

Support self-FKs, e.g.:

```text
Employee.ManagerId → Employee.EmployeeId
```

Display meaningfully:

```text
Employee: Employee (ManagerId)
```

Generate readable collision-safe aliases according to existing DTQL conventions.

Repeated target joins such as billing/shipping Address must get independent aliases.

Schemas may contain cycles. Candidate discovery must never recursively traverse them by itself or enter infinite loops.

## 16. Provenance and lifecycle

A JOIN creates a normal immutable RecordSet with lineage:

```text
rs-101 Invoice
   └── JOIN Customer via FK
          ↓
       rs-102 Invoice + Customer
```

Retain applied edge identity, derived DTQL, parent/query lineage where appropriate, execution metadata and schema metadata.

JOIN-derived results must work normally with Views, Selections, context attachments, docks, bookmarks and tags.

Existing restart semantics remain: restore snapshots/query/aliases without rerunning historical JOIN queries. Candidate lists may be recomputed from query + current schema metadata.

If schema/FK metadata changes, historical RecordSets/provenance remain unchanged. If a candidate is no longer valid when applied, fail clearly and recompute candidates; never guess a replacement.

## 17. Performance and empty state

Candidate discovery should feel immediate. Reuse existing schema metadata/cache; do not introspect the entire database on every cursor move.

Refresh at sensible boundaries: query creation, grid activation, schema refresh, or query/JOIN modification.

If there are no FK candidates, omit the section or show a compact `No FK joins available`. Do not ask AI to infer joins in Phase 5.

## 18. Errors

Use concise actionable errors:

```text
Cannot JOIN Customer: foreign-key metadata is no longer available.
```

```text
Cannot remove Customer: selected columns still reference this relation.
```

```text
Cannot add Address: choose BillingAddressId or ShippingAddressId.
```

Detailed diagnostics belong in debug logging.

## 19. Automated tests

Add deterministic coverage for:

- simple FK candidate and exact ON expression;
- reverse direction/cardinality;
- candidates for every relation instance in a multi-table query;
- exact active-edge suppression;
- two FKs to same target;
- composite FK;
- repeated physical table with aliases;
- self FK and alias generation;
- nested JOIN queries;
- cycles/no infinite traversal;
- candidate → valid derived DTQL;
- chained JOIN rediscovery;
- provenance/lineage;
- persistence/restart;
- session/project lifecycle regressions;
- ambiguous natural-language target must not be guessed.

Use synthetic schema fixtures for same-target FKs, composite FKs and self-FKs if Chinook does not cover them. Deterministic tests must not require live model calls.

## 20. Real Chinook acceptance

Using actual Chinook FK metadata:

```text
> Show recent invoices.
```

Verify real JOIN candidates appear.

Navigate to Customer and apply it.

Verify:

1. exact FK edge selected;
2. correct DTQL JOIN/ON derived;
3. DTQL validates;
4. normal DataTug/DALGO execution is used;
5. new immutable RecordSet produced;
6. joined data displayed;
7. lineage recorded;
8. candidates recomputed for all participating relation instances.

Then apply another candidate and verify chained exploration.

Also:

```text
> Join customers.
```

must invoke the same domain operation and produce equivalent DTQL/result.

Restart DataTug and verify the joined result restores and candidate discovery still works without rerunning historical queries.

## 21. Explicitly out of scope

Do not implement:

- inferred joins from names or data statistics;
- DataTug `entity.field` relationship discovery;
- ModelSpec relationship discovery;
- confidence ranking;
- AI-generated JOIN recommendations;
- automatic multi-hop JOIN insertion;
- automatic graph traversal;
- visual schema graph;
- full JOIN-type editor;
- unrelated grid/chart/query-builder features.

## 22. Implementation process

Work autonomously:

1. inspect merged code and current DTQL JOIN support;
2. specify JoinCandidate/edge semantics;
3. adversarially review aliases, reverse direction, same-target FKs, composites, self joins, nested joins, cycles and removal safety;
4. plan;
5. implement deterministic FK discovery;
6. render/navigate candidates under grids;
7. implement shared application action;
8. derive/execute real DTQL;
9. integrate agent actions;
10. add lineage;
11. verify persistence/restart;
12. add comprehensive tests;
13. test real Chinook;
14. inspect actual terminal UX;
15. remove unnecessary complexity;
16. run broader tests;
17. perform final adversarial review and fix findings;
18. merge to main only when acceptance criteria pass.

## 23. Definition of success

Phase 5 is complete when:

- every relation instance can expose FK-based candidates;
- candidates are grouped compactly by source relation under the grid;
- aliases/repeated tables work;
- same-target relationships are visibly distinguishable;
- composite FKs are single correct candidates;
- both FK directions/cardinality work;
- exact active edges are suppressed without hiding distinct relationships;
- nested/multi-relation queries work;
- keyboard navigation is practical;
- details explain exact fields/evidence/cardinality;
- applying a candidate derives valid DTQL and uses normal execution;
- FK metadata, not AI guessing, defines ON;
- joined results are immutable RecordSets with lineage;
- candidates are rediscovered after JOINs;
- chained exploration works;
- agent JOIN requests use the same domain action as UI;
- ambiguous relationships are never guessed;
- JOIN-derived state follows existing restart/session/bookmark lifecycle;
- synthetic edge-case tests and real Chinook acceptance pass.

### Most important invariant

**JOIN discovery is deterministic schema-aware DataTug functionality. The AI may ask DataTug to apply a relationship, but DataTug owns relationship identity, ON conditions, query derivation, execution, provenance and state.**

Once complete, merge Phase 5 to main and stop. Do not continue into inferred/project-metadata JOIN discovery until the FK-based interaction has been used and evaluated.
