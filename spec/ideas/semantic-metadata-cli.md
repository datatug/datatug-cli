---
format: https://specscore.md/idea-specification
status: Implemented
---

# Idea: Semantic Metadata Authoring CLI Verbs

**Status:** Implemented
**Date:** 2026-06-04
**Owner:** alex
**Promotes To:** cli/entity
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we give datatug a stable CLI surface to author and read semantic metadata — entities/fields, a dedicated table/column → entity/field mapping, and a validated derived copy — so the metadata skills wrap verbs instead of editing project JSON?

## Context

This Idea is **derived from** the approved skills Idea `conversational-metadata-authoring` in the `datatug-ai-skills` repo, which decided to add a conversational semantic-metadata layer as **new CLI verbs wrapped by skills** — explicitly sequencing the skills design *first* so it drives the CLI interface. This is that CLI interface.

The datatug model already has most of the bones: `Entity{ Fields, Tables }`, `EntityField{ id, type, title, isKeyField, NamePatterns }` (where `NamePatterns` is regexp/exact + case-sensitivity), `EntityFieldRef{ Entity, Field }`, recordset columns carrying `Meta *EntityFieldRef`, and a `KnownTypes` set. What is missing is an **authoring surface**: there is no verb to create or mutate an entity, define a field, declare a table/column → entity/field link, or read the resolved picture back. `scan` only ingests live-DB schema; `show`/`validate` only read/validate.

The parent Idea also settled the storage architecture: three layers — physical schema (scanned, owned by `scan`), the semantic model (entities/fields), and a **dedicated mapping artifact** that is the authoritative source of table↔entity and column↔field links. Mapping rules are **explicit or pattern-based** (glob/regex over table *and* column names), with **explicit mappings always winning** over patterns. Entities carry a **generated, never-hand-edited copy** of resolved links for fast reads, with consistency enforced by `datatug validate`.

## Recommended Direction

