# Feature: `datatug db copy`

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/copy?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/copy?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/copy?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/copy?op=request-change) |

**Status:** Implemented
**Source Idea:** [`cross-engine-db-copy`](../../../../ideas/cross-engine-db-copy.md)
**Parent Feature:** [`cli/db`](../README.md)

## Summary

`datatug db copy --from <url> --to <url>` is a streaming, cross-engine database-copy primitive. It reads the source schema and table data via the DALgo abstraction and writes them to the target via the same abstraction — auto-creating the target schema on the way. MVP is end-to-end-tested in **both directions** between SQLite (via `dalgo2sqlite`) and inGitDB (via `dalgo2ingitdb`) — i.e. SQLite → inGitDB AND inGitDB → SQLite.

**Current state — SQLite → inGitDB end-to-end (full Chinook).** The implementation replicates schema (collection definitions, primary keys, indexes) AND row data for the SQLite → inGitDB direction. All 11 Chinook tables replicate cleanly with 15,607 rows copied. Row streaming uses DALgo's `ExecuteQueryToRecordsReader` on the source and `RunReadwriteTransaction` → `InsertMulti` on the target. Per-feature notes:

- `DATETIME` / `NUMERIC(p,s)` SQLite types are recognized as `dbschema.Time` and `dbschema.Decimal` respectively (fixed upstream in `dalgo2sqlite`). `dbschema.Decimal` and `dbschema.Bytes` map to inGitDB's `Float` and `String` column types respectively — lossy carriers documented in `dalgo2ingitdb/type_mapping.go`.
- Composite-PK tables are copied with each row's target record ID as the `__`-joined `fmt.Sprintf("%v", v)` of every PK column (in the order `DescribeCollection` reports them); for example `PlaylistTrack`'s `(PlaylistId=1, TrackId=3402)` lands at `<projectPath>/PlaylistTrack/$records/1__3402.yaml`.
- inGitDB → SQLite row streaming works through `dalgo2sql` accepting `map[string]any` Record data alongside the existing struct path (fixed upstream). End-to-end reverse-direction E2E is not yet wired in tests; the building blocks are in place.
- PostgreSQL is recognized as a URL scheme but deferred from the MVP E2E bar until a PostgreSQL DALgo driver lands `dbschema.SchemaReader` + `ddl.SchemaModifier` + `dal.ConcurrencyAware`. The `ingitdb://` URL scheme dispatches to the inGitDB driver against a local-filesystem path.

The verb is a pure primitive: either side can be any DALgo-supported URL. A `--parallel-streams` flag governs per-table parallelism with a safety cap derived from the DALgo `ConcurrencyAware` capability. A non-empty target requires an explicit `--overwrite=recreate` (drop tables and recreate from source schema) or `--overwrite=reload` (truncate tables and reload data, preserving schema).

## Contents

| Child | Description |
|---|---|
| [filtering](filtering/README.md) | Extends `datatug db copy` with four orthogonal subsetting axes — table include/exclude, structured row predicates (push-down via DALgo `WhereField`), per-table row limits, and column subsetting (per-table whitelist/blacklist + global column exclusion) — exposed as CLI flags and mirrored 1:1 in a YAML config schema for `db snapshot`'s forwarder. Promotes the `db-copy-filtering` Idea. |

## Synopsis

```
datatug db copy --from <source-url> --to <target-url>
                [--parallel-streams=N]
                [--overwrite=recreate|reload]
                [--progress]
```

## Problem

DataTug already scans live databases into versioned project files and exposes the inverse through the UI, but there is no commandable path that moves table data from one engine to another. Today, moving a SQLite fixture into an inGitDB-backed project (or the reverse) requires custom code per pair. End-to-end tests need a deterministic SQLite↔inGitDB seeding step, users need a portable backup story that survives the engine they happen to use, and the DALgo abstraction is the obvious seam to do this through.

## Behavior

### CLI invocation

#### REQ: required-flags

The command MUST accept `--from <url>` and `--to <url>` as REQUIRED flags. Both MUST be supplied; either being absent MUST exit with status `2` (InvalidArgs) and a usage message naming the missing flag.

#### REQ: optional-flags

The command MUST accept the following optional flags:

