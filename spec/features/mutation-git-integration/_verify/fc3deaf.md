```yaml
feature: mutation-git-integration
revision: fc3deaf
verdicts:
  - ac: mutation-git-integration#ac:git-flag-rejects-unknown
    verdict: pass
    justification: "resolveGitMode returns cli.Exit(invalid --git value \"bogus\" (supported: none, stage, commit), 2); TestEntityAdd_GitFlag_RejectsUnknown asserts the message names bogus + the set."
  - ac: mutation-git-integration#ac:git-flag-default-none
    verdict: pass
    justification: "default mode none is a no-op; TestEntityAdd_GitFlag_DefaultNone git-inits a repo, runs add w/o --git, asserts file untracked and HEAD unchanged."
  - ac: mutation-git-integration#ac:git-stage-scoped
    verdict: pass
    justification: "stageFiles stages only written paths via go-git wt.Add (never add -A); test asserts entity file staged while unrelated change stays unstaged."
  - ac: mutation-git-integration#ac:git-partial-stages-written-only
    verdict: pass
    justification: "addEntitiesContinueOnError returns only actually-written paths; stageFiles stages exactly those; test asserts User staged, failed Order untracked."
  - ac: mutation-git-integration#ac:git-commit-not-supported
    verdict: pass
    justification: "resolveGitMode returns cli.Exit(--git=commit is not yet supported, 2) before any write; test asserts the message, no entity written, HEAD unchanged."
  - ac: mutation-git-integration#ac:git-stage-non-repo-failloud
    verdict: pass
    justification: "gitPreflight/openRepo runs before any read/write and returns cli.Exit(... is not a git repository, 2); test asserts the error and that no entities dir was created."
```

# Verify Report — mutation-git-integration @ fc3deaf

**Summary:** 6 passed, 0 failed, 0 unmapped, 0 errored (6 ACs total). The Feature is fully implemented and verified. All six ACs were independently verified by per-AC subagents at this revision.

## AC: git-flag-rejects-unknown

**Verdict:** pass

**Justification:** `resolveGitMode` returns `cli.Exit("invalid --git value \"bogus\" (supported values: none, stage, commit)", 2)` for an unknown value. `TestEntityAdd_GitFlag_RejectsUnknown` asserts the error names `bogus` and the supported set.

**Commits:**
- dc5d2c3 — feat(mutation-git-integration): shared --git flag + value semantics

**Evidence:**
- apps/datatugapp/commands/git_integration.go — resolveGitMode default (unknown) case
- apps/datatugapp/commands/cmd_entity_test.go — TestEntityAdd_GitFlag_RejectsUnknown (PASS)

## AC: git-flag-default-none

**Verdict:** pass

**Justification:** The default mode `none` makes `gitPreflight`/`applyGit` no-ops. `TestEntityAdd_GitFlag_DefaultNone` git-inits a temp repo, runs `entity add` without `--git`, and asserts the entity file is untracked/unstaged with no new commit.

**Commits:**
- dc5d2c3 — feat(mutation-git-integration): shared --git flag + value semantics

**Evidence:**
- apps/datatugapp/commands/git_integration.go — resolveGitMode (empty/none → gitModeNone)
- apps/datatugapp/commands/cmd_entity_test.go — TestEntityAdd_GitFlag_DefaultNone (PASS)

## AC: git-stage-scoped

**Verdict:** pass

**Justification:** `stageFiles` stages only the command's written paths via go-git `Worktree.Add(<relpath>)` — never `add -A` — so unrelated changes are untouched. `TestEntityAdd_GitStage_ScopedToWrittenFiles` asserts the entity file is staged (`A `) while an unrelated modification stays unstaged (` M`).

**Commits:**
- 88e74ee — feat(mutation-git-integration): go-git stage helper (changed-files-only)

**Evidence:**
- apps/datatugapp/commands/git_integration.go — stageFiles (per-path wt.Add)
- apps/datatugapp/commands/cmd_entity_test.go — TestEntityAdd_GitStage_ScopedToWrittenFiles (PASS)

## AC: git-partial-stages-written-only

**Verdict:** pass

**Justification:** `addEntitiesContinueOnError` returns only the paths actually written; the action stages exactly those. `TestEntityAdd_GitStage_PartialStagesWrittenOnly` (User new + Order existing, `--continue-on-error --git=stage`) asserts `User` is staged and the failed `Order` contributes nothing (untracked).

**Commits:**
- 88e74ee — feat(mutation-git-integration): go-git stage helper (changed-files-only)

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go — addEntitiesContinueOnError returns written paths; scoped stageFiles call
- apps/datatugapp/commands/cmd_entity_test.go — TestEntityAdd_GitStage_PartialStagesWrittenOnly (PASS)

## AC: git-commit-not-supported

**Verdict:** pass

**Justification:** `resolveGitMode` returns `cli.Exit("--git=commit is not yet supported", 2)` before any input read or write. `TestEntityAdd_GitFlag_CommitNotSupported` asserts the message, that no entity was written, and that HEAD is unchanged (no commit).

**Commits:**
- dc5d2c3 — feat(mutation-git-integration): shared --git flag + value semantics

**Evidence:**
- apps/datatugapp/commands/git_integration.go — resolveGitMode commit case
- apps/datatugapp/commands/cmd_entity_test.go — TestEntityAdd_GitFlag_CommitNotSupported (PASS)

## AC: git-stage-non-repo-failloud

**Verdict:** pass

**Justification:** The `--git=stage` preflight (`gitPreflight`→`openRepo`) runs before any read/write and returns `cli.Exit("... is not a git repository", 2)` off a git repo, so nothing is written. `TestEntityAdd_GitStage_NonRepoFailLoud` asserts the error and that no `entities/` dir was created.

**Commits:**
- 88e74ee — feat(mutation-git-integration): go-git stage helper (changed-files-only)

**Evidence:**
- apps/datatugapp/commands/git_integration.go — openRepo "not a git repository"
- apps/datatugapp/commands/cmd_entity.go — stage preflight before write
- apps/datatugapp/commands/cmd_entity_test.go — TestEntityAdd_GitStage_NonRepoFailLoud (PASS)
