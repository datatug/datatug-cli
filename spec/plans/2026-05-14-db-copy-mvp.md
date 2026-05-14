# `datatug db copy` MVP — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land `datatug db copy --from <url> --to <url>` end-to-end on the two MVP backends — SQLite (via `dalgo2sqlite`) and inGitDB (via `dalgo2ingitdb`) — satisfying the contract in [`spec/features/cli/db/copy/README.md`](../features/cli/db/copy/README.md).

**Architecture:** A new internal package `pkg/dbcopy/` holds the URL resolver, the per-backend adapter factory, the type-mapping table, and the per-table copy engine. A new Cobra-style subcommand `cmd_db_copy.go` (under `apps/datatugapp/commands/`) wires CLI flags into the engine. The existing `dbCommand()` in `cmd_open_db.go` gains a `copy` subcommand. Everything moves through DALgo's `dbschema` (read), `ddl` (write-schema), and `dal.ConcurrencyAware` (cap-to-1 rule). No engine-specific SQL is emitted.

**Tech Stack:** Go 1.26, `github.com/urfave/cli/v3` (matches existing command style), `github.com/xo/dburl` for URL parsing of `sqlite://` (already in `go.mod`), a hand-rolled prefix dispatcher for `ingitdb://` (`xo/dburl` does not register it), `github.com/stretchr/testify` for assertions.

**Spec:** [`spec/features/cli/db/copy/README.md`](../features/cli/db/copy/README.md) — **Approved**
**Source Idea:** [`spec/ideas/cross-engine-db-copy.md`](../ideas/cross-engine-db-copy.md) — **Approved**

**Status:** In progress — Tasks 0, 0.5, 1, 2, 3 (schema + rows for SQLite→inGitDB), 7, 9 complete.

## Plan revision 2026-05-14: schema-only first slice (now superseded)

A plan-time audit of the two drivers revealed:

- **`dalgo2ingitdb` had no row CRUD** — `Insert`, `RunReadwriteTransaction`, `ExecuteQuery*` were stubbed with `dal.ErrNotSupported`. **Resolved upstream** via `ingitdb-cli` commit `3444b2f feat(dalgo2ingitdb): implement record CRUD with file locking`. Currently consumed via local `replace` directive until a release tag includes it.
- **`dalgo2sqlite.DescribeCollection` rejects `DATETIME` and `NUMERIC(p,s)`** — still open; breaks 4 of 11 Chinook tables. Upstream issue staged at [`docs/upstream-issues/dalgo2sqlite-describe-datetime-numeric.md`](../../docs/upstream-issues/dalgo2sqlite-describe-datetime-numeric.md).

**Initial slice — SQLite → inGitDB (schema + rows).** Tasks 3, 9 are now complete for the forward direction: schema replicates via `dbschema` / `ddl`, rows stream via `ExecuteQueryToRecordsReader` → `InsertMulti`. Live binary verified: Chinook fixture (11 tables) yields 729 rows across the 6 describe-able single-PK tables, 4 tables describe-skipped, 1 (`PlaylistTrack`) row-skipped due to composite PK.

**Still open in this Feature scope:**

- Composite-PK row copy (e.g. `PlaylistTrack`). Needs a key-encoding decision for the inGitDB record filename.
- Reverse direction inGitDB → SQLite. Blocked: `dalgo2sql`'s inserter uses struct reflection and rejects `map[string]any` Record data. Fix is either upstream in `dalgo2sql` or a `reflect.StructOf` translation layer in this engine.
- Task 4 (empty-target check), Task 5 (overwrite=reload schema-match + truncate), Task 6 (parallel-streams cap), Task 8 (partial-failure semantics).

---

## Conventions

- **Working directory:** `/Users/alexandertrakhimenok/projects/datatug/datatug-cli`. All commands assume that cwd.
- **CLI framework:** `github.com/urfave/cli/v3` (`cli.Command`) — matches all other subcommands in `apps/datatugapp/commands/`.
- **Test framework:** Go stdlib `testing` with `stretchr/testify/assert`. Use `t.Parallel()` on leaf tests except those touching shared fixture files.
- **Commit style:** Conventional commits. One commit per task. Footer:
  ```
  Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
  ```
