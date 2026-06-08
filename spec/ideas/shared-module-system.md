---
format: https://specscore.md/idea-specification
status: Approved
---

# Idea: Shared, Standalone Module System (registry + namespaces + qualified references)

**Status:** Approved
**Date:** 2026-06-04
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we organize project items (entities, and later other item types) into named, path-registered modules with module-qualified references — as a standalone, product-agnostic system that DataTug, ingitdb, and SpecScore all align to, without coupling to one another?

## Context

Triggered by the sidekick seed captured during the cli/entity Feature (spec/ideas/seeds/module-system-with-a-modules-registry-and-module-qualified.md). Investigation across sibling repos found: ingitdb already implements a namespaced data system (.ingitdb/root-collections.yaml registry, namespace imports like 'payments.*: path', dot-qualified references) but fused to its collection/record data model; SpecScore defines a file-partition 'modules:' concept that is spec-only and explicitly NOT a namespace, with no Go implementation; Synchestra has no module system. No standalone, reusable module-registry library exists anywhere. DataTug already depends on ingitdb-cli (v1.11.5), whose pkg/ingitdb/config (RootConfig, ResolveNamespaceImports) is the closest prior art. The decision: extract the module system into its own neutral repo (code name github.com/specscore/modules) that all three projects align to — declare once, recognized by the data-access layer, the specs, and DataTug; coupled to none. cli/map will need module-qualified entity-field references, so this must be ideated/specified BEFORE cli/map.

## Recommended Direction

