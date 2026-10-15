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

`datatug db schema --db <url> [--format json|text]` is a read-only, non-interactive command that opens a database from a connection string, detects the provider from it, and prints the tables and views it holds. It exists so that tools (the DataTug Claude Code mod, scripts, CI) learn what a database contains from the CLI instead of parsing database files or catalog SQL themselves.

## Synopsis

```
datatug db schema --db <url> [--format json|text]
```

`--db` takes the same forms as [`datatug query run --db`](../../query/README.md): `sqlite://`, `postgres://`, `ingitdb://`, `http(s)://`, `openvaultdb://`, `bigquery://`, or `env:NAME` naming an environment variable that holds the whole URL.

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

SQLite and PostgreSQL MUST list their tables and views. PostgreSQL stays behind the existing preview switch (`DATATUG_PREVIEW_POSTGRES=1`) and is opened read-only.

#### REQ: unsupported-provider

A source whose scheme parses but is not `sqlite` or `postgres` MUST exit non-zero with a one-line error that names the supported providers (`sqlite, postgres`) and the unsupported one. The command MUST NOT guess or open the source.

### Output

#### REQ: json-shape

`--format json` (the default) prints one JSON object: `{"provider": "sqlite", "target": "<url>", "tables": [{"schema": "main", "name": "Album", "kind": "table"}]}`. `kind` is `table` or `view`. `tables` is always an array (empty, never null). SQLite reports schema `main`.

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
| `--db` | string | yes | Source URL or `env:NAME`. |
| `--format` | string | no | `json` (default) or `text`. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Listing printed |
| `2` | Missing/unparseable `--db`, unknown `--format`, unsupported provider |
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

### AC: read-only-open

**Requirements:** db/schema#req:read-only

The source is opened through `OpenProtected`.

## Out of Scope

- Column, index and constraint listings (a later feature).
- `ingitdb://`, `http(s)://`, `openvaultdb://` and `bigquery://` listings: reported as unsupported in the first release.

## Open Questions

- None for the first release; column listings and the remaining providers will get their own specs.

---
*This document follows the https://specscore.md/feature-specification*
