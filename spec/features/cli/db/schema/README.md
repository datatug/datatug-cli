---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: `datatug db schema`

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/schema?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/schema?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/schema?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/schema?op=request-change) |

**Status:** Approved
**Source Ideas:** —
**Parent Feature:** [`cli/db`](../README.md)

## Summary

`datatug db schema --db <url|path> [--format json|text] [--columns] [--counts]` is a read-only, non-interactive command that opens a database from a connection string, detects the provider from it, and prints the tables and views it holds. It exists so that tools (the DataTug Claude Code mod, scripts, CI) learn what a database contains from the CLI instead of parsing database files or catalog SQL themselves.

## Synopsis

```
datatug db schema --db <url|path> [--format json|text] [--columns] [--counts]
datatug db schema --db datatug://<project>/<db>[?env=<ENV>] [--format json|text] [--columns] [--counts]
```

`--db` takes the same forms as [`datatug query run --db`](../../query/README.md): `sqlite://`, `postgres://`, `ingitdb://`, `http(s)://`, `openvaultdb://`, `bigquery://`, or `env:NAME` naming an environment variable that holds the whole URL. A plain file path (no scheme) is accepted too and detected as SQLite. `--db datatug://<project>/<db>[?env=<ENV>]` names a database that is a source of a registered project (see [REQ: project-reference](#req-project-reference)).

## Problem

`datatug db` is a viewer and `datatug scan` writes into a project. Neither answers "what tables does this database hold?" non-interactively. Callers fake it by running `query run --from sqlite_master`, which is SQLite-only and puts catalog SQL in the caller.

## Behavior

### Source resolution

#### REQ: reuse-source-parsing

`--db` MUST be parsed by the same `dbcopy.Parse` as `query run --db` (including `env:NAME`), and the source MUST be opened through the same DALgo driver selection. No second URL parser or driver switch is written for this command.

#### REQ: env-source-keeps-secret-out-of-argv

When `--db env:NAME` is given the URL is read from the environment variable `NAME`; the command line carries only the variable name.

### Providers

#### REQ: supported-providers

SQLite and PostgreSQL MUST list their tables and views. PostgreSQL is opened read-only.

#### REQ: unsupported-provider

A source whose scheme parses but is not `sqlite` or `postgres` MUST exit non-zero with a one-line error that names the supported providers (`sqlite, postgres`) and the unsupported one. The command MUST NOT guess or open the source.

### File paths

#### REQ: path-detection

A `--db` value that is not in URL form (no `scheme:` prefix of two or more characters, not `env:NAME`) MUST be treated as a file path: a leading `~/` is expanded to the home directory and a relative path is resolved against the current directory. The path is a SQLite database when its extension (case-insensitive) is `.sqlite`, `.sqlite3`, `.db`, `.db3`, `.s3db` or `.sl3`, or, failing that, when the file starts with the 16 bytes `SQLite format 3\0`. Anything else MUST exit `2` with a one-line error. A path holding `?`, `#`, `@` or `%` MUST be refused with a one-line error, because the SQLite URL parser would read it as part of a URL.

#### REQ: project-reference

`--db` MUST accept a reference to a database that is a source of a registered DataTug project (one `datatug show --depth sources` lists, added by [`db add`](../add/README.md) or by a scan), so that a caller addresses a database by the id the project gives it and never by a path or a URL. The grammar is

```
datatug://<project>/<db>[?env=<ENV>]
datatug:///<db>[?env=<ENV>]
```

`<project>` is the id of a registered project, resolved as [`show --project`](../../show/README.md) resolves it (through the same code); `<db>` is the source id (the catalog id); `<ENV>` is an environment of the project. Each is a plain name (letters, digits, `.`, `_`, `-`); `env` is the only query parameter; anything else (user information, a fragment, another parameter, more path) is refused with a fixed sentence that quotes none of the reference. An empty `<project>` (`datatug:///<db>`) is the project of the working folder when it holds a project file, else the one registered project; with neither, the command exits `2` saying the reference names no project. `?env=` may be left out when the database id is listed by exactly one environment of the project; when several list it the command MUST exit `2` with a message that lists their ids and nothing else.

The reference is resolved by ONE function of the CLI (`parseSourceArg`, used by `db schema`, `db table`, `db column`, [`query run`](../../query/README.md#req-db-project-reference), `db copy` and `db export`) from the project's catalog and connection descriptor through the resolver `chat` and `serve` use (`pkg/api`), never by the command reading a project file: the source is the `sqlite://` URL of the file the catalog's path resolves to (`ResolveCatalogPath`) for a SQLite source, or `env:<dsnEnv>` for a PostgreSQL source, which is then parsed, checked and opened exactly as a `--db` value is (the variable need not be set when the reference is resolved; opening a source whose variable is not set says so, naming the variable). The reference is what the source is called: `target` of the output is the reference, and no message holds the path, a URL or the value of a variable (a PostgreSQL source's failure names the variable, as it does for `env:NAME`). Exit codes: `3` for a project, an environment or a database that is not there; `2` for a reference that is not well formed, one that names no project, a database id several environments list, a source whose driver is not `sqlite3` or `postgres` (fixed message naming the database and no path), or whose path or connection descriptor does not resolve (a catalog with no descriptor, a descriptor outside the project, a descriptor that cannot be read, a path that is a URL).

#### REQ: detected-from

The JSON output MUST carry `detectedFrom`: `scheme` for a URL or `env:NAME` source, `file name` when the extension decided, `file content` when the header did, `project` when the source was named by a `datatug://` reference.

### Columns and counts

#### REQ: columns-flag

With `--columns` every table and view MUST carry `columns`: an array of `{name, type, nullable, primaryKeyPosition?, default?}` in declared order. `type` is the provider's declared type text (`NVARCHAR(160)`, `NUMERIC(5,2)`), `primaryKeyPosition` (1-based) is present for primary-key columns, `default` is the default expression text when one is declared. A table whose columns cannot be read reports `columns: null`. SQLite does not describe views: a SQLite view reports its column names with an empty `type` and `nullable: true`. Without the flag `columns` is absent.

#### REQ: counts-flag

With `--counts` every table (never a view) MUST carry `rowCount`, the number of its rows from one native `COUNT(*)`. A table that cannot be counted (the source does not run COUNT natively, or the count fails) reports `rowCount: null`; the command MUST NOT read the table's rows to count them. Without the flag `rowCount` is absent.

#### REQ: foreign-keys-with-columns

With `--columns` every table whose definition could be read MUST also carry `foreignKeys`, its outgoing keys named as `datatug show` names them: `[{"name", "columns": [...], "refTable": {"schema", "name"}, "refColumns": [...]}]`; a table with no keys has an empty list. A view, and a table whose definition could not be read, carries no `foreignKeys` key. Without `--columns` the key is absent.

### Output

#### REQ: json-shape

`--format json` (the default) prints one JSON object: `{"provider": "sqlite", "target": "<url>", "detectedFrom": "scheme", "tables": [{"schema": "main", "name": "Album", "kind": "table"}]}`. `kind` is `table` or `view`. `tables` is always an array (empty, never null). SQLite reports schema `main`.

#### REQ: sorted

Entries MUST be sorted by schema, then kind (`table` before `view`), then name.

#### REQ: text-format

`--format text` prints a `provider:` line, a `target:` line, then one `schema<TAB>kind<TAB>name` line per entry in the same order.

#### REQ: unknown-format

An unknown `--format` exits `2`, naming the allowed values.

### Safety

#### REQ: read-only

The command MUST NOT modify the database. SQLite and PostgreSQL are opened through the protected (read-only) open path.

#### REQ: no-credentials-in-output

`target` is the source URL with userinfo, query string and fragment removed. For an `env:NAME` source it is the resolved URL stripped the same way, never the variable's raw value. No credential appears in stdout, stderr or any error.

## Parameters

| Flag | Type | Required | Description |
|---|---|---|---|
| `--db` | string | yes | Source URL, `env:NAME`, a SQLite file path, or a `datatug://<project>/<db>[?env=<ENV>]` reference to a source of a registered project. |
| `--format` | string | no | `json` (default) or `text`. |
| `--columns` | bool | no | List columns of every table and view. |
| `--counts` | bool | no | Count the rows of every table. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Listing printed |
| `2` | Missing/unparseable `--db` (a `datatug://` reference that is malformed, names no project, or a database several environments list included), unknown `--format`, unsupported provider, a source of the project that cannot be resolved |
| `3` | The referenced project is not registered, or the environment or the database is not in it |
| `4` | The database could not be opened or read |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [db](../README.md) | Parent. |
| [query](../../query/README.md) | Shares `--db` parsing and driver selection. |

## Acceptance Criteria

### AC: sqlite-lists-tables

**Requirements:** db/schema#req:supported-providers, db/schema#req:json-shape, db/schema#req:sorted

`datatug db schema --db sqlite://<chinook>` prints JSON with provider `sqlite` and Chinook's 11 tables, each with schema `main` and kind `table`, sorted by name.

### AC: sqlite-lists-views

**Requirements:** db/schema#req:json-shape, db/schema#req:sorted

A SQLite database holding a view lists it with kind `view`, after the tables.

### AC: env-source

**Requirements:** db/schema#req:reuse-source-parsing, db/schema#req:env-source-keeps-secret-out-of-argv

`DATATUG_OPEN_DB=sqlite://<path> datatug db schema --db env:DATATUG_OPEN_DB` lists the same tables as the direct URL.

### AC: postgres-lists-schemas

**Requirements:** db/schema#req:supported-providers, db/schema#req:sorted

For a PostgreSQL source, tables and views of every user schema are listed with their schema, sorted by schema, kind, name.

### AC: unsupported-provider-rejected

**Requirements:** db/schema#req:unsupported-provider

`--db ingitdb://./x` exits non-zero with one line naming `sqlite, postgres`.

### AC: no-credentials

**Requirements:** db/schema#req:no-credentials-in-output

`--db postgres://alice:secret@host/db?sslmode=disable` (open failing) prints neither `alice`, `secret` nor the query anywhere.

### AC: text-format

**Requirements:** db/schema#req:text-format, db/schema#req:unknown-format

`--format text` prints the provider, target and tab-separated lines; `--format yaml` exits `2`.

### AC: path-by-extension

**Requirements:** db/schema#req:path-detection, db/schema#req:detected-from

`datatug db schema --db ~/data/chinook.sqlite` lists the tables with `detectedFrom` `file name`; a relative path resolves against the current directory.

### AC: path-by-content

**Requirements:** db/schema#req:path-detection, db/schema#req:detected-from

A SQLite file named `chinook` (no extension) is detected with `detectedFrom` `file content`; a text file exits `2` with a one-line error.

### AC: project-reference

**Requirements:** db/schema#req:project-reference, db/schema#req:detected-from

For a project `shop` whose environment `local` lists the SQLite source `crm` (added by `db add` or scanned), `datatug db schema --db datatug://shop/crm` prints the same tables as `--db <that file>`, with `detectedFrom` `project` and `target` `datatug://shop/crm`; the output holds no path. `datatug:///crm` is the same in a project folder, or with one registered project. For a PostgreSQL source it opens `env:<dsnEnv>`.

### AC: project-reference-errors

**Requirements:** db/schema#req:project-reference

`datatug://nope/crm`, `datatug://shop/nope` and `datatug://shop/crm?env=nope` exit `3`; `datatug://shop` and `datatug://u:secret@shop/crm` exit `2` without repeating the reference; a database listed by environments `dev` and `local` without `?env=` exits `2` with `dev, local` and nothing else; a source of driver `sqlserver`, or a PostgreSQL source whose descriptor is missing, exits `2`.

### AC: columns-listed

**Requirements:** db/schema#req:columns-flag

`--columns` on Chinook lists `Album.Title` as type `NVARCHAR(160)`, `nullable` false, and `Album.AlbumId` with `primaryKeyPosition` 1; without the flag no `columns` key appears.

### AC: foreign-keys-listed

**Requirements:** db/schema#req:foreign-keys-with-columns

`datatug db schema --db <chinook> --columns` lists for `InvoiceLine` a key on `InvoiceId` to `refTable` `{schema: main, name: Invoice}` (any fixture with a table that references another behaves alike); a table with no key has `foreignKeys: []`; a view has no `foreignKeys` key.

### AC: counts-listed

**Requirements:** db/schema#req:counts-flag

`--counts` on Chinook reports `rowCount` 347 for `Album`; a view has no `rowCount`; a table that cannot be counted reports `null`.

### AC: read-only-open

**Requirements:** db/schema#req:read-only

The source is opened through `OpenProtected`.

## Out of Scope

- Index and constraint listings (a later feature).
- `ingitdb://`, `http(s)://`, `openvaultdb://` and `bigquery://` listings: reported as unsupported in the first release.

## Open Questions

- None for the first release; column listings and the remaining providers will get their own specs.

---
*This document follows the https://specscore.md/feature-specification*
