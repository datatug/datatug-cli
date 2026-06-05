# Design: `go-coverage-100` workflow

**Date:** 2026-06-05
**Status:** Approved (design) — pending implementation plan
**Author:** Alexander Trakhimenok (with Claude)

## 1. Purpose

A **reusable, dynamic workflow** (saved under `.claude/workflows/`) that drives a Go
module's statement coverage toward **100%**, using isolated per-task git worktrees,
a research stage that classifies *why* code is uncovered, optional shared test
helpers, and parallel test-engineer agents — with strict verification and an
auditable report trail.

By default it sweeps the **whole module**. It accepts an optional `args` path /
package-glob filter as an escape hatch to scope a single subtree.

## 2. Goals & Non-Goals

### Goals
- Raise statement coverage to 100% where achievable **without refactoring
  production code beyond seams**.
- Where 100% is *not* achievable without deeper refactoring, **document the gap**
  (grouped by reason + required refactor) rather than force it.
- Produce a per-package `TEST-COVERAGE.md` and a module-root
  `TEST-COVERAGE-OVERVIEW.md`.
- Keep all work isolated and integrated safely via dedicated worktrees and a
  single serialized git authority.

### Non-Goals
- No production-code refactoring beyond **seams** (`var doSomething = func(){}`).
- No attempt to reach branch-level coverage beyond what Go's statement coverage
  (`-coverprofile`) reports. "Branch" in this doc means the uncovered statements
  inside an `if`/`case`/`for`/`select` arm.
- No fixing of pre-existing build/test failures — those packages are quarantined.

## 3. Key Terminology

- **Statement coverage** — what `go test -coverprofile` produces. Go has no native
  *branch* coverage; an "uncovered branch" = the uncovered statements in a control-
  flow arm.
- **Seam** — the only permitted production change: replacing a direct call with an
  overridable package-level variable, e.g. `var now = time.Now`, so tests can swap
  behavior. Seams break `t.Parallel()` safety for tests that mutate them and are
  therefore themselves logged as production changes.
- **Sync branch** — the integration branch (`coverage/100-<stamp>`) all verified
  work merges into.
- **git-orchestrator** — the single agent that is the workflow's git authority.

## 4. Decisions (locked)

| # | Decision | Choice |
|---|----------|--------|
| Scope | Deliverable & target | Reusable saved workflow, **whole-module** default, optional `args` path filter |
| Researcher fan-out | Batching | **Per-batch**, sized by uncovered-function budget |
| Taxonomy | "Why uncovered" types | **Seed + allow extension** |
| Pre-flight | Non-clean packages | **Quarantine + report** (not counted against 100%) |
| Verification | Rigor | **Strict loop + meaningfulness check**, `K=2` retries |
| Models | Per role | **Opus** researcher + helper-synthesizer; **Sonnet** test-engineers / verifiers / test-infra builder; **Haiku** git-orchestrator |
| Helper phase | Shared test helpers | **Yes, threshold ≥3 packages**; prefer extending existing helpers |
| Git authority (B) | Who runs git | **git-orchestrator agent only**, invoked serially; other agents never touch git |
| Audit (C) | Seam logging + retries | Seam additions logged in `TEST-COVERAGE.md`; `K=2` verify retries |
| Worktrees | Per test-engineer | **Dedicated worktree per engineer**, created **just-in-time** at dispatch (off the post-helper sync branch) |

### 4.1 Constraint: workflow scripts cannot run git/bash

A Workflow-tool script can only spawn agents — it cannot run `git`, bash, access
the filesystem, or use `Date`. Therefore "the main agent is responsible for git" is
realized as a **single dedicated `git-orchestrator` agent** that the script invokes
with **sequential `await`s** for every worktree create / commit / merge / delete.
This keeps git serialized through one authority while remaining a saved, reusable,
deterministic workflow. Phase-0 worktree+branch setup is performed by the **main
session before invoking the workflow**, and the worktree path is injected via `args`.

## 5. Architecture / Pipeline

```
Phase 0  Setup            (main session, before Workflow runs)
Phase 1  Collect          (deterministic)
Phase 2  Research         (opus, per-batch)
Phase 3  Helper synthesis (BARRIER — opus synth + sonnet builder, own worktree)
Phase 4  Test-engineers   (sonnet, parallel, dedicated JIT worktrees)
Phase 5  Verify           (sonnet, loop K=2, pipelined; merge-on-green)
Phase 6  Overview         (main session)
```

git-orchestrator (haiku) serializes ALL worktree create/commit/merge/delete across
phases 3–5.

### Phase 0 — Setup (main session, before the workflow)
- Create worktree + sync branch `coverage/100-<stamp>` off the target commit.
- Invoke the workflow with `args = { worktreePath, syncBranch, pathFilter? }`.
- Every spawned agent is told to operate inside the relevant worktree path.

### Phase 1 — Collect (deterministic, minimal/no agent tokens)
- In the worktree: `go build ./...` and `go test ./... -coverprofile=cover.out
  -covermode=atomic` (scoped by `pathFilter` when provided).
- **Quarantine** packages that: don't build, already fail tests, or are generated /
  vendored / `testdata`. Recorded as *skipped-with-reason*; excluded from the 100%
  target.
- Parse `cover.out` mechanically into the skeleton:
  `package → file → function → uncovered line ranges`.
- Drop packages already at 100%.
- **Rationale:** the structural breakdown is mechanical, so it consumes no agent
  tokens; agents only add the semantic layer.

### Phase 2 — Research (opus, per-batch)
- Group uncovered packages into batches sized by uncovered-function budget:
  `researchers = ceil(total_uncovered_funcs / B)`, `B ≈ 25`, clamped, biased so
  same-top-level-dir packages stay in one batch (cohesion).
