# Plan: Entity Authoring CLI (cli/entity)

**Status:** Implementing
**Source Feature:** cli/entity
**Date:** 2026-06-04
**Owner:** alex
**Supersedes:** —

## Summary

Decomposes the approved `cli/entity` Feature into 9 linear, AC-mapped tasks implementing the `datatug entity …` verbs — `add`, `field add/set/rm`, `list`, `show` — with create-only/fail-loud guards, batch-first atomic writes, and the shared `--git` flag. All 19 acceptance criteria are covered; none deferred.

## Approach

Build the single-entity `add` path first (create-only guard + canonical JSON storage), then layer input handling (stdin + format resolution) and the batch atomic engine (preflight → stage-to-temp → batch-rename) onto it. Next the surgical field verbs (`add`, `set`, `rm`), then field-type validation, then non-mutating read-back. `--git` integration comes last because it consumes the mutating commands' changed-file sets. Tasks are sized to ~1–3 ACs each and ordered so every task's preconditions are established by an earlier task.

## Tasks

### Task 1: `entity add` — create-only from file + JSON storage

**Status:** done
**Verifies:** cli/entity#ac:add-creates-new, cli/entity#ac:add-rejects-existing, cli/entity#ac:storage-json

Implement `datatug entity add` for a single entity read from `-f <file>`: parse the definition, enforce the create-only guard (fail non-zero if the entity already exists, never overwrite), and persist it as canonical JSON at `entities/<Name>/<Name>.entity.json`.

### Task 2: `entity add` input — stdin + format resolution + empty-input

**Status:** done
**Verifies:** cli/entity#ac:add-reads-stdin, cli/entity#ac:add-empty-errors

Extend `entity add` input handling to read from stdin (`-f -`, or no `-f`), resolve the format as `--format` › file extension › content sniff, and treat empty input as an error that writes nothing.

### Task 3: `entity add` batch + atomic engine + `--continue-on-error`

**Status:** done
**Verifies:** cli/entity#ac:add-batch-atomic-rollback, cli/entity#ac:add-continue-on-error

Add multi-entity batch support with the default atomic commit (preflight → stage-to-temp → batch-rename), per-item result reporting, and non-zero exit on any failure; add the `--continue-on-error` flag for partial apply (write the items that pass, report the rest).

### Task 4: `entity field add` — additive, batch

**Status:** done
**Verifies:** cli/entity#ac:field-add-additive, cli/entity#ac:field-add-rejects-existing, cli/entity#ac:no-implicit-override

Implement `datatug entity field add <Entity>` as additive-only: add one or more new fields, fail non-zero if any named field already exists, and never overwrite existing field content (the no-implicit-override guarantee).

### Task 5: `entity field set` — surgical update incl `--key`

**Status:** done
**Verifies:** cli/entity#ac:field-set-updates, cli/entity#ac:field-set-key-flag, cli/entity#ac:field-set-missing-errors

Implement `datatug entity field set <Entity> <field>` to update an existing field's type, title, and key flag (`--key`), failing non-zero if the field does not exist.

### Task 6: `entity field rm`

**Status:** done
**Verifies:** cli/entity#ac:field-rm-removes

Implement `datatug entity field rm <Entity> <field>` to remove a named field, failing non-zero if the field is absent.

### Task 7: Field-type validation

**Status:** done
**Verifies:** cli/entity#ac:field-type-invalid

Add validation that rejects any field whose type is neither a datatug-core known type nor an `extends:<ref>` reference, surfacing a clear error and writing nothing. The known-type set (including any `currency` type) is owned by datatug-core; this task only enforces the rejection at the CLI boundary.

### Task 8: Read-back — `entity list` + `entity show`

**Verifies:** cli/entity#ac:entity-list-lists, cli/entity#ac:entity-show-renders

Implement the non-mutating read-back verbs: `datatug entity list` (list the project's entities) and `datatug entity show <Entity>` (render the entity's fields plus, when present, the read-only generated copy of its table/column links).

### Task 9: `--git` flag integration (none/stage)

**Verifies:** cli/entity#ac:git-default-none, cli/entity#ac:git-stage-scoped

Wire the shared `--git=<none|stage|commit>` flag (from the `mutation-git-integration` capability) into all mutating `entity` subcommands, acting only on the files the command changed. Verify default `none` (no VCS side effect) and `--git=stage` (stage exactly the written files, leaving unrelated changes alone). `--git=commit` is parser-accepted but not scheduled here, per the Feature.

## Open Questions

- **Dependency risk (Task 9):** Task 9 requires the cross-repo `mutation-git-integration` capability and should be sequenced after it lands. If that capability slips, Task 9 is treated as **blocked on the dependency** rather than reimplemented with a throwaway local helper — its availability is a precondition for Task 9.

---
*This document follows the https://specscore.md/plan-specification*
