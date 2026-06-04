# Feature: Git Integration for Mutating Commands

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/mutation-git-integration?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/mutation-git-integration?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/mutation-git-integration?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/mutation-git-integration?op=request-change) |
**Status:** Implementing
**Date:** 2026-06-04
**Owner:** alex
**Source Ideas:** mutation-git-integration
**Supersedes:** —
**Grade:** A

## Summary

A single, shared `--git=<none|stage|commit>` flag plus a small go-git helper that any DataTug command which writes project files can opt into — staging (or, later, committing) **exactly the files that command changed**, so each mutator doesn't reinvent git handling.

## Problem

DataTug projects are git-backed versioned JSON. Several commands mutate that tree (`scan`, the new `entity` authoring verbs) and more will follow. Without a shared capability each would hand-roll "write, then optionally `git add`/`git commit`", drifting in flag names, scoping, and edge-case behavior. This Feature provides the one consistent contract all mutators consume; it is the capability that `cli/entity`'s `--git` requirement depends on.

## Behavior

### The shared `--git` flag

#### REQ: git-flag-surface

Every mutating command MUST expose a shared `--git=<none|stage|commit>` flag with a default of `none`. An unrecognized value MUST cause the command to exit non-zero, naming the invalid value and the supported set.

#### REQ: git-none-noop

With `--git=none` (or no `--git` flag), the command MUST perform no version-control action — it writes its files and nothing else. `none` MUST be valid even when the project is not a git repository.

### Stage scoping

#### REQ: git-stage-changed-only

With `--git=stage`, the helper MUST stage exactly the set of files the command created or changed — never `git add -A`. Unrelated working-tree changes (whether already staged or unstaged) MUST be left untouched.

#### REQ: git-partial-apply

When a command writes only a subset of its intended changes (for example a `--continue-on-error` partial batch), `--git=stage` MUST act on exactly the files actually written — failed items contribute nothing to the index.

### Deferred behavior and failure modes

#### REQ: git-commit-deferred

`--git=commit` MUST be accepted by the parser but, in this MVP, MUST fail loud: it reports that `commit` is not yet supported, exits non-zero, and creates no commit. (Commit-message conventions and git-hook handling are a deferred follow-up.)

#### REQ: git-requires-repo

When `--git=stage` or `--git=commit` is requested against a directory that is not a git repository, the command MUST fail loud — exit non-zero with a clear "not a git repository" error — and MUST do so before writing any project files, so the run leaves the tree unchanged. `--git=none` is unaffected.

## Architecture & components

- **Shared flag.** One `urfave/cli/v3` `--git` string flag definition, reused by every mutating command rather than redefined per command.
- **Helper (go-git).** A small internal helper built on `go-git/go-git/v5`: open the repo with `PlainOpen`, obtain the worktree, and for each command-supplied changed path call `Worktree.Add(<repo-relative path>)`. The command supplies its exact changed-file list (the `entity` atomic engine already yields it precisely), so the helper needs no working-tree diffing.
- **Consumption.** A command adopts the capability by (a) registering the shared flag and (b) handing the helper its changed-file list after a successful write — no per-command git code. `cli/entity` is the first consumer (`entity add`, `field add/set/rm`); `scan` is the next expected adopter.
- **Scope guarantee.** All operations are local, on the current branch: no push, no remote, no branch creation/switching.

## Acceptance Criteria

### AC: git-flag-rejects-unknown (verifies REQ:git-flag-surface)

**Given** a git-backed project
**When** I run a mutating command with `--git=bogus`
**Then** the command exits non-zero, naming the invalid value `bogus` and the supported values `none`, `stage`, `commit`

### AC: git-flag-default-none (verifies REQ:git-none-noop)

**Given** a git-backed project with a clean working tree
**When** I run a mutating command that writes files, with no `--git` flag
**Then** the written files appear as untracked/unstaged (not added to the index) and no new commit is created

### AC: git-stage-scoped (verifies REQ:git-stage-changed-only)

**Given** a git-backed project with an unrelated unstaged change to an existing file
**When** I run a mutating command with `--git=stage`
**Then** exactly the files the command wrote are staged, and the unrelated change remains unstaged

### AC: git-partial-stages-written-only (verifies REQ:git-partial-apply)

**Given** a batch with one failing item and one succeeding item, run with `--continue-on-error`
**When** I run a mutating command with `--git=stage`
**Then** only the file for the successful item is staged, and the failed item contributes nothing to the index

### AC: git-commit-not-supported (verifies REQ:git-commit-deferred)

**Given** a git-backed project
**When** I run a mutating command with `--git=commit`
**Then** the command exits non-zero reporting that `commit` is not yet supported, and no new commit is created

### AC: git-stage-non-repo-failloud (verifies REQ:git-requires-repo)

**Given** a directory that is not a git repository
**When** I run a mutating command with `--git=stage`
**Then** the command exits non-zero with a clear "not a git repository" error, and no project files are written

## Rehearse Integration

Every AC above is CLI-observable (exit code, stderr message, git index / commit state, on-disk files), so all are testable. Rehearse stub scaffolding is deferred to the Plan phase, where each AC becomes a concrete test scenario alongside its task.

## Out of Scope

- Push / pull / any remote operation — local stage (and later commit) only.
- Branch creation or switching — operates on the current branch as-is.
- The `commit` phase itself — message conventions, auto-summaries, and git-hook handling are a deferred follow-up; MVP only parser-accepts `commit` and reports not-yet-supported.
- A standalone `datatug git` command surface — the shared flag is the deliverable.
- Commit signing, author override, or identity management.

## Open Questions

- (Deferred to the `commit` phase) The commit-message format/convention and how a command supplies its auto-summary; whether `commit` runs configured git hooks (e.g. the demo's `.git-hooks/pre-commit`) and what happens if a hook fails.

---
*This document follows the https://specscore.md/feature-specification*
