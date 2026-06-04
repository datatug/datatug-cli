# Feature: Entity Authoring CLI

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/entity?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/entity?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/entity?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/entity?op=request-change) |

**Status:** Approved
**Date:** 2026-06-04
**Owner:** alex
**Source Ideas:** semantic-metadata-cli
**Supersedes:** —
**Grade:** A

## Summary

`datatug entity …` verbs to author and read entities and their fields — the foundation the DataTug metadata skills wrap. Authoring is strongly guarded (no silent overwrite) and batch-first.

## Problem

DataTug projects already model entities (`entities/<Name>/<Name>.entity.json`), but there is no CLI surface to author them: `scan` only ingests live-DB schema, and `show`/`validate` only read. So the only way to define or refine an entity is hand-editing JSON. This Feature adds the authoring and read-back verbs the metadata skills need — without letting an automated caller silently clobber months of curated metadata.

## Behavior

### Entity creation

`datatug entity add` is the only way to create entities, and it is deliberately create-only.

#### REQ: add-create-only

`datatug entity add` MUST create one or more entities from a definition document. It MUST fail with a non-zero exit if any named entity already exists, and MUST NOT overwrite or modify an existing entity. There is no `apply`/upsert and no whole-entity override.

#### REQ: add-input

`entity add` MUST accept a definition as YAML or JSON, read from a file (`-f <path>`) or from stdin (`-f -`, or no `-f` flag). Input format MUST be resolved as: explicit `--format=<yaml|json>` if given, else the file extension, else by sniffing the content (required for stdin). Empty input MUST be an error.

#### REQ: add-batch-atomic

`entity add` MUST accept multiple entities in a single invocation. By default the batch MUST be atomic, applied as **preflight → stage-to-temp → batch-rename**: validate every item first, write all results to temp files, then rename into place — so a conflict or validation failure leaves the project unchanged. The command MUST report per-item results and exit non-zero if any item failed. A `--continue-on-error` flag MUST switch to partial apply: write the items that pass, report the rest, and still exit non-zero if any failed.

#### REQ: storage-json

Regardless of input format, entities MUST be persisted as canonical JSON at `entities/<Name>/<Name>.entity.json`.

### Field authoring

Changes to an existing entity go only through explicit, scoped field verbs.

#### REQ: field-add-additive

`datatug entity field add <Entity>` MUST add one or more new fields to an existing entity. It MUST be additive only: it MUST fail if any named field already exists, and MUST NOT modify existing fields. It MUST support adding several fields in one invocation, atomically (per `add-batch-atomic`).

#### REQ: field-set-surgical

`datatug entity field set <Entity> <field>` MUST update the attributes (type, title, key flag) of a single existing field. It MUST fail if the field does not exist.

#### REQ: field-rm-explicit

`datatug entity field rm <Entity> <field>` MUST remove a named field. It MUST fail if the field does not exist. Removal is always explicit.

#### REQ: field-type-validation

A field's type MUST be a recognized DataTug known type or an `extends:<ref>` reference. The CLI MUST reject a field whose type is neither. The set of known types (including any `currency` type) is owned by datatug-core; this Feature only surfaces the rejection.

#### REQ: no-override

No `entity` verb MUST overwrite a whole existing entity, and batch *mutation* of existing fields MUST NOT be offered. Existing content changes only through the surgical field verbs above; an additive path that would collide with existing content MUST fail rather than overwrite.

### Read-back

#### REQ: entity-list

`datatug entity list` MUST list the entities defined in the project and MUST NOT mutate the project.

#### REQ: entity-show

`datatug entity show <Entity>` MUST render the entity's fields and, when present, the read-only generated copy of its table/column links. It MUST NOT mutate the project. Full mapping resolution with precedence is provided separately by the `cli/map` Feature's `map resolve`.

### Cross-cutting

#### REQ: git-flag

Every mutating `entity` subcommand (`add`, `field add`, `field set`, `field rm`) MUST accept the shared `--git=<none|stage|commit>` flag (default `none`), acting only on the files the command changed. This capability is provided by the cross-repo `mutation-git-integration` work; this Feature consumes it rather than defining it. Per that capability's MVP, only `none` and `stage` are implemented end-to-end; `--git=commit` is parser-accepted but reports not-yet-supported until the capability's commit phase lands — so a Plan MUST NOT schedule `commit` behavior here.

## Acceptance Criteria

### AC: add-creates-new (verifies REQ:add-create-only)

**Given** a project with no `User` entity
**When** I run `datatug entity add` with a document defining entity `User`
**Then** a `User` entity is created and the command exits 0

### AC: add-rejects-existing (verifies REQ:add-create-only)

**Given** a project that already has a `User` entity
**When** I run `datatug entity add` with a document defining entity `User`
**Then** the command exits non-zero, reports that `User` already exists, and the existing `User` entity is left unchanged

### AC: add-reads-stdin (verifies REQ:add-input)

