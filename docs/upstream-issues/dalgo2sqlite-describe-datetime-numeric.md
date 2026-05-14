# Upstream issue: dalgo2sqlite DescribeCollection rejects DATETIME / NUMERIC

**Target repo:** `dal-go/dalgo2sqlite`
**Suggested title:** `dbschema: DescribeCollection rejects DATETIME and NUMERIC(p,s) — breaks 4/11 Chinook tables`

**File with:**

```sh
gh issue create --repo dal-go/dalgo2sqlite \
  --title 'dbschema: DescribeCollection rejects DATETIME and NUMERIC(p,s) — breaks 4/11 Chinook tables' \
  --body-file docs/upstream-issues/dalgo2sqlite-describe-datetime-numeric.md
```

---

## Problem

`*Database.DescribeCollection(ctx, ref)` returns

```
dbschema: operation not supported; op=DescribeCollection; backend=dalgo2sqlite; reason=column "X" has unrecognized SQLite type "Y"
```

for `DATETIME` and `NUMERIC(p,s)` columns. Reproduces against the canonical [Chinook SQLite fixture](https://github.com/datatug/chinook-database/blob/master/ChinookDatabase/DataSources/Chinook_Sqlite.sqlite?raw=true): 4 of 11 tables fail to describe:

| Table | Unrecognized type | Column |
|---|---|---|
| Employee | DATETIME | BirthDate, HireDate |
| Invoice | DATETIME | InvoiceDate |
| InvoiceLine | NUMERIC(10,2) | UnitPrice |
| Track | NUMERIC(10,2) | UnitPrice |

7 of 11 tables describe fine (Album, Artist, Customer, Genre, MediaType, Playlist, PlaylistTrack — these use only INTEGER / NVARCHAR / TEXT).

## Why it matters downstream

`datatug-cli`'s new `db copy` command (Feature spec [`cli/db/copy`](https://github.com/datatug/datatug-cli/blob/main/spec/features/cli/db/copy/README.md)) builds against `dbschema.SchemaReader` so it can replicate the source schema into any DALgo backend. With these two types missing, full-Chinook E2E test coverage isn't reachable — the consumer's type-mapping tests in [`pkg/dbcopy/typemap_test.go`](https://github.com/datatug/datatug-cli/blob/main/pkg/dbcopy/typemap_test.go) currently skip these 4 tables.

## Expected behavior

`DescribeCollection` should map:

- `DATETIME` → `dbschema.Time` (matching SQLite's date/time storage class affinity).
- `NUMERIC(p,s)` → `dbschema.Decimal` with the parsed precision/scale carried on the `FieldDef`'s `Precision` field (per `dal-go/dalgo/dbschema/type.go`).

Both types are already declared in the `dbschema` vocabulary, so this is a recognizer/parser gap in `dalgo2sqlite`'s SQLite-affinity → `dbschema.Type` mapping, not a missing dbschema feature.

## Reproduction

```go
db, _ := dalgo2sqlite.NewDatabase("chinook.db")
ref := dal.NewRootCollectionRef("Invoice", "")
_, err := db.DescribeCollection(context.Background(), &ref)
// err: dbschema: operation not supported; … reason=column "InvoiceDate" has unrecognized SQLite type "DATETIME"
```

---

Filed by the `datatug-cli` `db copy` Feature implementation. Tracking: `pkg/dbcopy/typemap_test.go` in datatug-cli logs each skipped collection with this error verbatim.
