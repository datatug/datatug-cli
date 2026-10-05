---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Scan

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug scan` connects to a database, introspects its schema (tables, views, columns and primary keys), and writes the resulting metadata into the DataTug project on disk. Re-running `scan` updates the metadata in place — this is the primary path for keeping a DataTug project synchronized with a live database.

## Synopsis

```
datatug scan --project <id> --driver <driver> --server <host> --db <name> --env <env> \
  [--port <n>] [--user <name>] [--password <pw>] [--dbmodel <id>]

datatug scan --directory <path> --driver sqlite3 --path <file> --db <name> --env <env>
```

## Problem

DataTug projects encode database schemas as versionable on-disk files. Authoring those files by hand is infeasible for any non-trivial database. The agent needs a one-command path to read the live `information_schema` (or driver equivalent), normalize the result, and write a deterministic file set that diffs cleanly under Git.

`scan` is intentionally a separate command from `serve` so users can run it as a CI job (against a staging environment, for example) and commit the result.

## Behavior

### Required project context

#### REQ: requires-project

`scan` MUST be invoked inside a project context — either via the cwd being a DataTug project, via `--project <id>`, or via `--directory <path>` (`-d`; the shared CLI conventions, [REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution), call this flag `--dir`, but `--directory` is the name `scan` defines). If no project context can be resolved, the command MUST exit `3` (NotFound).

### Required connection details

#### REQ: required-db-flag

`--db <name>` MUST be required. It identifies the database (catalog) to scan.

#### REQ: required-env-flag

`--env <env>` MUST be required. It identifies the environment within the project the scanned metadata belongs to (e.g., `LOCAL`, `DEV`, `SIT`, `UAT`, `PROD`).

#### REQ: driver-selection

`--driver`/`-D` MUST specify the database driver. Supported values today: `sqlite3` (the file given by `--path`) and `sqlserver`. `postgres` is refused with a message that says the scan is not available in this release: a DataTug project cannot record a postgres server yet, and the scan stops before it connects. The set of supported drivers MUST match the drivers the binary links: `sqlserver` by the import in `main.go`, and the SQLite scan's own pure-Go driver by the import in `pkg/api/scan_db_schema_api.go` (see [REQ: sqlite-pure-go](#req-sqlite-pure-go)), which is where the scan opens it.

#### REQ: sqlite-pure-go

A SQLite scan MUST open the database file through a pure-Go `database/sql` driver (`modernc.org/sqlite`, registered as `sqlite`), not through the cgo-only `sqlite3` driver: a release is built with cgo off, where that driver is a stub that fails on its first use, so a scan that opened it could read no SQLite file in any released binary. A SQLite scan MUST NOT create the file: a `--path` that is not an existing file is an error that names it. It MUST open the file read-only (`file:<path>?mode=ro`), with `%`, `?` and `#` in the path percent-encoded, so that the file opened is the file that was checked (a file named `a#b.db` is that file, not a file named `a`), and it MUST put the name of every table, view and index it reads from the file into the statements it makes with it as a quoted name, never as SQL: a database is not trusted, and the driver runs every statement of a query string, so an unquoted name could make the scan create files and change the database it was asked to read.

#### REQ: connection-string-construction

