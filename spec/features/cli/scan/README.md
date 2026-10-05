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

datatug scan --directory <path> --driver postgres --dsn-env <VARIABLE> --db <name> --env <env>
```

## Problem

DataTug projects encode database schemas as versionable on-disk files. Authoring those files by hand is infeasible for any non-trivial database. The agent needs a one-command path to read the live `information_schema` (or driver equivalent), normalize the result, and write a deterministic file set that diffs cleanly under Git.

`scan` is intentionally a separate command from `serve` so users can run it as a CI job (against a staging environment, for example) and commit the result.

## Behavior

### Required project context

#### REQ: requires-project

`scan` MUST be invoked inside a project context — either via the cwd being a DataTug project, via `--project <id>`, or via `--directory <path>` (`-d`; the shared CLI conventions, [REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution), call this flag `--dir`, but `--directory` is the name `scan` defines). If no project context can be resolved, the command MUST exit `3` (NotFound).

#### REQ: project-folder-link

When the last part of the folder given with `--directory` (or the working directory, when `--directory` is `.`) is a link (a symbolic link, or a Windows junction), `scan` MUST print the folder it leads to, name `--follow-project-link`, write nothing and exit non-zero, before the database is read; with `--follow-project-link` it MUST go on and write into the folder the link leads to. A folder that is not there, a file and a folder that is not a link are looked at as they always were. The folders above the last part are the person's own, and are not looked at; a project folder named with `--project` is a registered path and is not looked at either. A command that only reads a project is not changed. The help of `--follow-project-link` says what it does.

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

#### REQ: names-do-not-differ-only-by-case

`--env`, `--db` and the database model (`--dbmodel`, or else the model the scan resolves for `--db`) MUST NOT differ only by case from a name the project already has in the same place: an environment in `environments/`, a catalog in `environments/<env>/catalogs/` (a folder, or a file in the flat place `<db>.db.json` that datatug-core also reads), a model in `dbmodels/`. On a file system that does not tell the case of a name apart (macOS and Windows by default) the two are one folder, and a scan of one database would take the tables of another for its own and remove those it does not find (`--db crm --dbmodel Shop` into a project whose model is `shop`). Such a name MUST be refused before the database is read, and by the function that saves a scan for any caller, before anything is written or removed, with a message that names the flag and the name the project has, and says to use it. The names compared are the ones the folders list, so the refusal does not depend on the file system the project is on. A name that is the same as the project's, or differs by more than case, is accepted.

#### REQ: driver-selection

`--driver`/`-D` MUST specify the database driver; a scan without one, and a scan whose value is not a driver it reads, is refused with a message that names `--driver` and the drivers it reads (and not a flag of one driver, such as `--server`). This is the first thing the scan checks, before the project is looked at: a project that already holds the database under another driver, or no project at all, does not change what the message says. A value that is not a plain name is not repeated in the message (the flag may have been given a connection string). Supported values today: `sqlite3` (the file given by `--path`), `sqlserver` and `postgres` (the connection URL held in the variable given by `--dsn-env`; see [PostgreSQL](#postgresql)). The set of supported drivers MUST match the drivers the binary links: `sqlserver` by the import in `main.go`, the SQLite scan's own pure-Go driver by the import in `pkg/api/scan_db_schema_api.go` (see [REQ: sqlite-pure-go](#req-sqlite-pure-go)), which is where the scan opens it, and the PostgreSQL adapter (`dalgo2postgres`) by the import in `pkg/dbcopy/scan_open.go`.

#### REQ: sqlite-pure-go

A SQLite scan MUST open the database file through a pure-Go `database/sql` driver (`modernc.org/sqlite`, registered as `sqlite`), not through the cgo-only `sqlite3` driver: a release is built with cgo off, where that driver is a stub that fails on its first use, so a scan that opened it could read no SQLite file in any released binary. A SQLite scan MUST NOT create the file: a `--path` that is not an existing file is an error that names it. It MUST open the file read-only (`file:<path>?mode=ro`), with `%`, `?` and `#` in the path percent-encoded, so that the file opened is the file that was checked (a file named `a#b.db` is that file, not a file named `a`), and it MUST put the name of every table, view and index it reads from the file into the statements it makes with it as a quoted name, never as SQL: a database is not trusted, and the driver runs every statement of a query string, so an unquoted name could make the scan create files and change the database it was asked to read. A scan only reads, so it MUST leave no file beside the database: a read-only connection to a database in WAL mode creates a `-wal` and a `-shm` file unless it is told that the file cannot change, so a database whose header says WAL mode and that has no `-wal` and no `-shm` file (no connection has it open) is opened as `immutable=1` as well, and one that is open is opened `mode=ro` only, and read with its WAL.

#### REQ: sqlite-reads-past-mistakes

A SQLite scan MUST NOT fail for what a SQLite file can hold that a project cannot. SQLite's own tables (`sqlite_sequence`, `sqlite_stat1` and every other name that starts with `sqlite_`) are not tables of the project, and are not named. A table or view with an empty name is left out. A view whose definition cannot be read (it refers to a table that was dropped) is left out. A foreign key to a table that is not a table of the file is not read, and the table it is on is. A foreign key is read to the table as the file spells that table, whatever case its own text was written in. Each of the three that are left out is named on stderr, one line, with the table or view and the reason, and the scan exits `0` and writes everything else.

#### REQ: connection-string-construction

