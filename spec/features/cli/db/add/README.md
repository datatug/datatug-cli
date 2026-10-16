---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: `datatug db add`

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/add?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/add?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/add?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db/add?op=request-change) |

**Status:** Implementing
**Source Ideas:** —
**Parent Feature:** [`cli/db`](../README.md)

## Summary

`datatug db add --project <id> --db <path|url|env:NAME> [--env <ENV>] [--name <id>] [--format json|text]` registers a database as a source of an environment of a registered DataTug project, and does nothing else: it never opens the database, so it reads no metadata and no row. It writes the same files a [scan](../../scan/README.md) writes for a catalog (the catalog file and the environment entry, and for PostgreSQL the connection descriptor), less the metadata a scan reads, so that the source is listed by [`datatug show`](../../show/README.md), opened by [`db schema`, `db table`, `db column`](../schema/README.md), [`query run`](../../query/README.md) and `db copy` with `--db datatug://<project>/<db>`, and described later by a scan of the same `--db` id. It exists so that tools (the DataTug Claude Code mod, scripts) register a database through the CLI instead of writing project files themselves.

## Synopsis

```
datatug db add --project <id> --db <sqlite file|sqlite:// URL|env:NAME> [--env <ENV>] [--name <id>] [--format json|text]
```

## Problem

A project learns of a database only through `scan`, which connects to it, reads its whole schema and needs every connection flag. A caller that only wants to say "this project uses that database" has no lightweight command, and the alternative, writing `environments/<env>/…` and `connections/…` files by hand, makes every caller re-implement the project layout and the rule that a project never holds a credential.

## Behavior

### Registering a source

#### REQ: does-not-open-the-database