For a network database the connection string MUST be built via `pkg/datatug-core/dbconnection.NewConnectionString(driver, host, user, password, db, options...)`. `port` and `mode=ReadOnly` MUST be appended as options when supplied. The CLI MUST connect in read-only mode for schema introspection. A SQLite scan has no host, user or password: it takes its path from `dbconnection.NewSQLite3ConnectionParams` and opens the file as [REQ: sqlite-pure-go](#req-sqlite-pure-go) says, read-only.

#### REQ: dbmodel-default

When `--dbmodel` is omitted, the DB model ID MUST default to the value of `--db`. This makes single-database projects easy to scan while preserving the ability to map multiple physical databases onto one logical model.

### Output to project store

#### REQ: persist-via-project-store

The scan result MUST be written into the project folder, in the layout of [Project layout written by a scan](#project-layout-written-by-a-scan) and nowhere else. The project file, the environment files, the database model files and the catalog file MUST be written through datatug-core's project store (`SaveProject`, and `SaveEnvDbCatalog` for the catalog file). datatug-core has no writer for the per-table files today (its writers are commented out), so, for launch, the CLI writes those files itself (`pkg/api/scan_layout.go`), using datatug-core's own file type for them (`filestore.TableModelColumnsFile`). A later task may move that writer, and the readers of these files (`pkg/api/catalog_tables_api.go`), into datatug-core; the files will not change when it does.

#### REQ: project-layout

A scan MUST write the files of [Project layout written by a scan](#project-layout-written-by-a-scan), with those fields, and no other file. The files MUST be the ones that every reader of a project reads, so that a scanned project and the demo project are read by the same code, with no special case. A `README.md` that is already in the project folder MUST NOT be replaced or changed by a scan, on the first scan or on a rescan.

#### REQ: sqlite-path-stored-portably

The path of a SQLite file in the catalog file MUST be relative to the project folder, with `/` separators, when the file is inside the project folder; relative to the home directory with a leading `~/` when it is under the home directory; and absolute otherwise. A `--path` that is relative is the file from the working directory the scan was run in. Every reader MUST resolve the stored path to the file that was scanned, from any working directory (`ResolveCatalogPath` in `pkg/api/catalog_path.go`).

#### REQ: unusable-names-left-out

A table or view, or a schema, whose name cannot be a folder name on every system a project is opened on MUST be left out of the project and named on stderr, with the reason, one line for each; the scan MUST still exit `0` and write everything else. A name cannot be a folder name when it is empty, is `.` or `..`, is longer than 200 bytes, ends in a dot or a space, is one Windows reserves for a device (`con`, `prn`, `aux`, `nul`, `com1` to `com9`, `lpt1` to `lpt9`, with or without an extension), or holds a control character or one of `/ \ : * ? " < > |`. A schema, or a table or view, whose name differs only by case from one of its kind that is kept in the same folder MUST be left out the same way, as the two would be one folder on a case-insensitive file system; the first in byte order of the names is the one kept. A table or view whose columns file, named `<schema>.<T>.columns.json`, would have a name longer than 255 bytes (which two names that are each valid can make) MUST be left out the same way.

#### REQ: idempotent-rescan

Re-running `scan` against the same project, environment, and database MUST be idempotent on a database whose schema has not changed: the resulting on-disk files MUST be byte-identical to the prior run. This is the property that makes scans `git diff`-able.

### Sensitive data handling

#### REQ: no-password-in-logs

Database passwords MUST NOT appear in stdout or stderr at any verbosity. The current implementation passes `--password` straight into `NewConnectionString`. Logging of the connection string MUST mask the password (compare the `cmd_execute_sql.go` redaction pattern `password=******`).

## Parameters

| Flag | Aliases | Type | Required | Description |
|---|---|---|---|---|
| `--driver` | `-D` | string | yes | DB driver. Supported: `sqlite3`, `sqlserver`. |
| `--path` |  | string | yes (`sqlite3`) | The SQLite database file. It must exist. |
| `--server` | `-s` | string | yes (network DBs) | Network host. |
| `--port` |  | int | no | Network port; driver-default if omitted. |
| `--user` | `-U` | string | no | DB user. |
| `--password` | `-P` | string | no | DB password. |
| `--db` |  | string | yes | Catalog/database ID to scan. |
| `--dbmodel` |  | string | no | DB model ID. Defaults to `--db`. |
| `--env` |  | string | yes | Environment ID (`LOCAL`, `DEV`, etc.). |
| `--project` / `--directory` | `-p` / `-d` | string | (one of, or cwd) | Project context. See [parent feature](../README.md). |

## Project layout written by a scan

This is the one place the files of a scan are written down. It is the layout of the demo project (`chinook-demo`), and nothing new: the CLI (`chat`, `query run`, `serve`) and the web app read it, and `scan` is the only thing that writes it from a database. Names in `<angle brackets>` are the ids given to the scan: `<env>` is `--env`, `<db>` is `--db`, `<model>` is the database model id (the same as `<db>` today), `<schema>` is the schema the engine reports (`main` for SQLite) and `<T>` is the table or view name, exactly as the engine reports it, with its case.

| File | Written by | Fields |
|---|---|---|
| `datatug-project.json` | `SaveProject` | `id`, `access`, `created` |
| `README.md` | `SaveProject` | generated text, on the first scan; a `README.md` already in the folder is kept as it is: `SaveProject` replaces it on every save, so the scan puts it back (`saveKeepingReadme`, `pkg/api/scan_layout.go`) |
| `environments/<env>/<env>.env.json` | `SaveProject` | `id`; `dbServers[]` with `driver`, `host` and `port` (network engines only: none for `sqlite3`) and `catalogs[]`, the ids of the databases scanned on that server |
| `environments/<env>/catalogs/<db>/<db>.db.json` | `SaveEnvDbCatalog` | `id`; `driver`; `path` (`sqlite3`; see [REQ: sqlite-path-stored-portably](#req-sqlite-path-stored-portably)); `dbModel`, the model id; `schemas`, always `[]` |
| `dbmodels/<model>/<model>.dbmodel.json` | `SaveProject` | `id`; `environments[]` with `id` and `DbCatalogs[]` of `id` (no schemas, no tables) |
| `dbmodels/<model>/<schema>/tables/<T>/<schema>.<T>.columns.json` | the CLI | `columns[]`, in the engine's column order, each with `name`, `ordinalPosition`, `pkPosition` (the 1-based place in the primary key, left out when the column is not in it), `isNullable`, `dbType`, the other column properties the engine reports (such as `default` and `charMaxLength`), and `byEnv`, which holds `<env>` with `status` `exists` |
| `dbmodels/<model>/<schema>/views/<T>/<schema>.<T>.columns.json` | the CLI | the same, for a view |

A table or view is the folder `<T>`: the readers list tables and views by folder name, and read the columns from the one file in it whose name ends in `.columns.json`, found by listing the folder: a `[` in a table name, or in the path of the project, is a character of a name and not a pattern, so `t[1]` and `t1` are two tables. Nothing else is written. In particular the project holds no foreign keys, no indexes, no record counts and no DDL, and no password. A scan of PostgreSQL (not available in this release) will record its driver and its catalog id only, and no host, port, user or password in any project file.

The readers of the layout are `GetCatalogTables` and `GetCatalogSchema` (`pkg/api/catalog_tables_api.go`; chat, `serve` and saved queries), source resolution (`pkg/api/source_resolver.go`) and the web app's GitHub reader, which reads the environment folders, the `catalogs` of the environment file, the `dbModel` of the catalog file and the folder names under `dbmodels/<model>/<schema>/tables` and `views`.

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Scan succeeded, project file written |
| `2` | Missing required flag, invalid driver, or bad combination |
| `3` | Project directory or `--project` ID not found |
| `4` | Failed to connect to the database |
| `1` | Generic runtime error (write failure, internal error) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. Uses standard project-context resolution. |
| [init](../init/README.md) | A freshly-init'd project MUST be scannable. |
| [show](../show/README.md) | Consumes the project file `scan` writes. |
| [serve](../serve/README.md) | The Web UI may request a scan through the agent API. That codepath MUST share the same underlying `api.UpdateDbSchema` call. |

## Acceptance Criteria

### AC: scans-sqlserver-into-project

**Requirements:** scan#req:requires-project, scan#req:driver-selection, scan#req:persist-via-project-store

Given a project at `./proj` and a reachable SQL Server, `datatug scan --directory ./proj --driver sqlserver --server localhost --db sample --env DEV` exits `0` and updates the project files on disk to reflect the database's tables, views and columns (foreign keys are not stored in a project).

### AC: scans-sqlite-into-project

**Requirements:** scan#req:driver-selection, scan#req:persist-via-project-store, scan#req:project-layout, scan#req:sqlite-path-stored-portably

Given an empty folder `./shop` and a SQLite file with two tables, a view, a composite primary key and a table named in mixed case, `datatug scan --directory ./shop --driver sqlite3 --path shop.db --db shop --env local` exits `0`; the folder holds exactly the files of the layout; `LoadProject` loads it and `Validate` accepts it; the tables, views and columns are listed under their exact names, with the position of each primary-key column; the source resolves to the file that was scanned and a query runs against it; chat's project catalog lists the tables. (`TestScanJourneySQLite`.)

### AC: sqlite-scan-needs-no-cgo

**Requirements:** scan#req:sqlite-pure-go

In a build with `CGO_ENABLED=0`, as every release is, the scan of the previous criterion passes unchanged. A scan of a `--path` that does not exist exits non-zero, names the path and creates no file. (The `Scan without cgo` job of `.github/workflows/golangci.yml`.)

### AC: second-sqlite-scan-keeps-the-first

**Requirements:** scan#req:project-layout

After a second SQLite file is scanned into the same project and environment, both catalogs are listed, each resolves to its own file, and the catalog file and the columns files of the first scan are as the first scan wrote them. (`TestScanJourneySecondDatabaseKeepsTheFirst`.)

### AC: unusable-names-are-left-out

**Requirements:** scan#req:unusable-names-left-out

Given a SQLite file with a table named `a/b`, `datatug scan` exits `0`, names `a/b` on stderr and writes the other tables. (`TestScanJourneyNamesTheTablesItLeavesOut`.) Given schemas that differ only by case, or a table whose columns file name would be over 255 bytes, the same holds for them. (`TestSaveScannedProject_LeavesOutSchemasThatDifferOnlyByCase`, `TestSaveScannedProject_LeavesOutATableWhoseColumnsFileNameIsTooLong`.)

### AC: sqlite-names-are-not-sql

**Requirements:** scan#req:sqlite-pure-go

Given a SQLite file with tables named `it's` and `a]b`, and one named `x'); ATTACH DATABASE 'p.db' AS p; CREATE TABLE p.t(c); --`, `datatug scan`, run from an empty working directory, exits `0`; the three tables are in the project with their columns; and no `p.db` exists anywhere. (`TestScanJourneyNamesInTheFileAreNotSQL`, `TestScanDbCatalog_SQLite3_NamesInTheFileAreNotSQL`.)

### AC: names-with-brackets-are-read

**Requirements:** scan#req:project-layout

Given a SQLite file with tables named `t[1]`, `t1` and `a[b`, each with columns of its own, scanned into a project folder whose own path has a `[` in it, chat's catalog of the project lists each table with its own columns and no issue. (`TestScanJourneyNamesInTheFileAreNotSQL`, `TestGetCatalogSchemaNamesAndPathsWithBrackets`.)

### AC: sqlite-path-is-a-path

**Requirements:** scan#req:sqlite-pure-go

Given a SQLite file named `shop#1 50%.db`, `datatug scan --path` that file reads it, and creates no other file beside it. (`TestScanJourneyPathWithURICharacters`.)

### AC: existing-readme-is-kept

**Requirements:** scan#req:project-layout

Given a folder that holds a `README.md`, scanning into it, and scanning again, leaves the content of that file as it was. (`TestScanJourneyKeepsTheReadmeOfTheFolder`.)

### AC: missing-db-flag-rejected

**Requirements:** scan#req:required-db-flag

`datatug scan --driver sqlserver --server localhost --env DEV` exits `2` with a stderr message naming `--db`.

### AC: password-not-logged

**Requirements:** scan#req:no-password-in-logs

Running `datatug scan ... --password secret123` with logging enabled does NOT produce any line containing `secret123` on stdout or stderr.

### AC: dbmodel-defaults-to-db

**Requirements:** scan#req:dbmodel-default

`datatug scan ... --db sample` (no `--dbmodel`) writes metadata under `dbModel: sample`.

## Open Questions

- Should there be a `--dry-run` flag that connects, reads schema, but does not write the project? Useful for CI checks before committing.
- Should the command refuse to scan an `--env PROD` without an additional `--allow-prod` flag, as a foot-gun guard?
- The flag `--server` (`-s`) refers to a database host, not an HTTP server; the parent CLI also has `-s` used differently in other commands. Should there be a shared-flag REQ for what `-s` means?

---
*This document follows the https://specscore.md/feature-specification*