For a network database the connection string MUST be built via `pkg/datatug-core/dbconnection.NewConnectionString(driver, host, user, password, db, options...)`. `port` and `mode=ReadOnly` MUST be appended as options when supplied. The CLI MUST connect in read-only mode for schema introspection. A SQLite scan has no host, user or password: it takes its path from `dbconnection.NewSQLite3ConnectionParams` and opens the file as [REQ: sqlite-pure-go](#req-sqlite-pure-go) says, read-only.

#### REQ: dbmodel-default

When `--dbmodel` is omitted, the project records no catalog of that id in this environment, and no other environment records one, the DB model ID MUST default to the value of `--db` (a catalog the project records stays on its model, and one the other environments record is put on the model they agree on: both are below). This makes single-database projects easy to scan while preserving the ability to map multiple physical databases onto one logical model. When it is given, it MUST be the model: the `dbModel` of the catalog file, the folder `dbmodels/<model>/`, and the model file in it. A catalog that the project already records in that environment (its catalog file) stays on the model that file names: a scan without `--dbmodel` MUST keep that model, and a scan whose `--dbmodel` names another model MUST be refused, before anything is written, with a message that names both models, because a scan onto the other model would leave the tables of the first in the project for good and make a second model of the same database. A `--dbmodel` that names the recorded model is accepted. A catalog file that cannot be read, or that names no model that can be a folder name, records nothing. A catalog file is read as every reader reads it, through the project store: in the nested place (`catalogs/<db>/<db>.db.json`) and in the flat one (`catalogs/<db>.db.json`), which datatug-core keeps writing to when it is the one that is there.

One database is one model in every environment. When `--dbmodel` is omitted and the project records no catalog of that id in this environment, the model MUST be the one the other environments record for a catalog of that id, when they all name the same one (the model of the first environment that was scanned is the model of the next, with no flag); when they name different models the scan MUST be refused, before anything is written, with a message that names each model and its environments and says to scan with `--dbmodel`; and when none records one, the model is `--db`. When the model taken from the other environments is not the one `--db` would give, the scan MUST say so, in one line on stderr that names the model and the environments it was taken from (`note: database "shop" is on database model "retail" in environment "local", so this scan of environment "dev" puts it on that model too; --dbmodel chooses another`); a model that is called as the database is what no flag would give anyway, and is not said. A `--dbmodel` that is given is the model of a catalog the project does not record in this environment, whatever the other environments record: the flag settles it.

### Output to project store

#### REQ: persist-via-project-store

The scan result MUST be written into the project folder, in the layout of [Project layout written by a scan](#project-layout-written-by-a-scan) and nowhere else. The project file, the environment files, the database model files and the catalog file MUST be written through datatug-core's project store (`SaveProject`, and `SaveEnvDbCatalog` for the catalog file). datatug-core has no writer for the per-table files today (its writers are commented out), so, for launch, the CLI writes those files itself (`pkg/api/scan_layout.go`), using datatug-core's own file type for them (`filestore.TableModelColumnsFile`), and only as plain files in plain folders of the project (see [REQ: writes-plain-files-in-plain-folders](#req-writes-plain-files-in-plain-folders)). A later task may move that writer, and the readers of these files (`pkg/api/catalog_tables_api.go`), into datatug-core; the files will not change when it does.

#### REQ: writes-plain-files-in-plain-folders

A command writes only plain files in plain folders of the project: it does not write through a link. Every file a scan writes (the project file, the `README.md` it keeps, the environment file, the catalog file, the model file, the columns files, the connection descriptor of a PostgreSQL scan) and every folder it makes or removes, is reached from the project folder down by looking at each part with `Lstat` (`internal/plainfs`, the one walk of the CLI; datatug-core's file store has the same rule for what it writes). A part that is a link (a symbolic link, a junction or any other reparse point) or is not a plain folder, or a file that is not a plain file, is refused, with an error that names its path inside the project and never where a link leads, before anything is written through it; a file is opened without following a link where the platform can say so; every error of the operating system is returned. A rename lands only on a path that was checked the same way. What a scan reads to decide what it writes (the files it keeps state from) is read the same way, and a link in its place is refused. The project folder itself, as the person gave it, may be a link (see [REQ: project-folder-link](#req-project-folder-link)).

#### REQ: project-layout

A scan MUST write the files of [Project layout written by a scan](#project-layout-written-by-a-scan), with those fields, and no other file. The files MUST be the ones that every reader of a project reads, so that a scanned project and the demo project are read by the same code, with no special case. A `README.md` that is already in the project folder MUST NOT be replaced or changed by a scan, on the first scan or on a rescan.

#### REQ: sqlite-path-stored-portably

The path of a SQLite file in the catalog file MUST be relative to the project folder, with `/` separators, when the file is inside the project folder; relative to the home directory with a leading `~/` when it is under the home directory; and absolute otherwise. A `--path` that is relative is the file from the working directory the scan was run in. Every reader MUST resolve the stored path to the file that was scanned, from any working directory (`ResolveCatalogPath` in `pkg/api/catalog_path.go`).

#### REQ: unusable-names-left-out

A table or view, or a schema, whose name cannot be a folder name on every system a project is opened on MUST be left out of the project and named on stderr, with the reason, one line for each; the scan MUST still exit `0` and write everything else. A name cannot be a folder name when it is empty, is `.` or `..`, is longer than 200 bytes, ends in a dot or a space, is one Windows reserves for a device (`con`, `prn`, `aux`, `nul`, `com1` to `com9`, `lpt1` to `lpt9`, with or without an extension), or holds a control character or one of `/ \ : * ? " < > |`. A schema, or a table or view, whose name differs only by case from one of its kind that is kept in the same folder MUST be left out the same way, as the two would be one folder on a case-insensitive file system; the first in byte order of the names is the one kept. A table or view whose columns file, named `<schema>.<T>.columns.json`, would have a name longer than 255 bytes (which two names that are each valid can make) MUST be left out the same way.

#### REQ: idempotent-rescan

Re-running `scan` against the same project, environment, and database MUST be idempotent on a database whose schema has not changed: the resulting on-disk files MUST be byte-identical to the prior run, and a columns file that would not change MUST NOT be written again (the other files, which are the project file, the environment file, the model file, the catalog file and the `README.md`, are written again with the same bytes). This is the property that makes scans `git diff`-able.

#### REQ: rescan-removes-dropped-tables

A table or view that an environment's earlier scan wrote, and that the database no longer has, MUST be taken back by the next scan of that environment: the environment is removed from the `byEnv` of its columns, and when no environment has the table or view any more, its folder `dbmodels/<model>/<schema>/<tables|views>/<T>/` MUST be removed, and the scan MUST say so on stderr, one line for each folder removed, that names the folder (`removed: <folder>: <table or view> is no longer in the database`). Only that folder is removed: the `tables` and `views` folders of the schema, and the folder of the schema, stay, even when the last table or view of a schema went (less deletion is safer; a clone of the project from git has none of them, as git keeps no empty folder).

The first scan of a catalog in an environment (the project holds no catalog file of it in that environment) has no earlier scan of its own to take back, so it MUST remove nothing and say nothing of the tables that are already in the model for that environment: a model file that a person made, or that an older scan wrote, can list no catalog for an environment whose columns files list it, and a new catalog scanned onto that model must not take the tables of another database for its own.

A scan MUST remove nothing else, and MUST decide first whether a folder is its own: a folder directly in `tables` or `views` of a schema of the model is the scan's only when it holds the one file a scan writes there, named for the folder, `<schema>.<T>.columns.json` (the schema and `<T>`, the name of the folder, compared exactly), as a regular file, and that file is a columns file that lists the environment of the scan. The name of the file is part of the test, so a copy or a rename of the folder of a table, whose file keeps the name it had in the old folder (`tables/Customer.bak` or `tables/Old-2019`, holding `main.Customer.columns.json` or `main.Old.columns.json`), is not the scan's, and a snapshot of an older table, which no scan can write again, is not lost. Any other folder (a person's own, a copy or a rename, one that cannot be listed, one with no such file or whose file cannot be read, one whose columns do not list the environment of the scan, another environment's) is not the scan's: it MUST be left, and the scan MUST say nothing of it and MUST NOT fail for it, whatever it holds and whatever it is linked to, on this scan and on every later one. A folder that is the scan's and holds anything but that one file (a differently named columns file included) is left, and named on stderr. When the model also feeds another catalog in the same environment, the scan does not know what that database has: it MUST leave the folder, and name it.

A scan MUST NOT take back a folder that is its own through a link: when the folder, or any folder above it from `dbmodels` down, is not a plain folder as `Lstat` reports it (which a symbolic link is not, nor a Windows junction, nor any other reparse point), or cannot be inspected, it MUST refuse, with an error that names the folder inside the project, before it writes anything.

#### REQ: rescan-keeps-other-environments

Scanning the same `--db` for a second `--env` MUST keep the state the first environment's scan wrote. The `byEnv` of each column of a columns file MUST list every environment whose scan found that column in that table or view, the columns of the scan in the order the scan found them, followed by the columns that only other environments have. A table or view that only another environment has MUST stay as it is. The environments the columns files list MUST be among those the model file lists, and every environment of the model file that has the table or view MUST be in the `byEnv` of its columns.

The file holds one set of attributes for a column (its type, nullability, default and place) and one order of the columns: those of the last scan. So when the databases of two environments differ in a column they share, or one has a column that is not the last, a scan of one environment writes the file again with its own attributes and order, though no database changed since the scan of the other; a rescan of an environment is byte-identical only until another environment whose columns differ is scanned. That is the limit of [REQ: idempotent-rescan](#req-idempotent-rescan) for a project with more than one environment, and a test pins it.

### PostgreSQL

A PostgreSQL scan (`-D postgres`) reads a database through DALgo's schema reader, writes the layout of every other scan, and adds one file, the connection descriptor. The rules below hold for it and for no other driver.

#### REQ: postgres-connection-from-environment

The connection of a PostgreSQL scan MUST be a `postgres://` or `postgresql://` URL held in the environment variable named by `--dsn-env`. The URL carries the host, port, user and password together, so a password is never on a command line (the process list and the shell history keep it) or in a project file: `--server`, `--port`, `--user`, `--password` and `--path` MUST be refused, each named, and so MUST a variable whose name is not an environment variable name, one that is not set, one whose name does not start with `DATATUG_` and is not listed in `DATATUG_DSN_ENV_ALLOW` (a project file must not be able to select any variable of the machine), a URL that net/url and pgx read differently from how it was written (an unescaped `/`, `?` or `#` after digits in the password, or a user name that holds a colon), and a URL whose query sets `host`, `port`, `dbname` or `database` (the query is applied after the authority and the path and replaces what they name, so the line that says what the scan connects to would name a place the scan does not connect to: the host and port go in the authority and the database in the path). No refusal repeats a part of the URL; the last one names the key, which is not part of it.

#### REQ: postgres-project-holds-no-connection

No file a PostgreSQL scan writes MUST hold a host, a port, a user name, a password, a query string or a URL: not the project file, the environment file, the database model file, a columns file, the catalog file or the descriptor. The server of the environment file is the driver and nothing else (`{"driver": "postgres", "catalogs": ["shop"]}`), as is the server of the project: two PostgreSQL databases on two hosts are two catalogs of one server of the project. The catalog file holds the driver `postgres`, the id, the model, and as its `path` the project-relative path of the connection descriptor, `connections/<env>/<db>.json`. The descriptor MUST hold the name of the environment variable and nothing else (`{"dsnEnv": "DATATUG_SHOP_PG_URL"}`), and MUST lie inside the project folder: every reader MUST refuse a catalog whose `path` is absolute, starts with `~` or `$`, leaves the project folder through `..`, is a URL, leads out of the folder through a link, or is a path whose link check fails for any reason but "there is no such file" (a path that cannot be classified is not handed to the reader), without repeating the path in its message (`ResolveDescriptorPath` in `pkg/api/descriptor_path.go`). The reader resolves the catalog to the source `env:<VARIABLE>`, never to the URL.

#### REQ: postgres-descriptor-follows-the-project

The project MUST be validated before the descriptor is written, so a project that cannot be saved leaves no descriptor in a folder with no project. When the save fails after the descriptor was written, the scan MUST take the descriptor back: remove one it made, with the folders it made (a folder that holds anything else stays), and put back one that was there, as it was; a descriptor that is as the scan would write it is not written again. The writer MUST NOT follow a link, as the reader does not (see [REQ: writes-plain-files-in-plain-folders](#req-writes-plain-files-in-plain-folders)). Each of `connections`, `connections/<env>` and the descriptor file is looked at without following it (`Lstat`), and a link, or anything that is not a folder (the two folders) or not a regular file (the descriptor), is refused, naming its path in the project and not where it leads, before anything is read through it, written, or saved: what a link leads to is never read or written, and nothing the scan made on the way is left behind. A rescan with another `--dsn-env` MUST update the descriptor and the `path` of the catalog together: the catalog names the descriptor the scan wrote now, never one an earlier scan, or a person, left, and nothing else in the project changes.

#### REQ: postgres-and-sqlite-in-one-environment

A project records its servers by driver (and by host and port for the drivers that have them), so a SQLite catalog and a PostgreSQL catalog scanned into one environment of one project MUST both be kept, in either order, each resolving to its own source (the file, and the variable), and a rescan of either MUST leave the other as it was. Two servers that the project records by their driver alone MUST NOT be taken for one another by anything that reads the project. A catalog file is kept by environment and id, and not by driver, so one `--db` under two drivers in one environment (for any two drivers, a SQLite file and a PostgreSQL database among them) is one catalog file that two servers list, and the scan of the second would put its own catalog file in the place of the first's and take the first's tables back as the ones its database no longer has: a scan whose `--db` the environment already records under another driver MUST be refused, before the database is read or anything is written, with a message that names both drivers and says to use another `--db`. The same id in another environment is not in the way.

#### REQ: postgres-says-no-credentials

The line the scan logs to name what it connects to MUST be built by `dbcopy.SourceDisplay`: the scheme, the host, the port and the database of the URL (`connecting to postgres://db.example.com:5432/shop`), and never a user name, a password or a query string. Every error of the open of the source and of the read of the catalog MUST be the classified open failure (`open postgres source "env:<VARIABLE>": <a fixed sentence>`), never the words of the driver, which can quote the URL; the driver's own error stays reachable through `errors.Is` and `errors.As`. The sentence says why where it can be told without reading the driver's words: a rejected user or password (SQLSTATE `28P01`), a connection the server's access rules do not allow (SQLSTATE `28000`: the user, the database, the address or the encryption of the connection), a database that does not exist, a server that cannot be reached, a timeout, a failed TLS handshake. A PostgreSQL scan counts no records: no project file holds a count, and a count would be a full read of every table and every view of somebody's database, with no timeout, so the scan is given no counter, whatever the reader can do.

#### REQ: postgres-names-are-exact

PostgreSQL names are case-sensitive, and the scan opens the adapter in exact identifier mode, so a table, a view, a schema and a column are in the project under the name the server reports, with its case; a test pins the mode, because the default of the adapter folds every name to lower case. Tables that differ only by case, and names that cannot be folder names, follow [REQ: unusable-names-left-out](#req-unusable-names-left-out): each is named on stderr, left out, and the scan exits `0`. The schema `public` and every other schema the scan reads each get a folder of their own under the model (`dbmodels/<model>/<schema>/`); the schema `public` is written exactly where it always was, so a project scanned before every schema was read rescans to the same files for it. A schema name that cannot be a folder name is skipped like a table name is, by the same rule, with one line on stderr that names it, and the scan goes on. Foreign keys are not stored in the project.

#### REQ: postgres-reads-every-schema

A PostgreSQL scan MUST read every schema the reader lists (`ListSchemas` of `dalgo2postgres`: the schemas the role can use, without `pg_catalog`, `information_schema`, `pg_toast` and the temporary schemas) and save the tables and the views of each: the primary keys, the columns, and each column's default, in the layout below, in the folder of the schema. The reader's methods for it are its own, not an interface of `dbschema`, so the scan reaches them through the optional interface `dalgoschema.SchemaLister`, declared in this repository: a reader that is not one (a fake of a test, or an adapter that lists no schemas) is read in the one schema it lists, as before. A view and a materialized view MUST be saved among the views (`dbmodels/<model>/<schema>/views/`), and not counted. A foreign table, a partitioned table and a partition are tables, as the reader lists them. A schema with nothing in it, and a table, view, schema or column the role cannot see, is not in the project. Two tables of one name in two schemas are two tables, each with its own columns. A scan does not read foreign keys or indexes into the project (a foreign key into a table that the scan does not hold is left out of the scan, with a line in the log).

#### REQ: postgres-saves-column-defaults

A PostgreSQL scan MUST save a column's default in the `default` field of the column in its columns file, as the text of the SQL expression that the reader reports and PostgreSQL stores (`'new'::text`, `now()`, `0`, `nextval('sales.ticket_seq'::regclass)`): not evaluated, not normalised, and not a value. A generated column's default is the text `GENERATED ALWAYS AS (<expression>)`, which the text of a plain default never begins with. An identity column has no default: the reader reports it as an auto-incrementing column, and the columns file has no field for that, so it is saved with none. A column with no default has no `default` field.

#### REQ: postgres-help-says-what-works

The help of `scan` MUST say what works: that `-D postgres` takes its connection from the variable of `--dsn-env`, which stays in the environment and is never written to the project; that the scan reads every schema, saves a view as a view and records the default of each column, saves no foreign key and no index, and records the driver and the database id in the environment file and the name of the variable in a descriptor; and that the project never holds a host, a port, a user or a password. The text of `--driver` MUST name `postgres` among the drivers.

### Sensitive data handling

#### REQ: no-password-in-logs

Database passwords MUST NOT appear in stdout or stderr at any verbosity. The current implementation passes `--password` straight into `NewConnectionString`. Logging of the connection string MUST mask the password (compare the `cmd_execute_sql.go` redaction pattern `password=******`).

## Parameters

| Flag | Aliases | Type | Required | Description |
|---|---|---|---|---|
| `--driver` | `-D` | string | yes | DB driver. Supported: `sqlite3`, `sqlserver`, `postgres`. An empty or another value is refused first, naming `--driver`. |
| `--path` |  | string | yes (`sqlite3`) | The SQLite database file. It must exist. |
| `--dsn-env` |  | string | yes (`postgres`) | The environment variable that holds the PostgreSQL connection URL. Its name starts with `DATATUG_` or is listed in `DATATUG_DSN_ENV_ALLOW`. The URL is never written to the project. |
| `--server` | `-s` | string | yes (`sqlserver`) | Network host. |
| `--port` |  | int | no | Network port; driver-default if omitted. |
| `--user` | `-U` | string | no | DB user. |
| `--password` | `-P` | string | no | DB password. |
| `--db` |  | string | yes | Catalog/database ID to scan: a plain name (for `sqlserver` also the name of the database on the server). |
| `--dbmodel` |  | string | no | DB model ID: a plain name. Defaults to the model the project records for the catalog, else the model the other environments record for it (when they agree), or else `--db`. |
| `--env` |  | string | yes | Environment ID (`LOCAL`, `DEV`, etc.): a plain name. |
| `--follow-project-link` |  | bool | no | Write into the project folder even when the last part of the folder given with `--directory` is a link. Without it the scan prints the folder the link leads to and stops (see [REQ: project-folder-link](#req-project-folder-link)). |
| `--project` / `--directory` | `-p` / `-d` | string | (one of, or cwd) | Project context. See [parent feature](../README.md). A `--directory` that does not exist is made. With `--directory`, `--project` is the id of a new project; without it the id is the name of the folder (see [REQ: new-project-id](#req-new-project-id)). |

## Project layout written by a scan

This is the one place the files of a scan are written down. It is the layout of the demo project (`chinook-demo`), and nothing new: the CLI (`chat`, `query run`, `serve`) and the web app read it, and `scan` is the only thing that writes it from a database. Names in `<angle brackets>` are the ids given to the scan: `<env>` is `--env`, `<db>` is `--db`, `<model>` is the database model id (the same as `<db>` today), `<schema>` is the schema the engine reports (`main` for SQLite) and `<T>` is the table or view name, exactly as the engine reports it, with its case.

| File | Written by | Fields |
|---|---|---|
| `datatug-project.json` | `SaveProject` | `id`, `access`, `created` |
| `README.md` | `SaveProject` | generated text, on the first scan; a `README.md` already in the folder is kept as it is: `SaveProject` replaces it on every save, so the scan puts it back (`saveKeepingReadme`, `pkg/api/scan_layout.go`) |
| `environments/<env>/<env>.env.json` | `SaveProject` | `id`; `dbServers[]` with `driver`, `host` and `port` (`sqlserver` only: none for `sqlite3` or `postgres`, see [REQ: postgres-project-holds-no-connection](#req-postgres-project-holds-no-connection)) and `catalogs[]`, the ids of the databases scanned on that server |
| `environments/<env>/catalogs/<db>/<db>.db.json` | `SaveEnvDbCatalog` | `id`; `driver`; `path` (`sqlite3`: see [REQ: sqlite-path-stored-portably](#req-sqlite-path-stored-portably); `postgres`: the path of the connection descriptor); `dbModel`, the model id; `schemas`, always `[]` |
| `connections/<env>/<db>.json` | the CLI (`postgres` only) | `dsnEnv`, the name of the environment variable that holds the connection URL, and nothing else |
| `dbmodels/<model>/<model>.dbmodel.json` | `SaveProject` | `id`; `environments[]` with `id` and `DbCatalogs[]` of `id` (no schemas, no tables) |
| `dbmodels/<model>/<schema>/tables/<T>/<schema>.<T>.columns.json` | the CLI | `columns[]`, in the engine's column order, each with `name`, `ordinalPosition`, `pkPosition` (the 1-based place in the primary key, left out when the column is not in it), `isNullable`, `dbType`, the other column properties the engine reports (such as `default` and `charMaxLength`), and `byEnv`, which holds each environment that has the column, each with `status` `exists` (see [REQ: rescan-keeps-other-environments](#req-rescan-keeps-other-environments)) |
| `dbmodels/<model>/<schema>/views/<T>/<schema>.<T>.columns.json` | the CLI | the same, for a view |

A table or view is the folder `<T>`: the readers list tables and views by folder name, and read the columns from the one file in it whose name ends in `.columns.json`, found by listing the folder: a `[` in a table name, or in the path of the project, is a character of a name and not a pattern, so `t[1]` and `t1` are two tables. Nothing else is written. In particular the project holds no foreign keys, no indexes, no record counts and no DDL, and no password. A scan of PostgreSQL records its driver and its catalog id only, and the name of an environment variable in a descriptor, and no host, port, user, password or URL in any project file (see [PostgreSQL](#postgresql)).

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

Given that project, after one table is dropped from the database and another added, `datatug scan` run again exits `0` and says on stderr one line that names the folder it removed; the folder of the dropped table is gone, the folder of the new table is there, and every other file is as it was. A folder that is its own and is, or is in, a link (a symbolic link, or what `Lstat` reports as anything but a plain folder, as it does a Windows junction) is never removed: the scan refuses, names it, and writes nothing, at each level from `dbmodels` down. A folder that is not the scan's (a person's own folder with a `README.md`, a copy of the folder of a table made with `cp -r tables/Customer tables/Customer.bak`, a folder renamed with `mv tables/Old tables/Old-2019`, a folder whose columns list only another environment, even one that is linked) is not touched, not named, and does not fail the scan, on this scan or any later one, and the tree is the same by SHA-256 after two rescans; a folder of the scan's own that holds a file the scan did not write (another columns file included) stays and is named. The empty `tables`, `views` and schema folders stay. (`TestScanJourneySQLite`, `TestScanJourneyLeavesAFolderOfItsOwnAlone`, `TestScanJourneyLeavesACopyOrARenameOfATableFolderAlone`, `TestSaveScannedProject_RescanLeavesACopyOrARenameOfATableFolderAlone`, `TestSaveScannedProject_RescanTakesBackWhatTheDatabaseDropped`, `TestSaveScannedProject_RescanTakesBackAWholeSchema`, `TestSaveScannedProject_RescanNeverRemovesThroughASymbolicLink`, `TestSaveScannedProject_RescanLeavesWhatItDidNotWrite`, `TestApplyRetractions`.)

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

`datatug scan --directory ./proj --db shop --env local` exits non-zero with a message that names `--driver`, and does not name `--server`; so does an empty `-D`, in a folder that holds the database under another driver as well as in one that holds nothing, and with no project at all. `-D mysql`, or a value that is a connection string, exits non-zero with a message that names `--driver` and the drivers a scan reads, and repeats the value only when it is a plain name. (`TestScanJourneyMissingDriverNamesTheFlag`, `TestScanJourneyUnsupportedDriverNamesTheFlag`.)

### AC: scan-writes-no-file-through-a-link

**Requirements:** scan#req:writes-plain-files-in-plain-folders

Given a project folder where any file a scan writes, or any folder above one (for `-D sqlite3` and for `-D postgres`, in a folder that has none of the project yet and in one that has all of it), is a link to a place outside the folder, to a file or a folder that is there or to nothing, `datatug scan` exits non-zero with a message that names that path of the project and not where it leads, and the tree outside is byte-identical afterwards, with nothing new in it. A scan into a clean folder writes the same files, byte for byte, as the scan of the version before this rule. (`TestScanJourneyWritesNothingThroughALink`, `TestScanJourneyIntoACleanFolderWritesWhatMainWrote`, `TestSaveScannedProject_WritesNoFileThroughALink`, `TestSaveScannedProject_KeepsNoReadmeThroughALink`.)

### AC: project-folder-that-is-a-link-stops-the-scan

**Requirements:** scan#req:project-folder-link

Given `--directory ./link`, `./link/` or `.` (from a working directory entered through a link), where the link leads to a project folder, `datatug scan` exits non-zero, prints the folder the link leads to and the name of `--follow-project-link`, and writes nothing, for `-D sqlite3` and `-D postgres` (the server is not opened); with `--follow-project-link` it writes into the folder the link leads to. (`TestScanStopsAtAProjectFolderThatIsALink`, `TestScanStopsAtTheWorkingDirectoryThatIsALink`, `TestPostgresScanStopsAtAProjectFolderThatIsALink`, `TestCheckProjectFolderLink`.)

### AC: dbmodel-is-honoured

**Requirements:** scan#req:dbmodel-default

`datatug scan ... --db shop --dbmodel retail` writes `dbModel: retail` in the catalog file and the tables under `dbmodels/retail/`. (`TestScanJourneyHonoursTheDbModel`, `TestScanDbCatalog_SQLite3_CatalogIsMappedOntoTheModelThatWasAskedFor`.)

### AC: recorded-model-is-kept

**Requirements:** scan#req:dbmodel-default

Given a project scanned with `--db shop --dbmodel retail-model`, `datatug scan` run again without `--dbmodel` exits `0`, says nothing on stderr, makes no `dbmodels/shop/` and leaves every file as it was; the same with `--dbmodel retail-model`. With `--dbmodel other-model` it exits non-zero with a message that names `retail-model` and `other-model`, and writes nothing; so it does with `--dbmodel` naming another model when the first scan gave none (the model was `shop`). The same database in another environment, which the project does not record, takes the model it is given with `--dbmodel`; with no flag it takes the model the other environments record for it, when they all name the same one (the model file then lists both environments, and each column's `byEnv` lists both), and it is refused, naming each model and its environments, when they name different ones. A catalog file in the flat place (`catalogs/<db>.db.json`) records its model like a nested one: a rescan without `--dbmodel` stays on it. (`TestScanJourneyKeepsTheModelOfACatalogTheProjectHolds`, `TestResolveScanDbModel`.)

### AC: names-must-be-plain

**Requirements:** scan#req:names-are-plain

`datatug scan` with `--db ../evil`, `--env a:b`, `--dbmodel x/y`, or `--db con` exits non-zero with a message that names the flag, repeats the value only when it is a plain name, and writes nothing. The function that saves a scan checks the three ids itself, for any caller: an id such as `../../outside` is refused before anything is listed, written or removed. (`TestScanJourneyRefusesNamesThatAreNotPlain`, `TestCheckScanName`, `TestSaveScannedProject_RefusesIdsThatAreNotPlainNames`.)

### AC: names-must-not-differ-only-by-case

**Requirements:** scan#req:names-do-not-differ-only-by-case

Given a project scanned as `--db shop --env dev` (model `shop`), `datatug scan` with `--env Dev`, with `--db Shop`, with `--db crm --dbmodel Shop`, or with `--db Shop --env prod` (the model that `--db` gives) exits non-zero with a message that names the flag and the name the project has, before the database is read, and every file of the project is as it was (compared by SHA-256), with its tables, on any file system. `SaveScannedProject` refuses the same ids for any caller, before it writes or removes anything. A name that is the same as the project's is accepted. (`TestScanJourneyRefusesANameThatDiffersOnlyByCaseFromOneTheProjectHas`, `TestCheckScanNamesAgainstProject`, `TestSaveScannedProject_RefusesIdsThatDifferOnlyByCaseFromTheProjects`.)

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

**Requirements:** scan#req:sqlite-pure-go

Given a SQLite file named `shop#1 50%.db`, `what?mode=rw.db` or `with space.db`, or one with a tab, a newline or a carriage return in its name, or one in a folder whose name has a `?`, `#`, `%`, a space, a tab or a newline, `datatug scan --path` that file reads it, and creates no other file beside it; the path in the catalog file is resolved by `ResolveCatalogPath` and by every reader of the project (the source of a saved query, chat and `serve`) to that file, and a query through the source reads it, through the open of a source (`pkg/dbcopy`) and through the connection that runs native SQL (`pkg/secureread`): both hand the driver the path as a `file:` URI with `?`, `#` and `%` percent-encoded (`dbcopy.SQLiteFileURI`), so a `?` in a name is never read as the start of the driver's own parameters, and neither open creates a file. A relative `--path` run from a working directory whose path has a `?` is the same file. (`TestScanJourneyPathWithURICharacters`, `TestScanJourneyRelativePathFromAWorkingDirectoryWithAQuestionMark`, `TestScanDbCatalog_SQLite3_PathWithURICharactersIsReadBack`, `TestOpen_SQLiteFileWhoseNameHoldsAnyCharacterIsOpenedAndNothingIsCreated`.)

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

### AC: scans-postgres-into-project

**Requirements:** scan#req:driver-selection, scan#req:postgres-project-holds-no-connection, scan#req:postgres-names-are-exact, scan#req:persist-via-project-store

Given a PostgreSQL database (a fake schema reader behind the one seam through which the scan opens a source, which lists its schemas as `dalgoschema.SchemaLister` does) with mixed-case names, two schemas, a view, a composite primary key and columns with defaults, `datatug scan --directory ./shop --driver postgres --dsn-env DATATUG_SHOP_PG_URL --db shop --env local` exits `0`; the folder holds exactly the files of the layout and the descriptor; `LoadProject` loads it and `Validate` accepts it; the tables, views and columns of both schemas are listed under their exact names, with the position of each primary-key column, and the default of a column in the columns file; the web reader's walk finds the same, and so do the table list of `serve` (`GetCatalogTables`) and the project catalog of `chat`, which holds the tables as tables and the views as views and marks the source resolved; the source resolves to `env:DATATUG_SHOP_PG_URL`; and no file of the folder, or name of one, holds the host, the port, the user, the password, the query string or the URL of the fake server, and no `.json` file has a key named `host`, `port`, `user`, `password` or `server` at any depth (a port written as a number is not found by a search of the text). (`TestScanJourneyPostgres`, `TestAssertNoSourceInFindsAConnectionKeyAtAnyDepth`.)

### AC: postgres-second-scan-is-quiet

**Requirements:** scan#req:idempotent-rescan, scan#req:rescan-removes-dropped-tables

Given the project of the previous criterion, a second scan exits `0`, says nothing on stderr and leaves every file with the content it had (compared by SHA-256); after a table is dropped from the database and another added, a scan exits `0`, says on stderr one line that names the folder it removed, and the folder of the dropped table is gone, the new one is there and every other file is as it was. (`TestScanJourneyPostgres`.)

### AC: postgres-connection-is-refused-before-anything-is-opened

**Requirements:** scan#req:postgres-connection-from-environment, scan#req:no-password-in-logs

`datatug scan -D postgres` with no `--dsn-env`, with a variable that is not set, with a name that is not allowed, with a password on the command line (`-P`), with a URL whose user holds the password, with a URL whose password splits it, or with a URL whose query sets `host`, `port`, `dbname` or `database`, exits non-zero with a message that says what to do and repeats no part of the URL (the last names only the key), before the source is opened, with nothing logged that names a server or a user, and the folder is as it was. (`TestScanCommandAction_PostgresRefusesBeforeItOpensOrLogsAnything`, `TestNewPostgresScanParams_Refuses`.)

### AC: postgres-descriptor-is-inside-the-project

**Requirements:** scan#req:postgres-project-holds-no-connection

A catalog of the driver `postgres` whose `path` is `/etc/shop.json`, `../shop.json`, `~/shop.json`, `$HOME/shop.json`, a URL, or a file that a link leads out of the project folder to, resolves to no source, and the message repeats no part of the path. A path whose link check fails for a reason other than "there is no such file" resolves to no source either. A descriptor that names a variable the operator did not set aside, or that holds any field but `dsnEnv`, is refused. (`TestResolveDescriptorPath`, `TestResolveDescriptorPath_RefusesALinkThatLeadsOutOfTheProject`, `TestResolveDescriptorPath_RefusesAFileItCannotClassify`, `TestSourceURLFromCatalog_PostgresRefusesWhatItCannotTrust`.)

### AC: postgres-failed-save-takes-back-the-descriptor

**Requirements:** scan#req:postgres-descriptor-follows-the-project

Given a folder where the save of the project fails after the descriptor was written (a file where the folder of the models belongs), `datatug scan -D postgres` exits non-zero and leaves no `connections` folder; given a descriptor that was there, it is as it was. A rescan with another `--dsn-env` changes the descriptor, the catalog file still names it, and no other file changes. (`TestScanJourneyPostgresFailedSaveTakesBackTheDescriptor`, `TestScanJourneyPostgresRescanWithAnotherVariableUpdatesTheDescriptor`, `TestUpdateDbSchema_PostgresRescanUpdatesTheCatalogPathWithTheDescriptor`, `TestWriteDescriptor_UndoRemovesTheDescriptorAndTheFoldersItMade`, `TestWriteDescriptor_UndoLeavesTheFoldersThatWereThereAndWhatIsInThem`, `TestWriteDescriptor_UndoPutsBackADescriptorThatWasThere`.)

### AC: postgres-descriptor-is-not-written-through-a-link

**Requirements:** scan#req:postgres-descriptor-follows-the-project

Given a project where `connections`, `connections/local` or `connections/local/shop.json` is a link to a place outside the project folder (to a folder that holds a file of that name, or to a file), `datatug scan -D postgres --db shop --env local` exits non-zero with a message that names that path of the project and not where it leads, before anything is saved: the project folder is as it was, and the file the link leads to has the content it had and nothing is made beside it. A link to nothing, a folder where the descriptor goes, and a file or a link that cannot be looked at are refused the same way. (`TestScanJourneyPostgresRefusesALinkWhereTheDescriptorGoes`, `TestWriteDescriptor_RefusesALinkAnywhereOnItsPath`, `TestWriteDescriptor_RefusesADanglingLinkAndANonFileWhereTheDescriptorGoes`, `TestWriteDescriptor_ALinkThatCannotBeLookedAtIsRefused`, `TestWriteDescriptor_ADescriptorThatCannotBeLookedAtIsRefused`, `TestWriteDescriptor_AFileThatCannotBeWrittenLeavesNoFolderBehind`.)

### AC: one-database-id-has-one-driver

**Requirements:** scan#req:postgres-and-sqlite-in-one-environment

Given a project that holds the SQLite catalog `shop` in environment `local`, `datatug scan -D postgres --db shop --env local` exits non-zero with a message that names `sqlite3` and `postgres` and says to use another `--db`, before the PostgreSQL source is opened, and the folder is as it was; the same holds for a SQLite scan of a `shop` that the project holds as a PostgreSQL catalog. The same scan into environment `prod` works. (`TestScanJourneyOneDatabaseIdUnderTwoDriversIsRefused`, `TestCheckScanDriverAgainstProject`, `TestSaveScannedProject_RefusesADatabaseThatIsAnotherDriversInTheEnvironment`.)

### AC: postgres-and-sqlite-share-an-environment

**Requirements:** scan#req:postgres-and-sqlite-in-one-environment

A SQLite file and a PostgreSQL database scanned into one project and one environment, in either order, are both in the environment file, each under its own driver and with no host or port; each resolves to its own source; and a rescan of both leaves the folder byte-identical. Two PostgreSQL databases of two hosts are two catalogs of the one server of the project. (`TestScanJourneyPostgresAndSQLiteInOneEnvironment`, `TestScanJourneyTwoPostgresDatabasesInOneEnvironment`.)

### AC: postgres-prints-no-credentials

**Requirements:** scan#req:postgres-says-no-credentials

The scan of a server whose URL holds a user, a password and a query string logs `connecting to postgres://<host>:<port>/<database>` and nothing of the user, the password or the query; when the open of the server, the listing of its tables or the count of the records of one fails in words that quote the URL, the error or the log line is the classified open failure, which names the source as `env:<VARIABLE>`, and no secret is in the output, the log or any file (checked against every generated source string, `TestProperty_NoCommandPathEchoesASourceSecret`); a view is not counted, and a server whose reader cannot tell its views (the reader that ships) is not counted at all, though it runs `COUNT(*)` natively: no count query is sent. No test dials a server: the open is a seam, and the default of the seam in the test binary of `pkg/api` and of `apps/datatugapp/commands` stops the run. (`TestScanJourneyPostgresNamesTheServerWithoutTheCredentials`, `TestScanJourneyPostgresReadErrorsAreClassified`, `TestScanDbCatalog_PostgresReportsACountThatFailsAsAClassifiedFailure`, `TestScanDbCatalog_PostgresCountsTheRecordsOfATableAndNotOfAView`, `TestScanDbCatalog_PostgresRunsNoCountThroughAReaderThatCannotTellItsViews`, `TestNeverDialStopsTheRunOnAnyRealOpen`, `TestTheOpenOfThisTestBinaryStopsTheRunOnAnyOpen`, `TestTheOpenOfThisTestBinaryIsTheOneThatStopsTheRun`.)

### AC: postgres-names-keep-their-case

**Requirements:** scan#req:postgres-names-are-exact, scan#req:unusable-names-left-out

Given tables named `Customer` and `customer`, `a/b` and `con` in schema `public`, schemas `Reports` and `reports`, and a schema `x/y`, `datatug scan -D postgres` exits `0`, names on stderr the table that differs by case, `a/b`, `con`, the schema that differs by case and `x/y`, one line each, and writes every other table under the folder of its own schema; the adapter is opened in exact identifier mode. (`TestScanJourneyPostgresNamesThatCannotBeFolders`, `TestOpenSchemaScan_PinsTheExactIdentifierMode`, `TestScanCatalog_ReadsEachCollectionInTheSchemaItsReferenceNames`.)

### AC: postgres-reads-every-schema-and-saves-defaults

**Requirements:** scan#req:postgres-reads-every-schema, scan#req:postgres-saves-column-defaults

Given a real PostgreSQL 17 server (the CI job `Journey (PostgreSQL)`, which gates the release) whose database has, in the schema `public`, tables with a composite primary key, a mixed-case name, a view and columns with the defaults `now()`, `0` and `'open'`, and a second schema `sales` with a table of the same name as one in `public`, a table of its own, a view, a materialized view, and columns with a constant default, `now()`, a sequence, an identity column and a generated column, `datatug scan -D postgres` exits `0` and writes the columns file of every table and view of both schemas and nothing else of them: the views in `views/`, the two tables of one name each in its own schema's folder, each column with the type, the nullability and the primary-key position the server gives, and each default exactly as the server stores it (no `default` field for a column with none and for the identity column); a scan of the same database again leaves every file as it was. The files of the schema `public` are the files a scan wrote before every schema was read, apart from the `default` fields, and the views that were saved as tables. (`TestPostgresScanJourney`, `TestScanJourneyPostgres`, `TestScanCatalog_ReadsEverySchemaTheReaderLists`, `TestGetColumns_RecordsTheDefaultOfEachColumnAsTheReaderReportsIt`.)

### AC: postgres-help-names-the-driver

**Requirements:** scan#req:postgres-help-says-what-works

`datatug scan --help` names `postgres` among the drivers, says that the connection comes from the variable of `--dsn-env` and is never written to the project, that every schema is read, a view is saved as a view, the default of a column is saved, and foreign keys and indexes are not, and does not say that the scan is not available. (`TestScanCommand_HelpSaysWhatPostgresDoes`.)

### AC: first-scan-takes-nothing-back

**Requirements:** scan#req:rescan-removes-dropped-tables

Given a model file whose environment lists no catalog while the columns files of the model list the environment, a first scan of a new catalog onto that model removes no folder and says nothing of the tables that are there; the rescan of that catalog, once recorded, takes back what its database dropped. (`TestSaveScannedProject_AFirstScanOfACatalogTakesNothingBack`.)

### AC: model-taken-from-other-environments-is-said

**Requirements:** scan#req:dbmodel-default

A scan with no `--dbmodel` of a catalog that another environment records on the model `retail-model` puts it on that model and says so in one line on stderr that names the model and the environment; a model called as the database is not said. (`TestScanJourneyKeepsTheModelOfACatalogTheProjectHolds`, `TestResolveScanDbModelNoted`.)

### AC: names-are-one-definition

**Requirements:** scan#req:names-are-plain

Every name that `--env`, `--db` or `--dbmodel` accepts is a name every route of `serve` accepts for an environment or a catalog, so that a project a scan writes can always be browsed; a name that differs only by case from another that is there as written is not refused for it. (`TestEveryNameTheScanAcceptsIsOneTheRoutesOfServeAccept`, `TestCheckScanNamesAgainstProject`.)

## Open Questions

- Should there be a `--dry-run` flag that connects, reads schema, but does not write the project? Useful for CI checks before committing.
- Should the command refuse to scan an `--env PROD` without an additional `--allow-prod` flag, as a foot-gun guard?
- The flag `--server` (`-s`) refers to a database host, not an HTTP server; the parent CLI also has `-s` used differently in other commands. Should there be a shared-flag REQ for what `-s` means?

---
*This document follows the https://specscore.md/feature-specification*