Build a standalone, product-agnostic module system in a dedicated repository (code name github.com/specscore/modules; host org is an Open Question). It owns three generic concerns: (1) a registry that registers named modules by path; (2) namespacing; and (3) module-qualified reference resolution over a PLUGGABLE item abstraction — entities are the first item type, with generalized items by design so queries, mappings, and other items can plug in later. The shared deliverable is a CONTRACT, not shared product code: ideally a format spec plus conformance vectors (mirroring ingitdb's proven, vector-driven model) and a dependency-light Go library, so DataTug, ingitdb, and SpecScore align without code-coupling to each other. The repository file tree is the integration medium. SpecScore and DataTug are the two first consumers that prove the design on divergent item types: DataTug organizes entities into modules and resolves references such as payments/currency.id to a concrete entity file and field (exercising the qualified-reference layer), while SpecScore consumes the same registry to implement its own modules concept (exercising the registry/path layer). Only once proven by both does ingitdb migrate its existing namespaced implementation onto the shared system. ingitdb's existing root-collections/namespace implementation (ingitdb-cli pkg/ingitdb/config) is the reference to extract from — not the home. The identity model is decided by the shared spec, starting from ingitdb's proven PURE NAMESPACING and leaving seams for richer identity to be added later (and, if added, upstreamed into the shared spec so every consumer gains it at once). This is 'aligned, not coupled': a shared standard plus optional shared library, independent implementations.

## Alternatives Considered

- **Home the module system inside ingitdb.** ingitdb already has the most mature namespaced implementation, so this is tempting. Lost because the module system is generic infrastructure, while ingitdb is a git-backed record database — its namespacing is fused to collections/records. Hosting a product-agnostic concept inside one product is a layering inversion and would re-bind every other consumer to ingitdb.
- **Build a DataTug-local module system.** Fastest path to unblock cli/map. Lost because it violates "aligned, not coupled": it would re-implement what ingitdb already proved, then drift, so entities/modules would NOT be recognized by the data-access layer or the specs — defeating "declare once, respected by all."
- **Strictly mirror SpecScore's file-partition `modules:` model (no qualified references).** Maximally aligned with SpecScore. Lost because SpecScore modules are deliberately file-location partitions over a unified graph, with NO module-qualified references — which drops the headline capability (module-qualified entity-field references) that cli/map depends on.
- **Depend on ingitdb-cli's config package directly for module resolution.** Works as raw code reuse (DataTug already imports ingitdb-cli) and is the natural extraction seed. Lost as the long-term home because it couples the generic module concept to a data-DB product's release cadence and API; the standalone repo gets the reuse without the coupling.

## MVP Scope

A timeboxed extraction: a standalone module library (seeded from ingitdb-cli's pkg/ingitdb/config) plus a minimal format spec that can (a) register modules by path, and (b) resolve a module-qualified reference to a concrete item file and field. Success = two consumers prove it on divergent item types: DataTug organizes entities under a module and resolves payments/currency.id end-to-end to the right entity file and .fields[] entry (the qualified-reference layer), and SpecScore reuses the same registry/path layer for its own modules concept. Single-root entities/ remains the degenerate single-module default. Pure-namespacing identity only. Just enough to unblock cli/map and to prove the abstraction is genuinely product-agnostic — before ingitdb migrates onto it. If it isn't slightly embarrassing in scope, we waited too long.

## Not Doing (and Why)

- Rich identity / cross-module inheritance — deferred; designed-for via the pluggable item abstraction, not built in the MVP, and only added by upstreaming into the shared spec
- Migrating ingitdb onto the shared library — deliberately sequenced AFTER SpecScore and DataTug prove the design; it is the next step, not part of the MVP
- Deeper SpecScore-specific module-graph behavior beyond registry/path reuse — SpecScore's first consumption exercises only the shared registry/path layer; richer file-partition graph semantics stay in SpecScore
- Finalizing the host GitHub org (specscore vs strongo) — tracked as an Open Question; MVP proceeds under the working name github.com/specscore/modules
- Non-entity item types (queries, mappings, etc.) as concrete adapters — the abstraction supports them, but the MVP ships only the entity item type

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | A product-agnostic registry + namespacing + qualified-reference resolution can be cleanly separated from any single product's data model behind a pluggable item abstraction. | Prototype the extraction from ingitdb-cli `pkg/ingitdb/config` behind an item interface; have DataTug resolve `payments/currency.id` end-to-end to an entity file + `.fields[]` entry with no ingitdb-specific types leaking through. |
| Must-be-true | DataTug's entity (single `<Name>.entity.json` with `.fields[]`) can map losslessly onto the shared item/reference model so the same on-disk declaration is recognized by DataTug and ingitdb. | Round-trip a DataTug entity against ingitdb's collection/column model; confirm field-level references resolve identically under both readers. |
| Should-be-true | One identity model — starting from pure namespacing — satisfies DataTug, ingitdb, and SpecScore enough to align, with rich identity deferrable. | Map each consumer's concrete needs against pure namespacing; list exactly where (if anywhere) rich identity is genuinely required rather than convenient. |
| Should-be-true | Conformance vectors (ingitdb-style) are the right mechanism to enforce alignment across independent implementations. | Draft a handful of resolution vectors (registry → qualified ref → resolved item/field); confirm both a DataTug reader and ingitdb-cli can be made to pass them. |
| Might-be-true | The standalone module spec and its Go implementation should ship as two repos (spec + lib), mirroring `ingitdb/ingitdb` + `ingitdb-cli`. | Sketch both layouts; decide whether a single repo with spec + lib is simpler for the MVP before committing to a split. |


## SpecScore Integration

- **New Features this would create:** the standalone module-system format spec (its own repo, code name `github.com/specscore/modules`); a DataTug `cli` Feature for the module registry + module-qualified reference resolution; a SpecScore Feature consuming the shared system for its modules concept (the two proving consumers); and, sequenced after proof, an ingitdb migration Feature.
- **Existing Features affected:** `cli/entity` (entities gain optional module context; single-root `entities/` stays the degenerate single-module default); future `cli/map` (consumes module-qualified entity-field references and must be specified after this); SpecScore's existing file-partition modules spec (gains a shared implementation).
- **Dependencies:** must precede `cli/map`; SpecScore + DataTug consumption proves the system before ingitdb migrates onto it; relates to the `semantic-field-types-currency-as-constrained-string-via-extends` seed (e.g. `payments/currency.id` under a module); reuses `ingitdb-cli` `pkg/ingitdb/config` as the extraction reference.

## Open Questions

- Host GitHub org for the standalone repo: `specscore` (current lean) vs `strongo` (generic Go infra) vs other. Code name `github.com/specscore/modules`. If shipped as spec + library, do the standard and the Go implementation live in one repo or two (mirroring `ingitdb/ingitdb` + `ingitdb-cli`)?
- Identity model: pure namespacing (ingitdb-aligned) vs rich identity / cross-module inheritance — and if rich identity is needed, is it upstreamed into the shared spec so all consumers gain it together rather than forked into DataTug?
- Canonical qualified-reference syntax: ingitdb dot-notation (`payments.currency`) vs the seed's slash form (`payments/currency.id`); how the field segment is delimited without ambiguity against the namespace/collection dots.
- Is the shared deliverable spec-only, library-only, or spec + conformance vectors + library (full ingitdb-style)?
- Does the item/entity schema itself live in the shared repo, or only the module/registry/reference layer (with each product's item schema aligning to ingitdb's collection/column model independently)?
- Does ingitdb refactor its existing root-collections/namespace implementation onto the shared library, or merely stay format-compatible with it?

---
*This document follows the https://specscore.md/idea-specification*
