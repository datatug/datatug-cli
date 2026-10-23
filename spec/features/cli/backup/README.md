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
datatug backup new <profile> [--project <id> | --dir <path>] --source <datatug://…|env:NAME> --to <destination>
                   [--branch <name>] [--mode incremental|full]
                   [--filter-config <file>] [--git none|stage|commit]
datatug backup list [--strict]
datatug backup pin <profile>
datatug backup run <profile>... | --all [--expect-destination <pin>] [--trust <point>]
                   [--require-consistent-snapshot] [--fail-on-warning] [--progress]
datatug backup point list <profile>
datatug backup restore <profile> --to <target-url> [--at <point>]
                   [--overwrite recreate|reload] [--include …] [--exclude …] [--where …] [--limit …]
                   [--filter-config <file>] [--parallel-streams N] [--dry-run] [--fail-on-warning] [--progress]
datatug backup verify <profile> [--at <point> | --all-points] [--level structure|hashes|restore]
                   [--since-trusted [--trust <point>]] [--against-source]

every verb: [--project <id> | --dir <path>] [--format text|json|yaml | --json]
```

A backup is **`db copy` plus a destination policy**, not a second engine: every run opens the source through the same code as [`db copy`](../db/copy/README.md) (URL and `datatug://` resolution, `env:NAME`, PostgreSQL preview gate, read-only session), applies the same filters, serialises records with the inGitDB writer of [`db export`](#boundary-with-db-export-and-db-copy), and adds what a copy does not have: history of restore points, change detection between runs, locking and verification.

**Founder decisions this Feature encodes (2026-10-16):**

1. `export` stays a small local shortcut: `db copy --to ingitdb://<local dir>` (a plain path is allowed; the default is `./<name>.ingitdb`). It has no profiles, history or remote destinations.
2. Backup to cloud or remote storage belongs to the `backup` family, never to `export` or `db copy`.
3. A project can hold **several backup profiles** (different sources, destinations, filters or cadences).
4. Backups are **incremental** (a run stores only what changed) and **additive** (nothing already backed up is ever removed; deleted rows stay recoverable from the points at which they existed).

