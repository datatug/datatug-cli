---
type: sidekick-seed
captured_by: specstudio:implement
status: queued
---
# Semantic field types: currency as a constrained string (ISO len-3 enum) via extends / entity-field references

In `cli/entity` (Task 7) `currency` was registered as a bare `KnownType` — the minimal move to satisfy AC:field-set-updates. The owner's actual intent is richer: a **semantic type**, not a primitive.

Owner's model (verbatim intent):
- `currency` = underlying primitive `string` with constraints: `len = 3`, `values ∈ {USD, EUR, ...}` (ISO-4217 codes).
- "We do not need a separate type system — it's an integral part of entity definition." A field references an entity-field that carries the constraints, e.g. a `currency` entity with a constrained `id` field; columns/fields then reference `currency.id` and inherit `string(3) + enum`.
- The demo already shows entity-level `extends` (e.g. `Country` extends an external ISO def) — `extends:<ref>` is the existing seam (the CLI accepts `extends:<ref>` field types as of Task 7, but the model does not yet resolve/enforce them).

Scope of a follow-up Feature:
- Add constraint fields to `EntityField` (e.g. `len`/`minLen`/`maxLen`, `values`/`enum`), OR resolve them via entity-field references / `extends:<ref>`.
- Decide: distinct primitive vs. constrained-string-via-extends vs. entity-field reference (`currency.id`). Owner leans to entity-field reference.
- Enforce constraints in `datatug validate` and/or at author time.
- Relationship to the `cli/map` column→entity-field link (`entity_field: currency.id`).

Deliberately OUT of scope for `cli/entity` (no AC tests constraints). Sequence alongside the module-system idea.