- **Verification:** After each task, `go build ./...` and the task-scoped `go test` invocation MUST be clean before commit. After the last task, `go test ./...` MUST be clean.
- **Driver pinning:** This repo already has `//replace github.com/dal-go/dalgo => ../../dal-go/dalgo` commented out. Leave it commented; the released `dal-go/dalgo v0.41.15` already ships `dbschema`, `ddl`, and `ConcurrencyAware`. Add `dal-go/dalgo2sqlite` and `ingitdb/ingitdb-cli` as new dependencies (Task 0).

## File Map

| Action | File | Change |
|---|---|---|
| Modify | `go.mod` / `go.sum` | Add `github.com/dal-go/dalgo2sqlite` and `github.com/ingitdb/ingitdb-cli` (only `pkg/dalgo2ingitdb` and `pkg/ingitdb/validator` are imported). |
| Create | `pkg/dbcopy/url.go` | URL scheme dispatch: parses `sqlite://`, `ingitdb://`, recognizes `postgres://` and rejects with the "not yet wired" message. Returns a `dal.DB` (or a `BackendRef` wrapper carrying scheme + path). |
| Create | `pkg/dbcopy/url_test.go` | Unit tests for REQ:supported-schemes, REQ:ingitdb-url-local-only, REQ:unknown-scheme-rejected. |
| Create | `pkg/dbcopy/typemap.go` | Type-mapping table: every Chinook column type, both directed pairs (SQLite ↔ inGitDB). Exports `MapType(source dbschema.Type, targetBackend string) (dbschema.Type, error)`. |
| Create | `pkg/dbcopy/typemap_test.go` | Coverage test that walks the Chinook column set and asserts every type round-trips. Unsupported-type test asserts the exit-1 error path. |
| Create | `pkg/dbcopy/engine.go` | Top-level `Copy(ctx, source, target dal.DB, opts CopyOpts) error`. Per-table worker pool. Honors `--parallel-streams` cap rule. |
| Create | `pkg/dbcopy/engine_test.go` | Unit tests for REQ:concurrency-cap, REQ:empty-target-check, REQ:reload-schema-match, REQ:partial-failure-leaves-state. Uses gomock-style stub adapters. |
| Create | `pkg/dbcopy/progress.go` | `--progress` formatter. Stderr writer. |
| Create | `pkg/dbcopy/progress_test.go` | Asserts silent default and per-table line format. |
| Create | `apps/datatugapp/commands/cmd_db_copy.go` | Cobra-style subcommand wiring (`cli.Command`). Flag definitions, exit-code mapping, calls `dbcopy.Copy`. |
| Create | `apps/datatugapp/commands/cmd_db_copy_test.go` | CLI-level tests for required-flags rejection (REQ:required-flags) and `--overwrite` value validation (REQ:overwrite-values). |
| Modify | `apps/datatugapp/commands/cmd_open_db.go` | Attach `copyCommand()` as a subcommand of `dbCommand()`. No other change. |
| Create | `apps/datatugapp/commands/cmd_db_copy_e2e_test.go` | Build-tagged (`//go:build e2e`) E2E test: Chinook SQLite → inGitDB → SQLite round-trip. |
| Create | `testdata/chinook.db` (or use existing) | Confirmed/added in Task 0.5. |

## Audit (verified at plan time)

| Question | Answer |
|---|---|
| Does `dalgo2sqlite` implement `dbschema.SchemaReader`, `ddl.SchemaModifier`, and `dal.ConcurrencyAware`? | **Yes.** `dal-go/dalgo2sqlite/database.go` embeds `dal.NoConcurrency`; `schema_reader.go` and `schema_modifier.go` provide the methods. |
| Does `dalgo2ingitdb` implement them? | **Yes.** `ingitdb/ingitdb-cli/pkg/dalgo2ingitdb/database.go` embeds `dal.ConcurrencyAvailable`; `schema_reader.go` and `schema_modifier.go` provide the methods. |
| Does `dalgo2ingitdb.NewDatabase` need extra wiring? | **Yes.** Signature is `NewDatabase(projectPath string, reader ingitdb.CollectionsReader) (dal.DB, error)`. Use `validator.NewCollectionsReader()` from `pkg/ingitdb/validator` as the default reader. |
| Does `dburl.Register` exist? | Not as a public API. The URL resolver in `pkg/dbcopy/url.go` MUST intercept `ingitdb://` (and `postgres://`) before delegating SQLite to `dburl.Parse`. |
| Does `dbschema.SchemaReader.DescribeCollection` return everything needed for `ddl.CreateCollection`? | Yes — `CollectionDef` carries `FieldDef`s, `PrimaryKey`, and `IndexDef`s; that is exactly what `ddl.CreateCollection` consumes. |
| Is there an existing Chinook fixture in the repo? | **Open** — Task 0.5 confirms or seeds one. |

