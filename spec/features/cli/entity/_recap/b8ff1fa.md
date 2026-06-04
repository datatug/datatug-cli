```yaml
feature: cli/entity
revision: b8ff1fa
verify_revision: 1b0448e
drift:
  - ac: cli/entity#ac:add-creates-new
    verdict: no-drift
    narrative: "entity add creates entities/User/User.entity.json and exits 0 when no prior entity exists; covered by TestEntityAdd_CreatesNew_StorageJSON"
  - ac: cli/entity#ac:add-rejects-existing
    verdict: no-drift
    narrative: "entity add on an existing User exits non-zero reporting already-exists and leaves it unchanged; even a corrupt file blocks creation"
  - ac: cli/entity#ac:add-reads-stdin
    verdict: no-drift
    narrative: "entity add reads the definition from stdin when -f is absent or dash and creates the entity; TestEntityAdd_ReadsStdin covers it"
  - ac: cli/entity#ac:add-empty-errors
    verdict: no-drift
    narrative: "empty/whitespace stdin exits non-zero with an empty-input error before any write; no entities dir created"
  - ac: cli/entity#ac:add-batch-atomic-rollback
    verdict: no-drift
    narrative: "atomic mode preflights, on any conflict writes nothing, flags Order as the conflict, and exits non-zero"
  - ac: cli/entity#ac:add-continue-on-error
    verdict: no-drift
    narrative: "--continue-on-error writes the new User, reports Order failed, and exits non-zero"
  - ac: cli/entity#ac:storage-json
    verdict: no-drift
    narrative: "entity add writes canonical JSON to entities/User/User.entity.json; test asserts the path exists and json.Valid is true"
  - ac: cli/entity#ac:field-add-additive
    verdict: no-drift
    narrative: "field add appends primaryCurrency and leaves existing id untouched"
  - ac: cli/entity#ac:field-add-rejects-existing
    verdict: no-drift
    narrative: "field add of an existing field id collides, writes nothing, and exits non-zero; entity byte-identical before/after"
  - ac: cli/entity#ac:field-set-updates
    verdict: no-drift
    narrative: "field set --type currency sets the field type to currency and exits 0; no type validation rejects it at this commit"
  - ac: cli/entity#ac:field-set-missing-errors
    verdict: no-drift
    narrative: "field set on a missing field exits non-zero before any write, leaving the entity unchanged"
  - ac: cli/entity#ac:field-set-key-flag
    verdict: no-drift
    narrative: "field set --key sets IsKeyField true and exits 0"
  - ac: cli/entity#ac:field-rm-removes
    verdict: no-drift
    narrative: "field rm removes the field; a second run on the now-absent field exits non-zero"
  - ac: cli/entity#ac:field-type-invalid
    verdict: no-drift
    narrative: "an unknown field type not-a-type exits non-zero reporting unknown field type and writes nothing; validation runs before any write"
  - ac: cli/entity#ac:no-implicit-override
    verdict: no-drift
    narrative: "field add collides on field ID, so id:string against existing id:integer is rejected pre-write and id stays integer"
  - ac: cli/entity#ac:entity-list-lists
    verdict: no-drift
    narrative: "entity list prints all project entities; test adds User and Order and asserts both appear"
  - ac: cli/entity#ac:entity-show-renders
    verdict: no-drift
    narrative: "entity show renders fields plus the read-only generated mapping copy to stdout, leaving the on-disk file byte-identical"
  - ac: cli/entity#ac:git-default-none
    verdict: no-drift
    narrative: "with --git absent the mode is none; gitPreflight and applyGit are no-ops, files written, nothing staged or committed"
  - ac: cli/entity#ac:git-stage-scoped
    verdict: no-drift
    narrative: "stage mode stages exactly the written entity files via per-file add and leaves an unrelated change unstaged"
```

# Recap — cli/entity @ b8ff1fa

Per-AC drift report comparing the `cli/entity` Feature's acceptance criteria against the commits that delivered them (`Verifies:` trailers), built on the verify report at `_verify/1b0448e.md` (19/19 pass). All 19 ACs map to exactly one focused commit; every drift verdict is **no-drift**. Exit 0 (no contradictions, no errors).

## AC: add-creates-new

**Drift verdict:** no-drift

**Narrative:** `entity add -f` with a doc whose `id` is `User` creates `entities/User/User.entity.json` and returns nil (exit 0) when no prior entity exists — exactly the AC's given/when/then, covered by passing test TestEntityAdd_CreatesNew_StorageJSON.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 6ad2122 — feat(cli/entity): add create-only `entity add` with nested JSON storage

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go (create path, parseEntityDoc)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityAdd_CreatesNew_StorageJSON

## AC: add-rejects-existing

**Drift verdict:** no-drift

