# Idea: datatug db snapshot command

**Status:** Approved
**Date:** 2026-05-14
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we make point-in-time database snapshots a one-command operation that resolves connection strings from a DataTug project and produces a versionable, inspectable artifact?

## Context

`datatug db copy --from <url> --to <url>` (spec/features/cli/db/copy/, Implemented) already moves table data across DALgo backends, and its spec explicitly designates inGitDB as the canonical 'dump and load' intermediate. But every snapshot use case (dev fixture refresh, CI release backup, QA env reset, DBA portable backup) starts with the same friction: a user already has `--project`, `--db`, and `--env` IDs in their DataTug project config, and they don't want to translate that into a connection string by hand each time. The existing `scan` command established the `--project/--db/--env` flag triple as the project-lookup addressing convention (apps/datatugapp/commands/cmd_scan_db.go). No symmetric command exists for the 'capture a snapshot from this addressed DB' direction. This Idea closes that gap by adding a thin wrapper, `datatug db snapshot`, that resolves project addressing to a DALgo URL, picks a sensible destination path, and delegates the actual copy to `db copy` underneath.

## Recommended Direction

Ship `datatug db snapshot` as a sibling subcommand under `db` that does three things and nothing more: (1) resolves the source DB via either `--project/--db/--env` (project addressing, consistent with `scan`) or `--from <url>` (raw DALgo URL, consistent with `db copy`) — passing both is rejected at exit 2; (2) writes to a local inGitDB tree by default, with `--to <url>` as an optional override for an alternative snapshot-storage destination (any DALgo URL `db copy` accepts) — in project-addressing mode the default path is `<datatug-project-dir>/snapshots/<db>/<env>/<timestamp>/`, and in raw `--from <url>` mode the default is `./snapshots/<timestamp>/`; (3) invokes `datatug db copy` underneath with the resolved source and target plus any subset/filter options supplied via `--config <path>`. All subset semantics (table include/exclude, row WHERE filters) belong in `db copy` as a primitive capability — this Idea has a hard sibling dependency on a forthcoming `db-copy-filtering` Idea, and snapshot's config-file schema is a thin forwarder for whatever `db copy` accepts. Multi-env (`--env=dev,qa,uat`) is rejected at parse with exit 2 in MVP; the rejection message names the future direction so users know it is forthcoming. This Idea is intentionally narrow: it adds project addressing + default-destination convention + config-file forwarding on top of an existing primitive. The reverse direction (`db restore`) is out of scope and will be a separate Idea.

## Alternatives Considered

- **No new command — add `--snapshot-to <dir>` to `db copy`.** Closer to "smallest possible change". Lost because the *value* of this Idea is project/db/env addressing, which doesn't belong on the primitive — `db copy` deliberately stays URL-in/URL-out. A flag-on-primitive design would force every snapshot user to manually translate project IDs into connection strings anyway.
- **Owning subset/filter logic inside `db snapshot` (not `db copy`).** Makes snapshot a "fat" command with table include/exclude and WHERE-clause filters of its own. Lost because filters are a property of *copying*, not of *snapshotting* — if `db copy` ever needs them (e.g. for a future `db restore` flow, or for production-to-staging seeding without a snapshot intermediate), the logic gets duplicated. Cleaner factoring: `db copy` learns filters once via a sibling Idea, snapshot forwards them.
- **Config-file-only surface (no CLI flags for addressing or destination).** Every invocation is `datatug db snapshot --config file.yaml`. Lost because the smallest happy path — "snapshot dev right now" — should not require writing a YAML file. Flags-with-optional-config-overlay gives both quick CLI use and reproducible CI use.
- **Pair this with a symmetric `db restore` command in the same Idea.** Feels architecturally complete. Lost because `db copy --from ingitdb://./snap --to <target>` already performs restore today via the primitive, and a `db restore` convenience command can wait until single-direction usage proves out. Splitting it into a future Idea keeps this one tight.

