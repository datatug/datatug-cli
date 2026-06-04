# Idea: Git Integration for Mutating Commands

**Status:** Specified
**Date:** 2026-06-04
**Owner:** alex
**Promotes To:** mutation-git-integration
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we give any datatug command that writes project files a single, consistent way to optionally stage or commit **exactly the files it changed** — so each command doesn't reinvent git handling?

## Context

DataTug projects are git-backed: the project is versioned JSON (and the demo project even ships `.git-hooks/pre-commit`). Several commands mutate that tree — `scan` ingests live-DB schema, and the proposed semantic-metadata verbs (`semantic-metadata-cli`) author entities, fields, and mapping rules — and more mutators will follow. Each would otherwise hand-roll "write, then optionally `git add`/`git commit`," drifting in flags, scoping, and behavior.

This Idea was split out while ideating `semantic-metadata-cli`: a `--git` flag was recognized there as a **cross-command concern**, not a semantic-metadata one. `scan` wants the identical capability. Rather than bake it into one command's Idea, it is captured here as the shared capability all mutators consume.

## Recommended Direction

Build one **shared git-integration capability** — a common `--git=<none|stage|commit>` flag (default `none`) plus a small internal helper — that any mutating command opts into. The command declares the **exact set of files it created or changed**; the helper then, depending on the flag, stages or commits **only those files** (never `git add -A`), leaving the rest of the working tree untouched. `commit` uses an explicit `-m <message>` or an auto-generated summary the command supplies ("scan: 4 tables updated"; "add 3 entities, 12 mapping rules"). On a project that is not a git repo, `stage`/`commit` **fail loudly**; `none` is always valid. When a command writes only a subset of its intended changes (e.g. a `--continue-on-error` partial batch in `semantic-metadata-cli`), the helper acts on exactly the files actually written — nothing more.

The default is deliberately `none`: writing files is a command's job, touching version control is an explicit choice — many flows want to review the diff first, and AI/automation should commit only when told. Because mutators already know their changed-file set (e.g. the temp+rename atomic-commit design in `semantic-metadata-cli` yields it precisely), the helper needs no working-tree diffing; it acts on a known path list. Implemented once as a Go helper + a shared `urfave/cli` flag, it is consumed by `scan`, the semantic-metadata verbs, and every future mutator — one behavior, one UX, one place to fix bugs. The helper is built on **go-git**. The flag exposes `none|stage|commit` from the start, but **`stage` is the MVP write path**; `commit` (message conventions and git-hook handling) is a deferred follow-up, so until then `--git=commit` is accepted by the parser but reports that it is not yet supported.

We keep it to **local stage/commit on the current branch**: no push, no branch management, no remote operations. Commits go through normal git, so configured hooks run and the repo's own author/identity is used — no bespoke signing or author handling.

## Alternatives Considered

- **Per-command bespoke git handling.** Each mutator implements its own `git add`/commit. Rejected: guaranteed drift in flag names, scoping, and edge-case behavior, plus duplicated bugs — the opposite of one consistent contract.
- **Always auto-commit (no flag, no opt-out).** Rejected: surprising and destructive to review workflows; an automated `scan` would litter history and could commit half-considered changes. Opt-in `none` default is safer.
- **A standalone `datatug git add/commit` the user runs afterwards.** Useful as a possible complement, but as the *only* mechanism it loses the one-call "write-and-commit" ergonomics and, crucially, the exact changed-files scoping (a follow-up `git add -A` would sweep unrelated work). The flag is the ergonomic win, especially for AI/tools.
- **`git add -A` / commit-everything scoping.** Rejected outright: it captures unrelated working-tree changes, violating the surgical guarantee every datatug mutator relies on.

## MVP Scope

A focused spike: the shared `--git` flag + helper (built on **go-git**), wired into **at least one real mutator** end-to-end (the metadata `entity add`, or `scan`), supporting **`none|stage`** (the `commit` value is defined but reports not-yet-supported in MVP), with **changed-files-only** scoping and fail-loud behavior off a git repo — demonstrated on a git-backed demo project. Success = a second command can adopt `--git` by handing the helper its changed-file list, with zero command-specific git code.

## Not Doing (and Why)

- Push / pull / any remote operation — local stage and commit only.
- Branch creation or switching — operates on the current branch as-is.
- Merge or conflict resolution — out of scope.
- Commit signing, author override, or identity management — use the repo's git config.
- A full standalone `datatug git` command surface — the shared flag is the deliverable; a separate command can come later if wanted.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Mutating commands can reliably report the exact set of files they created/changed. | Confirm against the `semantic-metadata-cli` temp+rename design and `scan`'s write path; both should already know their touched paths. |
| Must-be-true | Staging a specific file subset (via **go-git**) without disturbing the rest of the working tree is straightforward. | Prototype `--git=stage` with go-git; confirm unrelated staged/unstaged changes are left untouched. |
| Should-be-true | An auto-generated commit message is good enough for the common case, with `-m` as the override. | Draft a message convention; review summaries for scan and metadata batches. |
| Should-be-true | (Post-MVP, when `commit` lands) running configured git hooks on commit is acceptable behavior. | Test against the demo project's `.git-hooks/pre-commit`; decide what happens if a hook fails. |
| Might-be-true | A standalone `datatug git commit` would later complement the flag for human-driven flows. | Defer; gauge demand after the flag ships. |

## SpecScore Integration

- **New Features this would create:** the shared **git-integration capability** (the `--git` flag + helper) consumed by mutating commands.
- **Existing Features affected:** `scan` (gains `--git`); the proposed semantic-metadata verbs; any future command that writes project files.
- **Dependencies:** none hard. It is *consumed by* `semantic-metadata-cli` (which records this as a dependency); the two are siblings.

## Open Questions

- Deferred until `--git=commit` is implemented (post-MVP): the commit-message format/convention and how a command supplies its summary; whether commit runs configured git hooks (e.g. the demo's `.git-hooks/pre-commit`); and what happens if a hook fails (abort the command vs. leave changes staged).

_Resolved during ideation:_ implementation uses **go-git** (not shelling to `git`); MVP ships `none|stage` only; a partial `--continue-on-error` batch stages exactly the files actually written; a standalone `datatug git commit` command is **not** pursued (a new proposal, not existing in code or specs — see Not Doing).

---
*This document follows the https://specscore.md/idea-specification*