**Scope of this Feature (v1):** git and local-folder destinations, plus object-storage backends of the same append-only chain. Nothing in v1 deletes backed-up data. Retention and pruning (removing old points, tombstone archives for what they held) are a separate, later Feature, `cli/backup/prune`, not specified here; see [Later work](#later-work).

## Problem

`db copy` and `db export` make one full copy of a database and forget it. People who use them as a backup today must invent, per database: a place to keep copies, a naming scheme, a way to keep the credentials out of scripts, a way to tell what changed since the last copy, a way to get one deleted row back, and a way to prove that a copy can be restored. Each of those is where home-made backups fail silently. The usual tools are engine-specific (`pg_dump`, `pg_basebackup`, `.backup` for SQLite) or file-level (restic, borg), so none gives a record-level, reviewable history that can be restored into another engine — the thing DataTug's inGitDB format already makes cheap.

## What the CLI can and cannot do today (verified 2026-10-16 on `main` at `09d9dbdc`)

| Capability | Today | Source |
|---|---|---|
| Source schemes | `sqlite`, `ingitdb` (local), `postgres` (read-only session), `http(s)` DataTug query projects, `openvaultdb`, and `env:NAME` holding any of them; `datatug://<project>/<db>[?env=…]` resolves a project source | `pkg/dbcopy/url.go` (`supportedSchemes`), `pkg/dbcopy/datatug_ref.go` |
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
| History, locking, verification | No | No | Yes (retention later) |
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
records_format: jsonl                        # default jsonl for git, see REQ:deterministic-tree
require_consistent_snapshot: false
verify_after_run: hashes                     # none | structure | hashes (default) | restore
```

Unknown keys MUST be refused (exit 2, naming the key), so a typo never silently changes a backup.

#### REQ: multiple-profiles

A project MAY hold any number of profiles. Two profiles MAY name the same source (for example a nightly git profile and a monthly folder profile) and MUST NOT share state: each has its own destination branch or prefix, lock, cache and point sequence. Two profiles of one project MUST NOT name the same destination branch or prefix. `backup new` MUST refuse the new profile (exit 2) naming the existing one; `backup run` MUST refuse the colliding profile that is later in name order (exit 2, naming both) and run the other.

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

Once the transaction-scoped reader of stage 4 has landed, a run MUST read every collection of the source in one consistent snapshot where the source supports it: PostgreSQL in one `REPEATABLE READ` read-only transaction, SQLite in one read transaction, the rules of [REQ:faithful-snapshot](../db/copy/README.md#req-faithful-snapshot). This needs a source `dal.DB` (or row reader) scoped to one transaction, which does not exist today (see [External dependencies](#external-dependencies)); until it lands every source is read as `consistency: none` and the warning and manifest field below are what the earlier stages owe. The point MUST record `consistency: snapshot` when it was read so. For a source without a shared snapshot (inGitDB, HTTP, OpenVaultDB) the point MUST record `consistency: none` and the run MUST emit a `no-consistent-snapshot` warning; with `require_consistent_snapshot: true` (or `--require-consistent-snapshot`) such a run MUST exit `1` before writing anything.

#### REQ: deterministic-tree

What a run writes for a point MUST be a pure function of the source state and the profile: the same source rows MUST produce byte-identical records and definition files. Records MUST be ordered by primary key (keyless collections: by the canonical record bytes) with one record per line in the default `jsonl` format, so a git diff of two points is a diff of records. Volatile values (capture times, engine version, tool version, durations, host) MUST live only in the point manifest, never in the inGitDB tree: when the inGitDB `source_schema` follow-up adds `capturedAt` and `engineVersion` to `source_definition_json`, the backup writer MUST leave them out of the tree and record them in the manifest. The order of keys is one comparator, the byte order of the JCS encoding of the key tuple; rows that a source returns in another order (a PostgreSQL locale collation, an unordered provider) MUST be re-sorted, never trusted.

#### REQ: record-hash

Each record's identity is its collection plus its primary-key tuple; its **record hash** is the SHA-256 of the RFC 8785 (JCS) canonical JSON of the record exactly as the inGitDB writer serialises it (storage-class tags such as `{"$blob":""}` included). The same canonicalisation MUST be used for the collection definition hash, matching `definitionSha256` of the inGitDB `source_schema` follow-up (PR datatug-cli#385, "Proposal, amended after the review", item 3) once it lands; until then the definition hash is the JCS hash of the `definition.yaml` the writer produces, with any capture time and engine version removed. Change detection of definitions MUST use this hash, never the file bytes.

#### REQ: change-detection-v1

Without any source-side change tracking, a run MUST: read the source in one snapshot (REQ:consistent-snapshot); compute every record hash; compare, per collection, against the predecessor point's state (key → hash) by a streaming merge on the key order; and write only added and changed records plus a **deletion marker** for each key present in the predecessor and absent now. This works for every DALgo source. A source that cannot return rows in key order MUST be sorted through bounded temporary storage (the temporary-SQLite staging `dbcompare` already uses), never in memory.

#### REQ: unchanged-run-writes-nothing

When no record, no collection definition and no collection set changed since the predecessor point, the run MUST NOT create a point, a commit or an object; it MUST report `status: unchanged`, the predecessor point id, and exit `0`.

#### REQ: keyless-collections

A collection without a primary key MUST be compared as a multiset of record hashes (as [`compare`](../../../../docs/database-compare.md) does). Its records are identified by their record hash (a duplicate row gets the hash plus an occurrence number), never by position, so an insert does not renumber every following row. When its multiset changed, the point MUST store the whole collection again and emit a `keyless-collection-rewritten` warning naming it. Instead of per-key deletion markers, the manifest records the record hashes (with occurrence numbers) that left the multiset; the rows themselves stay restorable from the earlier points (REQ:additive-by-default).

#### REQ: schema-changes-tracked

A point MUST store the collection definitions in force at that point. A changed definition (column added, removed, retyped, key changed) MUST make the point store the whole collection again, with a `schema-changed` note naming the collection and the definition hashes before and after. A collection that disappeared from the source MUST get a collection-level deletion marker (its records and definition stay restorable from the earlier points); one that appeared is stored in full. Renames are a deletion plus an addition (no rename detection).

#### REQ: point-published-atomically

A point MUST become visible only after every collection and its manifest are durably written: for a git destination one commit pushed in one ref update; for a chain destination (folder, object storage) the manifest written last, as the commit marker. A run that fails or is interrupted MUST leave no visible point; what it already uploaded is unreferenced, stays (nothing deletes in v1) and may be reused by a later run (REQ:resumable-by-rerun).

#### REQ: resumable-by-rerun

There is no checkpoint protocol. Every object MUST be written atomically, so a partly written object never exists under its final name: the folder store writes a temporary file (`tmp/<random>`) on the same file system, `fsync`s it, renames it to its final name and `fsync`s the folder; object stores write each object in one PUT (a multipart upload becomes visible only when it completes). An object's name is the SHA-256 of its **uncompressed** content (the JSONL bytes); the stored bytes are that content compressed with zstd, and the uncompressed hash is also stored as object metadata (`datatug-sha256`) so `verify` can check it after decompressing.

Because nothing in v1 deletes objects, a re-run MUST re-read the source in a new snapshot and MUST reuse every object that already exists under the name it needs, except one marked corrupt: `verify` writes a create-only `corrupt/<object name>.json` for each object whose content does not match its name, and a run that needs such content writes it under the next free name `<hash>.<n>.jsonl.zst` and reports `object-rewritten`. The cost of a retry is therefore the source read plus what had not yet been uploaded.

#### REQ: point-manifest

Every point MUST carry a manifest (`format: datatug-backup-manifest/v1`) with: point sequence number (predecessor + 1, the first point is `1`), point id (`<seq>-<UTC time YYYY-MM-DDTHH-MM-SSZ>`), predecessor point id and the SHA-256 of the predecessor's manifest bytes, profile name, source reference as written in the profile (never a resolved URL), source engine and engine version, consistency achieved, whether a filter applied (`partial: true`), per collection (row count, key columns, definition hash, a digest of the sorted record hashes, counts added/changed/deleted, the objects that hold them), totals, warnings, tool version, start and end times.

#### REQ: single-run-per-profile

At most one run of a profile MUST publish a point at a time.

- **Locally** `run` MUST take an exclusive lock in the CLI's per-user configuration folder (`os.UserConfigDir()/datatug/backup/locks/<project-id>/<profile>.lock`, where `datatug` already keeps per-user state).
- **Across machines, git:** a run pushes a fast-forward of the profile branch and never forces. Ref compare-and-swap is the whole lock.
- **Across machines, chain:** the manifest `points/<seq>.json` is created with create-only semantics (`If-None-Match: *` on S3 and Azure Blob, `ifGenerationMatch=0` on GCS, write-to-temp then `link` on a folder), so two runs cannot publish the same sequence number. Before creating it the run MUST check that every object the manifest references exists; one that does not (removed by something outside the CLI) is written again as `<hash>.<n>` before the manifest is created.

The loser MUST make no point visible, report `status: locked` (local lock) or `status: conflict` (the destination moved under it) and exit `1`.

#### REQ: run-several-profiles

`backup run a b c` and `backup run --all` MUST run the named profiles one after the other, continue after a failed profile, report each, and exit `0` when every run exited `0`, otherwise with the exit code of the first profile, in run order, that did not.

#### REQ: additive-by-default

Backups are append-only in v1: no verb overwrites or deletes anything at the destination, so a run needs only create permissions (plus read) and a chain destination works on write-once storage (S3 Object Lock, GCS retention policies, Azure immutable blobs). Every point stays restorable, so every record ever backed up — keyed rows, rows of keyless tables, whole dropped tables, rows under an older schema — is recoverable by restoring a point at which it existed (REQ:restore-deletes). Removing old points is the later `cli/backup/prune` Feature.

Stated limits: a lifecycle or expiration rule on the bucket or prefix, a person deleting objects, or a git host deleting the branch removes backed-up data behind the CLI's back; `verify --level structure` reports the missing objects or commits, and the next run repairs only what the new point needs (REQ:single-run-per-profile, REQ:incomplete-predecessor).

#### REQ: incomplete-predecessor

When the predecessor point cannot be materialised for a collection (an object is missing or marked corrupt, as after a `published-unverified` run), the run MUST store that collection in full in the new point, with a `predecessor-incomplete` warning naming it, instead of failing; change detection resumes from the new point. This also covers a CI runner without a cache.

#### REQ: destination-owned-by-profile

Every manifest records the project id and profile name. A run MUST refuse (exit `1`, `status: foreign-destination`) a destination whose latest manifest names another project or profile, so two projects that use the same profile name and destination never interleave their points.

### Destinations

#### REQ: destination-git

A destination URL `git+ssh://…`, `git+https://…` or `git+file://…` is a git destination. Each profile owns one branch (REQ:profile-file); each point is one commit on it whose tree is an inGitDB project (the deterministic tree, REQ:deterministic-tree) plus `.datatug-backup/`: the manifest, and a record-hash index per collection (`.datatug-backup/hashes/<collection>.jsonl`, one `{"k": <key>, "h": <record hash>}` line per record in the records' key order, so it changes only where records change). Git provides incremental storage (unchanged files are the same blobs) and additive history natively; deletion markers are the removal of the record line, kept in history. The CLI MUST work from a cache clone in its per-user folder (`run` needs only the commits since the last trusted point, which it checks against the cache; `verify` fetches the whole branch), MUST treat as a point only a commit that changes `.datatug-backup/manifest.json` to the next sequence number and whose parent is the previous point (point 1 is the branch's first commit), and MUST refuse a profile branch holding any other commit (exit `1`, "branch holds commits that are not backups"); `verify --level structure` reports such a commit, and MUST NOT force-push, rebase or rewrite history.

#### REQ: git-file-size-cap

A records, definition or record-hash index file larger than the destination's file-size cap MUST fail the run before anything is pushed (exit `1`, naming the collection and its size). The default cap is 95 MB (GitHub refuses files over 100 MB); a profile MAY set a lower `destination.max_file_mb`. A run MUST warn (`file-near-cap`) when a file passes 80 % of the cap, so the move to a folder chain is planned and not a CI failure. A single push larger than the host accepts (GitHub refuses pushes over 2 GB, which a first run of a large database can reach with every file under the cap) MUST fail with exit `1` and a message that names the limit; nothing is published. Splitting one collection over several files needs an inGitDB records layout that does not exist yet (see [External dependencies](#external-dependencies)); until it does, a database with a collection over the cap uses a chain destination.

#### REQ: destination-chain

A folder destination (`file:///abs/path`, `file:./rel/path`, or a plain path) and, in a later stage, object storage (`s3://bucket/prefix`, `gs://bucket/prefix`, `azblob://account/container/prefix`) store an explicit **snapshot chain**:

```
<prefix>/<profile>/
  points/<seq>.json                    # the manifest; created last, create-only
  objects/sha256/<2 hex>/<hash>[.<n>].jsonl.zst  # record segments, definitions, archive segments
  corrupt/<object name>.json           # create-only marks written by verify
  probe/, tmp/                         # create-only probe objects; folder temp files
```

A point's segments hold, per collection, the records added or changed and the deletion markers, sorted by key, each line `{"k": <key>, "h": <record hash>, "op": "put"|"del", "v": <record>}`. Point 1, and any point after a definition change, holds the full collection. The state of point N is the merge of its chain back to the last full segment of each collection. All three backends implement one small store interface (put-if-absent, get, list by prefix, delete), so the folder backend is the test double of object storage.

Not every store honours create-only writes (some S3-compatible servers ignore `If-None-Match`, `O_EXCL` is unreliable on some NFS mounts). The first run of a profile on a destination MUST probe it: create `probe/<random>` create-only twice and require the second to fail with a precondition error. A store that overwrites MUST be refused (exit `1`, `store-not-create-only`) unless the profile sets `destination.single_writer: true`, which declares that only one machine ever writes and leaves only the local lock. The probe result is cached per destination in the per-user folder (an ephemeral CI runner probes on every run). The probe deletes its object afterwards where the store allows it; on write-once storage it stays (one small object per probe).

Stated limits of object stores: on a versioned bucket every object is kept as written (v1 never deletes, so this costs nothing extra); a lifecycle expiration rule on the prefix deletes backed-up data and MUST NOT be configured (REQ:additive-by-default); S3 reports a multipart object's `Last-Modified` as the start of its upload, which v1 does not use.

An object store's endpoint, region and addressing style are profile fields, never URL query parameters: `destination.endpoint` (an `https://` URL without credentials; `http://` only for `localhost`, for the MinIO, fake-gcs-server and Azurite tests), `destination.region`, `destination.path_style: true|false`.

#### REQ: chain-rebase

When a collection's chain since its last full segment exceeds 30 points or its delta bytes exceed half the full segment, the next run MUST also write a new full segment for it, computed from the chain (no extra source read). This bounds restore and diff cost without deleting anything.

#### REQ: local-state-cache

A run MAY cache the latest point's state index (key → record hash per collection) in the same per-user folder, keyed by profile and by the point's identity at the destination: the commit SHA for git, the SHA-256 of the manifest bytes for a chain. It MUST rebuild the cache from the destination when that identity is not the destination's latest point. `verify` MUST NOT use the cache; it always reads the destination.

#### REQ: rewritten-history-refused

When the destination's history no longer contains the last trusted point (a git branch moved by a non-fast-forward update, a chain manifest replaced or missing), `run` MUST NOT write and MUST exit `1` with `status: history-rewritten`, naming that point. The last trusted point is, in order: `--trust` (a commit SHA for git, the SHA-256 of the manifest bytes for a chain — a point id alone is not accepted, because a replaced manifest can reuse it), else the local cache (REQ:local-state-cache). **An ephemeral CI runner has neither**, and then trusts whatever the destination shows (trust on first use): the report MUST say `trust: destination-head` and name the point it continued from, so a CI job that keeps the previous run's point (a job output, a protected variable) can pass it back as `--trust` and get the check. `verify` detects only what the destination shows by itself (broken predecessor hashes, sequence gaps, non-point commits); `verify --since-trusted` adds the rewrite check against the same last trusted point (`--trust`, else the cache; without either it exits `2`).

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

A restore to PostgreSQL and a run from PostgreSQL MUST open it as `db copy` does. PostgreSQL is no longer behind a preview switch (founder, 2026-10-23: database sources are fully active), so neither is gated by one.

### Verification

#### REQ: verify-levels

`backup verify <profile> [--at <point> | --all-points] [--level …]` MUST check, by level (each includes the previous):

- `structure`: every manifest parses; sequence numbers are contiguous from `1`; each predecessor hash equals the SHA-256 of the predecessor's manifest bytes; every manifest names this project and profile; every referenced object (or git commit and file) exists.
- `hashes` (default): recompute each stored record hash and each collection digest of the point(s) from the stored bytes and compare them, and the row counts, with the manifest.
- `restore`: materialise the point, restore it into a scratch SQLite file in a temporary folder through REQ:restore-is-db-copy, and compare the scratch database with the materialised tree through `pkg/dbcompare.Compare`; the result MUST be zero added, removed and changed records and the manifest's row counts. This level proves that the point can be restored through `db copy`; integrity of the stored bytes is the `hashes` level.

A failure MUST exit `1` and name the point, the collection and, for a record, its key (never its values). `verify` MUST NOT open the source; `--against-source` additionally compares the latest point with the live source through `dbcompare` and reports drift as information (the source may have moved on), never as a failure.

#### REQ: verify-after-run

`backup run` MUST run `verify` at the profile's `verify_after_run` level (default `hashes`) on the new point before reporting success. A verification failure after a point was published MUST exit `1` with `status: published-unverified` so CI does not treat the point as good.

### Size over time

#### REQ: git-keeps-all-history

A git destination keeps every point; its history is never rewritten. To bound its size the person starts a new profile with a **new repository** (a new branch in the same repository does not shrink it, and deleting a branch on a hosted service does not reclaim its objects) and deletes the old repository when it is no longer needed; the CLI does neither. Git's own delta compression is what keeps incremental points small.

### CI friendliness and output

#### REQ: no-scheduler

The CLI MUST NOT schedule, daemonise or poll. A backup runs when something runs `backup run`: a person, or a CI job triggered by an event (a release, a merged migration, `workflow_dispatch`). The documentation MUST show an event-triggered CI example and MUST NOT show a cron trigger.

#### REQ: non-interactive

No verb MAY prompt. Every verb MUST accept `--format text|json|yaml`, defaulting to `yaml` for `list` and `point list` (read commands, per the CLI convention) and to `text` for the others (`--json` is an alias of `--format json`, as in `db copy`); `--format json` MUST print one object on stdout also on failure (`status`, `exitCode`, `error`), as [db copy `--json`](../../../../docs/db-copy.md) does. `--progress` prints per-collection lines on stderr in `db copy`'s format.

#### REQ: exit-codes

Verbs MUST follow the [shared CLI exit codes](../README.md#shared-exit-code-contract): `0` done (including `unchanged`), `1` failed (run, verification, local lock held, conflict, foreign destination, size or push limit), `2` usage or invalid profile, `3` profile or point not found, `4` source or destination could not be reached. With `--fail-on-warning` a run or restore that finished with warnings MUST exit `1` with `status: failed-on-warning` (not `3`, which the shared contract gives to "not found"; `db copy` uses `3` for this, and a restore maps it to `1`).

### Security

#### REQ: no-secret-in-output

No profile, manifest, commit message, log line, report, temporary file name or error MUST contain a credential, a resolved connection URL or the value of an `env:` variable. Errors from drivers and SDKs MUST pass through `dbcopy.RedactErrorWithSecrets` with every resolved secret, as `db export` does. When the system `git` binary is used, a token MUST NOT appear on its command line or in an environment variable it could print: it is handed over through a `GIT_ASKPASS` helper reading a file readable only by the user (mode `0600`; on Windows, a file in the per-user temporary folder, whose ACL is the user's) that is removed when the command ends, also on failure; every invocation sets `-c credential.helper=` (so no configured helper stores the token) and `GIT_TERMINAL_PROMPT=0`.

#### REQ: backups-hold-data

A backup holds the source's rows. `backup new` MUST print, once, that the destination receives a copy of the data and that its access control is the person's responsibility. For a `github.com` git destination the run MUST refuse a repository that the GitHub API reports as public (exit `1`) unless the profile sets `destination.allow_public: true`; when visibility cannot be determined it MUST warn (`destination-visibility-unknown`).

#### REQ: same-access-as-db-copy

A backup reads what `db copy` reads with the same credentials, and, like `db copy` and `db export`, is not evaluated against the query access policies of `~/.datatug/policies/`: whoever can run it with the source's credentials can copy every row. A deliberately partial backup is expressed by the profile's `filter` and recorded as `partial: true`. Gating backups by policy is a non-goal of this Feature.

#### REQ: destination-pinned

A profile is a committed file, so a pull request that edits `destination` (or `source`) could make the next CI run copy every row to a destination its author controls; the public-repo refusal does not help against a private one. A writing verb MUST therefore refuse (exit `1`, `status: destination-not-pinned`) unless the profile's **pin** — the SHA-256 of its normalised source reference, what that reference resolves to in the project (the driver and the SQLite path, or the `dsnEnv` variable name — never its value), destination URL, branch or prefix and endpoint — matches one of: `--expect-destination <pin>`, the environment variable `DATATUG_BACKUP_PINS` (a comma list, meant to be set in protected CI configuration outside the repository), or the per-user pins file written by `backup new` and `backup pin <profile>` (which prints the pin and asks for no confirmation; it is a deliberate command). The refusal MUST print the expected pin and what changed, never a credential. Because a pin is a hash of values anyone can read, it protects only when it comes from outside the repository: `--expect-destination` and `DATATUG_BACKUP_PINS` MUST be set in protected CI configuration (an environment with required reviewers, a protected variable), never in a workflow file the same pull request can edit; the documentation MUST say so.

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
- **Git record-hash index:** about 64 hex characters plus the key per row, so for a narrow table it can be larger than the records file; it is under the same file-size cap and its churn equals the records' churn.
- **Git hosting:** GitHub refuses files over 100 MB (REQ:git-file-size-cap) and recommends repositories stay small (under 1 GB ideally, under 5 GB strongly); a database whose serialised size approaches that belongs on a chain destination.
- **Object storage:** cost is one PUT per changed segment plus one per manifest; segments are zstd-compressed; reads happen only for restore, verify and an empty local cache.

### Optimisations after v1 (not in this Feature's ACs)

1. **Watermark** per collection (`updated_at`, `rowversion`, an increasing id) declared in the profile: read only rows above the last watermark; deletions are found by a keys-only scan (key columns only) every run, or by a full reconcile every N runs. Falls back to v1 when the column is missing or goes backwards.
2. **Range digests pushed down** (PostgreSQL): hash key ranges in SQL and read only the ranges whose digest changed — cuts egress without any source change.
3. **PostgreSQL logical replication** (a slot with `pgoutput`): reads only changes, but needs the `REPLICATION` attribute and a slot that retains WAL while backups are not running. Opt-in only; the slot is dropped when the profile stops using it, and a lost slot falls back to v1.

## User Journey

One person and one CI job; Chinook as the source; a private git repository as the destination.

1. **Create the profile.** Alex runs `datatug backup new prod --project chinook --source datatug://chinook/chinook?env=prod --to git+ssh://git@github.com/acme/chinook-backups.git --git=commit`. *Good result:* `backups/prod.yaml` exists and is committed; it contains no host, password or token; nothing connected to the database or to GitHub; the profile's pin is in Alex's pins file and printed, so it can be put in the CI's protected `DATATUG_BACKUP_PINS`.
2. **First backup.** Alex runs `datatug backup run prod --json`. *Good result:* exit `0`; the report shows point `1`, 11 collections, 15,607 rows, all `added`; branch `datatug-backup/chinook/prod` has one commit holding an inGitDB project and `.datatug-backup/manifest.json`; the post-run `hashes` verification passed.
3. **Nothing changes, run again.** *Good result:* exit `0`, `status: unchanged`, no new commit; `backup point list prod` still shows one point.
4. **The database changes** (3 invoices updated, 1 customer deleted, 2 tracks added) and the CI job, triggered by the release of the app that changed them, runs `datatug backup run prod --json`. *Good result:* point `2`; report counts `changed 3, deleted 1, added 2`; `git diff` of the two commits shows exactly those six records (and their index entries).
5. **Restore an earlier point.** Alex runs `datatug backup restore prod --at 1 --to sqlite://./prod-at-1.sqlite`. *Good result:* the file holds point 1's row counts per table, including the customer deleted in step 4; the report names the fidelity used.
6. **Recover one row.** `datatug backup restore prod --at 1 --include Customer --where Customer:CustomerId:=:42 --to sqlite://./customer-42.sqlite`. *Good result:* one row, the deleted customer.
7. **Verify everything.** `datatug backup verify prod --all-points --level restore`. *Good result:* exit `0`. After someone pushes a commit that edits a record line on the backup branch, the same command exits `1` naming that commit as not a backup; after someone force-pushes a point's commit with an edited record line, it exits `1` naming the point, the collection and the key (found through the record-hash index of REQ:destination-git).
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
**Then** point `2` reports `changed 3, deleted 1, added 2`; `git diff <point1> <point2> --stat` touches only the records files and record-hash index files of the affected collections and the manifest, and the records diff touches exactly 6 records (3 changed, 1 removed, 2 added), with the matching 6 entries in the index diff.

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
**Then** exit `1` naming the collection and its size; the remote branch is unchanged. With a 1.7 MB file the run succeeds with a `file-near-cap` warning. A fake remote that refuses a push over its limit makes the run exit `1` naming the push limit, with nothing published.

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

### AC: inconsistent-source-is-flagged

**Requirements:** backup#req:consistent-snapshot

**Given** an `ingitdb://` source
**When** the user runs `backup run` and then the same with `--require-consistent-snapshot`
**Then** the first succeeds with a `no-consistent-snapshot` warning and `consistency: none` in the manifest; the second exits `1` and writes nothing.

### AC: postgres-preview-gated

**Requirements:** backup#req:restore-postgres-preview

**Given** no `DATATUG_PREVIEW_*` variable set and a profile whose source is a PostgreSQL project source
**When** the user runs `backup run` or `backup restore --to postgres://…`
**Then** both open PostgreSQL as `db copy` does, with no preview refusal.

### AC: public-github-repo-refused

**Requirements:** backup#req:backups-hold-data

**Given** a fake GitHub API that reports the destination repository as public
**When** the user runs `backup run prod`
**Then** exit `1` naming `allow_public`; nothing is pushed.

### AC: rewritten-history-refused

**Requirements:** backup#req:rewritten-history-refused, backup#req:local-state-cache

**Given** a git destination with points 1 and 2 and a local cache at point 2
**When** someone force-pushes the branch back to point 1 with an edited record and the user runs `backup run prod`
**Then** exit `1`, `status: history-rewritten`, naming point 2; nothing is pushed.

### AC: interrupted-write-leaves-no-partial-object

**Requirements:** backup#req:resumable-by-rerun

**Given** a folder destination where a run was killed while writing an object
**When** the destination is listed and the run is repeated
**Then** no object under `objects/` is partial (only `tmp/` holds the unfinished file); the re-run uploads only the objects that were missing and publishes a point that verifies at level `hashes`.

### AC: store-without-create-only-refused

**Requirements:** backup#req:destination-chain, backup#req:single-run-per-profile

**Given** a fake object store that silently overwrites on a create-only put
**When** the first `backup run` of a profile targets it
**Then** exit `1` with `store-not-create-only`; with `destination.single_writer: true` the run proceeds.

### AC: unpinned-destination-refused

**Requirements:** backup#req:destination-pinned

**Given** a profile whose `destination.url` was changed in a commit (and, separately, a profile whose source's registered `dsnEnv` was changed in the project), an empty pins file and no `DATATUG_BACKUP_PINS`
**When** CI runs `backup run --all`
**Then** that profile exits `1` with `status: destination-not-pinned` and nothing is written anywhere; with the new pin in `DATATUG_BACKUP_PINS`, or passed as `--expect-destination`, it runs; `backup pin prod` prints the pin and writes it to the per-user pins file, after which the run also succeeds on that machine.

### AC: ci-without-cache-trusts-explicitly

**Requirements:** backup#req:rewritten-history-refused

**Given** a git destination with points 1 and 2, then force-pushed back to point 1, and a run with no local cache
**When** the run is made without `--trust`, and again with `--trust <point 2 SHA>`
**Then** the first continues from point 1 and reports `trust: destination-head`; the second exits `1` with `status: history-rewritten` naming point 2.

### AC: list-and-point-list

**Requirements:** backup#req:list-profiles, backup#req:points-listing

**Given** a project with one valid profile with two points and one profile file with an unknown key
**When** the user runs `backup list` and `backup list --strict`, and `backup point list prod --format json`
**Then** `list` shows both (the second `valid: false`, with the key named) and exits `0`; `--strict` exits `2`; `point list` prints both points newest first with their counts; no command contacted the destination for `list`.

### AC: no-scheduler-and-seams

**Requirements:** backup#req:no-scheduler, backup#req:encryption-seam, backup#req:same-access-as-db-copy

**Given** the built CLI and its documentation
**Then** no `backup` verb starts a background process or timer (the process tree after each verb is empty); the documented CI example has no `schedule:`/cron trigger; every object written in a run passes through the `ObjectTransform` (a counting identity transform in the test sees every put) and the manifest names `transform: identity`; with an access policy that masks a column present in `~/.datatug/policies/`, the backup still holds the column's values, as `db copy` would.

### AC: additive-history-recovers-everything

**Requirements:** backup#req:additive-by-default, backup#req:keyless-collections, backup#req:schema-changes-tracked, backup#req:restore-deletes

**Given** a folder destination and three points where between them a keyless table lost one row, a keyed table gained a column and later lost a row, and a third table was dropped
**When** each earlier point is restored into an empty SQLite file
**Then** the keyless row, the keyed row with the columns it had, and every row of the dropped table come back from the points at which they existed; no object written by an earlier run was modified or removed.

### AC: incomplete-predecessor-is-repaired

**Requirements:** backup#req:incomplete-predecessor, backup#req:resumable-by-rerun, backup#req:verify-after-run

**Given** a folder destination where one segment of point 2 was deleted by hand and another replaced with wrong bytes, and no local cache
**When** `backup verify prod --at 2` runs, then `backup run prod`
**Then** verify exits `1` naming both objects and writes `corrupt/<object name>.json` for the replaced one; the run stores the two affected collections in full with `predecessor-incomplete` warnings, writes the corrupt content as `<hash>.1` if it needs it, and point 3 verifies at level `hashes`.

### AC: foreign-destination-refused

**Requirements:** backup#req:destination-owned-by-profile

**Given** a folder destination holding points of project `a`, profile `prod`
**When** project `b` runs its profile `prod` against the same folder
**Then** exit `1`, `status: foreign-destination`, nothing written.

### AC: chain-rebase-bounds-restore

**Requirements:** backup#req:chain-rebase

**Given** a folder destination and 31 runs that each change one row of one collection
**When** the 31st run is made and its point is restored
**Then** the 31st point holds a new full segment for that collection, computed without an extra source read (the source fake counts one read per run), and the restore reads no segment older than it; no earlier object was changed.

### AC: git-history-only-grows

**Requirements:** backup#req:git-keeps-all-history, backup#req:destination-git

**Given** a git destination after 10 runs
**When** every verb of this Feature is run against it
**Then** the branch's history is a superset of what it was before each verb (no push was forced, no commit removed), and no verb offers to delete points.

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
| 3a | `backup run` into a **local** git repository: the Differ (REQ:change-detection-v1 counts and deletions, keyless multisets, schema changes), writer → commit, record-hash index, manifest, unchanged no-op, `--format json`, `hashes` verification after run, `backup point list`, destination pins | L (~1,100 LOC) | 2, 3, 4 (local repository) |
| 3b | Remote git: cache clone, fetch, fast-forward push (system `git` binary with an askpass helper and no credential helper if go-git proves unreliable), local lock, `--trust` and rewritten-history refusal, size cap, public-repo refusal | M (~700 LOC) | 2–4 against a bare remote |
| 4 | Snapshot reader: a `dal.DB`/row reader scoped to one read transaction for SQLite and PostgreSQL (upstream in `dalgo2sqlite` / `dalgo2postgres` or a `pkg/dbcopy` reader beside `faithful`), used by `ExportInGitDB` | M (~600 LOC, part upstream) | 2 |
| 5 | `backup restore` (own report, `--dry-run`, `--fail-on-warning`) and `backup verify` (all levels, `dbcompare` restore check); whole-journey e2e steps 1–7 | M (~800 LOC) | 5, 6, 7 |
| 6 | Chain format + folder store: segments, deletion markers, atomic writes, corrupt marks, create-only probe, create-only manifests, incomplete-predecessor repair, chain rebase; journey step 8 | L (~1,000 LOC) | 8 |
| 7a–c | Object storage stores: S3, GCS, Azure Blob — one PR each, conditional-create locking, emulator tests (MinIO, fake-gcs-server, Azurite) | M each (~400 LOC) | 8 with a cloud store |
| later | `cli/backup/prune` (retention for chains, tombstone archive); watermarks, range digests, logical replication; faithful restore after inGitDB follow-up A; partitioned records after the inGitDB layout lands | separate Features | — |

Stages 1–3b and 5 are the minimum that delivers the founder's ask for a git remote; 4 makes points consistent; 6 adds the local folder; 7 adds cloud storage.

## Later work

- **`cli/backup/prune`** (a separate Feature, to be specified): retention for chain destinations, removing old points while keeping restore and `verify` consistent, and a tombstone archive that keeps the last version of every record those points held. Three review rounds of this Feature showed that pruning concurrent with runs, prune bookkeeping that `verify` accepts, and an archive that keeps identically defined tables and re-deleted keys apart each need their own design; v1 ships without them because it never deletes.
- Git destinations will not get pruning; a new repository is the way to start over (REQ:git-keeps-all-history).

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
- Deleting backed-up data in v1 (retention is the later `cli/backup/prune` Feature).
- Restoring into the source without an explicit `--to` and `--overwrite`.
- Merging two profiles' histories, or cross-profile deduplication.

## Open Questions

Decisions needed from the founder; each has a recommendation that the spec already follows.

1. **Git first, with a 95 MB per-file cap, before inGitDB can split a collection?** A git remote is the founder's first-ranked destination, and it gives incremental and additive history for free, but a single collection whose records file passes 100 MB cannot be pushed to GitHub. *Recommendation:* yes — ship git first with the cap (REQ:git-file-size-cap) and the folder chain right after it for large databases; file "partitioned records" in `ingitdb-go` as a separate request instead of blocking on it.
2. **Should `db copy --to ingitdb://` become the native exporter, so `export` is literally `db copy --to ingitdb://`?** Today they are two writers with different trees. `backup` uses the exporter either way (stage 1). Moving `db copy` onto it means the exporter must also implement `--overwrite=recreate|reload` and the empty-target check, and the layout of every copy to inGitDB changes (one file per record → one records file per collection), which rewrites the expectations of `AC:sqlite-to-ingitdb-chinook-roundtrip` and the filtering ACs. *Recommendation:* yes, as its own `cli/db/export` + `db copy` change after stage 1, announced in the release notes; not a blocker for `backup`.
3. **Retire the Approved `db snapshot` Idea?** [`db-snapshot-command`](../../../ideas/db-snapshot-command.md) proposed `db snapshot --project/--db/--env` writing timestamped local inGitDB trees. With `export` as the ad-hoc local shortcut and `backup` (folder destination) as the named, repeatable one, it duplicates both. *Recommendation:* archive it as superseded by this Feature and the `db export` decision; carry its default-path idea into the `cli/db/export` Feature.

Not founder decisions (resolved in this spec, listed so a reviewer can challenge them): restore always needs `--to`; `--fail-on-warning` exits `1`, not `3`; backups are not gated by query access policies; v1 never deletes backed-up data (retention is a later Feature); git destinations keep all history; chain concurrency uses create-only manifests, not leases.

---
*This document follows the https://specscore.md/feature-specification*
