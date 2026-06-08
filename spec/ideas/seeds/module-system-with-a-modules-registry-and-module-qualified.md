---
type: sidekick-seed
captured_by: specstudio:implement
status: queued
---
# Module system with a modules registry and module-qualified entity references

Organize project items (entities, and later other items) into **modules**, modeled on how SpecScore/Synchestra configures modules.

- A root registry registers **modules by path**, e.g. `payments: modules/payments`.
- Each module owns its own `entities/` dir; an entity lives at `<module-path>/entities/<Name>/<Name>.entity.json`.
- Entity references become **module-qualified**, e.g. `payments/currency.id` resolves to `modules/payments/entities/currency/currency.entity.json` → `.fields.id`.
- Single root `entities/` (no modules) is the degenerate single-module/default case — this is what the `cli/entity` Feature assumes, so modules are deliberately **out of scope** there.

Open questions:
- Registry file format & location (NOT `entities.yaml` per discussion; likely a modules-level config like SpecScore's).
- Is module identity just **namespacing/location**, or does it carry richer **identity semantics** (same name meaning different things per module, cross-module inheritance)? This determines whether modules should be specced *before* `cli/map`.
- Qualified-reference syntax and collision/uniqueness rules.
- Alignment with the `cli/map` column→entity-field link key name (`entity_field` vs alternatives).

Recommended sequencing: build `cli/entity` on single-root first; ideate/specify this **before `cli/map`**.
