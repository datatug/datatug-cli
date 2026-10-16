---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: `datatug backup`

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/backup?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/backup?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/backup?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/backup?op=request-change) |

**Status:** Draft
**Source Ideas:** —
**Parent Feature:** [`cli`](../README.md)

## Summary

`datatug backup` is a command family for **named, repeatable backups** of a database that is a source of a DataTug project. A backup **profile** is a small file in the project that says what to back up (a project source, optionally filtered), where to keep it (a git remote, a local folder, later object storage) and how (incremental, additive, verification). A profile holds names of secrets, never secrets, so it is safe to commit and share.

```
datatug backup new <profile> [--project <id> | --dir <path>] --source <datatug://…|env:NAME> --to <destination> [options]
datatug backup list
datatug backup run <profile>... | --all
datatug backup point list <profile>
datatug backup restore <profile> --to <target-url> [--at <point> | --tombstones] [db copy filters and --overwrite]
datatug backup verify <profile> [--at <point> | --all-points] [--level structure|hashes|restore]
datatug backup prune <profile> [--apply]
```

A backup is **`db copy` plus a destination policy**, not a second engine: every run opens the source through the same code as [`db copy`](../db/copy/README.md) (URL and `datatug://` resolution, `env:NAME`, PostgreSQL preview gate, read-only session), applies the same filters, serialises records with the inGitDB writer of [`db export`](#boundary-with-db-export-and-db-copy), and adds what a copy does not have: history of restore points, change detection between runs, retention, locking and verification.

**Founder decisions this Feature encodes (2026-10-16):**

1. `export` stays a small local shortcut: `db copy --to ingitdb://<local dir>` (a plain path is allowed; the default is `./<name>.ingitdb`). It has no profiles, history or remote destinations.
2. Backup to cloud or remote storage belongs to the `backup` family, never to `export` or `db copy`.
3. A project can hold **several backup profiles** (different sources, destinations, filters or cadences).
4. Backups are **incremental** (a run stores only what changed) and **additive** (a run never removes anything already backed up; deleted rows stay recoverable) by default.

## Problem

`db copy` and `db export` make one full copy of a database and forget it. People who use them as a backup today must invent, per database: a place to keep copies, a naming scheme, a way to keep the credentials out of scripts, a way to tell what changed since the last copy, a way to get one deleted row back, and a way to prove that a copy can be restored. Each of those is where home-made backups fail silently. The usual tools are engine-specific (`pg_dump`, `pg_basebackup`, `.backup` for SQLite) or file-level (restic, borg), so none gives a record-level, reviewable history that can be restored into another engine — the thing DataTug's inGitDB format already makes cheap.

## What the CLI can and cannot do today (verified 2026-10-16 on `main` at `09d9dbdc`)

| Capability | Today | Source |
|---|---|---|
| Source schemes | `sqlite`, `ingitdb` (local), `postgres` (preview, `DATATUG_PREVIEW_POSTGRES=1`, read-only session), `http(s)` DataTug query projects, `openvaultdb`, and `env:NAME` holding any of them; `datatug://<project>/<db>[?env=…]` resolves a project source | `pkg/dbcopy/url.go` (`supportedSchemes`), `pkg/dbcopy/datatug_ref.go` |
| Destination schemes of `db copy` | `sqlite`, `ingitdb` (local paths only; remote refused, exit 2), `postgres` (preview), `bigquery` (create-only), `sqlscript` (a MySQL script file from a SQLite source, PR #421) | [db copy REQ:supported-schemes](../db/copy/README.md#req-supported-schemes) |
| Remote storage (git push, S3, GCS, Azure Blob) | **None.** No command writes to a git remote or object storage. go-git v5 is already a dependency (`--git=none\|stage\|commit` of [mutation-git-integration](../../mutation-git-integration/README.md)); no S3/GCS/Azure Blob SDK is | `go.mod`, `apps/datatugapp/commands/git_integration.go` |
| `db export` | `db export --from <url> --to ingitdb://<dir>`: `--to` is required, must be `ingitdb://`, must not exist; staged and published atomically; one native records file per collection (`--records-format json\|jsonl\|ingr\|csv\|yaml`); writes the `source_schema` block | `apps/datatugapp/commands/cmd_db_export.go`, `pkg/dbcopy/export_ingitdb.go` |
| Two inGitDB writers | `db export` uses the native exporter (`dbcopy.ExportInGitDB`, writes `source_schema`; no filters, no overwrite, destination must not exist; keyless rows get ordinal ids `row-%012d`); `db copy --to ingitdb://` writes through `dalgo2ingitdb` `InsertMulti` (no `source_schema`; filters, `--overwrite`, empty-target check). They produce different trees | same files, `pkg/dbcopy/engine.go` |
| Consistent source snapshot | Only inside `faithful.Copy` (SQLite/PostgreSQL pairs): a private `*sql.Tx`, PostgreSQL `REPEATABLE READ`, SQLite one read transaction ([REQ:faithful-snapshot](../db/copy/README.md#req-faithful-snapshot)). `ExportInGitDB` and the DALgo path read table by table through a `dal.DB` with no shared snapshot, and no `dal.DB` scoped to one transaction exists | `pkg/dbcopy/faithful/copy.go`, `pkg/dbcopy/export_ingitdb.go` |
| Restore fidelity from inGitDB | `db copy --from ingitdb://` rebuilds tables from `columns` only: foreign keys, checks, defaults and `source_schema` are not read back (follow-up A of PR #385) | PR datatug-cli#385 body |
| Flags of `db copy` from an `ingitdb://` source | `--json`, `--dry-run`, `--fail-on-warning` are refused (exit 2): they exist only for SQLite/PostgreSQL pairs and `sqlscript`. `--include/--exclude/--where/--limit` work | [db copy REQ:optional-flags](../db/copy/README.md#req-optional-flags) |
| Access policies | `db copy` and `db export` are not evaluated against `~/.datatug/policies/` (only `query run` and the viewers are) | `pkg/dbcopy/postgres_preview.go` comments |
| Database comparison | `datatug compare <left> <right> --project …` and the `pkg/dbcompare.Compare(ctx, leftName, left dal.DB, rightName, right dal.DB, options)` API: full row read, schema metadata, keyless tables as multisets, bounded memory through temporary SQLite staging | `pkg/dbcompare/compare.go`, [docs/database-compare.md](../../../../docs/database-compare.md) |
| `internal/crossverify` | On `main` since PR #421: the internal verifier of the `sqlscript` CI journey (exact value canon per column kind). Internal on purpose; its stated long-term home is an extension of `compare`, so `backup` uses it only through `compare` | `internal/crossverify/doc.go` |
| Incremental/delta copy, resumability | None: [db copy Out of Scope](../db/copy/README.md#out-of-scope) lists both | — |
| Secrets by reference | `env:NAME` sources; PostgreSQL project sources are descriptors that hold only `{"dsnEnv": "NAME"}` (names start with `DATATUG_` or are allow-listed in `DATATUG_DSN_ENV_ALLOW`); `dbcopy.RedactErrorWithSecrets` scrubs errors | [db add REQ:postgres-by-environment-variable](../db/add/README.md#req-postgres-by-environment-variable) |

## Boundary with `db export` and `db copy`

| | `db copy` | `db export` | `backup` |
|---|---|---|---|
| Purpose | Move a database into another engine, once | Write a local inGitDB copy, once | Keep a history of restore points |
| Destination | Any `--to` scheme | A local folder only | Git remote, local folder, later object storage |
| Configuration | Flags | Flags | A profile file in the project |
| History, retention, locking, verification | No | No | Yes |
| Engine | Itself | The inGitDB writer | `db copy`'s source side + the inGitDB writer + a destination store |

`db export` changes that the founder decision implies (a plain path for `--to`, default `./<name>.ingitdb`, being literally `db copy --to ingitdb://`) belong to a `cli/db/export` Feature, which does not exist yet; `backup` depends on the exporter being extended for backups (stage 1), not on the answer to [Q2](#open-questions).

## Behavior

### Profiles

#### REQ: profile-file

A profile MUST be a YAML file at `<project>/backups/<name>.yaml` with `format: datatug-backup-profile/v1`, where `<name>` is a [plain name](../scan/README.md#req-names-are-plain). The file holds:

```yaml
format: datatug-backup-profile/v1
source: datatug://chinook/chinook?env=prod   # or env:DATATUG_CHINOOK_PROD_URL
filter:                                      # optional; the db copy filter-config schema, verbatim
  exclude: [AuditLog]
destination:
  url: git+ssh://git@github.com/acme/chinook-backups.git
  branch: datatug-backup/chinook-prod        # default datatug-backup/<project>/<name>
  credentials: env:DATATUG_BACKUP_GH_TOKEN   # optional, https and object storage only (ssh uses ssh-agent); a reference, never a value
mode: incremental                            # incremental (default) | full
history: additive                            # additive (default) | windowed
records_format: jsonl                        # default jsonl for git, see REQ:deterministic-tree
require_consistent_snapshot: false
verify_after_run: hashes                     # none | structure | hashes (default) | restore
retention: {}                                # empty = keep every point (default)
```

Unknown keys MUST be refused (exit 2, naming the key), so a typo never silently changes a backup.

#### REQ: multiple-profiles

A project MAY hold any number of profiles. Two profiles MAY name the same source (for example a nightly git profile and a monthly folder profile) and MUST NOT share state: each has its own destination branch or prefix, lock, cache and point sequence. Two profiles of one project MUST NOT name the same destination branch or prefix; `backup new` and `backup run` MUST refuse it (exit 2) naming both profiles.

#### REQ: profile-holds-no-secret

A profile MUST NOT hold a credential. `source` MUST be a `datatug://` project-source reference or `env:NAME`; any other source form MUST be refused (exit 2), including a literal `postgres://` URL (it carries credentials) and a `sqlite://` path (register the file with [`db add`](../db/add/README.md) and name it by its `datatug://` reference, so the profile stays portable). `destination.url` MUST NOT hold userinfo, a password, a token, or a query parameter whose name contains `key`, `secret`, `token`, `password`, `signature` or `credential`. Credential fields MUST be `env:NAME` references following the [db add name rule](../db/add/README.md#req-postgres-by-environment-variable) (`DATATUG_` prefix or `DATATUG_DSN_ENV_ALLOW`), or be absent so the destination's default credential chain is used (ssh-agent and git credential helpers for git; the provider SDK chain for object storage, as BigQuery uses Application Default Credentials today). A refusal MUST name the field and MUST NOT echo its value.

#### REQ: profile-new

`backup new <name> [--project <id> | --dir <path>] --source … --to … [--branch …] [--mode …] [--history …] [--filter-config <file>]` MUST write the profile file and do nothing else: like [`db add`](../db/add/README.md#req-does-not-open-the-database) it MUST NOT open the source or the destination. It MUST refuse an existing profile name (exit 2) and accept `--git=none|stage|commit` from [mutation-git-integration](../../mutation-git-integration/README.md) so the new file can be committed to the project.

#### REQ: list-profiles

`backup list` MUST print every profile of the project with: name, source reference, destination display form (no credentials), mode, history, and — from the local cache only, never by contacting the destination — the last known point and the time of the last run on this machine. A profile that does not parse MUST be listed with `valid: false` and the reason, and the command MUST still exit `0` unless `--strict` is given (then `2`).

### Running a backup

#### REQ: run-reuses-db-copy

`backup run` MUST open the source through the same code `db copy` and `db export` use (`dbcopy.BackendRef` and its open paths, including `datatug://` resolution, `env:NAME`, the PostgreSQL preview switch and read-only session), MUST apply the profile's `filter` through the [db copy filtering](../db/copy/filtering/README.md) code (push-down, same validation, same exit `2` on a bad filter), and MUST serialise records with `dbcopy.ExportInGitDB`, extended for backup with: the profile's filters pushed down, writing a point into an existing tree (not only a new destination), deterministic output (REQ:deterministic-tree) and keyless ids derived from content rather than position. It MUST NOT contain its own source readers, type mapping or record serialiser. Whether `db copy --to ingitdb://` also moves to this writer is Q2; `backup` does not depend on that answer.

#### REQ: consistent-snapshot

A run MUST read every collection of the source in one consistent snapshot where the source supports it: PostgreSQL in one `REPEATABLE READ` read-only transaction, SQLite in one read transaction, the rules of [REQ:faithful-snapshot](../db/copy/README.md#req-faithful-snapshot). This needs a source `dal.DB` (or row reader) scoped to one transaction, which does not exist today (see [External dependencies](#external-dependencies)); until it lands every source is read as `consistency: none`. The point MUST record `consistency: snapshot` when it was read so. For a source without a shared snapshot (inGitDB, HTTP, OpenVaultDB) the point MUST record `consistency: none` and the run MUST emit a `no-consistent-snapshot` warning; with `require_consistent_snapshot: true` (or `--require-consistent-snapshot`) such a run MUST exit `1` before writing anything.

#### REQ: deterministic-tree

What a run writes for a point MUST be a pure function of the source state and the profile: the same source rows MUST produce byte-identical records and definition files. Records MUST be ordered by primary key (keyless collections: by the canonical record bytes) with one record per line in the default `jsonl` format, so a git diff of two points is a diff of records. Volatile values (capture times, engine version, tool version, durations, host) MUST live only in the point manifest, never in the inGitDB tree: when the inGitDB `source_schema` follow-up adds `capturedAt` and `engineVersion` to `source_definition_json`, the backup writer MUST leave them out of the tree and record them in the manifest. The order of keys is one comparator, the byte order of the JCS encoding of the key tuple; rows that a source returns in another order (a PostgreSQL locale collation, an unordered provider) MUST be re-sorted, never trusted.

#### REQ: record-hash

Each record's identity is its collection plus its primary-key tuple; its **record hash** is the SHA-256 of the RFC 8785 (JCS) canonical JSON of the record exactly as the inGitDB writer serialises it (storage-class tags such as `{"$blob":""}` included). The same canonicalisation MUST be used for the collection definition hash, matching `definitionSha256` of the inGitDB `source_schema` follow-up (PR datatug-cli#385, "Proposal, amended after the review", item 3) once it lands; until then the definition hash is the JCS hash of the `definition.yaml` the writer produces, with any capture time and engine version removed. Change detection of definitions MUST use this hash, never the file bytes.

#### REQ: change-detection-v1

Without any source-side change tracking, a run MUST: read the source in one snapshot (REQ:consistent-snapshot); compute every record hash; compare, per collection, against the predecessor point's state (key → hash) by a streaming merge on the key order; and write only added and changed records plus a **deletion marker** for each key present in the predecessor and absent now. This works for every DALgo source. A source that cannot return rows in key order MUST be sorted through bounded temporary storage (the temporary-SQLite staging `dbcompare` already uses), never in memory.

#### REQ: unchanged-run-writes-nothing

When no record, no collection definition and no collection set changed since the predecessor point, the run MUST NOT create a point, a commit or an object; it MUST report `status: unchanged`, the predecessor point id, and exit `0`.

#### REQ: keyless-collections

A collection without a primary key MUST be compared as a multiset of record hashes (as [`compare`](../../../../docs/database-compare.md) does). Its records are identified by their record hash (a duplicate row gets the hash plus an occurrence number), never by position, so an insert does not renumber every following row. When its multiset changed, the point MUST store the whole collection again and emit a `keyless-collection-rewritten` warning naming it; deletion markers are not written for keyless collections.

#### REQ: schema-changes-tracked

A point MUST store the collection definitions in force at that point. A changed definition (column added, removed, retyped, key changed) MUST make the point store the whole collection again, with a `schema-changed` note naming the collection and the definition hashes before and after. A collection that disappeared from the source MUST get a collection-level deletion marker; one that appeared is stored in full. Renames are a deletion plus an addition (no rename detection).

#### REQ: point-published-atomically

A point MUST become visible only after every collection and its manifest are durably written: for a git destination one commit pushed in one ref update; for a chain destination (folder, object storage) the manifest written last, as the commit marker. A run that fails or is interrupted MUST leave no visible point; what it already uploaded is unreferenced and is removed by `prune` or ignored by every other verb.

#### REQ: resumable-by-rerun

There is no checkpoint protocol. Re-running a failed or interrupted run MUST re-read the source in a new snapshot and MUST NOT upload again any content-addressed object (segment or definition) that already exists at the destination, so the cost of a retry after an interruption is the source read plus what had not yet been uploaded.

#### REQ: point-manifest

Every point MUST carry a manifest (`format: datatug-backup-manifest/v1`) with: point sequence number (predecessor + 1, the first point is `1`), point id (`<seq>-<UTC time YYYY-MM-DDTHH-MM-SSZ>`), predecessor point id and the SHA-256 of the predecessor's manifest bytes, profile name, source reference as written in the profile (never a resolved URL), source engine and engine version, consistency achieved, whether a filter applied (`partial: true`), per collection (row count, key columns, definition hash, a digest of the sorted record hashes, counts added/changed/deleted, the objects that hold them), totals, warnings, tool version, start and end times.

#### REQ: single-run-per-profile

At most one run of a profile MUST write at a time. Locally the run MUST take an exclusive lock in the CLI's per-user configuration folder (`os.UserConfigDir()/datatug/backup/locks/<project-id>/<profile>.lock`, where `datatug` already keeps per-user state). Across machines (two CI jobs) the destination write MUST be compare-and-swap: git pushes a fast-forward of the profile branch and never forces; a chain destination creates `points/<seq>` with create-only semantics (`If-None-Match: *` on S3 and Azure Blob, `ifGenerationMatch=0` on GCS, `O_EXCL` on a folder). The loser MUST make no point visible, report `status: locked` (or `status: conflict` when the destination moved under it) and exit `1`.

#### REQ: run-several-profiles

`backup run a b c` and `backup run --all` MUST run the named profiles one after the other, continue after a failed profile, report each, and exit with the highest exit code of the runs.

#### REQ: additive-by-default

`backup run` never deletes anything from the destination in any mode, so a run needs only create permissions and works against write-once storage (S3 Object Lock, GCS retention policies, Azure immutable blobs). The two `history` modes differ only in what `prune --apply` may lose:

- `additive` (the default): **every record ever backed up stays recoverable in its last stored version.** Prune MAY remove points (and with them intermediate versions of records that still exist), but before it removes the last point that holds a record which no kept point holds, it MUST write that record's last stored version, with the point id at which it was deleted, into the profile's **tombstone archive** (`tombstones/` objects for a chain; `.datatug-backup/tombstones/` in the tree of a git generation, REQ:git-retention-is-rotation). The archive is an inGitDB tree and is restored with `backup restore <profile> --tombstones --to <url>`. `prune` never removes archive entries in `additive` mode.
- `windowed`: prune keeps only the points the retention selects; a row deleted before the oldest kept point is gone. Chosen explicitly for data that must not be kept. Switching a profile to `windowed` makes the next `prune --apply` empty its tombstone archive too; rows in old git generations stay until the person deletes those branches, which the output says. Erasing one row from history is not supported (non-goal).

### Destinations

#### REQ: destination-git

A destination URL `git+ssh://…`, `git+https://…` or `git+file://…` is a git destination. Each profile owns one branch (REQ:profile-file); each point is one commit on it whose tree is an inGitDB project (the deterministic tree, REQ:deterministic-tree) plus `.datatug-backup/` (the manifest, and in a later generation the tombstone archive). Git provides incremental storage (unchanged files are the same blobs) and additive history natively; deletion markers are the removal of the record line, kept in history. The CLI MUST work from a cache clone in its per-user folder (`run` needs only the commits since the last trusted point, which it checks against the cache; `verify` fetches the whole branch), MUST treat as a point only a commit that changes `.datatug-backup/manifest.json` to the next sequence number and whose parent is the previous point (a generation's root commit, REQ:git-retention-is-rotation, has no parent and names its predecessor in `rebasedFrom`), and MUST refuse a profile branch holding any other commit (exit `1`, "branch holds commits that are not backups"); `verify --level structure` reports such a commit, and MUST NOT force-push, rebase or rewrite history.

#### REQ: git-file-size-cap

A records or definition file larger than the destination's file-size cap MUST fail the run before anything is pushed (exit `1`, naming the collection and its size). The default cap is 95 MB (GitHub refuses files over 100 MB); a profile MAY set a lower `destination.max_file_mb`. Splitting one collection over several files needs an inGitDB records layout that does not exist yet (see [External dependencies](#external-dependencies)); until it does, a database with a collection over the cap uses a chain destination.

#### REQ: destination-chain

A folder destination (`file:///abs/path`, `file:./rel/path`, or a plain path) and, in a later stage, object storage (`s3://bucket/prefix`, `gs://bucket/prefix`, `azblob://account/container/prefix`) store an explicit **snapshot chain**:

```
<prefix>/<profile>/
  points/<seq>.json                    # the manifest; created last, create-only
  objects/sha256/<2 hex>/<hash>.jsonl.zst  # content-addressed record segments and definitions
```

A point's segments hold, per collection, the records added or changed and the deletion markers, sorted by key, each line `{"k": <key>, "h": <record hash>, "op": "put"|"del", "v": <record>}`. Point 1, and any point after a definition change, holds the full collection. The state of point N is the merge of its chain back to the last full segment of each collection. All three backends implement one small store interface (put-if-absent, get, list by prefix, delete), so the folder backend is the test double of object storage.

#### REQ: chain-rebase

When a collection's chain since its last full segment exceeds 30 points or its delta bytes exceed half the full segment, the next run MUST also write a new full segment for it, computed from the chain (no extra source read). This bounds restore and diff cost without deleting anything.

#### REQ: local-state-cache

A run MAY cache the latest point's state index (key → record hash per collection) in the same per-user folder, keyed by profile and by the point's identity at the destination: the commit SHA for git, the SHA-256 of the manifest bytes for a chain. It MUST rebuild the cache from the destination when that identity is not the destination's latest point. `verify` MUST NOT use the cache; it always reads the destination.

#### REQ: rewritten-history-refused

When the destination's history no longer contains the point the cache last saw (a git branch moved by a non-fast-forward update, a chain manifest replaced or missing), `run` MUST NOT write and MUST exit `1` with `status: history-rewritten`, naming the last point it trusted. `verify` detects only what the destination shows by itself (broken predecessor hashes, sequence gaps, non-point commits); detecting a rewrite that left a self-consistent history needs the last trusted point, so `verify --since-trusted` MAY read it from the cache for that one check.

### Restore points

#### REQ: points-listing

`backup point list <profile>` MUST list the destination's points newest first with: point id, sequence, time, totals, counts added/changed/deleted, consistency, `partial`, warnings count. `--format json` gives the manifests' summary fields.

#### REQ: point-selector

`--at` MUST accept: `latest` (default), a sequence number, a point id, an RFC 3339 time (the latest point at or before it), or, for git destinations, a commit SHA or prefix of at least 7 hex digits. A selector that matches nothing MUST exit `3`; an ambiguous SHA prefix MUST exit `2` listing the candidates.

### Restore

#### REQ: restore-requires-target

`backup restore <profile> --to <target-url>` MUST require `--to` (exit `2` without it). There is no implicit target: restoring onto the profile's own source is allowed only by naming it in `--to` and passing `--overwrite=recreate|reload`, exactly as [db copy REQ:empty-target-check](../db/copy/README.md#req-empty-target-check) requires for any non-empty target.

#### REQ: restore-is-db-copy

A restore MUST materialise the selected point as a read-only inGitDB tree in a temporary folder (git: check out the commit; chain: merge the segments, streaming, in key order) and then run `db copy`'s transfer from it to `--to` as a library call, passing the `db copy` flags that apply to an `ingitdb://` source today: `--overwrite`, `--include`, `--exclude`, `--where`, `--limit`, `--filter-config`, `--progress`, `--parallel-streams`. `--format`/`--json`, `--dry-run` and `--fail-on-warning` belong to `backup restore` itself, because `db copy` refuses them for an `ingitdb://` source: the restore builds its own report (point, fidelity, per-table counts, the warnings the transfer returned, `status`, `exitCode`), `--dry-run` prints the point, tables, counts and target and opens no target, and `--fail-on-warning` exits `1` (REQ:exit-codes). The temporary folder MUST be removed when the command ends. Restore fidelity is therefore exactly `db copy --from ingitdb://`'s: today columns, keys and values; foreign keys, checks and defaults only after the inGitDB restore follow-up (faithful `ReadInGitDB`) lands. The report MUST say which fidelity applied.

#### REQ: restore-deletes

A restore of point N MUST produce the source as it was at point N: rows deleted after N are present, rows deleted before N are absent. A single deleted row is recovered by restoring an earlier point with a filter, e.g. `backup restore prod --at 41 --include Customer --where Customer:CustomerId:=:42 --to sqlite://./recovered.db`.

#### REQ: restore-postgres-preview

A restore to PostgreSQL and a run from PostgreSQL MUST obey the same preview switch as `db copy` (`DATATUG_PREVIEW_POSTGRES=1`; otherwise exit `4` with `db copy`'s fixed sentence, before any connection).

### Verification

#### REQ: verify-levels

`backup verify <profile> [--at <point> | --all-points] [--level …]` MUST check, by level (each includes the previous):

- `structure`: every manifest parses; sequence numbers are contiguous; each predecessor hash equals the SHA-256 of the predecessor's manifest bytes; every referenced object (or git commit and file) exists.
- `hashes` (default): recompute each stored record hash and each collection digest of the point(s) from the stored bytes and compare them, and the row counts, with the manifest.
- `restore`: materialise the point, restore it into a scratch SQLite file in a temporary folder through REQ:restore-is-db-copy, and compare the scratch database with the materialised tree through `pkg/dbcompare.Compare`; the result MUST be zero added, removed and changed records and the manifest's row counts. This level proves that the point can be restored through `db copy`; integrity of the stored bytes is the `hashes` level.

A failure MUST exit `1` and name the point, the collection and, for a record, its key (never its values). `verify` MUST NOT open the source; `--against-source` additionally compares the latest point with the live source through `dbcompare` and reports drift as information (the source may have moved on), never as a failure.

#### REQ: verify-after-run

`backup run` MUST run `verify` at the profile's `verify_after_run` level (default `hashes`) on the new point before reporting success. A verification failure after a point was published MUST exit `1` with `status: published-unverified` so CI does not treat the point as good.

### Retention

#### REQ: retention-explicit

Retention MUST only ever be applied by `backup prune <profile>`, which by default prints what it would remove and changes nothing; `--apply` removes it. The profile's `retention` (`keep_last`, `keep_daily`, `keep_weekly`, `keep_monthly`; empty = keep all) selects the points to keep. Prune MUST NOT remove the latest point or any object a kept point needs; for a chain it first writes the full segments a kept point would lose (REQ:chain-rebase), then removes unreferenced objects and unreferenced leftovers of interrupted runs.

#### REQ: git-retention-is-rotation

A git destination's history is never rewritten. Its `prune --apply` MUST instead start a new **generation**: a new branch `<branch>@<n>` whose first commit is the latest point (a full tree), recorded in the new manifest as `rebasedFrom`; the profile switches to it, and removing the old branch is left to the person (the CLI prints the command). Sequence numbers continue across generations (the new root is the next sequence number). Points of the old generation stay listed by `point list` while the old branch exists. Rotation frees storage only once the person deletes the old branch and the host garbage-collects it. In `additive` mode the new root carries the tombstone archive (REQ:additive-by-default).

### CI friendliness and output

#### REQ: no-scheduler

The CLI MUST NOT schedule, daemonise or poll. A backup runs when something runs `backup run`: a person, or a CI job triggered by an event (a release, a merged migration, `workflow_dispatch`). The documentation MUST show an event-triggered CI example and MUST NOT show a cron trigger.

#### REQ: non-interactive

No verb MAY prompt. Every verb MUST accept `--format text|json|yaml`, defaulting to `yaml` for `list` and `point list` (read commands, per the CLI convention) and to `text` for the others (`--json` is an alias of `--format json`, as in `db copy`); `--format json` MUST print one object on stdout also on failure (`status`, `exitCode`, `error`), as [db copy `--json`](../../../../docs/db-copy.md) does. `--progress` prints per-collection lines on stderr in `db copy`'s format.

#### REQ: exit-codes

Verbs MUST follow the [shared CLI exit codes](../README.md#shared-exit-code-contract): `0` done (including `unchanged`), `1` failed (run, verification, lock lost, conflict, size cap), `2` usage or invalid profile, `3` profile or point not found, `4` source or destination could not be reached. With `--fail-on-warning` a run or restore that finished with warnings MUST exit `1` with `status: failed-on-warning` (not `3`, which the shared contract gives to "not found"; `db copy` uses `3` for this, and a restore maps it to `1`).

### Security

#### REQ: no-secret-in-output

No profile, manifest, commit message, log line, report, temporary file name or error MUST contain a credential, a resolved connection URL or the value of an `env:` variable. Errors from drivers and SDKs MUST pass through `dbcopy.RedactErrorWithSecrets` with every resolved secret, as `db export` does.

#### REQ: backups-hold-data

A backup holds the source's rows. `backup new` MUST print, once, that the destination receives a copy of the data and that its access control is the person's responsibility. For a `github.com` git destination the run MUST refuse a repository that the GitHub API reports as public (exit `1`) unless the profile sets `destination.allow_public: true`; when visibility cannot be determined it MUST warn (`destination-visibility-unknown`).

#### REQ: same-access-as-db-copy

A backup reads what `db copy` reads with the same credentials, and, like `db copy` and `db export`, is not evaluated against the query access policies of `~/.datatug/policies/`: whoever can run it with the source's credentials can copy every row. A deliberately partial backup is expressed by the profile's `filter` and recorded as `partial: true`. Gating backups by policy is a non-goal of this Feature.

#### REQ: encryption-seam

Encryption at rest is not part of this Feature. The writer MUST pass every object through one `ObjectTransform` step (identity in v1) between serialisation and the store, so an encryptor keyed by an `env:`/KMS reference can be added without changing the chain format other than a manifest field naming the transform. Encrypting a git destination's tree defeats git's diffs and deduplication; an encrypted profile will therefore be a chain destination.

## Architecture

```
profile (project/backups/<name>.yaml)
        │
        ▼
 source open (dbcopy.BackendRef: datatug:// | env:NAME, preview gate, read-only)
        │  one snapshot (stage-4 reader for SQLite/PostgreSQL)
        ▼
 filter (db copy filtering, push-down) ──► inGitDB writer (deterministic, sorted, JCS record hashes)
                                                   │
                                  ┌────────────────┴───────────────┐
                                  ▼                                ▼
                   git store (cache clone, commit,      chain store (manifest + content-addressed
                   fast-forward push, size cap)         segments; folder | S3 | GCS | Azure Blob)
                                  │                                │
                                  └──────────────┬─────────────────┘
                                                 ▼
                         verify (structure/hashes; restore = materialise → db copy → dbcompare)
```

| Component | Responsibility | Reuses |
|---|---|---|
| Profile loader | Parse, validate, refuse secrets | `dbcopy.ValidEnvName`, `dbcopy.ParseDatatugRef`, the filter-config parser |
| Snapshot reader | One snapshot, ordered rows | `pkg/dbcopy` open paths, the transaction-scoped reader of stage 4 (new; `faithful`'s snapshot is private to `faithful.Copy`), `dbcompare` temporary-SQLite sorter |
| inGitDB writer | Deterministic records + definitions, record hashes | `dbcopy.ExportInGitDB` (made incremental-capable and deterministic) |
| Differ | Merge-join old/new state, counts, deletion markers | new, small |
| Stores | git (go-git), folder, S3, GCS, Azure Blob | go-git v5 already in `go.mod`; cloud SDKs new |
| Verifier | structure, hashes, restore check | `pkg/dbcompare.Compare`; `crossverify`'s value canon once it moves into `compare` |
| Restore | Materialise point → `db copy` transfer | `db copy`'s transfer as a library call; own report |

### External dependencies

- **inGitDB `source_schema` follow-ups (PR datatug-cli#385):** C (spec, JCS `definitionSha256`), B (`SourceDefinition` with engine version, checks, uniques, identity counters), A (`faithful.ReadInGitDB`, so a restore from inGitDB carries foreign keys, checks and defaults). The backup works without them at today's restore fidelity and upgrades when they land.
- **A transaction-scoped source reader** for SQLite and PostgreSQL (stage 4): today the only snapshot is private to `faithful.Copy`.
- **The backup-capable inGitDB writer** (stage 1).
- **Partitioned records files in inGitDB** (`ingitdb/ingitdb-go`, not filed): records of one collection split over several files by key range or key hash, so a git destination can hold a collection over the file-size cap. Today a collection is either one file per record or all records in one file (`RecordFileDef.RecordType`).

## Dependencies

- cli/db/copy
- cli/db/copy/filtering
- mutation-git-integration

## Comparison with existing tools

| Tool | What it does better | Why DataTug backup still exists |
|---|---|---|
| `pg_dump` / `pg_restore` | Complete PostgreSQL objects (functions, roles, extensions), fast | One engine, no incremental, binary or SQL text that does not diff by record |
| pgBackRest, WAL-G, Barman | Physical backups with WAL: point-in-time recovery to any second, huge databases | PostgreSQL-only, server access, no record-level history or cross-engine restore. **For production PostgreSQL PITR these remain the right tool; this Feature does not compete with them.** |
| Litestream | Continuous SQLite replication to object storage | SQLite-only, page-level, a running process |
| restic, borg, kopia | Encrypted, deduplicated file backups | File-level: a database file changes as a whole; no record diff, no restore into another engine |
| Dolt | Versioned SQL database with git semantics | Replaces the database; DataTug backs up the database you already run |
| Debezium, Airbyte | Change data capture and sync | Sync, not history: they move changes, they do not keep restore points |

DataTug's place: engine-neutral, record-level, human-reviewable history (a point is a git diff of records), restore into any `db copy` target, secrets by reference, verification built in.

## Cost, size and large databases

- **v1 reads the whole source on every run** (REQ:change-detection-v1); incremental means what is *stored and uploaded*, not what is *read*. For a cloud database this is egress per run; for AdventureWorks (about 127 MB as SQLite) a run reads all of it every time, and the stored delta is what changed.
- **PostgreSQL:** a `REPEATABLE READ` snapshot held for the length of a long run delays vacuum of rows deleted meanwhile; the run reports its snapshot duration.
- **Memory:** bounded — records stream in key order; out-of-order sources are sorted through temporary SQLite (disk use scales with the largest collection, as `compare` documents).
- **Git hosting:** GitHub refuses files over 100 MB (REQ:git-file-size-cap) and recommends repositories stay small (under 1 GB ideally, under 5 GB strongly); a database whose serialised size approaches that belongs on a chain destination.
- **Object storage:** cost is one PUT per changed segment plus one per manifest; segments are zstd-compressed; reads happen only for restore, verify and an empty local cache.

### Optimisations after v1 (not in this Feature's ACs)

1. **Watermark** per collection (`updated_at`, `rowversion`, an increasing id) declared in the profile: read only rows above the last watermark; deletions are found by a keys-only scan (key columns only) every run, or by a full reconcile every N runs. Falls back to v1 when the column is missing or goes backwards.
2. **Range digests pushed down** (PostgreSQL): hash key ranges in SQL and read only the ranges whose digest changed — cuts egress without any source change.
3. **PostgreSQL logical replication** (a slot with `pgoutput`): reads only changes, but needs the `REPLICATION` attribute and a slot that retains WAL while backups are not running. Opt-in only; the slot is dropped when the profile stops using it, and a lost slot falls back to v1.

## User Journey

One person and one CI job; Chinook as the source; a private git repository as the destination.

1. **Create the profile.** Alex runs `datatug backup new prod --project chinook --source datatug://chinook/chinook?env=prod --to git+ssh://git@github.com/acme/chinook-backups.git --git=commit`. *Good result:* `backups/prod.yaml` exists and is committed; it contains no host, password or token; nothing connected to the database or to GitHub.
2. **First backup.** Alex runs `datatug backup run prod --json`. *Good result:* exit `0`; the report shows point `1`, 11 collections, 15,607 rows, all `added`; branch `datatug-backup/chinook/prod` has one commit holding an inGitDB project and `.datatug-backup/manifest.json`; the post-run `hashes` verification passed.
3. **Nothing changes, run again.** *Good result:* exit `0`, `status: unchanged`, no new commit; `backup point list prod` still shows one point.
4. **The database changes** (3 invoices updated, 1 customer deleted, 2 tracks added) and the CI job, triggered by the release of the app that changed them, runs `datatug backup run prod --json`. *Good result:* point `2`; report counts `changed 3, deleted 1, added 2`; `git diff` of the two commits shows exactly those six record lines.
5. **Restore an earlier point.** Alex runs `datatug backup restore prod --at 1 --to sqlite://./prod-at-1.sqlite`. *Good result:* the file holds point 1's row counts per table, including the customer deleted in step 4; the report names the fidelity used.
6. **Recover one row.** `datatug backup restore prod --at 1 --include Customer --where Customer:CustomerId:=:42 --to sqlite://./customer-42.sqlite`. *Good result:* one row, the deleted customer.
7. **Verify everything.** `datatug backup verify prod --all-points --level restore`. *Good result:* exit `0`. After someone pushes a commit that edits a record line on the backup branch, the same command exits `1` naming that commit as not a backup; after someone force-pushes a point's commit with an edited record line, it exits `1` naming the point, the collection and the key.
8. **A second profile.** Alex adds `backup new prod-monthly … --to ./backups-archive` (a folder chain) and runs both with `backup run --all`. *Good result:* both report independently; neither touches the other's destination or lock.

The whole journey MUST run as one hermetic end-to-end test: a Chinook SQLite source and a local bare repository (`git+file://`) as the remote, no network (AC: whole-journey-e2e).

## Acceptance Criteria

### AC: profile-new-writes-only-the-profile

**Requirements:** backup#req:profile-file, backup#req:profile-new, backup#req:profile-holds-no-secret

**Given** a registered project with a PostgreSQL source registered by `db add --db env:DATATUG_PROD_URL` and `DATATUG_PROD_URL` set
**When** the user runs `datatug backup new prod --source datatug://p/prod --to git+file:///tmp/remote.git`
**Then** exit `0`; `backups/prod.yaml` is the only new file; it does not contain the value of `DATATUG_PROD_URL`; no connection was made to the database (a fake driver records zero opens) and the remote is untouched.

### AC: profile-refuses-literal-secrets

**Requirements:** backup#req:profile-holds-no-secret, backup#req:no-secret-in-output

**Given** any project
**When** `backup new` is given `--source postgres://u:pw@h/db`, or `--to s3://b/p?X-Amz-Signature=abc`, or a profile file with `credentials: ghp_xxx` is loaded
**Then** each exits `2`, names the field, and neither stdout nor stderr contains `pw`, `abc` or `ghp_xxx`.

### AC: multiple-profiles-are-independent

**Requirements:** backup#req:multiple-profiles, backup#req:run-several-profiles

**Given** two profiles of one source, one git and one folder destination, and a third profile naming the first one's branch
**When** the user runs `backup run --all`
**Then** the first two produce one point each in their own destination; the third is refused with exit `2` naming both profiles; the command's exit code is `2`.

### AC: first-run-full-point

**Requirements:** backup#req:run-reuses-db-copy, backup#req:consistent-snapshot, backup#req:deterministic-tree, backup#req:point-manifest, backup#req:destination-git

**Given** the Chinook SQLite source and an empty bare repository
**When** the user runs `backup run prod --json`
**Then** exit `0`; point `1` reports 11 collections and 15,607 rows added; the branch has one commit whose tree is an inGitDB project that `db copy --from ingitdb://` reads back with the same row counts; the manifest records `consistency: snapshot` (once the snapshot reader of stage 4 has landed; `none` with a `no-consistent-snapshot` warning before it) and no resolved URL.

### AC: unchanged-run-is-a-no-op

**Requirements:** backup#req:unchanged-run-writes-nothing, backup#req:deterministic-tree

**Given** point `1` from the previous AC and an unchanged source
**When** the user runs `backup run prod --json`
**Then** exit `0`, `status: unchanged`, and the remote branch head is the same commit.

### AC: incremental-run-stores-only-changes

**Requirements:** backup#req:change-detection-v1, backup#req:record-hash, backup#req:destination-git

**Given** point `1`, then 3 updated rows, 1 deleted row and 2 inserted rows in the source
**When** the user runs `backup run prod --json`
**Then** point `2` reports `changed 3, deleted 1, added 2`; `git diff <point1> <point2> --stat` touches only the records files of the affected collections and the manifest, and the diff has exactly 6 changed record lines.

### AC: chain-incremental-and-deletion-markers

**Requirements:** backup#req:destination-chain, backup#req:change-detection-v1, backup#req:additive-by-default

**Given** a folder destination and the same two source states
**When** two runs are made
**Then** point `2`'s new objects hold only the 5 put lines and 1 `del` line; nothing written by point `1` was modified or removed (file modification times and hashes unchanged).

### AC: keyless-and-schema-changes

**Requirements:** backup#req:keyless-collections, backup#req:schema-changes-tracked

**Given** a source with a keyless table and a keyed table, and between two runs one row of the keyless table changes and a column is added to the keyed table
**When** the second run is made
**Then** both collections are stored whole in point `2` with a `keyless-collection-rewritten` warning and a `schema-changed` note carrying both definition hashes; restoring point `1` gives the old column set.

### AC: interrupted-run-leaves-no-point-and-rerun-skips-uploads

**Requirements:** backup#req:point-published-atomically, backup#req:resumable-by-rerun

**Given** a folder destination and a fault injected after half of the segments of a run were written
**When** the run fails and is then re-run
**Then** after the failure `backup point list` shows no new point; the re-run publishes the point and writes only the segments that were not already present (the store's put counter shows it).

### AC: concurrent-runs-one-wins

**Requirements:** backup#req:single-run-per-profile

**Given** two processes running `backup run prod` against the same bare repository (and, separately, the same folder destination), started together
**Then** exactly one point is published; the other process exits `1` with `status: locked` or `status: conflict`; the branch history is linear and no push was forced.

### AC: git-size-cap

**Requirements:** backup#req:git-file-size-cap

**Given** a profile with `destination.max_file_mb: 1` and a collection whose records file is 2 MB
**When** the user runs `backup run`
**Then** exit `1` naming the collection and its size; the remote branch is unchanged.

### AC: restore-point-to-new-sqlite

**Requirements:** backup#req:restore-requires-target, backup#req:restore-is-db-copy, backup#req:restore-deletes, backup#req:point-selector

**Given** points `1` and `2` from AC:incremental-run-stores-only-changes
**When** the user runs `backup restore prod --at 1 --to sqlite://./r1.sqlite` and `backup restore prod --to sqlite://./r2.sqlite`
**Then** both exit `0`; `r1.sqlite` holds the deleted row and point `1`'s counts, `r2.sqlite` does not hold it and has point `2`'s counts; `backup restore prod` without `--to` exits `2`; `--at 99` exits `3`; the temporary folders are gone.

### AC: restore-refuses-non-empty-target

**Requirements:** backup#req:restore-requires-target

**Given** a SQLite target that already holds a source-named table with rows
**When** the user runs `backup restore prod --to sqlite://./existing.sqlite` without `--overwrite`
**Then** exit `1`, the message suggests `--overwrite=recreate` or `--overwrite=reload`, and the target is unchanged.

### AC: verify-detects-tampering

**Requirements:** backup#req:verify-levels

**Given** points `1` and `2`
**When** the user runs `backup verify prod --all-points --level restore`, then force-pushes, as another user, a copy of point `2`'s commit with one record line edited, and runs `backup verify prod --at 2`
**Then** the first exits `0`; the second exits `1` naming point `2`, the collection and the key, and not the record's values. Deleting the manifest of point `1` of a chain destination makes `--level structure` exit `1` naming the broken link.

### AC: post-run-verification-gates-ci

**Requirements:** backup#req:verify-after-run, backup#req:exit-codes, backup#req:non-interactive

**Given** a store fault that corrupts a segment after it is written
**When** the user runs `backup run prod --json`
**Then** exit `1`, stdout is one JSON object with `status: published-unverified` and `exitCode: 1`.

### AC: prune-is-explicit-and-safe

**Requirements:** backup#req:retention-explicit, backup#req:additive-by-default, backup#req:chain-rebase

**Given** a folder destination with 40 points and `retention: {keep_last: 5}`
**When** the user runs `backup prune prod`, then `backup prune prod --apply`
**Then** the first changes nothing and lists 35 points; after the second, 5 points remain, each restores (AC verify at level `restore` passes), and no `backup run` ever removed an object.

### AC: git-prune-rotates

**Requirements:** backup#req:git-retention-is-rotation

**Given** a git destination with 10 points and `retention: {keep_last: 1}`
**When** the user runs `backup prune prod --apply`
**Then** a new branch `<branch>@2` exists whose single commit is the latest point's tree with `rebasedFrom` set; the old branch is unchanged; the output prints the command that would delete the old branch.

### AC: inconsistent-source-is-flagged

**Requirements:** backup#req:consistent-snapshot

**Given** an `ingitdb://` source
**When** the user runs `backup run` and then the same with `--require-consistent-snapshot`
**Then** the first succeeds with a `no-consistent-snapshot` warning and `consistency: none` in the manifest; the second exits `1` and writes nothing.

### AC: postgres-preview-gated

**Requirements:** backup#req:restore-postgres-preview

**Given** `DATATUG_PREVIEW_POSTGRES` unset and a profile whose source is a PostgreSQL project source
**When** the user runs `backup run` or `backup restore --to postgres://…`
**Then** both exit `4` with `db copy`'s fixed preview sentence and no connection attempt.

### AC: public-github-repo-refused

**Requirements:** backup#req:backups-hold-data

**Given** a fake GitHub API that reports the destination repository as public
**When** the user runs `backup run prod`
**Then** exit `1` naming `allow_public`; nothing is pushed.

### AC: additive-prune-keeps-deleted-rows

**Requirements:** backup#req:additive-by-default, backup#req:retention-explicit

**Given** a folder destination with `history: additive`, 10 points, a row deleted at point 3, and `retention: {keep_last: 2}`
**When** the user runs `backup prune prod --apply` and then `backup restore prod --tombstones --to sqlite://./t.sqlite`
**Then** 2 points remain and `t.sqlite` holds the deleted row's last stored version; with `history: windowed` the same prune leaves no copy of that row.

### AC: rewritten-history-refused

**Requirements:** backup#req:rewritten-history-refused, backup#req:local-state-cache

**Given** a git destination with points 1 and 2 and a local cache at point 2
**When** someone force-pushes the branch back to point 1 with an edited record and the user runs `backup run prod`
**Then** exit `1`, `status: history-rewritten`, naming point 2; nothing is pushed.

### AC: whole-journey-e2e

**Requirements:** backup#req:profile-new, backup#req:change-detection-v1, backup#req:unchanged-run-writes-nothing, backup#req:restore-is-db-copy, backup#req:restore-deletes, backup#req:verify-levels, backup#req:multiple-profiles

**Given** the Chinook SQLite file and a local bare repository, no network
**When** one test walks the eight steps of the [User Journey](#user-journey) through the built `datatug` binary, with no manual step between them
**Then** every step's good result holds, asserted from the destination's contents and the JSON reports (not only exit codes).

## Staged delivery

Each stage is one PR (sizes are production code + tests, excluding fixtures). Each stage ships something usable, keeps `main` releasable, and lists the journey steps its tests walk.

| # | PR | Size | Journey steps |
|---|---|---|---|
| 0 | This spec | docs | — |
| 1 | Backup-capable inGitDB writer: `ExportInGitDB` gains filters (push-down), writing into an existing tree, deterministic bytes (sorted, no volatile fields), content-derived keyless ids, JCS record hashes; determinism test (same DB twice → identical bytes) | L (~1,000 LOC) | — |
| 2 | Profiles: file format, secret refusal, `backup new`, `backup list` | S (~400 LOC) | 1 |
| 3a | `backup run` into a **local** git repository: writer → commit, manifest, unchanged no-op, `--format json`, `hashes` verification after run, `backup point list` | M (~800 LOC) | 2, 3 |
| 3b | Remote git: cache clone, fetch, fast-forward push (system `git` binary if go-git shallow push proves unreliable), local lock, rewritten-history refusal, size cap, public-repo refusal | M (~700 LOC) | 2, 3, 4 |
| 4 | Snapshot reader: a `dal.DB`/row reader scoped to one read transaction for SQLite and PostgreSQL (upstream in `dalgo2sqlite` / `dalgo2postgres` or a `pkg/dbcopy` reader beside `faithful`), used by `ExportInGitDB` | M (~600 LOC, part upstream) | 2 |
| 5 | `backup restore` (own report, `--dry-run`, `--fail-on-warning`) and `backup verify` (all levels, `dbcompare` restore check); whole-journey e2e steps 1–7 | M (~800 LOC) | 5, 6, 7 |
| 6 | Chain format + folder store: segments, deletion markers, create-only manifests, resumable re-run, chain rebase; journey step 8 | L (~1,000 LOC) | 8 |
| 7 | `backup prune` (additive tombstone archive, windowed retention, git rotation), `restore --tombstones` | M (~700 LOC) | — |
| 8a–c | Object storage stores: S3, GCS, Azure Blob — one PR each, conditional-create locking, emulator tests (MinIO, fake-gcs-server, Azurite) | M each (~400 LOC) | 8 with a cloud store |
| later | Watermarks, range digests, logical replication; faithful restore after inGitDB follow-up A; partitioned records after the inGitDB layout lands | separate Features | — |

Stages 1–3b and 5 are the minimum that delivers the founder's ask for a git remote; 4 makes points consistent; 6 adds the local folder; 8 adds cloud storage.

## Non-goals

- Scheduling, daemons, polling or cron of any kind (REQ:no-scheduler).
- Point-in-time recovery between points, WAL shipping, physical or page-level backups.
- Backing up what `db copy` does not carry (functions, triggers across engines, roles, grants, extensions, sequences beyond auto-number counters).
- Encryption at rest in v1 (only the seam, REQ:encryption-seam).
- Writing secrets anywhere; managing credentials; creating cloud buckets or git repositories.
- Rename detection of tables or columns.
- Erasing selected rows from existing points (right-to-erasure tooling); only whole points and archives are removed.
- Evaluating backups against query access policies (REQ:same-access-as-db-copy).
- Rewriting git history, force pushes, or git LFS.
- Restoring into the source without an explicit `--to` and `--overwrite`.
- Merging two profiles' histories, or cross-profile deduplication.

## Open Questions

Decisions needed from the founder; each has a recommendation that the spec already follows.

1. **Git first, with a 95 MB per-file cap, before inGitDB can split a collection?** A git remote is the founder's first-ranked destination, and it gives incremental and additive history for free, but a single collection whose records file passes 100 MB cannot be pushed to GitHub. *Recommendation:* yes — ship git first with the cap (REQ:git-file-size-cap) and the folder chain right after it for large databases; file "partitioned records" in `ingitdb-go` as a separate request instead of blocking on it.
2. **Should `db copy --to ingitdb://` become the native exporter, so `export` is literally `db copy --to ingitdb://`?** Today they are two writers with different trees. `backup` uses the exporter either way (stage 1). Moving `db copy` onto it means the exporter must also implement `--overwrite=recreate|reload` and the empty-target check, and the layout of every copy to inGitDB changes (one file per record → one records file per collection), which rewrites the expectations of `AC:sqlite-to-ingitdb-chinook-roundtrip` and the filtering ACs. *Recommendation:* yes, as its own `cli/db/export` + `db copy` change after stage 1, announced in the release notes; not a blocker for `backup`.
3. **Retire the Approved `db snapshot` Idea?** [`db-snapshot-command`](../../../ideas/db-snapshot-command.md) proposed `db snapshot --project/--db/--env` writing timestamped local inGitDB trees. With `export` as the ad-hoc local shortcut and `backup` (folder destination) as the named, repeatable one, it duplicates both. *Recommendation:* archive it as superseded by this Feature and the `db export` decision; carry its default-path idea into the `cli/db/export` Feature.

Not founder decisions (resolved in this spec, listed so a reviewer can challenge them): restore always needs `--to`; `--fail-on-warning` exits `1`, not `3`; backups are not gated by query access policies; prune is the only deleting verb and, in the default `additive` mode, keeps every deleted record in the tombstone archive.

---
*This document follows the https://specscore.md/feature-specification*