## MVP Scope

A two-week spike landing `datatug db snapshot --project X --db Y --env Z` end-to-end against Chinook: project-lookup resolves to a DALgo URL, snapshot writes a timestamped inGitDB tree under the default destination `<datatug-project-dir>/snapshots/<db>/<env>/<timestamp>/` in under 10 seconds, an optional `--to <url>` overrides the default with any `db copy`-supported target URL, and a `--config <path>` flag that supplies include/exclude lists is honored (depends on the sibling `db-copy-filtering` Idea shipping the filter primitives in `db copy`). One env per invocation. Mutual exclusion of `--from` and project flags enforced with exit 2. No restore command, no retention, no multi-env.

## Not Doing (and Why)

- Multi-env fan-out (`--env=dev,qa,uat`) — MVP rejects comma-separated lists at parse with exit 2; revisit when single-env usage is solid
- Row anonymization / data masking — separate concern, do not bundle
- Scheduled or cron-driven snapshots — external scheduling is the unix way, not the CLI's job
- Retention policy / pruning (`--keep N`) — out of MVP, user manages disk themselves
- Encryption at rest — out of scope, rely on filesystem-level encryption
- Owning subset/filter logic — filters live in `db copy` per the sibling `db-copy-filtering` Idea, snapshot only forwards via config
- A bespoke dump format — inGitDB tree is the canonical intermediate, inherited from `db copy`'s Out of Scope
- Symmetric `db restore` command — separate future Idea, `db copy --from ingitdb://./snap --to <target>` already does restore today via the primitive
- Remote `ingitdb://` URLs — inherited from `db copy`'s constraint, local filesystem only

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | `db copy` learns subset/filter primitives (table include/exclude, WHERE-clause row filters) in a sibling Idea (`db-copy-filtering`, not yet filed) before snapshot's config-file forwarding can work end-to-end. | File the sibling Idea before promoting this one to Feature; spec the flag surface there; gate snapshot's config-file ACs on the sibling Idea reaching Approved. |
| Must-be-true | The DataTug project config exposes a deterministic `(project, db, env) → DALgo URL` resolver for at least the engines listed in `db copy`'s supported schemes (`sqlite`, `ingitdb`). | Walk the existing `projectBaseCommand.initProjectCommand` flow (apps/datatugapp/commands/project_base_command.go) and the storage filestore layer; confirm a single function exposes the URL for a given triple; verify against the demo Chinook project. |
| Must-be-true | The default destination paths (`<datatug-project-dir>/snapshots/<db>/<env>/<timestamp>/` in project mode, `./snapshots/<timestamp>/` in raw `--from` mode) do not collide with files DataTug's existing storage layer writes inside the project directory or with paths a user is likely to already have in CWD. | Inspect the demo projects under `datatug-demo-projects/` for an existing `snapshots/` directory; if absent, the namespace is free; if present, rename or nest under a non-colliding prefix at plan time. |
| Should-be-true | The config-file schema for include/exclude/where can stay flat and small (≤ ~10 top-level keys), mirroring the `db copy` filter flags 1:1 without inventing new layers. | Sketch the `latest_customers.yaml` example end-to-end; if the schema cannot be expressed as a literal `--include`/`--exclude`/`--where` mapping, escalate to the sibling `db-copy-filtering` Idea before this MVP. |
| Should-be-true | Bounded-memory streaming inherited from `db copy` is fast enough that a full-Chinook snapshot from a local source to a local inGitDB target completes in under 10 seconds. | Time the existing `db copy` SQLite→inGitDB E2E test; if it's already in budget, snapshot inherits the budget for free. |
| Should-be-true | A timestamp format derived from `time.Now().UTC()` and rendered as `2006-01-02T15-04-05Z` is filename-safe across the three supported OSes (Linux, macOS, Windows) AND sorts lexically by chronology. | Spot-check the rendered form on each OS; verify lexical sort matches chronological sort over 100 generated samples. |
| Might-be-true | Users will eventually want `db restore` as the named inverse, beyond the implicit "`db copy --from ingitdb://./snap`" form. | Defer; revisit after snapshot usage data shows the friction of typing the inverse `db copy` line. |
| Might-be-true | Multi-env fan-out (`--env=dev,qa,uat`) will be a common-enough request to add as a follow-up. | Defer; the MVP rejection message names the future direction, so demand will surface as user reports. |
| Might-be-true | Auto-commit-to-git when `--to` lands inside an existing git working tree is desirable behavior. | Defer; MVP writes files only, leaves git interaction to the user. |