**Narrative:** `entity add` on an existing `User` returns a non-nil error containing "User" and "already exists" (non-zero exit) and writes nothing, leaving the existing entity untouched; even a corrupt existing file blocks creation — exactly the AC's three Then clauses.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 6ad2122 — feat(cli/entity): add create-only `entity add` with nested JSON storage

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go (entityExists guard; atomic-mode already-exists path)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityAdd_RejectsExisting / _CorruptFile

## AC: add-reads-stdin

**Drift verdict:** no-drift

**Narrative:** `entity add` reads the definition from stdin when `-f` is absent or `-` (via root.Reader/os.Stdin) and creates the entity; YAML stdin path is exercised by TestEntityAdd_ReadsStdin with no `-f`, asserting the entity file is written.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- c4d4f60 — feat(cli/entity): read `entity add` input from stdin + --format + empty-input guard

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go (fromStdin := filePath == "" || filePath == "-")
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityAdd_ReadsStdin

## AC: add-empty-errors

**Drift verdict:** no-drift

**Narrative:** Empty/whitespace-only stdin returns cli.Exit("empty input from stdin", 2) before any parse or write; test confirms non-zero error and that no entities dir is created, exactly matching the AC.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- c4d4f60 — feat(cli/entity): read `entity add` input from stdin + --format + empty-input guard

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go (TrimSpace length guard → cli.Exit(..., 2))
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityAdd_EmptyStdin_Errors

## AC: add-batch-atomic-rollback

**Drift verdict:** no-drift

**Narrative:** Atomic mode preflights all items and on any conflict writes nothing, reports "failed: Order (already exists)", and returns an error (non-zero exit); test TestEntityAdd_BatchAtomicRollback asserts User not written, Order unchanged, report flags Order — exactly the AC.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 27fbf10 — feat(cli/entity): batch `entity add` with atomic commit + --continue-on-error

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::addEntitiesAtomic (preflight loop; failure guard skips atomicWriteFiles)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityAdd_BatchAtomicRollback

## AC: add-continue-on-error

**Drift verdict:** no-drift

**Narrative:** --continue-on-error path (addEntitiesContinueOnError) writes the new User, prints "failed: Order (already exists)", and returns a non-zero error on any failure; test TestEntityAdd_BatchContinueOnError exercises the exact User-new/Order-existing batch and asserts all three outcomes.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 27fbf10 — feat(cli/entity): batch `entity add` with atomic commit + --continue-on-error

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::addEntitiesContinueOnError
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityAdd_BatchContinueOnError

## AC: storage-json

**Drift verdict:** no-drift

**Narrative:** `entity add` parses YAML, writes canonical JSON to entities/<id>/<id>.entity.json via saveJSONFile; EntityFileSuffix="entity" yields User.entity.json, and the test asserts FileExists plus json.Valid(data) for entity User — exactly what the AC names.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 6ad2122 — feat(cli/entity): add create-only `entity add` with nested JSON storage

**Evidence:**
- pkg/datatug-core/storage/filestore/store_entities.go (entityFilePath → entities/<id>/<id>.entity.json)
- apps/datatugapp/commands/cmd_entity_test.go (path + json.Valid assertions)

## AC: field-add-additive

**Drift verdict:** no-drift

**Narrative:** `entity field add User` appends new field `primaryCurrency` to existing User entity and leaves field `id` untouched (existing fields never modified); test TestEntityFieldAdd_Additive asserts exactly the AC's given/when/then.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 64dfad5 — feat(cli/entity): add additive batch `entity field add`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::entityFieldAddCommandAction (appends toAdd; never mutates existing)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityFieldAdd_Additive

## AC: field-add-rejects-existing

**Drift verdict:** no-drift

**Narrative:** Code matches AC exactly: in atomic mode any field-id collision (e.g. adding `id` when `User` already has `id`) collects into `failures`, writes nothing, and returns a non-nil error (non-zero exit); test TestEntityFieldAdd_RejectsExisting asserts error plus byte-identical before/after entity file.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 64dfad5 — feat(cli/entity): add additive batch `entity field add`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::entityFieldAddCommandAction (`case existing[f.ID]:` collision; nothing-written guard)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityFieldAdd_RejectsExisting

## AC: field-set-updates

**Drift verdict:** no-drift

**Narrative:** `entity field set User primaryCurrency --type currency` sets field.Type to "currency" and returns nil (exit 0); no type validation rejects it at this commit. Test TestEntityFieldSet_UpdatesType asserts the exact AC.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 4dbbbc8 — feat(cli/entity): add surgical `entity field set`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::entityFieldSetCommandAction (setType → field.Type = ...; returns nil)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityFieldSet_UpdatesType

## AC: field-set-missing-errors

**Drift verdict:** no-drift

**Narrative:** `entity field set User xyz --title X` returns cli.Exit(...,1) before any write when no matching field is found, so it exits non-zero and leaves User byte-for-byte unchanged; TestEntityFieldSet_MissingErrors reproduces the exact AC scenario and passes.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 4dbbbc8 — feat(cli/entity): add surgical `entity field set`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go (`if field == nil { return cli.Exit(...,1) }` precedes any write)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityFieldSet_MissingErrors