Add a coherent semantic-metadata command group to `datatug` that the skills can wrap. It covers four jobs: (1) **entity/field authoring** — create a whole entity with all its fields via `add`, which is **create-only and fails loudly if the entity already exists** (it never silently overwrites). `add` carries the full definition as **YAML or JSON** — format resolved by an optional `--format=<yaml|json>`, else the file extension, else content sniffing for stdin — and reads from **stdin** (`-f -`, or no `-f` at all) so a skill can pipe an AI-generated or scan-seeded doc straight in with no temp file. Updates to an existing entity are **explicit and surgical** — granular verbs (`field set` / `field add` / `field rm`; set a field's type from `KnownTypes` or via `extends`, title, key flag) — and any change that overwrites or deletes existing curated content takes an explicit verb or flag, never an implicit whole-entity overwrite. A **batch field-add** (several new fields at once) is allowed because it is purely additive — it fails if any named field already exists; **batch mutation** of existing fields is deliberately not offered by default (whether and how to guard it is an open question). There is deliberately **no `apply`/upsert** and **no whole-entity override** (see Alternatives Considered). On-disk storage stays canonical JSON (the existing `.entity.json` convention); YAML is the human/AI-friendly authoring and echo form, as `dataset-def` already emits; (2) **mapping authoring** — manage rules in the dedicated mapping artifact, supporting both explicit (one named column → one field) and pattern-based rules (glob/regex over table *and* column names), with explicit-always-wins precedence and pattern-vs-pattern by specificity. As with entities, mutation is **fail-loud**: a create verb (`add`) refuses to replace an existing rule, an update verb (`update`) refuses to invent a missing one, and removal is explicit — a deliberate mapping is never silently overwritten; (3) **derive + validate** — regenerate the entities' derived copy from the authoritative mapping and validate consistency (wired into `datatug validate`, with a regenerate step that *fixes*, not just detects); (4) **read-back** — render the resolved semantic picture (entities, fields, and which table/column each maps to) for humans and AI.

A cross-cutting tenet runs through the whole surface: **curated metadata is guarded strongly — every mutation is fail-loud, never a silent overwrite.** Create verbs refuse to clobber what exists, update verbs refuse to invent what doesn't, batch operations are additive-only by default, and any destructive change is explicit. This is the deliberate inversion of "convenient upsert": the metadata is months of accumulated human judgment, and protecting it outranks save-a-keystroke ergonomics.

Operations are also **batch-first**: every authoring verb accepts many items in a single invocation (multiple entities, fields, or mapping rules in one YAML/JSON doc or stdin stream), because the primary caller is an AI reconciling a freshly scanned schema and should do it in **one CLI call, not N** — a single item is just the degenerate batch. Every batch **reports per-item results** (which items applied, which failed and why) and exits non-zero if any item failed. The default is **atomic**, implemented in three phases: **(1) preflight** — validate every item's fail-loud guard and validity, writing nothing; **(2) stage** — write all resulting changes to temp files; **(3) commit** — rename the temp files into place as a batch. The live project files are never touched until the final renames, so a conflict (caught in preflight) or a staging failure leaves the project untouched — atomicity needs **no snapshot/restore machinery**. Every mutating command here consumes a **shared, repo-wide `--git=<none|stage|commit>` capability** (default `none`) rather than reinventing it — the same need applies to existing mutators such as `scan`, so it is a **separate cross-command Feature** this Idea depends on and helps motivate, not something this Idea owns. Its contract: stage, or add-and-commit, **exactly the files the command changed** (never `git add -A`, preserving the surgical guarantee); `commit` takes a message (`-m`) or an auto-summary; on a non-git project `stage`/`commit` fails loudly. A `--continue-on-error` mode is offered for mechanical callers that prefer **partial apply** (write what passes, report the rest), since AI/tools can strip the applied items and retry the failures. **Empty input is an error.**

The mapping artifact is the single source of truth; entities hold a generated copy. Pattern matching **reuses/generalizes** the model's existing `EntityField.NamePatterns` (adding a table/collection selector) so there is one pattern system, not two. The concrete verb names, the mapping artifact's on-disk schema/layout, and the qualified-key format are deliberately designed against the skills' real authoring intents (the parent Idea is the driver), not invented up front.

We choose new dedicated verbs over overloading `scan`/`show` because authoring is a distinct concern from ingestion and display, and a stable, validatable contract is what lets the skills stop hand-editing JSON. The DataTug-core model gains a new mapping artifact rather than altering scanned-column or entity structures, keeping each layer scan-safe and single-purpose.

## Alternatives Considered

- **Overload `scan`/`show` instead of new verbs.** Lost: `scan` is ingestion (and regenerates files), `show` is display; neither is an authoring contract, and bolting authoring onto them couples unrelated concerns and risks clobbering authored intent on re-scan.
- **No dedicated mapping artifact — stamp links on scanned columns or entity defs.** Lost (already decided in the parent Idea): the scanned-column path is clobbered by `scan`; the entity-definition path mixes physical-storage detail into the semantic model. A dedicated, authoritative mapping keeps the three layers clean and scan-safe.
- **No CLI at all — skills write project JSON directly.** Lost: that is the very problem this Idea exists to remove. Direct writes bake schema knowledge into prompts, offer no reusable contract, and drift as the on-disk format evolves.
- **Declarative `apply`/upsert (idempotent create-or-update).** Rejected. Upsert's convenience — re-runnable, no existence check — is outweighed by its blast radius: an AI that scans tables, decides it needs a `User` entity, and `apply`s it would **silently overwrite months of hand-curated metadata**, work lost with no error and no diff. For a project whose value *is* accumulated human judgment, silent overwrite is the one intolerable failure. We deliberately choose **fail-loud over silent-converge**: `add` errors when the entity exists, forcing the producer (AI or human) to *recognize* the conflict and decide — update specific fields surgically, or create a differently-named entity. Idempotency is preserved where it's safe (a granular `field set` to the same value is a no-op); only the dangerous whole-entity blind overwrite is removed.

## MVP Scope

A focused spike landing the **minimum verb set** to: create **multiple entities in one atomic batch `add`** (create-only — the whole batch fails if any already exists; YAML or JSON, from a file **or stdin**) plus one incremental field edit via a granular verb, batch-**add** (create-only) explicit and wildcard mapping rules in a single call (explicit-always-wins), **regenerate** the entities' derived copy, **validate** that copy against the mapping, and **read back** the resolved picture — demonstrated end-to-end on the chinook demo project, including a scan → single-batch-doc → `add` flow of the kind an AI would use. The mapping artifact's schema/layout is resolved just enough to ship this loop; the richer shape (multi-table entities, transforms, full specificity model) follows. Success = the `datatug-ai-skills` metadata skills can be built on these verbs without touching JSON directly.

## Not Doing (and Why)

- The metadata skills themselves — they live in `datatug-ai-skills` as separate Features that depend on these verbs.
- Downstream consumption (country flags, exchange-rate enrichment in query results) — a separate future Feature, not a CLI authoring concern.
- The full pattern-specificity model for many overlapping rules — MVP ships explicit-always-wins plus a simple table-qualified-before-global ordering.
- Deprecating or migrating recordset-column `Meta` and `Entity.Tables` — MVP can dual-write or leave them; the derive-vs-deprecate decision is deferred.
- Interactive TUI authoring — CLI verbs first; any TUI comes later.
- Defining the `--git` integration itself — it is a shared, cross-command capability (also wanted by existing mutators like `scan`); this Idea consumes and motivates it, but a separate Idea/Feature owns it.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | The dedicated mapping artifact's schema and on-disk layout can be defined with keys **qualified** by database/catalog/schema + table (not bare names). | Draft the schema; prototype a scan→author→re-scan cycle on the demo and confirm no collisions and no clobbering. |
| Must-be-true | A "currency-code"-style semantic type is expressible. | Check `KnownTypes` (has `Money`, not `currency`) and the `extends` mechanism; decide new known type vs. `extends` to an ISO currency def. |
| Must-be-true | `datatug validate` can host the mapping ↔ derived-copy consistency check, and a regenerate step can rebuild the copy. | Inspect the `validate` plumbing in `datatug-cli`; prototype a generate + a drift check. |
| Should-be-true | The verb surface composes cleanly and maps 1:1 to the skills' NL intents (entity add, field set, map add/update for explicit or pattern rules, regenerate, read-back). | Walk the parent Idea's example flows through draft verbs; confirm each NL intent has one verb. |
| Should-be-true | Explicit-always-wins precedence (and pattern-vs-pattern specificity) is implementable predictably. | Prototype resolution against deliberately overlapping rules on the demo. |
| Should-be-true | Batch authoring can be atomic via **preflight → stage-to-temp → batch-rename** (no snapshot/rollback) and report per-item results. | Prototype a multi-item batch with one conflicting item; confirm preflight rejects the whole batch with a per-item report and nothing is staged/renamed; confirm `--continue-on-error` applies the rest and reports the failures. |
| Might-be-true | `scan`-derived `NamePatterns` can seed auto-suggested mapping rules. | Run `scan` on the demo DB; inspect how many columns get plausible rule suggestions. |

## SpecScore Integration

- **New Features this would create:** the semantic-metadata **CLI verb group** (entity/field authoring, mapping CRUD, regenerate, validate hook, read-back); the **dedicated mapping artifact** model + its schema/validation in datatug-core.
- **Existing Features affected:** the datatug-core data model (new mapping artifact; possible consolidation of `EntityField.NamePatterns` into the mapping); `validate` (new consistency check + regenerate); `scan` (seed source for suggested rules).
- **Dependencies:** source / driver is the `datatug-ai-skills` Idea `conversational-metadata-authoring` (cross-repo). The skills Features depend on this verb group landing first. This Idea also depends on a shared **git-integration capability** (`--git` on mutating commands), tracked as its own Idea/Feature since `scan` and other mutators want it too.

## Open Questions

- Command grouping and naming — separate `datatug entity …` + `datatug map …`, or a unified `datatug meta …` group?
- Authoring ergonomics — updates go through surgical verbs (no `apply`/upsert). Is the granular set (`field set`/`field add`/`field rm`) enough for the skills, or is a **guarded** whole-doc `update`/merge also needed? Is YAML or JSON the default echo format (both accepted on input)?
- Batch operations against an existing entity: batch *additions* are allowed (additive, fail-if-exists). Should batch *mutations* of existing fields be possible at all, and if so behind what guard (explicit `--allow-overwrite` / `--allow-delete`, or a shown diff + confirm) rather than happening implicitly?
- Does a full-replace `replace`/`put` exist at all, given its overwrite risk, or are all updates forced through surgical verbs?
- For the mapping layer's fail-loud guard, what counts as a conflicting existing rule that `map add` must refuse — the same exact column/pattern target, or any overlapping pattern?
- The atomic commit is **validate → stage to temp → batch rename**; the only remaining edge is a crash *between* renames in a large batch (some files renamed, others not) — accept with a clear report, or sequence renames to shrink the window?
- The `--git` capability is owned by a separate shared Feature (see Dependencies). Residual *for this Idea*: must `commit` regenerate + validate the derived copy first so it never commits an inconsistent state (a metadata-specific concern even though the flag is shared)?
- The mapping artifact's on-disk layout and scoping (one per database? per project?) and the exact qualified-key format.
- How is a "currency code" represented — a new `KnownType`, a namePattern-constrained `String`, or `extends` to an ISO currency definition?
- Do recordset-column `Meta` and `Entity.Tables` become **derived** from the authoritative mapping, or **deprecated**?
- Does `datatug validate` itself regenerate (fix) the derived copy, or only detect drift while a separate verb rebuilds it?

---
*This document follows the https://specscore.md/idea-specification*