## SpecScore Integration

- **New Features this would create:**
  - `spec/features/cli/db/snapshot/` — sibling subcommand under `db`, alongside `copy`. Owns the project-addressing resolver, the default-destination naming convention, and the `--config <path>` forwarder.
- **Existing Features affected:**
  - `spec/features/cli/db/README.md` — gains a `snapshot` row in its Subcommands table.
  - `spec/features/cli/db/copy/README.md` — becomes a hard dependency; this Idea calls `db copy` underneath. No contract change to `copy` itself for this Idea, but the sibling `db-copy-filtering` Idea (below) extends it.
  - `spec/features/cli/scan/README.md` — informational cross-reference; both commands use the `--project/--db/--env` triple, and their flag descriptions should stay aligned.
- **Dependencies:**
  - **`db-copy-filtering` (new sibling Idea, not yet filed).** Hard, blocking for the config-file forwarding bar. Adds `--include`, `--exclude`, and `--where <table>:<predicate>` (or equivalent) flags to `db copy`. snapshot's `--config <path>` parses YAML into those flags. MUST be filed and Approved before this Idea promotes to Feature.
  - **`projectBaseCommand` reuse** (apps/datatugapp/commands/project_base_command.go). Existing code; verify it exposes the URL-for-triple resolver in a form callable from `dbCopyAction`'s peer command. Plan-time audit only.
  - **`db copy`'s `--from` URL parser** (`pkg/dbcopy.Parse`). Existing; snapshot calls it with the resolved URL once project lookup completes.

## Open Questions

- **Timestamp format.** ISO-8601 (`2026-05-14T13-22-05Z`, replacing `:` with `-` for cross-platform filename safety) vs compact (`20260514T132205Z`). The Recommended Direction picks ISO-8601-with-dashes; confirm at Feature-spec time.
- **Default `--to` path in raw `--from <url>` mode.** Direction picks `./snapshots/<timestamp>/` (CWD-relative). Alternatives: derive from source URL (`./snapshots/<source-db-name>/<timestamp>/`), or refuse to default outside project context. Confirm at Feature-spec time.
- **Where does the source-resolver function live?** Snapshot needs `(project, db, env) → DALgo URL`. Options: extend `projectBaseCommand`; add to `pkg/datatug-core/storage`; introduce a new `pkg/dbresolve`. Plan-time decision, not Idea-time.
- **Config-file YAML schema.** Concretely: top-level `include: [tables…]`, `exclude: [tables…]`, `where: {table: predicate}`. Confirm 1:1 with the sibling `db-copy-filtering` flag surface before specifying.
- **Multi-env rejection message wording.** The message MUST forecast the future flag so users know it's coming. Suggested: `"--env accepts a single environment in MVP; multi-env fan-out is a planned follow-up"`. Confirm at Feature-spec time.
- **Behavior when the resolved project-DB URL points at an unsupported DALgo scheme.** Fall back to a clear exit-2 "scheme not supported by db copy" message vs surfacing `db copy`'s native error. Likely the latter, but pin at Feature time.
- **`db restore` framing.** When the future Idea is filed, should it be `db restore` (verb-symmetric with snapshot) or `db apply` (broader semantic)? Out of scope here; flag for the future Idea's intake.

---
*This document follows the https://specscore.md/idea-specification*
