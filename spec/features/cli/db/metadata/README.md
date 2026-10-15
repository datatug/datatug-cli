---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: `datatug db` metadata commands

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/metadata?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/metadata?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/metadata?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/metadata?op=request-change) |

**Status:** Approved
**Source Ideas:** —
**Parent Feature:** [`cli/db`](../README.md)

## Summary

A read-only, non-interactive set of `datatug db` commands that report everything a tool needs to know about a database opened from a connection string or file path: [`db schema`](../schema/README.md) (database level, existing), `db table <name>` (one table or view in full), `db column <table> <column>` (one column) and `db objects` (everything else: indexes, triggers, sequences, routines, materialized views). They exist so that the DataTug Claude Code mod and other callers read types, nullability, keys and relationships from the CLI instead of inferring them from rows or from project scans.

## Synopsis

```
datatug db table  <name> --db <url|path> [--schema <schema>] [--counts] [--format json|text]
datatug db column <table> <column> --db <url|path> [--schema <schema>] [--format json|text]
datatug db objects --db <url|path> [--kind index|trigger|view|materialized-view|sequence|routine] [--schema <schema>] [--format json|text]
```

`--db` takes every form `db schema` takes (URL, `env:NAME`, plain SQLite path, with `detectedFrom`).

## Problem

`db schema --columns` lists names and declared types but not keys, relationships, indexes, defaults semantics or what a provider cannot answer. A caller that needs them today guesses from sample rows, and every guess is a defect waiting to happen.

## Behavior

### Common rules

#### REQ: common-envelope

Every command prints one JSON object (default) beginning `{"provider", "target", "detectedFrom", ...}` with the same meaning as in `db schema`; `--format text` prints a human summary. Source resolution, read-only opening, the PostgreSQL preview switch, exit codes (2 usage, 4 database) and credential redaction are exactly those of `db schema`. Field names are stable; releases add fields and never rename or remove one.

#### REQ: unsupported-marker

A fact the provider cannot answer MUST NOT be guessed or silently omitted. Each object carries `unsupported`, an array of the field names (`"comment"`, `"size"`, `"triggers"`, ...) that this provider/release does not report, so that a missing key means "not supported" and an empty array means "none".

#### REQ: no-internal-keys

No output may contain DALgo's internal record key (`$key`, `__dalgo_record_id`) as a column or field.

#### REQ: not-found

A table, view or column that does not exist MUST exit `2` with a one-line error naming the missing object. A name that exists in several schemas without `--schema` exits `2` listing the schemas.

### `db table`

#### REQ: table-fields

`db table` prints, in addition to the envelope: `schema`, `name`, `kind` (`table`, `view`, `materialized-view`), `columns` (every column object of REQ column-fields, in definition order), `primaryKey` (ordered column names), `foreignKeys` (outgoing: `{name, columns, referencedSchema, referencedTable, referencedColumns, onUpdate, onDelete}`), `referencedBy` (incoming: `{schema, table, columns}` the referencing columns), `indexes` (`{name, columns, unique, partial, definition}`), `constraints` (`{name, type}` as the provider lists them), `definition` (the SQL of a view, when the provider keeps it), and `rowCount` with `--counts` (native COUNT only; `null` when it cannot be counted, as in `db schema`).

#### REQ: table-extras

Later phases add `uniqueConstraints`, `checkConstraints`, `triggers`, `comment` and `size`; until a provider reports them they are named in `unsupported`.

### `db column`

#### REQ: column-fields

A column object has: `name`, `ordinal` (1-based definition order), `type` (the declared type text, e.g. `NVARCHAR(160)`), `length`, `precision`, `scale` (numbers parsed from the declared type; absent when it has none), `family` (`numeric`, `text`, `date`, `boolean`, `binary`, `json`, `other`), `nullable`, `default` (expression text, absent when none), `primaryKeyPosition` (1-based, absent when not in the key), `autoIncrement`, `foreignKeys` (outgoing keys the column is part of) and `referencedBy` (incoming keys that reference it). `collation`, `generated` and `comment` are reported when the provider answers and are named in `unsupported` otherwise (as `columns.collation`, `columns.generated`, `columns.comment`).

