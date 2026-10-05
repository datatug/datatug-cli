---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Scan

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug scan` connects to a database, introspects its schema (tables, views, columns and primary keys), and writes the resulting metadata into the DataTug project on disk. Re-running `scan` updates the metadata in place — this is the primary path for keeping a DataTug project synchronized with a live database: nothing changes when the database did not, a table or view the database dropped is removed, and the state another environment of the same model scanned is kept.

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

#### REQ: project-folder-created

When `--directory` names a folder that does not exist, `scan` MUST make it (with the folders above it) once it has read the database, and the folder is then a project. A scan that fails before it has read the database MUST make no folder. A `--directory` that is a file, or that cannot be looked at, MUST be refused, with a message that names it, before anything is read.

#### REQ: new-project-id

The id of a new project (a folder that holds no project file) MUST be the value of `--project`, or else the name of the project folder, and MUST be a valid project id as `dto.ValidateProjectID` of datatug-core defines it (lower-case ASCII letters, digits, `-` and `_`, starting and ending with a letter or a digit, and not too long). An id that is not valid MUST be refused, before anything is written, with a message that names `--project`. An id is never made up: scanning one database into two folders of the same name makes two projects of the same id. The project file of a folder that already is a project is read, and the project keeps its id, whatever the folder is called.

### Required connection details

#### REQ: required-db-flag

`--db <name>` MUST be required. It identifies the database (catalog) to scan.

#### REQ: required-env-flag

`--env <env>` MUST be required. It identifies the environment within the project the scanned metadata belongs to (e.g., `LOCAL`, `DEV`, `SIT`, `UAT`, `PROD`).

#### REQ: names-are-plain

`--db`, `--env` and `--dbmodel` are the names of folders of the project (`environments/<env>/`, `environments/<env>/catalogs/<db>/` and `dbmodels/<model>/`), so each MUST be a plain name: letters and digits of any script, `.`, `_` and `-`, starting with a letter or a digit, at most 128 characters, and a name that is a folder name on every system a project is opened on (see [REQ: unusable-names-left-out](#req-unusable-names-left-out)). A value that is not MUST be refused before anything is read or written, with a message that names the flag, and the message MUST NOT repeat a value that is not a plain name (a flag given a connection string must not echo it). This holds for every driver. For `sqlserver` the value of `--db` is also the name of the database on the server (it goes into the connection string), so a SQL Server database whose name is not a plain name (a space, a `$` as in `ReportServer$INSTANCE`) cannot be scanned until the id of the catalog in the project is separated from the name of the database on the server; that is a task after launch.

#### REQ: driver-selection

`--driver`/`-D` MUST specify the database driver; a scan without it is refused with a message that names `--driver` (and not a flag of one driver, such as `--server`). Supported values today: `sqlite3` (the file given by `--path`) and `sqlserver`. `postgres` is refused with a message that says the scan is not available in this release: a DataTug project cannot record a postgres server yet, and the scan stops before it connects. The set of supported drivers MUST match the drivers the binary links: `sqlserver` by the import in `main.go`, and the SQLite scan's own pure-Go driver by the import in `pkg/api/scan_db_schema_api.go` (see [REQ: sqlite-pure-go](#req-sqlite-pure-go)), which is where the scan opens it.

#### REQ: sqlite-pure-go

A SQLite scan MUST open the database file through a pure-Go `database/sql` driver (`modernc.org/sqlite`, registered as `sqlite`), not through the cgo-only `sqlite3` driver: a release is built with cgo off, where that driver is a stub that fails on its first use, so a scan that opened it could read no SQLite file in any released binary. A SQLite scan MUST NOT create the file: a `--path` that is not an existing file is an error that names it. It MUST open the file read-only (`file:<path>?mode=ro`), with `%`, `?` and `#` in the path percent-encoded, so that the file opened is the file that was checked (a file named `a#b.db` is that file, not a file named `a`), and it MUST put the name of every table, view and index it reads from the file into the statements it makes with it as a quoted name, never as SQL: a database is not trusted, and the driver runs every statement of a query string, so an unquoted name could make the scan create files and change the database it was asked to read. A scan only reads, so it MUST leave no file beside the database: a read-only connection to a database in WAL mode creates a `-wal` and a `-shm` file unless it is told that the file cannot change, so a database whose header says WAL mode and that has no `-wal` and no `-shm` file (no connection has it open) is opened as `immutable=1` as well, and one that is open is opened `mode=ro` only, and read with its WAL.

