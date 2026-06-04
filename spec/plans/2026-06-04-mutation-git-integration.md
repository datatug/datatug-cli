# Plan: Git Integration for Mutating Commands (mutation-git-integration)

**Status:** Implementing
**Source Feature:** mutation-git-integration
**Date:** 2026-06-04
**Owner:** alex
**Supersedes:** —

## Summary

Decomposes the approved `mutation-git-integration` Feature into 2 linear, AC-mapped tasks: the shared `--git` flag with its value semantics, then the go-git stage helper with changed-files-only scoping and fail-loud-off-repo behavior. All 6 acceptance criteria are covered; none deferred.

## Approach

Split along the natural seam between **flag/value handling** (which needs no git operations — `none` does nothing, `commit` reports not-yet-supported, unknown values are rejected) and the **actual go-git staging** (open repo, stage exactly the command's changed files, fail loud before writing when off a git repo). Task 1 lands the shared flag and its non-git value semantics, wired into the `entity` mutators so the behavior is observable. Task 2 layers the go-git helper and the `stage` path on top, including the partial-apply case. Ordering is forced: the staging path (Task 2) consumes the flag established in Task 1. The entity verbs are the representative consumer used to exercise these ACs. **Out of scope for this Plan:** `cli/entity`'s own `--git` finalization across every entity subcommand and its `git-default-none`/`git-stage-scoped` ACs are owned by the `cli/entity` Feature (its Task 9), not this Plan.

## Tasks

### Task 1: Shared `--git` flag + value semantics (none / commit / unknown)

**Status:** done
**Verifies:** mutation-git-integration#ac:git-flag-rejects-unknown, mutation-git-integration#ac:git-commit-not-supported, mutation-git-integration#ac:git-flag-default-none

Add one reusable `urfave/cli/v3` `--git=<none|stage|commit>` flag (default `none`) plus a mode-resolution helper that rejects an unknown value non-zero (naming the value and supported set) and rejects `commit` non-zero as "not yet supported". Wire the flag into the `entity` mutating commands so that `--git=none` (or absent) performs no version-control action — files are written and nothing is staged or committed.

### Task 2: go-git stage helper — changed-files-only, fail-loud off-repo, partial apply

**Verifies:** mutation-git-integration#ac:git-stage-scoped, mutation-git-integration#ac:git-stage-non-repo-failloud, mutation-git-integration#ac:git-partial-stages-written-only

Implement a go-git helper that, given the command's exact changed-file list, stages only those paths on the current branch (never `git add -A`), leaving unrelated staged/unstaged changes untouched. When `--git=stage` (or `commit`) is requested against a non-git directory, fail loud (non-zero, "not a git repository") before any project files are written. Wire `--git=stage` into the entity mutators, ensuring a `--continue-on-error` partial batch stages exactly the files actually written.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