## Out of Scope (consistent with the Feature's "Out of Scope" section)

- PostgreSQL E2E (no driver implements the capability interfaces yet)
- `--progress` ETA / row-count estimation heuristics beyond a simple count query
- Resumability after partial failure
- Per-engine bulk-load optimizations
- Remote `ingitdb://` URLs

---

## Task 0: Wire driver dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add dependencies**
  - `go get github.com/dal-go/dalgo2sqlite@latest`
  - `go get github.com/ingitdb/ingitdb-cli@latest`
  - `go mod tidy`

- [ ] **Step 2: Verify build**
  - `go build ./...` must pass with no new errors.
  - If `ingitdb-cli` pulls heavy transitive deps (TUI, charm libs), evaluate whether to import only `pkg/dalgo2ingitdb` and `pkg/ingitdb/validator` (Go's import discipline will only build what's referenced, but `go.sum` growth is worth noting).

- [ ] **Step 3: Commit**
  ```
  chore(deps): add dalgo2sqlite + ingitdb-cli (dalgo2ingitdb) for db-copy
  ```

## Task 0.5: Chinook fixture

**Files:**
- Add or confirm: `testdata/chinook.db` (or under `pkg/dbcopy/testdata/`)

- [ ] **Step 1:** Decide the canonical location for the fixture. Match the convention used by `pkg/datatug-core` or `apps/datatugapp/commands/testdata` if either already has fixtures.
- [ ] **Step 2:** If no Chinook fixture exists, download it from the canonical source (https://github.com/lerocha/chinook-database/releases) and place the SQLite variant into `testdata/`. Commit the file; it's small (~1MB) and tests need byte-stable input.
- [ ] **Step 3: Commit**
  ```
  test(dbcopy): add Chinook SQLite fixture
  ```

## Task 1: URL scheme dispatch

**Files:**
- Create: `pkg/dbcopy/url.go`
- Create: `pkg/dbcopy/url_test.go`

Implements REQ:supported-schemes, REQ:ingitdb-url-local-only, REQ:unknown-scheme-rejected.

- [ ] **Step 1: Write failing tests** covering:
  - `sqlite:///abs.db` → returns a `BackendRef{Scheme: "sqlite", Path: "/abs.db"}`.
  - `sqlite://./rel.db` → relative resolution.
  - `ingitdb://./project` → returns `BackendRef{Scheme: "ingitdb", Path: "./project"}`.
  - `ingitdb://github.com/owner/repo` → error matching "local paths only".
  - `mongodb://host/db` → error containing `mongodb` and the supported list `sqlite, ingitdb, postgres`.
  - `postgres://...` → error containing "PostgreSQL backend not yet wired" (per the updated REQ:supported-schemes status column).

- [ ] **Step 2: Implement** the dispatcher. Skeleton:
  ```go
  func Parse(rawURL string) (BackendRef, error)
  func (r BackendRef) Open(ctx context.Context) (dal.DB, error)
  ```
  `Open` switches on `r.Scheme`:
  - `sqlite` → `dalgo2sqlite.NewDatabase(r.Path)`
  - `ingitdb` → `dalgo2ingitdb.NewDatabase(r.Path, validator.NewCollectionsReader())`
  - `postgres` → return a typed `ErrPostgresNotWired`
  - other → unreachable (caught at parse time)

- [ ] **Step 3: Verify**
  - `go test ./pkg/dbcopy/ -run TestParse`

- [ ] **Step 4: Commit**
  ```
  feat(dbcopy): URL scheme dispatch for sqlite, ingitdb, postgres-rejected
  ```

## Task 2: Type mapping

**Files:**
- Create: `pkg/dbcopy/typemap.go`
- Create: `pkg/dbcopy/typemap_test.go`

Implements REQ:type-mapping-coverage (narrowed to SQLite ↔ inGitDB).

- [ ] **Step 1:** Enumerate every `dbschema.Type` that appears on Chinook columns. Build a coverage table.
- [ ] **Step 2: Write the failing coverage test** that opens the Chinook SQLite fixture via `dalgo2sqlite`, calls `dbschema.ListCollections` + `DescribeCollection`, and asserts `MapType(col.Type, "ingitdb")` succeeds for every column.
- [ ] **Step 3: Write the failing unsupported-type test** — feeding a synthetic `dbschema.Type` outside the closed set MUST return a typed error naming the column type and the target backend.
- [ ] **Step 4: Implement** `MapType` as a switch on `dbschema.Type.Kind()`. Direction-aware: SQLite-as-source vs inGitDB-as-source can have different identity vs widen rules; encode both directions.
- [ ] **Step 5: Verify**
  - `go test ./pkg/dbcopy/ -run TestMapType`
- [ ] **Step 6: Commit**
  ```
  feat(dbcopy): type-mapping table for SQLite ↔ inGitDB (Chinook coverage)
  ```

## Task 3: Copy engine — happy path (serial, no overwrite)

**Files:**
- Create: `pkg/dbcopy/engine.go`
- Create: `pkg/dbcopy/engine_test.go`

Implements REQ:source-schema-via-dbschema, REQ:target-schema-via-ddl, REQ:bounded-memory-streaming, REQ:row-insert-via-dalgo, REQ:source-introspection-failure (source-empty branch).

- [ ] **Step 1: Define `CopyOpts`:**
  ```go
  type CopyOpts struct {
      ParallelStreams int          // 0 = default runtime.NumCPU()-1
      Overwrite       string       // "", "recreate", "reload"
      Progress        io.Writer    // nil = silent
  }
  ```
- [ ] **Step 2: Write tests with stub adapters** that exercise:
  - Source with 0 tables → exit 0, stderr "source has no tables; nothing to copy", target untouched.
  - Source with 1 table, 3 rows → target has the table created and all 3 rows inserted via `ddl.CreateCollection` + the adapter's insert path.
- [ ] **Step 3: Implement** `Copy(ctx, source, target dal.DB, opts CopyOpts) error`:
  - Call `dbschema.ListCollections(ctx, source, nil)`.
  - If empty: write the stderr line via `opts.Progress` (if non-nil — but stderr line is mandatory per the REQ; route through a dedicated `opts.Stderr`-style sink that defaults to `os.Stderr`).
  - For each collection: `dbschema.DescribeCollection` → `ddl.CreateCollection` (via the target's `SchemaModifier`) → stream rows via the source adapter's `Reader` API → write via the target adapter's `InsertMulti` (or per-driver equivalent).
- [ ] **Step 4: Bounded-memory check** — assert in test that a 10k-row table is streamed in batches of at most N (pick a small default like 500).
- [ ] **Step 5: Verify**
  - `go test ./pkg/dbcopy/ -run TestCopy_HappyPath`
- [ ] **Step 6: Commit**
  ```
  feat(dbcopy): serial copy engine — introspect + create + stream
  ```

## Task 4: Empty-target check (no `--overwrite`)

**Files:**
- Modify: `pkg/dbcopy/engine.go`
- Modify: `pkg/dbcopy/engine_test.go`

Implements REQ:empty-target-check.

- [ ] **Step 1: Tests** for the three cases in the Feature ACs:
  - empty-target-with-source-named-rows-rejected
  - empty-target-with-unrelated-tables-accepted
  - empty-target-with-source-named-empty-tables-accepted
- [ ] **Step 2: Implement** the pre-flight check on the target. Uses `dbschema.ListCollections(ctx, target, nil)` to enumerate, then a per-collection count query (start with reading 1 row via the adapter; if a count helper exists in the adapter, prefer it).
- [ ] **Step 3: Verify & commit**
  ```
  feat(dbcopy): empty-target pre-flight check
  ```

## Task 5: Overwrite policy — `recreate` and `reload`

**Files:**
- Modify: `pkg/dbcopy/engine.go`
- Modify: `pkg/dbcopy/engine_test.go`

Implements REQ:overwrite-values, REQ:recreate-drops-first, REQ:reload-schema-match.

- [ ] **Step 1: Tests** for ACs `recreate-drops-source-tables-only`, `reload-rejects-schema-mismatch`, `reload-accepts-superset-target`, `overwrite-bogus-rejected`.
- [ ] **Step 2: Implement** the two branches:
  - `recreate`: enumerate source tables → `ddl.DropCollection(IfExists())` for each on the target → proceed with normal create+stream.
  - `reload`: for each source table, `dbschema.DescribeCollection(target, table)`, compare columns (source ⊆ target by name+mapped-type), compare PK column set. First mismatch → typed error → exit 1. On match → TRUNCATE (target adapter's equivalent — see Task 5.5) → stream.
- [ ] **Step 3: Verify & commit**
  ```
  feat(dbcopy): --overwrite=recreate|reload with schema-match validation
  ```

### Task 5.5 (open question to resolve here): TRUNCATE path

DALgo doesn't have a portable `Truncate` op today. For MVP, `reload` implements TRUNCATE as:
- SQLite: emit via the target adapter's transaction surface — `DELETE FROM <table>` is acceptable (the Feature explicitly does not require performance optimization for MVP).
- inGitDB: same — issue per-row deletes through the DALgo Writer until the table is empty, OR (preferred if the driver exposes it) drop+recreate the collection without indexes.

Decision: **use `DELETE`-via-Writer for MVP**. If `dalgo2ingitdb` doesn't expose a way to drop all records of a collection without re-introspecting, file a sibling Idea in `ingitdb-cli` to add a `TruncateCollection` capability; for MVP, iterate-and-delete is acceptable. Document this in a code comment.

## Task 6: Concurrency cap (`--parallel-streams`)

**Files:**
- Modify: `pkg/dbcopy/engine.go`
- Modify: `pkg/dbcopy/engine_test.go`

Implements REQ:parallel-streams-flag, REQ:concurrency-cap.

- [ ] **Step 1: Tests** for `concurrency-cap-warns-on-explicit-request` and `concurrency-cap-silent-on-default`.
- [ ] **Step 2: Implement** the cap rule:
  - Query `source.(dal.ConcurrencyAware).SupportsConcurrentConnections()` and same for target.
  - If either is `false`, cap effective parallelism to 1.
  - If user explicitly passed `--parallel-streams=N` (`N > 1`), emit one stderr warning naming the constraining driver.
  - Per-table worker pool: `sync.WaitGroup` + `chan struct{}` semaphore sized to effective N. Each table is one worker; within a table, the row stream is serial.
- [ ] **Step 3: Verify & commit**
  ```
  feat(dbcopy): parallel-streams with cap-to-1 honoring ConcurrencyAware
  ```

## Task 7: Progress reporting

**Files:**
- Create: `pkg/dbcopy/progress.go`
- Create: `pkg/dbcopy/progress_test.go`

Implements REQ:progress-reporting.

- [ ] **Step 1: Tests** for `progress-silent-by-default` and `progress-flag-emits-per-table-lines`.
- [ ] **Step 2: Implement** a `progressWriter` that, when enabled, prints per-table start/finish lines on stderr. When disabled, all calls no-op. Format pinned to the AC strings.
- [ ] **Step 3: Verify & commit**
  ```
  feat(dbcopy): --progress per-table stderr lines
  ```

## Task 8: Partial-failure semantics

**Files:**
- Modify: `pkg/dbcopy/engine.go`
- Modify: `pkg/dbcopy/engine_test.go`

Implements REQ:partial-failure-leaves-state.

- [ ] **Step 1: Test** `partial-failure-leaves-completed-tables` using a fault-injection target adapter that fails midway through table `b`'s row stream.
- [ ] **Step 2: Implement:** when any worker errors, cancel the pool's context, wait for in-flight workers to drain (allowed to fail or stop mid-stream), exit with the FIRST error. Already-completed workers stay completed.
- [ ] **Step 3: Verify & commit**
  ```
  feat(dbcopy): partial-failure semantics — leave completed tables intact
  ```

## Task 9: CLI wiring

**Files:**
- Create: `apps/datatugapp/commands/cmd_db_copy.go`
- Create: `apps/datatugapp/commands/cmd_db_copy_test.go`
- Modify: `apps/datatugapp/commands/cmd_open_db.go` — attach `copyCommand()` to the `db` command's `Commands` slice.

Implements REQ:required-flags, REQ:optional-flags, REQ:exit-codes.

- [ ] **Step 1: Tests** for AC `missing-from-rejected`, `missing-to-rejected`, `overwrite-bare-rejected`, `overwrite-bogus-rejected`. Drive via `cli.Command.Run(ctx, []string{...})`.
- [ ] **Step 2: Implement** the Cobra-style command:
  ```go
  func copyCommand() *cli.Command {
      return &cli.Command{
          Name:  "copy",
          Usage: "Copy a database from one DALgo URL to another",
          Flags: []cli.Flag{
              &cli.StringFlag{Name: "from", Required: true},
              &cli.StringFlag{Name: "to",   Required: true},
              &cli.IntFlag   {Name: "parallel-streams"},
              &cli.StringFlag{Name: "overwrite"},
              &cli.BoolFlag  {Name: "progress"},
          },
          Action: copyAction,
      }
  }
  ```
  Map `dbcopy.Copy` errors to exit codes per REQ:exit-codes via a small `errorToExitCode(err) int` table.
- [ ] **Step 3: Verify & commit**
  ```
  feat(cli): db copy subcommand wiring
  ```

## Task 10: E2E round-trip

**Files:**
- Create: `apps/datatugapp/commands/cmd_db_copy_e2e_test.go` (build tag `//go:build e2e`)

Implements AC `sqlite-to-ingitdb-chinook-roundtrip` and `ingitdb-to-sqlite-chinook-roundtrip`.

- [ ] **Step 1:** Test 1 — `datatug db copy --from sqlite:///testdata/chinook.db --to ingitdb://${t.TempDir()}/proj`, then walk every Chinook table and assert row count parity via direct `dalgo2ingitdb` reads.
- [ ] **Step 2:** Test 2 — chain Test 1's output: copy `ingitdb://...proj` → fresh `sqlite:///${t.TempDir()}/out.db`. Assert row count parity AND PK identity per AC.
- [ ] **Step 3: Verify** with `go test -tags=e2e ./apps/datatugapp/commands/...`
- [ ] **Step 4: Commit**
  ```
  test(dbcopy): E2E Chinook SQLite ↔ inGitDB round-trip
  ```

## Task 11: Plans index + Feature status flip

**Files:**
- Modify: `spec/features/cli/db/copy/README.md` — flip `Status:` from `Approved` to `Implemented`.
- Create: `spec/plans/README.md` — plans index seeded with this plan.

- [ ] **Step 1:** Update the Feature status header.
- [ ] **Step 2:** Create the plans index following the convention in `dal-go/dalgo/spec/plans/README.md`.
- [ ] **Step 3: Commit**
  ```
  docs(spec): mark db copy Feature Implemented; seed plans index
  ```

---

## Verification matrix

| Verification | When | Command |
|---|---|---|
| Unit tests pass | After each task | `go test ./pkg/dbcopy/...` |
| CLI tests pass | After Task 9 | `go test ./apps/datatugapp/commands/...` |
| Full build | After every task | `go build ./...` |
| Full unit suite | After Task 9 | `go test ./...` |
| E2E suite | After Task 10 | `go test -tags=e2e ./...` |
| Feature ACs traced to tests | After Task 10 | Manual: every AC in `spec/features/cli/db/copy/README.md` has at least one `t.Run` or test fn name that names it. |

## Outstanding Questions (deferred from spec)

- **`time.Duration` format in the `--progress` "copied <table>" line.** Suggest `time.Duration.Round(time.Millisecond).String()`. Decide in Task 7.
- **Row-count `est. <N>` heuristic** when the source has no cheap count query. inGitDB-as-source: count of record files on disk is cheap. SQLite-as-source: `SELECT COUNT(*)` is fine. Hard-code per backend in Task 7.
- **TRUNCATE-via-DELETE performance for `--overwrite=reload` on large tables.** Acceptable for MVP per Task 5.5. Track as a follow-up sibling Idea if benchmarks show pathological behavior on Chinook×100.
- **Whether to capture the implicit `extra_internal_flag` NULL default behavior in `reload-accepts-superset-target` via a target-adapter-specific check** (some engines reject NULLs into NOT NULL columns even after TRUNCATE). For MVP, accept that the test runs only against SQLite-as-target where NULLs into nullable columns are universally fine.

---

*This document follows the https://specscore.md/plan-specification*