#### REQ: column-command

`db column <table> <column>` prints the envelope, `schema`, `table` and the one column object of REQ column-fields.

### `db objects`

#### REQ: objects-listing

`db objects` lists `{schema, kind, name, table?, definition?}` for every object of the requested kind(s) (all kinds without `--kind`), sorted by schema, kind, name. A kind the provider cannot list is named in `unsupported`.

## Provider capability matrix

| Fact | SQLite | PostgreSQL | Phase |
|---|---|---|---|
| tables, views, schemas | yes (schema `main`) | yes | done (`db schema`) |
| column name, order, declared type, nullable, default, PK position | yes | yes | 1 |
| length / precision / scale | parsed from declared type | from declared type | 1 |
| autoIncrement | yes | yes (identity) | 1 |
| primary key | yes | yes | 1 |
| foreign keys, both directions | yes | yes | 1 |
| indexes (columns, unique, partial/expression text) | yes | yes | 1 |
| constraint names and kinds | primary, foreign | primary, unique, foreign | 1 |
| view definition | yes | later (DALgo reads it for the configured schema only) | 1 (SQLite), 2 (PostgreSQL) |
| unique / check constraints | unique via indexes; check unsupported | unique; check later | 2 |
| collation, generated, comment | collation/generated later; comment not applicable | later | 2 |
| triggers, sequences, routines, materialized views, size, server version | triggers later; none of the others exist | later | 3 |

## Phases

Each phase lands on its own: phase 1 is `db table` and `db column` with the facts the matrix marks 1; phase 2 adds the constraint, collation, generated and comment facts; phase 3 adds `db objects` and the remaining object kinds. A fact a phase does not yet report is named in `unsupported`.

## Acceptance Criteria

### AC: table-lists-keys-and-indexes

**Requirements:** db/metadata#req:table-fields

`datatug db table Album --db <chinook>` prints columns with types and nullability, `primaryKey` `["AlbumId"]`, a foreign key to `Artist`, `referencedBy` containing `Track`, and its index on `ArtistId`.

### AC: column-detail

**Requirements:** db/metadata#req:column-fields, db/metadata#req:column-command

`datatug db column Album Title --db <chinook>` prints type `NVARCHAR(160)` with `length` 160, `family` `text`, `nullable` false and `ordinal` 2; `db column Album ArtistId` lists the outgoing key to `Artist` under `foreignKeys`.

### AC: unsupported-is-marked

**Requirements:** db/metadata#req:unsupported-marker

A fact the provider has no answer to appears in `unsupported` and is absent from the object; it is never printed as an empty value.

### AC: missing-object

**Requirements:** db/metadata#req:not-found

`db table Nope` and `db column Album Nope` exit `2` with a one-line error naming the missing object.

### AC: no-key-leak

**Requirements:** db/metadata#req:no-internal-keys

No output of these commands contains `$key` or `__dalgo_record_id`.

### AC: envelope-matches-schema

**Requirements:** db/metadata#req:common-envelope

`db table` with an `env:NAME` source prints the same `provider`, `target` and `detectedFrom` as `db schema` and no credentials.

## Known provider limits

- The SQLite adapter refuses to describe a table that has a column of a type it does not recognise (for example `JSON`): `db table` and `db column` exit `4` for such a table, and `db schema --columns` reports `columns: null` for it. The fix belongs to the adapter.
- A SQLite view reports column names only (no type, nullability or key facts); they are named in `unsupported`.

## Open Questions

- Which provider reports table size cheaply without a catalog query DALgo does not expose (phase 3 decides)?

---
*This document follows the https://specscore.md/feature-specification*