- `--parallel-streams <N>` — positive integer; default `runtime.NumCPU() - 1` (minimum `1`). Governs per-table parallelism, subject to the cap-to-1 rule in REQ:concurrency-cap.
- `--overwrite <recreate|reload>` — value MUST be exactly one of `recreate` or `reload`. A bare `--overwrite` (with no value) MUST be rejected with exit `2`. Omitting the flag means "require empty target" per REQ:empty-target-check.
- `--progress` — boolean; when present, enables per-table progress lines on stderr (see REQ:progress-reporting). Default off (silent).

### URL scheme dispatch

#### REQ: supported-schemes

The MVP URL parser MUST accept these schemes:

| Scheme | Form | DALgo driver | MVP status |
|---|---|---|---|
| `sqlite` | `sqlite:///absolute/path.db` or `sqlite://./relative/path.db` | `dalgo2sqlite` (lives in `dal-go/dalgo2sqlite`) | E2E-tested |
| `ingitdb` | `ingitdb://./path-to-project` (local-filesystem path) | `dalgo2ingitdb` (lives in `ingitdb/ingitdb-cli`) | E2E-tested |
| `postgres` | `postgres://user:pw@host:port/dbname?sslmode=...` | TBD (no driver currently exposes `dbschema.SchemaReader` + `ddl.SchemaModifier` + `dal.ConcurrencyAware`) | **Deferred** — scheme recognized; opening MUST exit `1` with a "PostgreSQL backend not yet wired" message until a driver is registered. |

#### REQ: ingitdb-url-local-only

The `ingitdb://` scheme MVP MUST resolve only to a local-filesystem path. Remote URLs (e.g. `ingitdb://github.com/owner/repo`) are explicitly out of scope and MUST be rejected with exit `2` and a message naming "local paths only".

#### REQ: unknown-scheme-rejected

A URL whose scheme is not in the supported list MUST be rejected before any connection attempt with exit `2` and a message that names the unsupported scheme AND lists the supported schemes.

### Schema introspection (source)

#### REQ: source-schema-via-dbschema

The command MUST introspect the source schema via the DALgo `dbschema` package (shipped in `dal-go/dalgo/dbschema`). Specifically, it calls `Adapter.ListTables(ctx)` to enumerate tables and `Adapter.DescribeTable(ctx, name)` for each table's column definitions, primary key, and indexes. The command MUST NOT hand-roll engine-specific introspection queries (e.g. `SELECT … FROM sqlite_master`); the DALgo dbschema adapter is the single seam.

#### REQ: source-introspection-failure

If the source URL parses cleanly but the source backend cannot be opened (file missing, connection refused, auth failure), the command MUST exit `4` (connection failure) with stderr naming the source URL and the underlying error.

If introspection succeeds in opening the source but `ListTables` returns an empty list, the command MUST exit `0` after emitting a single stderr line "source has no tables; nothing to copy". No target writes occur.

### Schema creation (target)

#### REQ: target-schema-via-ddl

The command MUST create target tables (and primary-key declarations) via the DALgo DDL surface (`dal-go/dalgo/ddl`). Per source table, it builds a `ddl.CreateTable` operation from the introspected `TableDef` and applies it through the target's `dal.Adapter`. The command MUST NOT hand-roll engine-specific `CREATE TABLE` SQL.

For indexes, the command MUST apply each non-primary index via `ddl.CreateIndex` after the table is created and BEFORE row data is loaded for that table. The order is: `CreateTable` → `CreateIndex` (×N) → row stream. This order is engine-agnostic; per-engine performance hints (e.g. "drop indexes, bulk-load, recreate indexes") are out of scope for MVP.

#### REQ: type-mapping-coverage

The MVP type-mapping table MUST cover every column type appearing in the canonical Chinook fixture across both directed pairs of the MVP backends (SQLite ↔ inGitDB). Types outside that closed set MUST fail at schema-creation time (exit `1`) with a per-column error naming the unsupported source type and the target backend. The exact mapping table is plan-time content; this REQ pins the coverage bar. (PostgreSQL pairs are out of MVP scope; when the Postgres driver lands, the type-mapping table extends to the four additional directed pairs.)

#### REQ: recreate-drops-first