## AC: field-set-key-flag

**Drift verdict:** no-drift

**Narrative:** `entity field set User email --key` sets field.IsKeyField=true and returns nil (exit 0); test TestEntityFieldSet_KeyFlag asserts email becomes a key field with no error, matching the AC exactly.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 4dbbbc8 — feat(cli/entity): add surgical `entity field set`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go (setKey → field.IsKeyField = c.Bool(...))
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityFieldSet_KeyFlag

## AC: field-rm-removes

**Drift verdict:** no-drift

**Narrative:** `entity field rm User tmp` removes the field then exits non-zero (code 1, error mentions "tmp") when re-run on the now-absent field — exactly what the AC's given/when/then describes; test TestEntityFieldRm_Removes asserts both the removal and the second-run failure.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 669ac48 — feat(cli/entity): add `entity field rm`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::entityFieldRmCommandAction (idx<0 → cli.Exit(...,1))
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityFieldRm_Removes

## AC: field-type-invalid

**Drift verdict:** no-drift

**Narrative:** Code rejects unknown field type `not-a-type` with a non-zero exit reporting "unknown field type", validating before any write so nothing is persisted; tests assert error mentions not-a-type and os.Stat on the entity dir fails. Extra known type `currency` and extends:<ref> acceptance are orthogonal and do not loosen the AC.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 6b9dc36 — feat(cli/entity): reject unknown field types across mutation paths

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::validateFieldType / validateEntityFieldTypes (gates add/field-add/field-set before write)
- apps/datatugapp/commands/cmd_entity_test.go (not-a-type input; non-zero; entity dir absent)

## AC: no-implicit-override

**Drift verdict:** no-drift

**Narrative:** Code matches AC exactly: field add keys collision on field ID; same-ID input (id:string vs existing id:integer) is rejected pre-write, exits non-zero, and atomic mode writes nothing so id stays integer. Test TestEntityFieldAdd_NoImplicitOverride asserts the precise Given/When/Then.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 64dfad5 — feat(cli/entity): add additive batch `entity field add`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::entityFieldAddCommandAction (collision on f.ID; nothing-written guard)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityFieldAdd_NoImplicitOverride (id remains integer)

## AC: entity-list-lists

**Drift verdict:** no-drift

**Narrative:** `entity list` loads all project entities and prints each (ID, optionally with title); test TestEntityList_Lists adds User and Order then asserts both appear in output, exactly matching the AC.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 10321fa — feat(cli/entity): add read-back `entity list` and `entity show`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::entityListCommandAction
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityList_Lists

## AC: entity-show-renders

**Drift verdict:** no-drift

**Narrative:** `entity show <Entity>` loads and renders the entity's fields plus the `tables` generated mapping copy under a "generated mapping copy (read-only)" label, writing only to stdout (LoadEntity is read-only); test asserts field+mapping render and byte-identical on-disk file, matching the AC's fields/mapping/unchanged-project triad exactly.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 10321fa — feat(cli/entity): add read-back `entity list` and `entity show`

**Evidence:**
- apps/datatugapp/commands/cmd_entity.go::entityShowCommandAction + renderEntityShow (read-only; labelled mapping copy)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityShow_Renders (render + before==after non-mutation)

## AC: git-default-none

**Drift verdict:** no-drift

**Narrative:** With --git absent, gitFlag defaults to "none" → resolveGitMode returns gitModeNone, and both gitPreflight and applyGit are no-ops; entity files are written via atomicWriteFiles while nothing is staged/committed, exactly matching the AC across entity add and field add/set/rm.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 89f9e5b — feat(cli/entity): wire --git into all entity mutators (Task 9)

**Evidence:**
- apps/datatugapp/commands/git_integration.go (gitFlag Value:"none"; resolveGitMode; applyGit/gitPreflight no-op for none)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityAdd_GitFlag_DefaultNone

## AC: git-stage-scoped

**Drift verdict:** no-drift

**Narrative:** stageFiles adds exactly the written paths via per-file wt.Add (never git add -A); preflight runs before write. Test TestEntityAdd_GitStage_ScopedToWrittenFiles asserts the entity file is staged (A) while an unrelated tracked change stays unstaged ( M), matching the AC precisely. Suite green at HEAD.

**Verify verdict:** pass

**Verify justification:** carried from 700e415 (pass); behavior unchanged, full suite green at 1b0448e

**Commits:**
- 89f9e5b — feat(cli/entity): wire --git into all entity mutators (Task 9)

**Evidence:**
- apps/datatugapp/commands/git_integration.go (stageFiles per-file wt.Add; gitPreflight)
- apps/datatugapp/commands/cmd_entity_test.go::TestEntityAdd_GitStage_ScopedToWrittenFiles