#### REQ: sqlite-reads-past-mistakes

A SQLite scan MUST NOT fail for what a SQLite file can hold that a project cannot. SQLite's own tables (`sqlite_sequence`, `sqlite_stat1` and every other name that starts with `sqlite_`) are not tables of the project, and are not named. A table or view with an empty name is left out. A view whose definition cannot be read (it refers to a table that was dropped) is left out. A foreign key to a table that is not a table of the file is not read, and the table it is on is. A foreign key is read to the table as the file spells that table, whatever case its own text was written in. Each of the three that are left out is named on stderr, one line, with the table or view and the reason, and the scan exits `0` and writes everything else.

#### REQ: connection-string-construction

For a network database the connection string MUST be built via `pkg/datatug-core/dbconnection.NewConnectionString(driver, host, user, password, db, options...)`. `port` and `mode=ReadOnly` MUST be appended as options when supplied. The CLI MUST connect in read-only mode for schema introspection. A SQLite scan has no host, user or password: it takes its path from `dbconnection.NewSQLite3ConnectionParams` and opens the file as [REQ: sqlite-pure-go](#req-sqlite-pure-go) says, read-only.

#### REQ: dbmodel-default

When `--dbmodel` is omitted, the DB model ID MUST default to the value of `--db`. This makes single-database projects easy to scan while preserving the ability to map multiple physical databases onto one logical model. When it is given, it MUST be the model: the `dbModel` of the catalog file, the folder `dbmodels/<model>/`, and the model file in it. A catalog that the project already records in that environment (its catalog file) stays on the model that file names: a scan without `--dbmodel` MUST keep that model, and a scan whose `--dbmodel` names another model MUST be refused, before anything is written, with a message that names both models, because a scan onto the other model would leave the tables of the first in the project for good and make a second model of the same database. A `--dbmodel` that names the recorded model is accepted. A catalog file that cannot be read, or that names no model that can be a folder name, records nothing.

### Output to project store

#### REQ: persist-via-project-store

The scan result MUST be written into the project folder, in the layout of [Project layout written by a scan](#project-layout-written-by-a-scan) and nowhere else. The project file, the environment files, the database model files and the catalog file MUST be written through datatug-core's project store (`SaveProject`, and `SaveEnvDbCatalog` for the catalog file). datatug-core has no writer for the per-table files today (its writers are commented out), so, for launch, the CLI writes those files itself (`pkg/api/scan_layout.go`), using datatug-core's own file type for them (`filestore.TableModelColumnsFile`). A later task may move that writer, and the readers of these files (`pkg/api/catalog_tables_api.go`), into datatug-core; the files will not change when it does.

#### REQ: project-layout

A scan MUST write the files of [Project layout written by a scan](#project-layout-written-by-a-scan), with those fields, and no other file. The files MUST be the ones that every reader of a project reads, so that a scanned project and the demo project are read by the same code, with no special case. A `README.md` that is already in the project folder MUST NOT be replaced or changed by a scan, on the first scan or on a rescan.

#### REQ: sqlite-path-stored-portably

The path of a SQLite file in the catalog file MUST be relative to the project folder, with `/` separators, when the file is inside the project folder; relative to the home directory with a leading `~/` when it is under the home directory; and absolute otherwise. A `--path` that is relative is the file from the working directory the scan was run in. Every reader MUST resolve the stored path to the file that was scanned, from any working directory (`ResolveCatalogPath` in `pkg/api/catalog_path.go`).

#### REQ: sqlite-path-has-no-question-mark

A SQLite scan MUST refuse a `--path` that has a `?` in it, before it reads or writes anything (no project folder is made, no database is opened), with a message that names the character and says to rename the file. The scan could read such a file, but the project could not open it again: the open of a source (`pkg/dbcopy`) hands the bare path to the driver, which reads a `?` as the start of its own parameters, opens the name before it and creates a file of that name beside it. The refusal is the limit of that open path, not of the scan, and lifts on the day that open reads such a name back. A `#` or a `%` in the path is not refused: they are read back to the same file (see [REQ: sqlite-pure-go](#req-sqlite-pure-go)).

#### REQ: unusable-names-left-out

A table or view, or a schema, whose name cannot be a folder name on every system a project is opened on MUST be left out of the project and named on stderr, with the reason, one line for each; the scan MUST still exit `0` and write everything else. A name cannot be a folder name when it is empty, is `.` or `..`, is longer than 200 bytes, ends in a dot or a space, is one Windows reserves for a device (`con`, `prn`, `aux`, `nul`, `com1` to `com9`, `lpt1` to `lpt9`, with or without an extension), or holds a control character or one of `/ \ : * ? " < > |`. A schema, or a table or view, whose name differs only by case from one of its kind that is kept in the same folder MUST be left out the same way, as the two would be one folder on a case-insensitive file system; the first in byte order of the names is the one kept. A table or view whose columns file, named `<schema>.<T>.columns.json`, would have a name longer than 255 bytes (which two names that are each valid can make) MUST be left out the same way.

#### REQ: idempotent-rescan

Re-running `scan` against the same project, environment, and database MUST be idempotent on a database whose schema has not changed: the resulting on-disk files MUST be byte-identical to the prior run, and a columns file that would not change MUST NOT be written again (the other files, which are the project file, the environment file, the model file, the catalog file and the `README.md`, are written again with the same bytes). This is the property that makes scans `git diff`-able.

#### REQ: rescan-removes-dropped-tables

A table or view that an environment's earlier scan wrote, and that the database no longer has, MUST be taken back by the next scan of that environment: the environment is removed from the `byEnv` of its columns, and when no environment has the table or view any more, its folder `dbmodels/<model>/<schema>/<tables|views>/<T>/` MUST be removed, and the scan MUST say so on stderr, one line for each folder removed, that names the folder (`removed: <folder>: <table or view> is no longer in the database`). Only that folder is removed: the `tables` and `views` folders of the schema, and the folder of the schema, stay, even when the last table or view of a schema went (less deletion is safer; a clone of the project from git has none of them, as git keeps no empty folder).

A scan MUST remove nothing else, and MUST decide first whether a folder is its own: a folder directly in `tables` or `views` of a schema of the model is the scan's only when it holds exactly one columns file that lists the environment of the scan. Any other folder (a person's own, one that cannot be listed, one with no columns file or whose file cannot be read, one whose columns do not list the environment of the scan, another environment's) is not the scan's: it MUST be left, and the scan MUST say nothing of it and MUST NOT fail for it, whatever it holds and whatever it is linked to. A folder that is the scan's and holds anything but that one columns file is left, and named on stderr. When the model also feeds another catalog in the same environment, the scan does not know what that database has: it MUST leave the folder, and name it.

A scan MUST NOT take back a folder that is its own through a link: when the folder, or any folder above it from `dbmodels` down, is not a plain folder as `Lstat` reports it (which a symbolic link is not, nor a Windows junction, nor any other reparse point), or cannot be inspected, it MUST refuse, with an error that names the folder, before it writes anything.

#### REQ: rescan-keeps-other-environments

Scanning the same `--db` for a second `--env` MUST keep the state the first environment's scan wrote. The `byEnv` of each column of a columns file MUST list every environment whose scan found that column in that table or view, the columns of the scan in the order the scan found them, followed by the columns that only other environments have. A table or view that only another environment has MUST stay as it is. The environments the columns files list MUST be among those the model file lists, and every environment of the model file that has the table or view MUST be in the `byEnv` of its columns.

The file holds one set of attributes for a column (its type, nullability, default and place) and one order of the columns: those of the last scan. So when the databases of two environments differ in a column they share, or one has a column that is not the last, a scan of one environment writes the file again with its own attributes and order, though no database changed since the scan of the other; a rescan of an environment is byte-identical only until another environment whose columns differ is scanned. That is the limit of [REQ: idempotent-rescan](#req-idempotent-rescan) for a project with more than one environment, and a test pins it.

### Sensitive data handling

#### REQ: no-password-in-logs

Database passwords MUST NOT appear in stdout or stderr at any verbosity. The current implementation passes `--password` straight into `NewConnectionString`. Logging of the connection string MUST mask the password (compare the `cmd_execute_sql.go` redaction pattern `password=******`).

## Parameters

| Flag | Aliases | Type | Required | Description |
|---|---|---|---|---|
| `--driver` | `-D` | string | yes | DB driver. Supported: `sqlite3`, `sqlserver`. |
| `--path` |  | string | yes (`sqlite3`) | The SQLite database file. It must exist, and its path must have no `?` in it. |
| `--server` | `-s` | string | yes (network DBs) | Network host. |
| `--port` |  | int | no | Network port; driver-default if omitted. |
| `--user` | `-U` | string | no | DB user. |
| `--password` | `-P` | string | no | DB password. |
| `--db` |  | string | yes | Catalog/database ID to scan: a plain name (for `sqlserver` also the name of the database on the server). |
| `--dbmodel` |  | string | no | DB model ID: a plain name. Defaults to the model the project records for the catalog, or else `--db`. |
| `--env` |  | string | yes | Environment ID (`LOCAL`, `DEV`, etc.): a plain name. |
| `--project` / `--directory` | `-p` / `-d` | string | (one of, or cwd) | Project context. See [parent feature](../README.md). A `--directory` that does not exist is made. With `--directory`, `--project` is the id of a new project; without it the id is the name of the folder (see [REQ: new-project-id](#req-new-project-id)). |

## Project layout written by a scan

This is the one place the files of a scan are written down. It is the layout of the demo project (`chinook-demo`), and nothing new: the CLI (`chat`, `query run`, `serve`) and the web app read it, and `scan` is the only thing that writes it from a database. Names in `<angle brackets>` are the ids given to the scan: `<env>` is `--env`, `<db>` is `--db`, `<model>` is the database model id (the same as `<db>` today), `<schema>` is the schema the engine reports (`main` for SQLite) and `<T>` is the table or view name, exactly as the engine reports it, with its case.

| File | Written by | Fields |
|---|---|---|
| `datatug-project.json` | `SaveProject` | `id`, `access`, `created` |
| `README.md` | `SaveProject` | generated text, on the first scan; a `README.md` already in the folder is kept as it is: `SaveProject` replaces it on every save, so the scan puts it back (`saveKeepingReadme`, `pkg/api/scan_layout.go`) |
| `environments/<env>/<env>.env.json` | `SaveProject` | `id`; `dbServers[]` with `driver`, `host` and `port` (network engines only: none for `sqlite3`) and `catalogs[]`, the ids of the databases scanned on that server |
| `environments/<env>/catalogs/<db>/<db>.db.json` | `SaveEnvDbCatalog` | `id`; `driver`; `path` (`sqlite3`; see [REQ: sqlite-path-stored-portably](#req-sqlite-path-stored-portably)); `dbModel`, the model id; `schemas`, always `[]` |
| `dbmodels/<model>/<model>.dbmodel.json` | `SaveProject` | `id`; `environments[]` with `id` and `DbCatalogs[]` of `id` (no schemas, no tables) |
| `dbmodels/<model>/<schema>/tables/<T>/<schema>.<T>.columns.json` | the CLI | `columns[]`, in the engine's column order, each with `name`, `ordinalPosition`, `pkPosition` (the 1-based place in the primary key, left out when the column is not in it), `isNullable`, `dbType`, the other column properties the engine reports (such as `default` and `charMaxLength`), and `byEnv`, which holds each environment that has the column, each with `status` `exists` (see [REQ: rescan-keeps-other-environments](#req-rescan-keeps-other-environments)) |
| `dbmodels/<model>/<schema>/views/<T>/<schema>.<T>.columns.json` | the CLI | the same, for a view |

A table or view is the folder `<T>`: the readers list tables and views by folder name, and read the columns from the one file in it whose name ends in `.columns.json`, found by listing the folder: a `[` in a table name, or in the path of the project, is a character of a name and not a pattern, so `t[1]` and `t1` are two tables. Nothing else is written. In particular the project holds no foreign keys, no indexes, no record counts and no DDL, and no password. A scan of PostgreSQL (not available in this release) will record its driver and its catalog id only, and no host, port, user or password in any project file.

The readers of the layout are `GetCatalogTables` and `GetCatalogSchema` (`pkg/api/catalog_tables_api.go`; chat, `serve` and saved queries), source resolution (`pkg/api/source_resolver.go`) and the web app's GitHub reader, which reads the environment folders, the `catalogs` of the environment file, the `dbModel` of the catalog file and the folder names under `dbmodels/<model>/<schema>/tables` and `views`.

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Scan succeeded, project written (what the scan left out is named on stderr) |
| `2` | Missing required flag, invalid driver, or bad combination |
| `3` | No project context (neither `--directory` nor `--project`), or `--project` names no registered project |
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

### AC: second-scan-is-quiet

**Requirements:** scan#req:idempotent-rescan

Given the project of the previous criterion, `datatug scan` run again with the same flags, on a database that did not change, exits `0`, says nothing on stderr, and leaves every file of the folder with the content it had (compared by SHA-256), and the columns file with the modification time it had. (`TestScanJourneySQLite`, `TestSaveScannedProject_RescanOfAnUnchangedDatabaseWritesNothing`.)

### AC: dropped-table-is-removed

**Requirements:** scan#req:rescan-removes-dropped-tables

Given that project, after one table is dropped from the database and another added, `datatug scan` run again exits `0` and says on stderr one line that names the folder it removed; the folder of the dropped table is gone, the folder of the new table is there, and every other file is as it was. A folder that is its own and is, or is in, a link (a symbolic link, or what `Lstat` reports as anything but a plain folder, as it does a Windows junction) is never removed: the scan refuses, names it, and writes nothing, at each level from `dbmodels` down. A folder that is not the scan's (a person's own folder with a `README.md`, a folder whose columns list only another environment, even one that is linked) is not touched, not named, and does not fail the scan, on this scan or any later one; a folder of the scan's own that holds a file the scan did not write stays and is named. The empty `tables`, `views` and schema folders stay. (`TestScanJourneySQLite`, `TestScanJourneyLeavesAFolderOfItsOwnAlone`, `TestSaveScannedProject_RescanTakesBackWhatTheDatabaseDropped`, `TestSaveScannedProject_RescanTakesBackAWholeSchema`, `TestSaveScannedProject_RescanNeverRemovesThroughASymbolicLink`, `TestSaveScannedProject_RescanLeavesWhatItDidNotWrite`, `TestApplyRetractions`.)

### AC: second-environment-keeps-the-first

**Requirements:** scan#req:rescan-keeps-other-environments, scan#req:rescan-removes-dropped-tables

Given a SQLite file scanned as `--db shop --env local`, scanning another file for `--db shop --env dev` lists `dev` and `local` in the `byEnv` of every column both have, lists `dev` only for a column or table only `dev` has and `local` only for a table only `local` has (which the scan of `dev` does not remove), and the model file lists both environments; each environment's catalog file names its own database file, after the second scan and after every rescan; a rescan of either, with the other not scanned in between and no shared column differing, changes nothing; a table that `dev` drops goes when `local` does not have it either. Where the two databases differ in a column they share, or in its place, the file holds the attributes and order of the last scan, and the scan of the other environment writes them again. (`TestScanJourneySecondEnvironmentKeepsTheFirst`, `TestSaveScannedProject_SecondEnvironmentKeepsTheFirst`, `TestScanJourneyASharedColumnHoldsTheAttributesOfTheLastScan`, `TestSaveScannedProject_ASharedColumnHoldsTheAttributesAndOrderOfTheLastScan`.)

### AC: scan-creates-the-project-folder

**Requirements:** scan#req:project-folder-created, scan#req:new-project-id

`datatug scan --directory ./work/shop ...` with a `./work/shop` that does not exist exits `0` and leaves a project of the id `shop`; the same scan of a database that cannot be read exits non-zero and makes no `./work`; a `--directory` that is a file is refused before the database is read. (`TestScanJourneyCreatesTheProjectFolder`, `TestScanJourneyDoesNotCreateTheProjectFolderForAScanThatFails`.)

### AC: new-project-id-is-given-or-the-folder-name

**Requirements:** scan#req:new-project-id

Scanning into `./shop-project` makes the project `shop-project`, into any folder with `--project my-shop` makes `my-shop`, and into `./My Shop` with no `--project`, or with `--project Shop`, exits non-zero with a message that names `--project` and makes nothing; a project that exists in `./My Shop` is scanned into, and keeps its id. (`TestScanJourneyIDOfANewProject`, `TestScanDbCatalog_SQLite3_NewProjectIDMustBeAValidProjectID`, `TestScanDbCatalog_SQLite3_ExistingProjectKeepsItsID`.)

### AC: missing-driver-names-the-flag

**Requirements:** scan#req:driver-selection

`datatug scan --directory ./proj --db shop --env local` exits non-zero with a message that names `--driver`, and does not name `--server`. (`TestScanJourneyMissingDriverNamesTheFlag`.)

### AC: dbmodel-is-honoured

**Requirements:** scan#req:dbmodel-default

`datatug scan ... --db shop --dbmodel retail` writes `dbModel: retail` in the catalog file and the tables under `dbmodels/retail/`. (`TestScanJourneyHonoursTheDbModel`, `TestScanDbCatalog_SQLite3_CatalogIsMappedOntoTheModelThatWasAskedFor`.)

### AC: recorded-model-is-kept

**Requirements:** scan#req:dbmodel-default

Given a project scanned with `--db shop --dbmodel retail-model`, `datatug scan` run again without `--dbmodel` exits `0`, says nothing on stderr, makes no `dbmodels/shop/` and leaves every file as it was; the same with `--dbmodel retail-model`. With `--dbmodel other-model` it exits non-zero with a message that names `retail-model` and `other-model`, and writes nothing; so it does with `--dbmodel` naming another model when the first scan gave none (the model was `shop`). The same database in another environment, which the project does not record, takes the model it is given. (`TestScanJourneyKeepsTheModelOfACatalogTheProjectHolds`, `TestResolveScanDbModel`.)

### AC: names-must-be-plain

**Requirements:** scan#req:names-are-plain

`datatug scan` with `--db ../evil`, `--env a:b`, `--dbmodel x/y`, or `--db con` exits non-zero with a message that names the flag, repeats the value only when it is a plain name, and writes nothing. The function that saves a scan checks the three ids itself, for any caller: an id such as `../../outside` is refused before anything is listed, written or removed. (`TestScanJourneyRefusesNamesThatAreNotPlain`, `TestCheckScanName`, `TestSaveScannedProject_RefusesIdsThatAreNotPlainNames`.)

### AC: sqlite-mistakes-are-read-past

**Requirements:** scan#req:sqlite-reads-past-mistakes, scan#req:unusable-names-left-out

Given a SQLite file with a foreign key to a table that is not in it, a view of a dropped table, a table with an empty name, and `sqlite_sequence` and `sqlite_stat1`, `datatug scan` exits `0`, names the foreign key, the view and the table on stderr, one line each, and the project holds the other tables and views and none of SQLite's own. (`TestScanJourneyNamesWhatItLeavesOutOfAFileWithMistakesInIt`, `TestScanDbCatalog_SQLite3_ForeignKeyToATableNotInTheFile`, `TestScanDbCatalog_SQLite3_ViewOfADroppedTable`, `TestScanDbCatalog_SQLite3_OwnTablesAreNotListed`, `TestScanDbCatalog_SQLite3_EmptyNames`, `TestScanDbCatalog_SQLite3_EmptyViewName`.)

### AC: wal-database-leaves-no-files

**Requirements:** scan#req:sqlite-pure-go

Given a SQLite database in WAL mode that nobody has open, `datatug scan --path` that file reads it and leaves no `-wal` and no `-shm` file beside it; given one that is open and holds changes in its `-wal` file, the scan reads them. (`TestScanJourneyLeavesNoSidecarFilesBesideAWALDatabase`, `TestScanDbCatalog_SQLite3_WALDatabaseLeavesNoSidecarFiles`, `TestScanDbCatalog_SQLite3_WALDatabaseThatIsOpenIsReadWithItsWAL`.)

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

**Requirements:** scan#req:sqlite-pure-go, scan#req:sqlite-path-has-no-question-mark

Given a SQLite file named `shop#1 50%.db`, `datatug scan --path` that file reads it, and creates no other file beside it; the path in the catalog file is resolved by `ResolveCatalogPath` and by every reader of the project (the source of a saved query, chat and `serve`) to that file, and a query through the source reads it. A file whose path has a `?` is refused: `datatug scan --path` it exits non-zero, with a message that names `"?"` and says to rename the file, before anything is read or written, and no file is made. (`TestScanJourneyPathWithURICharacters`, `TestScanDbCatalog_SQLite3_PathWithURICharactersIsReadBack`, `TestCheckSQLitePath`.)

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