When `--overwrite=recreate` is supplied, BEFORE introspecting the source the command MUST drop every source-table-named table that exists on the target via `ddl.DropTable` (`IfExists()`). Indexes on dropped tables are dropped transitively per the DDL surface's contract. Tables on the target that are NOT in the source MUST be left alone. After all relevant drops complete, the command proceeds to introspect-and-create as in REQ:target-schema-via-ddl.

### Row streaming

#### REQ: bounded-memory-streaming

For each source table, the command MUST iterate rows via the DALgo `dal.Reader` interface (or its equivalent in the source backend's adapter). Whole-table loads into a `[]map[string]any` slice are FORBIDDEN. The implementation MAY buffer up to one DALgo batch at a time per table; plan-time picks the batch size.

#### REQ: row-insert-via-dalgo

Target row writes MUST go through the DALgo `dal.ReadwriteTransaction.InsertMulti` interface (or the per-driver equivalent surfaced by `dal.Adapter`). The command MUST NOT hand-roll engine-specific bulk-insert SQL (e.g. `COPY` for PostgreSQL, multi-VALUES INSERT for SQLite). Per-engine performance optimization is out of scope for MVP; correctness via the DALgo abstraction is the bar.

### Parallel streaming

#### REQ: parallel-streams-flag

`--parallel-streams=N` MUST control the maximum number of source tables being copied concurrently. Default is `runtime.NumCPU() - 1` (minimum `1`). Each table is one worker; within a single table, rows are streamed serially per REQ:bounded-memory-streaming.

#### REQ: concurrency-cap

Before launching workers, the command MUST query both the source and target adapters for the DALgo `ConcurrencyAware` capability (`dal-go/dalgo`'s `ConcurrencyAware` interface, shipped via the concurrency-capability Feature). If EITHER adapter advertises `Concurrency() == 1` (or equivalent "single-writer / single-reader" signal), the effective `--parallel-streams` value MUST be silently capped at `1`. When the cap reduces the user's requested value, a single stderr warning line MUST be emitted naming the constraining driver (e.g. `"warning: sqlite (target) requires serial writes; ignoring --parallel-streams=8, using 1"`).

When the user did not pass `--parallel-streams` and the default `runtime.NumCPU() - 1` is capped to 1 by this rule, NO warning is emitted — the default is implicit and the cap is the expected behavior.

### Overwrite policy

#### REQ: overwrite-values

`--overwrite` accepts exactly two values: `recreate` and `reload`. Their semantics:

- `recreate`: drop every source-table-named table on the target (per REQ:recreate-drops-first), then create tables and load data from scratch.
- `reload`: validate every source-table-named target table has a schema compatible with the source (per REQ:reload-schema-match), truncate each, then load source data. The target's existing schema is preserved.

Any other value (including bare `--overwrite` with no `=value`) MUST be rejected with exit `2` and a message listing the two valid values.

#### REQ: reload-schema-match

When `--overwrite=reload` is supplied, BEFORE any TRUNCATE or INSERT, the command MUST compare each source table's introspected schema (column names, types per the MVP type-map, primary key) to the target table's introspected schema. The comparison rules:

- Target MUST have every source column with a compatible type per the MVP type-mapping table.
- Target's primary-key column set MUST equal source's.
- Target MAY have additional columns; those are left untouched during reload.

On the FIRST mismatch detected, the command MUST exit `1` with a stderr diff naming the table and column(s) at issue (e.g. `"target users.email is TEXT, source is VARCHAR(255)"`). NO target write occurs in this case. The user resolves by `--overwrite=recreate` or by manually fixing the target schema.

### Empty target detection

#### REQ: empty-target-check

When `--overwrite` is OMITTED, the command MUST verify the target is "empty for this copy": after source introspection produces a list of source table names, the command queries the target for those specific tables. The target MUST be considered non-empty if ANY of the source-named tables exists on the target AND contains ≥1 row.

A source-named table that exists on the target but is empty (0 rows) is NOT considered non-empty for this check. A target with unrelated tables (not in source) is NOT considered non-empty. This precise rule lets users target a database that holds unrelated state without forcing `--overwrite`.

If the check identifies a non-empty target, the command MUST exit `1` BEFORE any write, with stderr naming the first conflicting table and its row count, and the message MUST suggest `--overwrite=recreate` or `--overwrite=reload`.

### Progress reporting

#### REQ: progress-reporting

Without `--progress`, the command MUST be silent on stdout/stderr during a successful run. Errors, warnings (per REQ:concurrency-cap), and exit messages are NOT suppressed by the silent default.

With `--progress`, the command MUST emit, per table, one stderr line when copy starts (`"copying <table> (est. <N> rows)…"` where the row count comes from the source's count query if available, else the literal `?`) and one when copy finishes (`"copied <table>: <M> rows in <duration>"`). stdout remains silent so the tool composes in pipelines.

### Failure semantics

#### REQ: partial-failure-leaves-state

If a copy fails midway (mid-stream insert error, target connection drop, source read error), the command MUST exit `1` with stderr naming the failing table. Tables already copied at the time of failure stay copied. The failing table is left in whatever state the failure produced (some rows inserted, some not). Tables not yet started never start. The user reruns with `--overwrite=recreate` or `--overwrite=reload` to retry.

#### REQ: exit-codes

| Exit code | Meaning |
|---|---|
| `0` | All source tables copied successfully (or source had no tables) |
| `1` | Generic runtime error (mid-copy failure, schema mismatch on reload, non-empty target without overwrite, type-mapping gap) |
| `2` | Invalid flags (missing required, unsupported `--overwrite` value, unknown scheme, remote `ingitdb://`) |
| `4` | Could not connect to source or target |

## Architecture

### Components

| Component | Responsibility | Lives in |
|---|---|---|
| `cmd/db/copy.go` | Cobra command definition, flag wiring, top-level orchestration | this repo |
| URL scheme resolver | Parse `--from`/`--to` URLs and dispatch to the right DALgo `dal.Adapter` (`sqlite`/`postgres` via `dalgo2sql`; `ingitdb` via `dalgo2ingitdb`) | this repo (`pkg/dbcopy/url.go` proposed) |
| Type mapper | Cross-engine column-type translation table for the MVP triplet (SQLite ↔ PostgreSQL ↔ inGitDB) | this repo (`pkg/dbcopy/typemap.go` proposed) |
| Copy engine | Per-table worker pool that drives `Reader` → `InsertMulti`; honors `--parallel-streams` cap | this repo (`pkg/dbcopy/engine.go` proposed) |
| `dal.Adapter` (source/target) | DALgo `dbschema` introspection + DDL application + row read/write | `dal-go/dalgo` (shipped via dbschema + ddl + concurrency-capability Features) |
| `dalgo2ingitdb` | inGitDB DALgo driver (read + write + dbschema + DDL coverage; advertises `ConcurrencyAvailable`) | `ingitdb/ingitdb-cli/pkg/dalgo2ingitdb` |
| `dalgo2sqlite` | SQLite DALgo driver (read + write + dbschema + DDL coverage; advertises `NoConcurrency`) | `dal-go/dalgo2sqlite` |
| PostgreSQL driver | TBD — deferred until a driver implements `dbschema.SchemaReader` + `ddl.SchemaModifier` + `dal.ConcurrencyAware`. `dalgo2sql` covers data-plane today but not the schema/concurrency capabilities. | `dal-go/dalgo2sql` (data-plane only today) |

### Data flow

```
┌─────────────────┐    introspect    ┌──────────────┐
│  source adapter │ ───────────────► │  TableDef[]  │
│  (--from)       │                  │  IndexDef[]  │
└─────────────────┘                  └──────┬───────┘
                                            │
            ┌───────────────────────────────┴─────────┐
            │  per-table workers (≤ --parallel-streams)│
            │                                          │
            │  ┌─────────────────┐    ┌─────────────┐ │
            │  │ source.Read     │ ──►│ target.Write│ │
            │  │ (bounded batch) │    │ InsertMulti │ │
            │  └─────────────────┘    └─────────────┘ │
            └──────────────────────────────────────────┘
                                            │
                                            ▼
                                  ┌─────────────────────┐
                                  │ target adapter (--to)│
                                  │ CreateTable/Index   │
                                  │ Insert rows         │
                                  └─────────────────────┘
```

### Dependencies

- **DALgo `dbschema`** — `dal-go/dalgo/dbschema` — **Implemented**.
- **DALgo `ddl`** — `dal-go/dalgo/ddl` — **Implemented**.
- **DALgo `ConcurrencyAware`** — `dal-go/dalgo` — **Implemented**.
- **`dalgo2ingitdb`** — `ingitdb/ingitdb-cli/pkg/dalgo2ingitdb` — **Ready** (implements `dbschema.SchemaReader`, `ddl.SchemaModifier`, embeds `dal.ConcurrencyAvailable`).
- **`dalgo2sqlite`** — `dal-go/dalgo2sqlite` — **Ready** (implements `dbschema.SchemaReader`, `ddl.SchemaModifier`, embeds `dal.NoConcurrency`).
- **PostgreSQL driver** — none of `dalgo2sql` / a hypothetical `dalgo2postgres` currently exposes the three capability interfaces. The `postgres://` scheme is recognized but disabled until a driver lands; see the MVP-status column in REQ:supported-schemes.
- **URL parser** — `github.com/xo/dburl` is the existing parser used by `datatug db <url>` (the viewer Feature). The `ingitdb://` scheme MUST be registered with `dburl` (either via `dburl.Register` if available or via a small wrapper in `pkg/dbcopy/url.go`).

## Testing Strategy

E2E tests against the canonical Chinook fixture. PostgreSQL pairs are deferred until a Postgres DALgo driver implements the three capability interfaces.

| Source → Target | E2E test target |
|---|---|
| SQLite → inGitDB | yes (MVP minimum) |
| inGitDB → SQLite | yes (MVP minimum) |
| SQLite → PostgreSQL | deferred (no driver) |
| inGitDB → PostgreSQL | deferred (no driver) |
| PostgreSQL → SQLite | deferred (no driver) |
| PostgreSQL → inGitDB | deferred (no driver) |

Per the source Idea: "If the primitive ships and only one direction (e.g. SQLite→inGitDB) is wired for E2E, that is still a shippable MVP — the contract is set, additional backends fill in." The two SQLite↔inGitDB directions are the MVP bar; the four PostgreSQL pairs fill in when the Postgres driver lands.

Unit tests cover: URL scheme dispatch (REQ:supported-schemes), the type-mapping table (REQ:type-mapping-coverage), the concurrency-cap rule (REQ:concurrency-cap), the empty-target detection (REQ:empty-target-check), the reload schema-match validator (REQ:reload-schema-match).

## Rehearse Integration

All ACs are testable via `go test ./...` plus shell-driven E2E runs invoking the built `datatug` binary. PostgreSQL E2E requires a running Postgres instance (use `testcontainers-go` or a CI fixture); SQLite and inGitDB E2E need only the test binary and a temp directory. No external scaffolding beyond standard Go-test mechanisms.

## Out of Scope

Inherited from the source Idea, reinforced at Feature-spec time:

- **`project export` / `project import` wrappers** — discarded. The source Idea originally floated these as stretch goals; the design conversation concluded they don't earn the surface area. `db copy --from <project-url> --to <other-url>` and the reverse suffice.
- **Incremental / delta sync** — MVP is one-shot full copy. Delta sync is a separate future Idea.
- **In-flight row filters, column mappings, anonymization** — separate concern. `db copy` is structural; transformations live in a follow-up command.
- **Conflict resolution on a non-empty target beyond `--overwrite=recreate|reload`** — no `--overwrite=merge`, no row-level upsert. MVP requires the user to choose drop-and-rebuild or truncate-and-reload.
- **All DALgo backends** — only SQLite, inGitDB, and PostgreSQL are E2E-tested. Other DALgo backends (e.g. Datastore, Firestore) MAY work through the abstraction but are not guaranteed by this Feature.
- **Schema diffing / migration** — target schema is auto-created from source. There is no contract for reconciling an existing target schema with the source schema beyond the strict REQ:reload-schema-match rule.
- **A bespoke dump-file format** — inGitDB serves as the canonical inspectable, versionable intermediate. `db copy --to ingitdb://./snap` followed by `db copy --from ingitdb://./snap --to <other>` is the "dump and load" workflow.
- **Resumability after partial failure** — per REQ:partial-failure-leaves-state, the user reruns with `--overwrite`. There is no checkpoint/resume protocol.
- **Per-engine bulk-load optimizations** — `COPY FROM STDIN` (PostgreSQL), prepared-statement batching with explicit transactions for throughput, drop-indexes-bulk-load-recreate dance. Correctness via DALgo abstractions is the MVP bar; performance work is a follow-up.
- **Remote `ingitdb://` URLs** — `ingitdb://github.com/owner/repo` is rejected. MVP is local-filesystem only.

## Assumption Carryover

From the source Idea:

| Idea assumption | Status |
|---|---|
| Must-be-true: DALgo DDL surface ships and covers CREATE TABLE, CREATE INDEX, primary-key declaration across `dalgo2sql` and `dalgo2ingitdb` | **Resolved.** DDL Feature batch shipped (`dal-go/dalgo/spec/features/ddl/` Implemented). Plan-time work: verify `dalgo2sql` + `dalgo2ingitdb` coverage. |
| Must-be-true: `dalgo2ingitdb` covers the read/write surface needed (list tables, stream rows, write rows with a known key shape) | Carried; validated by E2E at plan time. |
| Must-be-true: Cross-engine type mapping for the MVP triplet is tractable as a small, deterministic table | Carried; encoded in REQ:type-mapping-coverage. |
| Should-be-true: Streaming with bounded memory works for projects up to ~1M rows | Carried; not a REQ contract (perf concern) — documented as a plan-time benchmark. |
| Should-be-true: A URL scheme covers all MVP endpoints cleanly through `xo/dburl` or an extension | Carried; encoded in REQ:supported-schemes. |
| Should-be-true: Parallel per-table streaming is safe for `dalgo2ingitdb` | Open. Until proven, `dalgo2ingitdb` SHOULD advertise `Concurrency() == 1` via `ConcurrencyAware`, which triggers the cap-to-1 rule for any copy where `dalgo2ingitdb` is the target. Stress-testing is plan-time work. |
| Might-be-true: Users will eventually want incremental/delta sync | Deferred (Out of Scope). |
| Might-be-true: Resumability after partial failure is needed in MVP | Deferred (REQ:partial-failure-leaves-state codifies the no-resume contract). |

## Acceptance Criteria

**Note:** All ACs in this section assume NO filtering flags are present (no `--include`, `--exclude`, `--where`, `--limit`, or `--filter-config`). Subsetting behavior is specified by the [`filtering` sub-feature](filtering/README.md) (REQ:copy-acs-no-filter-baseline there). Column-subsetting flags (`--columns`, `--exclude-columns`, `--exclude-columns-global`) are deferred from MVP — see the filtering sub-feature's Out of Scope.

### AC: missing-from-rejected

**Requirements:** copy#req:required-flags

**Given** any working directory
**When** the user runs `datatug db copy --to sqlite:///tmp/out.db` (no `--from`)
**Then** the command exits `2` and stderr names `--from` as the missing required flag.

### AC: missing-to-rejected

**Requirements:** copy#req:required-flags

**Given** any working directory
**When** the user runs `datatug db copy --from sqlite:///tmp/in.db` (no `--to`)
**Then** the command exits `2` and stderr names `--to` as the missing required flag.

### AC: sqlite-to-ingitdb-chinook-roundtrip

**Requirements:** copy#req:supported-schemes, copy#req:source-schema-via-dbschema, copy#req:target-schema-via-ddl, copy#req:bounded-memory-streaming, copy#req:row-insert-via-dalgo

**Given** a SQLite database at `./chinook.db` containing the Chinook fixture and an empty directory at `./out-project/`
**When** the user runs `datatug db copy --from sqlite:///./chinook.db --to ingitdb://./out-project`
**Then** the command exits `0`; every Chinook table has a corresponding collection at `./out-project/<table>/`; every row from each source table is present in the target collection; primary-key columns survive (each record's key matches the source row's PK value).

### AC: ingitdb-to-sqlite-chinook-roundtrip

**Requirements:** copy#req:supported-schemes, copy#req:source-schema-via-dbschema, copy#req:target-schema-via-ddl, copy#req:bounded-memory-streaming, copy#req:row-insert-via-dalgo

**Given** an inGitDB project at `./chinook-project/` (the output of the SQLite→inGitDB direction) and a non-existent file path at `./out.db`
**When** the user runs `datatug db copy --from ingitdb://./chinook-project --to sqlite:///./out.db`
**Then** the command exits `0`; `./out.db` exists and contains every source table with row counts matching the inGitDB project; an `INSERT` round-trip back to the original schema is byte-for-byte stable on primary keys.

### AC: unknown-scheme-rejected

**Requirements:** copy#req:unknown-scheme-rejected

**Given** any working directory
**When** the user runs `datatug db copy --from mongodb://localhost:27017/test --to sqlite:///tmp/out.db`
**Then** the command exits `2`; no connection is attempted to either URL; stderr contains the substring `mongodb` AND lists `sqlite`, `postgres`, `ingitdb` as the supported schemes.

### AC: remote-ingitdb-rejected

**Requirements:** copy#req:ingitdb-url-local-only

**Given** any working directory
**When** the user runs `datatug db copy --from sqlite:///tmp/in.db --to ingitdb://github.com/owner/repo`
**Then** the command exits `2`; stderr names "local paths only" for the `ingitdb://` scheme.

### AC: source-empty-exits-zero

**Requirements:** copy#req:source-introspection-failure

**Given** a fresh SQLite database with no tables at `./empty.db` and an empty directory at `./out-project/`
**When** the user runs `datatug db copy --from sqlite:///./empty.db --to ingitdb://./out-project`
**Then** the command exits `0`; `./out-project/` is unchanged (no collections created); stderr contains the substring "source has no tables; nothing to copy".

### AC: connection-failure-exits-4

**Requirements:** copy#req:source-introspection-failure

**Given** a SQLite file path `./does-not-exist.db` that the filesystem does not have
**When** the user runs `datatug db copy --from sqlite:///./does-not-exist.db --to sqlite:///tmp/out.db`
**Then** the command exits `4`; stderr names the source URL and the underlying error (e.g. "no such file or directory").

### AC: concurrency-cap-warns-on-explicit-request

**Requirements:** copy#req:concurrency-cap

**Given** a SQLite source and a SQLite target (where `dalgo2sql` SQLite adapter advertises `Concurrency() == 1` for writes)
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db --parallel-streams=8`
**Then** the command runs successfully (single-threaded under the hood); stderr contains exactly one warning line naming `sqlite (target)`, the requested value `8`, and the effective value `1`.

### AC: concurrency-cap-silent-on-default

**Requirements:** copy#req:concurrency-cap

**Given** a SQLite source and a SQLite target
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db` (no `--parallel-streams` flag)
**Then** the command runs successfully; the implicit default `runtime.NumCPU() - 1` is silently capped to `1`; NO concurrency warning appears on stderr (only `--progress` output, if enabled).

### AC: overwrite-bare-rejected

**Requirements:** copy#req:overwrite-values

**Given** any working directory
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db --overwrite` (no value)
**Then** the command exits `2`; stderr names the two valid values `recreate` and `reload`.

### AC: overwrite-bogus-rejected

**Requirements:** copy#req:overwrite-values

**Given** any working directory
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db --overwrite=merge`
**Then** the command exits `2`; stderr names the offending value `merge` AND lists `recreate, reload` as the valid options.

### AC: recreate-drops-source-tables-only

**Requirements:** copy#req:recreate-drops-first

**Given** a SQLite target at `./out.db` containing two tables: `users` (also in source) and `audit_log` (not in source)
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db --overwrite=recreate` where `--from` has only the table `users`
**Then** the command exits `0`; on `./out.db` after copy, `users` exists with source data; `audit_log` is unchanged (table + rows still present).

### AC: reload-rejects-schema-mismatch

**Requirements:** copy#req:reload-schema-match

**Given** a target SQLite database with table `users(id INTEGER PRIMARY KEY, email TEXT)` and a source SQLite with table `users(id INTEGER PRIMARY KEY, email VARCHAR(255), age INTEGER)`
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db --overwrite=reload`
**Then** the command exits `1` BEFORE any TRUNCATE; stderr names the table `users` and the missing column `age` on the target; the target's existing rows are unchanged.

### AC: reload-accepts-superset-target

**Requirements:** copy#req:reload-schema-match

**Given** a target SQLite database with table `users(id INTEGER PRIMARY KEY, email TEXT, extra_internal_flag BOOLEAN)` and a source SQLite with table `users(id INTEGER PRIMARY KEY, email TEXT)`
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db --overwrite=reload`
**Then** the command exits `0`; the target's `users` table is truncated, source rows reloaded, `extra_internal_flag` remains a column on the target and its values for the reloaded rows are NULL (its default).

### AC: empty-target-with-source-named-rows-rejected

**Requirements:** copy#req:empty-target-check

**Given** a target SQLite database with table `users` containing 5 rows and a source SQLite with table `users` (any row count)
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db` (no `--overwrite`)
**Then** the command exits `1`; stderr names the table `users` and its target row count `5`; stderr suggests `--overwrite=recreate` or `--overwrite=reload`. No target write occurs.

### AC: empty-target-with-unrelated-tables-accepted

**Requirements:** copy#req:empty-target-check

**Given** a target SQLite database with table `audit_log` containing 100 rows and a source SQLite with table `users` (no `audit_log` table in source)
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db`
**Then** the command exits `0`; the target gains a `users` table populated from source; `audit_log` is unchanged.

### AC: empty-target-with-source-named-empty-tables-accepted

**Requirements:** copy#req:empty-target-check

**Given** a target SQLite database with table `users` (already exists but contains 0 rows) and a source SQLite with table `users` (containing rows)
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db`
**Then** the command exits `0`; the existing empty `users` table receives the source rows (no overwrite needed because the per-source-table presence-with-rows check counts empty as "okay to write").

### AC: progress-silent-by-default

**Requirements:** copy#req:progress-reporting

**Given** any valid source/target pair
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db` (no `--progress` flag)
**Then** on successful completion, stdout is empty AND stderr contains NO per-table progress lines. (Errors, warnings, and exit messages are NOT suppressed by this rule.)

### AC: progress-flag-emits-per-table-lines

**Requirements:** copy#req:progress-reporting

**Given** a source with two tables `users` (10 rows) and `orders` (50 rows)
**When** the user runs `datatug db copy --from sqlite:///./in.db --to sqlite:///./out.db --progress`
**Then** stderr contains four lines: two `"copying <name> (est. <N> rows)…"` lines (one per table) AND two `"copied <name>: <M> rows in <duration>"` lines. stdout remains empty.

### AC: partial-failure-leaves-completed-tables

**Requirements:** copy#req:partial-failure-leaves-state

**Given** a source with three tables `a`, `b`, `c` where the row insert into `b` is configured to fail mid-stream (e.g. a NOT NULL violation injected by a fault-injection harness)
**When** the user runs `datatug db copy --from ... --to ...`
**Then** the command exits `1`; on the target after exit, `a` is fully copied (every source row present); `b` exists (table created) and has between 0 and len(`b`)-1 rows (the partial state at the moment of failure); `c` does NOT exist on the target (its worker never started).

## Open Questions

- **Type-mapping table contents.** The Idea references "a small, deterministic table without per-table user overrides" for the MVP triplet. The table's exact contents (which SQLite affinity maps to which PostgreSQL type, how `BLOB` round-trips through inGitDB, etc.) is plan-time work — REQ:type-mapping-coverage pins only the coverage bar (every Chinook column type, all six directed pairs). Resolve at plan time.
- **`dalgo2sql` and `dalgo2ingitdb` capability verification.** Both drivers need to be confirmed to implement the DALgo `dbschema.Adapter`, `ddl.Applier`, and `ConcurrencyAware` interfaces shipped in the prior Feature batches. Plan-time audit; if either driver is missing a method, that's a scoped follow-up against the corresponding driver, not a blocker for this Feature's interface design.
- **`ingitdb://` URL parser registration with `xo/dburl`.** `dburl.Register` may or may not exist as a public API. Plan-time: confirm; if not, wrap `dburl.Parse` with a small dispatcher in `pkg/dbcopy/url.go` that intercepts `ingitdb://` before delegating.
- **`dalgo2ingitdb` parallel-write safety.** Per the Idea's Open Question and REQ:concurrency-cap's plan-time note: until stress-testing proves otherwise, `dalgo2ingitdb` SHOULD advertise `Concurrency() == 1` for writes. The stress test itself is plan-time work in the `ingitdb-cli` repo, not this Feature.
- **Progress line format on stderr.** REQ:progress-reporting pins the contract ("one start line, one finish line, named substrings"); the exact `time.Duration` format and the row-count `est.` heuristic for sources that don't cheaply expose row counts are plan-time decisions.

---

*This document follows the https://specscore.md/feature-specification*