The command MUST NOT open the database, connect to it or read a row from it. For a SQLite file it looks at the path only (the file must exist and must not be a folder) and, for a `--db` that is a path with no recognised extension, reads the first 16 bytes the way [`db schema` path detection](../schema/README.md#req-path-detection) does; for `env:NAME` it never connects. The catalog it writes has no database model, so [`show`](../../show/README.md) lists the source as `notScanned` until a scan describes it.

#### REQ: project-is-registered

`--project <id>` MUST name a registered project (as [`show --project`](../../show/README.md) resolves it, through the same code). It is required. A name that is not registered, or a registered folder that holds no project file, MUST exit `3`.

#### REQ: db-forms

`--db` is required and takes one of: a plain SQLite file path (detected as [`db schema`](../schema/README.md#req-path-detection) detects it, with `~/` expanded and a relative path resolved against the working directory); a `sqlite://` URL; or `env:NAME`, the name of an environment variable that holds a PostgreSQL URL. A `postgres://` or `postgresql://` URL, which carries credentials, MUST be refused with a one-line message that says to put the URL in an environment variable and pass `--db env:NAME`; the message and every other output MUST NOT hold any part of the URL. Any other provider MUST be refused with a one-line message that names the provider and the accepted forms.

#### REQ: sqlite-path-stored-as-scan-stores-it

A SQLite source MUST be recorded with driver `sqlite3` and its path stored as [a scan stores it](../../scan/README.md#req-sqlite-path-stored-portably): relative to the project folder when the file is inside it, `~/…` when under the home directory, absolute otherwise. A SQLite source that is the same file as one the environment already lists (compared after the stored path is resolved with `ResolveCatalogPath`, and after links are resolved) is the same database.

#### REQ: postgres-by-environment-variable

An `env:NAME` source MUST be recorded with driver `postgres`, a catalog whose path is the connection descriptor `connections/<env>/<name>.json`, and a descriptor that holds `{"dsnEnv": "NAME"}` and nothing else, written by the one writer a PostgreSQL scan uses. NAME MUST be an environment variable name (`[A-Z][A-Z0-9_]*`) that starts with `DATATUG_` or is listed in `DATATUG_DSN_ENV_ALLOW`, as for [scan `--dsn-env`](../../scan/README.md#req-postgres-connection-from-environment). No project file holds a host, port, user, password or URL ([REQ: postgres-project-holds-no-connection](../../scan/README.md#req-postgres-project-holds-no-connection)). The variable is not required to be set: when it is not, the command says so on stderr (one `note:` line) and records its name; when it is set it MUST hold a PostgreSQL URL (a value of another provider, or one that cannot be read, is refused with a message that names the variable and not its value). The same database is the same variable name.

#### REQ: names-are-plain

`--env` and `--name` are names of folders of the project, so each MUST be [a plain name](../../scan/README.md#req-names-are-plain), and MUST NOT differ only by case from one the project has; otherwise the command exits `2` before it writes, with a message that names the flag and does not repeat a value that is not a plain name.

#### REQ: default-name

`--name` defaults, for a SQLite file, to its file name without the extension, each character a plain name does not allow written as `-` and leading characters that are not a letter or digit removed; when that is not a plain name the command MUST exit `2` and say `--name` is required. For an `env:NAME` source `--name` is required (it exits `2` without it).

#### REQ: default-env

When `--env` is omitted it defaults to: the project's environment when the project has exactly one; the environment whose id is `local` when the project has several and one of them is `local`; otherwise `--env` is required and the command MUST exit `2`, with a fixed message that lists the environment ids when there are several (an environment is never made by default), or says the project has no environment yet when there is none. An explicit `--env` is used as given: one that the project does not have is made, as a scan makes it. The `env` field of the [JSON result](#req-json-shape) reports the environment used.

#### REQ: idempotent

When the same database is already a source of the environment (the same resolved SQLite file, or the same `dsnEnv` variable name) under any id, the command MUST change nothing on disk and print `added` false with the id the source already has. Otherwise it writes and prints `added` true.

#### REQ: never-overwrites-another-database

When the environment already has a source with the id of `--name` that is another database (another file, another variable, or another driver), the command MUST exit `2` and write nothing; the message names the id, the environment and the recorded driver, and no path, URL or variable value. A source whose recorded path or descriptor cannot be read cannot be told from the database given: it is refused the same way, with a message that says so.

#### REQ: failed-write-takes-back

A write that fails after the descriptor or the catalog file was written MUST take back what the command wrote (the descriptor and the folders it made, the catalog file), so that no catalog is left that the environment file does not list.

### Output

#### REQ: json-shape

`--format json` (the default) MUST print one line of JSON: `{"added": <bool>, "id": "<source id>", "env": "<environment>", "driver": "<sqlite3|postgres>", "dbRef": "<reference>"}`. `id` is the id the source has in the project (for `added` false, the existing one). `dbRef` is the exact `--db` value that addresses the source in `db schema`, `db table`, `db column`, `query run`, `db copy` and `db export`: `datatug://<project>/<id>` ([grammar](../schema/README.md#req-project-reference)), with `?env=<env>` when `--env` was given or when another environment of the project lists a source of the same id (so the reference is not ambiguous). The line holds no path, URL, variable value or credential. Fields are added and never renamed.

#### REQ: text-format

`--format text` MUST print one line: `added <id> (<driver>) in environment <env>: --db <dbRef>`, or `already registered as <id> (<driver>) in environment <env>: --db <dbRef>`. Any other `--format` than `json` and `text` MUST exit `2`.

#### REQ: no-credentials-in-output

No credential, URL or connection string appears in stdout, stderr, an error or a project file.

## Parameters

| Flag | Aliases | Type | Required | Description |
|---|---|---|---|---|
| `--project` | `-p` | string | yes | Registered project id/name the source is added to. |
| `--db` | | string | yes | SQLite file path or `sqlite://` URL, or `env:NAME` naming the variable that holds a PostgreSQL URL. |
| `--env` | | string | when the project has none, or several without `local` | Environment of the project; made when the project has none of that id. Default: its only environment, or `local` when it has several. |
| `--name` | | string | for `env:NAME`, or when the file name is not a plain name | Id of the source: a plain name. |
| `--format` | | string | no | `json` (default) or `text`. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | The source was added, or was already a source (`added` false) |
| `2` | Missing `--project`, `--db`, `--env` or `--name` where required; an unsupported `--format`; a name that is not plain; a `--db` that is a credentialed URL, another provider, a missing file or a variable a project may not name; an id already taken by another database |
| `3` | `--project` names no registered project, or its folder holds no project file |
| `1` | A file of the project cannot be read or written |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [db](../README.md) | Parent. |
| [scan](../../scan/README.md) | Writes the same files; a later scan of the same `--db` id and environment describes the source and keeps the path (SQLite) or descriptor (PostgreSQL) it was registered with. |
| [show](../../show/README.md) | Lists the registered source (`notScanned` until scanned), with `path` for SQLite and `dsnEnv` for PostgreSQL. |
| [db schema](../schema/README.md) | Opens the source with `--db <dbRef>`; the reference grammar is defined there. |
| [query](../../query/README.md) | `query run --db <dbRef>` reads the source. |

## Acceptance Criteria

### AC: adds-a-sqlite-source

**Requirements:** db/add#req:does-not-open-the-database, db/add#req:sqlite-path-stored-as-scan-stores-it, db/add#req:json-shape

`datatug db add --project shop --db ./shop.sqlite --env local` prints `{"added":true,"id":"shop","env":"local","driver":"sqlite3","dbRef":"datatug://shop/shop?env=local"}`, writes `environments/local/local.env.json` and `environments/local/catalogs/shop/shop.db.json` (driver `sqlite3`, the portable path, empty `dbModel`, `schemas` `[]`), and `show --depth sources` lists `shop` as `notScanned` with its `path`.

### AC: adds-a-postgres-source-by-variable

**Requirements:** db/add#req:postgres-by-environment-variable, db/add#req:no-credentials-in-output

`DATATUG_SHOP_PG=postgres://u:secret@h/db datatug db add --project shop --db env:DATATUG_SHOP_PG --name shop-pg --env prod` writes the catalog file with driver `postgres` and path `connections/prod/shop-pg.json`, a descriptor holding `{"dsnEnv": "DATATUG_SHOP_PG"}`, and an environment server holding the driver only; no file and no output holds `secret`, `u` as a user or the host.

### AC: credentialed-url-is-refused

**Requirements:** db/add#req:db-forms, db/add#req:no-credentials-in-output

`--db postgres://u:secret@h/db` exits `2` with one line that says to use an environment variable and `env:NAME`; the line, stdout and the project hold no part of the URL, and nothing is written.

### AC: same-database-is-idempotent

**Requirements:** db/add#req:idempotent

Running the same `db add` twice prints `added` true and then `added` false with the same id, and the second run changes no byte of the project. A second `--name` for the same SQLite file, or the same variable, prints `added` false with the first id.

### AC: another-database-under-the-same-name-is-refused

**Requirements:** db/add#req:never-overwrites-another-database

Adding another file, or an `env:NAME` source, under the `--name` of an existing source exits `2`, writes nothing, and names the id, the environment and the recorded driver.

### AC: default-environment-and-name

**Requirements:** db/add#req:default-env, db/add#req:default-name

In a project with one environment `--env` may be left out; with several it uses `local` when the project has it, and exits `2` listing the ids when it does not. `--db ./My Shop.sqlite` is `My-Shop`; `--db env:DATATUG_X` without `--name` exits `2`.

### AC: db-ref-opens-the-source

**Requirements:** db/add#req:json-shape

After `db add --project shop --db ./crm.sqlite --env local`, `datatug db schema --db datatug://shop/crm?env=local` (the `dbRef` printed) lists the tables of `./crm.sqlite`.

### AC: unknown-project-is-not-found

**Requirements:** db/add#req:project-is-registered

`--project nope` exits `3`.

### AC: a-failed-save-takes-the-write-back

**Requirements:** db/add#req:failed-write-takes-back

When the environment file cannot be saved, the catalog file and the descriptor this command wrote are removed.

## Open Questions

- Should `db add` also take `--directory` for a project folder that is not registered (as `show` does)? Today the registered project is the one form, as the callers register their projects first.