- Each researcher receives its batch's skeleton + source read access, and adds the
  **semantic layer only**: classify each uncovered region into the seeded,
  extensible taxonomy and suggest 1+ approaches (simple test / seam / mock / fixture / etc.).
  - **Seed taxonomy:** error-path, OS/env-dependent, time/random, defensive/
    unreachable, concurrency, external-IO, generated-code (extensible).
- Main session merges researcher outputs into the consolidated **researcher report**:
  - **By type:** each type → nature → suggested approaches → packages → functions
    (with source file).
  - **By package:** files → functions → uncovered branches with the why-type.

### Phase 3 — Helper synthesis (BARRIER)
- **Synthesizer (opus)** reads the *merged* researcher findings, identifies
  approaches recurring across **≥3 packages**, and **first checks for existing
  helpers in the module to extend** before proposing new ones. Emits a helper plan.
- **Test-infra builder (sonnet)** implements approved shared helpers in a dedicated
  support package (e.g. `internal/testutil`) **in its own worktree**.
- Verified → **git-orchestrator merges into the sync branch first**, so every
  test-engineer worktree created afterward already contains the helpers.
- This is a genuine cross-item dependency, so the barrier here is correct.

### Phase 4 — Test-engineers (sonnet, parallel, dedicated worktrees)
- **Partition computed up front** (which packages → which engineer); physical
  `git worktree add` is **just-in-time** at dispatch, off the post-helper sync
  branch (git-orchestrator performs it). Live worktrees are capped by the
  concurrency limit and deleted right after merge.
- Each engineer's input: its package(s) + the researcher's per-region type+approach
  annotations + helper hints (e.g. "time-dependent → `testutil.FakeClock`") + the
  seam policy + `TEST-COVERAGE.md` rules.
- **Target 100%.** Only permitted production change = **seams**. Anything requiring
  deeper refactoring → appended to that package's `TEST-COVERAGE.md`, grouped by
  why-type, with the required refactoring explained.
- **Engineers write files and run `go test` only — they never run git.**
- Dedicated worktrees because seam edits can transiently break the build; isolation
  prevents one engineer's broken intermediate state from polluting another's
  `go test`.

### Phase 5 — Verify (sonnet, strict loop, pipelined)
- As each package finishes, **inside its own worktree**: re-run
  `go test -cover ./pkg`; require **build-green** AND coverage == 100% **or**
  remainder fully documented in `TEST-COVERAGE.md`. On shortfall, loop back to the
  engineer up to **K=2** times.
- **Meaningfulness check** (adversarial verifier): confirms tests assert real
  behavior, not coverage-padding (no empty asserts, no `_ = result`).
- On green: **git-orchestrator commits the worktree changes to a per-engineer
  branch, merges into the sync branch (serialized), then deletes the worktree.**
  Disjoint packages → merges are conflict-free except rare shared files
  (`go.mod`/`go.sum`), handled one at a time.

### Phase 6 — Overview (main session)
- Read all created/updated `TEST-COVERAGE.md` + verification results.
- Write `TEST-COVERAGE-OVERVIEW.md` at the module root:
  - Global before/after coverage %.
  - Per-package status (100% / documented-gap / quarantined-with-reason).
  - Quarantine list.
  - **Taxonomy roll-up:** count of each remaining why-type + the refactoring each
    would need.

## 6. git-orchestrator (haiku) contract

- Sole git authority; invoked **serially** by the script.
- Operations: `worktree add`, `add`/`commit`, `merge` into sync branch,
  `worktree remove`, branch cleanup.
- Prompts are **prescriptive exact-command scripts**.
- **Guardrail:** on any anomaly (merge conflict, non-clean `git status`, non-zero
  exit) it **aborts and reports** rather than improvising; the script flags/
  escalates that single package instead of letting it freelance on the integration
  branch.

## 7. Report formats

### Per-package `TEST-COVERAGE.md` (written by test-engineers)
- **Grouped by why-type.** For each: the uncovered functions/branches, why they
  can't be tested as-is, and the kind of refactoring required.
- A **Seams added** section listing every seam introduced (audit trail for
  production changes).

### Root `TEST-COVERAGE-OVERVIEW.md` (written by main session)
- Global before/after %, per-package status, quarantine list, taxonomy roll-up.

## 8. Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| Whole-module sweep = hundreds of agents, high token/time cost | Optional `args` path filter; concurrency cap; mechanical structural parsing saves tokens |
| Disk blowup from many full-checkout worktrees | JIT worktree creation capped at concurrency limit; delete-after-merge |
| Engineers' seam edits break the build | Dedicated per-engineer worktrees; verify-before-merge |
| Coverage-padding (tests that don't assert) | Adversarial meaningfulness check in Phase 5 |
| Integration branch corruption by weak git model | Haiku git-orchestrator with abort-on-anomaly guardrail + serialized merges |
| Helpers not visible to engineers | Helper merged into sync branch *before* engineer worktrees are created |
| Pre-existing broken packages waste agents | Phase-1 quarantine |

## 9. Open items for the implementation plan
- Exact coverprofile parsing approach (stdlib `go tool cover -func` + profile line
  parsing) and the structural-skeleton data shape.
- The `StructuredOutput` schemas for researcher, synthesizer, engineer, and verifier
  agent returns.
- Batch bin-packing heuristic specifics (budget `B`, dir-cohesion tie-breaking).
- `<stamp>` derivation (passed via `args` since `Date` is unavailable in scripts).
- Escalation path when git-orchestrator aborts on a conflict.
