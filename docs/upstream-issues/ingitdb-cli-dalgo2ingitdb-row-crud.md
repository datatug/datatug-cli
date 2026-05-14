# Upstream issue: dalgo2ingitdb row CRUD

**Target repo:** `ingitdb/ingitdb-cli`
**Suggested title:** `dalgo2ingitdb: implement row CRUD (Insert / Get / RunReadwriteTransaction / ExecuteQuery*) — blocks datatug db copy row streaming`

**File with:**

```sh
gh issue create --repo ingitdb/ingitdb-cli \
  --title 'dalgo2ingitdb: implement row CRUD (Insert / Get / RunReadwriteTransaction / ExecuteQuery*) — blocks datatug db copy row streaming' \
  --body-file docs/upstream-issues/ingitdb-cli-dalgo2ingitdb-row-crud.md
```

---

## Context

`pkg/dalgo2ingitdb` currently implements only the schema half of `dal.DB`:

- ✅ `dbschema.SchemaReader` — `ListCollections`, `DescribeCollection`, etc.
- ✅ `ddl.SchemaModifier` — `CreateCollection`, `DropCollection`, `AlterCollection`.
- ✅ `dal.ConcurrencyAware` — embeds `dal.ConcurrencyAvailable`.

Every row-level method is stubbed with `dal.ErrNotSupported` (per `pkg/dalgo2ingitdb/database.go` lines 82-118):

```go
func (db *Database) Get(...)                            → dal.ErrNotSupported
func (db *Database) GetMulti(...)                       → dal.ErrNotSupported
func (db *Database) Exists(...)                         → dal.ErrNotSupported
func (db *Database) RunReadonlyTransaction(...)         → dal.ErrNotSupported
func (db *Database) RunReadwriteTransaction(...)        → dal.ErrNotSupported
func (db *Database) ExecuteQueryToRecordsReader(...)    → dal.ErrNotSupported
func (db *Database) ExecuteQueryToRecordsetReader(...)  → dal.ErrNotSupported
```

The driver's package comment acknowledges this: "record access will be added in a follow-up."

## Problem

`datatug db copy --from <source> --to ingitdb://<project>` cannot stream rows into an inGitDB project today. The downstream Feature ([`datatug-cli` Feature spec `cli/db/copy`](https://github.com/datatug/datatug-cli/blob/main/spec/features/cli/db/copy/README.md), [Plan `2026-05-14-db-copy-mvp`](https://github.com/datatug/datatug-cli/blob/main/spec/plans/2026-05-14-db-copy-mvp.md)) is shipping in a **schema-only** initial slice — collection defs, indexes, and PKs are replicated to the target; row data is deferred — explicitly because of this gap.

## Minimum surface needed by `datatug db copy`

In rough priority order for the row-copy step to light up:

1. `RunReadwriteTransaction` returning a `dal.ReadwriteTransaction` whose `InsertMulti(ctx, records, opts...)` writes record bodies to the inGitDB collection storage (one YAML/JSON file per record, keyed by PK).
2. `ExecuteQueryToRecordsetReader` for full-table scans (source-side need; can be deferred if inGitDB is target-only at first).
3. `Get` / `Exists` — used by the "empty target check" pre-flight in `db copy`.

`InsertMulti` alone unblocks the "any → inGitDB" half of the spec's MVP.

## How `datatug db copy` will consume this

Per source table, build records whose `Key.ID` is the PK column value(s) and whose `Data()` is something the driver can serialize. The downstream consumer is open to whatever shape inGitDB wants (`map[string]any`, a typed struct, a byte buffer) — the spec just needs the Record→file write path to exist.

## Out of scope (for this issue)

- Transactional guarantees across a multi-table copy (the consumer already commits to "partial failure leaves completed tables intact" per `db copy` REQ:partial-failure-leaves-state).
- Concurrent writers (the consumer caps `--parallel-streams` per the existing `ConcurrencyAware` signal).

## Acceptance bar

A consumer can call

```go
db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
    return tx.InsertMulti(ctx, records)
})
```

against an inGitDB-backed `*Database` and find the resulting records on disk under the project root, readable via the inGitDB CLI's existing read paths.

---

Filed by the `datatug-cli` `db copy` Feature implementation. Tracking link: see `spec/plans/2026-05-14-db-copy-mvp.md` and the related Idea `spec/ideas/cross-engine-db-copy.md` in the datatug-cli repo.