**Given** a YAML entity definition piped on stdin
**When** I run `datatug entity add` with no `-f` flag
**Then** the definition is read from stdin and the entity is created

### AC: add-empty-errors (verifies REQ:add-input)

**Given** empty stdin
**When** I run `datatug entity add`
**Then** the command exits non-zero with an "empty input" error and writes nothing

### AC: add-batch-atomic-rollback (verifies REQ:add-batch-atomic)

**Given** a batch document defining `User` (new) and `Order` (which already exists)
**When** I run `datatug entity add` in the default atomic mode
**Then** the command exits non-zero, neither `User` nor `Order` is written, and the per-item report flags `Order` as the conflict

### AC: add-continue-on-error (verifies REQ:add-batch-atomic)

**Given** the same batch (`User` new, `Order` existing) and the `--continue-on-error` flag
**When** I run `datatug entity add --continue-on-error`
**Then** `User` is created, `Order` is reported as failed, and the command exits non-zero

### AC: storage-json (verifies REQ:storage-json)

**Given** a YAML definition for entity `User`
**When** I add it
**Then** the file `entities/User/User.entity.json` exists and contains valid JSON

### AC: field-add-additive (verifies REQ:field-add-additive)

**Given** entity `User` with field `id`
**When** I run `datatug entity field add User` adding a new field `primaryCurrency`
**Then** `primaryCurrency` is added and `id` is unchanged

### AC: field-add-rejects-existing (verifies REQ:field-add-additive)

**Given** entity `User` with field `id`
**When** I run `datatug entity field add User` adding a field named `id`
**Then** the command exits non-zero and `User` is left unchanged

### AC: field-set-updates (verifies REQ:field-set-surgical)

**Given** entity `User` with field `primaryCurrency` of type `string`
**When** I run `datatug entity field set User primaryCurrency --type currency`
**Then** the field's type becomes `currency` and the command exits 0

### AC: field-set-missing-errors (verifies REQ:field-set-surgical)

**Given** entity `User` with no field named `xyz`
**When** I run `datatug entity field set User xyz --title X`
**Then** the command exits non-zero and `User` is unchanged

### AC: field-set-key-flag (verifies REQ:field-set-surgical)

**Given** entity `User` with a non-key field `email`
**When** I run `datatug entity field set User email --key`
**Then** `email` becomes a key field and the command exits 0

### AC: field-rm-removes (verifies REQ:field-rm-explicit)

**Given** entity `User` with field `tmp`
**When** I run `datatug entity field rm User tmp`
**Then** `tmp` is removed; running the same command again exits non-zero because the field is absent

### AC: field-type-invalid (verifies REQ:field-type-validation)

**Given** an entity-authoring input with a field whose type is `not-a-type`
**When** the command runs
**Then** it exits non-zero reporting an unknown field type and writes nothing

### AC: no-implicit-override (verifies REQ:no-override)

**Given** entity `User` with field `id` typed `integer`
**When** I run `datatug entity field add User` including a field `id` typed `string`
**Then** the command exits non-zero and `id` remains typed `integer` (existing content is never overwritten implicitly)

### AC: entity-list-lists (verifies REQ:entity-list)

**Given** a project with entities `User` and `Order`
**When** I run `datatug entity list`
**Then** both `User` and `Order` appear in the output

### AC: entity-show-renders (verifies REQ:entity-show)

**Given** entity `User` with fields and a generated mapping copy present
**When** I run `datatug entity show User`
**Then** the output shows `User`'s fields and the read-only mapping copy, and the project is unchanged

### AC: git-default-none (verifies REQ:git-flag)

**Given** a git-backed project
**When** I run `datatug entity add …` without a `--git` flag
**Then** the entity files are written but nothing is staged or committed

### AC: git-stage-scoped (verifies REQ:git-flag)

**Given** a git-backed project with an unrelated unstaged change
**When** I run `datatug entity add … --git=stage`
**Then** exactly the entity files the command wrote are staged, and the unrelated change is left unstaged

## Rehearse Integration

Every AC above is CLI-observable (exit code, stdout, and on-disk files), so all are testable. Rehearse stub scaffolding is deferred to the Plan phase, where each AC becomes a concrete test scenario alongside its task — recorded here rather than scaffolding ~18 stub files at spec time.

## Open Questions

- The exact `entity show` output rendering (YAML vs. grid) and how much of the generated mapping copy to display.
- Whether `--format` also governs read-back echo format (default YAML).
- The concrete set of known field types — including whether `currency` is a new known type or an `extends:` reference — is resolved in the datatug-core dependency, not here.

## Sidekick Seeds Generated

- [module-system-with-a-modules-registry-and-module-qualified](../../../ideas/seeds/module-system-with-a-modules-registry-and-module-qualified.md) — captured 2026-06-04 by specstudio:implement

---
*This document follows the https://specscore.md/feature-specification*
