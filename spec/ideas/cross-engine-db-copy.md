# Idea: Cross-Engine Database Copy Command

**Status:** Approved
**Date:** 2026-05-12
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we let users copy any DALgo-supported database into any other DALgo-supported target — including DataTug's own Git-backed project format (inGitDB) — so a single command moves data between SQLite, PostgreSQL, and inGitDB?

## Context

DataTug already scans live databases into versioned project files and exposes the inverse through the UI, but there is no commandable path that moves table data from one engine to another. Today, moving a SQLite fixture into an inGitDB-backed project (or the reverse) requires custom code per pair. End-to-end tests need a deterministic SQLite↔inGitDB seeding step, users need a portable backup story that survives the engine they happen to use, and the underlying DALgo abstraction is the obvious seam to do this through.

Two facts shape the work: (1) inGitDB already has a DALgo driver at [`ingitdb/ingitdb-cli/pkg/dalgo2ingitdb`](https://github.com/ingitdb/ingitdb-cli/tree/main/pkg/dalgo2ingitdb), so the "does the backend exist" question is settled — only its coverage of the read/write surface we need must be verified. (2) DALgo (`dal-go/dalgo`) is co-maintained with DataTug, so the missing schema-modification (DDL) surface (CREATE TABLE, CREATE INDEX, primary-key declaration) can be added directly upstream rather than shimmed locally; that DDL extension is a sibling Idea, not a fork-and-pray dependency.

## Recommended Direction

Ship `datatug db copy --from <url> --to <url>` as the primitive: one streaming command that reads any DALgo backend and writes to any DALgo backend, auto-creating the target schema by introspecting the source. Tables stream in parallel; `--parallel-streams` defaults to `runtime.NumCPU() - 1` and is capped at `1` whenever either the source or the target DALgo driver advertises that it does not support concurrent connections (e.g. SQLite as writer). Endpoints are URLs — `sqlite:///path.db`, `postgres://…`, `ingitdb://<local-path>` (the `ingitdb://` scheme dispatches to `dalgo2ingitdb`; MVP supports local-filesystem paths only — remote Git URLs are out of scope). A key simplification: inGitDB becomes the canonical intermediate format. Users wanting a 'dump then load' workflow do `db copy --to ingitdb://./snap` followed by `db copy --from ingitdb://./snap --to <target>` — no separate dump-file format is invented, and no project-pinned `export`/`import` wrappers are added (the primitive's symmetric form is sufficient). Conflict on a non-empty target requires explicit `--overwrite=recreate` (drop tables and recreate from source schema) or `--overwrite=reload` (truncate tables and reload data, preserving schema); a bare `--overwrite` is rejected. MVP commits to three backends end-to-end-tested: SQLite, inGitDB (via `dalgo2ingitdb`), PostgreSQL. The DALgo DDL extension is a hard, blocking dependency; DataTug and DALgo are co-maintained, so sequencing is **spec-and-prototype-first**: the DDL Feature is specified in `dal-go/dalgo`, a working prototype lands on a DALgo branch, and `datatug-cli` activates its existing `replace` directive (`//replace github.com/dal-go/dalgo => ../../dal-go/dalgo`) to consume that branch while implementing `db copy`. DALgo merges and tags when both sides are happy; this repo removes the `replace` and depends on the tagged release.

## Alternatives Considered

- **Two-stage `dump` + `load` with a bespoke dump-file format.** Closer to `pg_dump`/`pg_restore`. Lost because inGitDB already gives us a versionable, human-inspectable, file-based intermediate for free — inventing a second format adds surface area without adding capability. Users who want a stop-and-inspect workflow can still get it by routing through `ingitdb://`.
- **Project-only export/import (no generic `db copy`).** Lost because it doesn't satisfy the user's stated "any DALgo source → any DALgo target" goal. SQLite↔PostgreSQL transfers would need a separate code path. The primitive's symmetric `--from/--to` form already covers project-flavored workflows (`db copy --from ingitdb://./current-project --to <target>` is the export use case, and the reverse is the import use case).
- **Defer schema auto-creation; require pre-provisioned targets.** Decouples the work from the DALgo DDL extension. Lost because the E2E-test motivation collapses without it — every fixture would need a hand-rolled schema-setup step, defeating the point of a seed command. The DDL extension is the work; avoiding it just postpones it.

## MVP Scope

A focused spike landing `datatug db copy --from <url> --to <url>` with three E2E-tested backends (SQLite, inGitDB, PostgreSQL) and the minimum DALgo DDL extension required for auto-target-schema. Configurable parallel streaming is a stretch goal on top of the primitive. If the primitive ships and only one direction (e.g. SQLite→inGitDB) is wired for E2E, that is still a shippable MVP — the contract is set, additional backends fill in.

## Not Doing (and Why)

- Incremental/delta sync — MVP is one-shot full copy
- In-flight row filters, column mappings, or anonymization — separate concern, do not bundle
- Conflict resolution on a non-empty target — MVP requires empty target or explicit --overwrite=recreate|reload
- All DALgo backends in MVP — only SQLite, inGitDB, and PostgreSQL are E2E-tested; others remain best-effort
- Schema diffing or migration — target schema is auto-created from source, not reconciled with an existing target schema
- A bespoke dump-file format — inGitDB serves as the canonical inspectable, versionable intermediate

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | DALgo's new DDL surface (CREATE TABLE, CREATE INDEX, primary-key declaration) can be designed once and implemented across `dalgo2sql` (SQLite, PostgreSQL) and `dalgo2ingitdb` (inGitDB) with reasonable parity. Tracked as a sibling Idea in `dal-go/dalgo`. | Design the interface in the sibling Idea; prototype against `dalgo2ingitdb` first (lowest-cost backend) and `dalgo2sql` for SQLite; confirm primary-key + indexable-field semantics survive both. |
| Must-be-true | `dalgo2ingitdb` (the inGitDB DALgo backend) covers the read and write surface this command needs — listing collections/tables, streaming rows, writing rows with a known key shape. | Read-walk and write-walk a representative fixture (Chinook) end-to-end via `dalgo2ingitdb` before MVP starts; record any missing operations against `dalgo2ingitdb` as scoped follow-ups. |
| Must-be-true | Cross-engine type mapping for the MVP triplet (SQLite ↔ PostgreSQL ↔ inGitDB) is tractable enough to encode in a small, deterministic table without per-table user overrides. | Enumerate the type matrix on a representative fixture (e.g. Chinook) and walk it end-to-end for all six directed pairs. |
| Should-be-true | Streaming with bounded memory works for projects up to ~1M rows on a developer laptop in under a few minutes for the MVP backends. | Benchmark on Chinook scaled 100×; record wall-time and peak RSS. |
| Should-be-true | A URL scheme covers all MVP endpoints cleanly (`sqlite:///…`, `postgres://…`, `ingitdb://./…`) and round-trips through `xo/dburl` or an extension thereof. | Prototype the URL parser early; confirm `ingitdb://` registers without forking the dependency. |
| Should-be-true | Parallel per-table streaming is safe for inGitDB (file-level writes don't deadlock or corrupt the Git working tree). | Stress test with 16 concurrent tables; verify resulting tree matches a serial-baseline run byte-for-byte. |
| Might-be-true | Users will eventually want incremental/delta exports. | Defer; revisit after MVP usage data. |
| Might-be-true | Resumability after partial failure is needed in MVP. | Defer; MVP is "rerun from scratch" with an empty/`--overwrite` target. |


## SpecScore Integration

- **New Features this would create:**
  - `spec/features/cli/db/copy/` — primitive `datatug db copy --from <url> --to <url>` command contract.
- **Existing Features affected:**
  - `spec/features/cli/db/` — gains a `copy` subcommand; the `db` feature index updates.
  - `spec/features/cli/scan/` — conceptually adjacent (scan writes project metadata; copy moves table data). Cross-reference but no contract change.
  - `spec/features/cli/init/` — unaffected; targets for `copy` may need a pre-existing project at the `ingitdb://` URL or auto-init behavior to be specified.
- **Dependencies:**
  - **`dal-go/dalgo` DDL extension** — sibling Idea at `dal-go/dalgo/spec/ideas/dalgo-schema-modification.md` (co-maintained, lands upstream — no local shim needed). Hard, blocking.
  - **`dal-go/dalgo` concurrency capability** — sibling Idea at `dal-go/dalgo/spec/ideas/concurrency-capability.md`. Needed for the `--parallel-streams` cap-to-1 rule; soft-blocking (the consumer could ship with a hard-coded engine table until this lands, but the table is exactly what the Idea exists to remove).
  - **`dalgo2ingitdb`** (lives in `ingitdb/ingitdb-cli/pkg/dalgo2ingitdb`) as the inGitDB DALgo driver — already exists; only coverage verification required.
  - **inGitDB default file format** — sibling Idea at `ingitdb/ingitdb-cli/spec/ideas/default-record-format.md` (proposes INGR as the default). Informational only — `db copy` writes via `dalgo2ingitdb` and inherits whatever format the project is configured for.
  - **URL scheme support** — register `ingitdb://` (likely via an extension to `xo/dburl` or a local resolver layer).

## Open Questions

- **Backend concurrency-capability surface in DALgo.** Tracked as a sibling DALgo Idea: [`dal-go/dalgo/spec/ideas/concurrency-capability.md`](https://github.com/dal-go/dalgo/blob/main/spec/ideas/concurrency-capability.md) (proposes a single optional `dal.ConcurrencyAware` interface). Shape is open in that Idea; this Idea consumes whatever it produces.
- **`dalgo2ingitdb` parallel-write safety.** SQLite is known unsafe-for-concurrent-writers; PostgreSQL is fine; `dalgo2ingitdb` writing many table files in parallel to a single working tree is unproven. Stress-test before claiming `dalgo2ingitdb` is concurrent-safe; until then it advertises `concurrent=false`.

---
*This document follows the https://specscore.md/idea-specification*
