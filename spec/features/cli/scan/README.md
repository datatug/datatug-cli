# Feature: Scan

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/scan?op=request-change) |

**Status:** Implementing

## Summary

`datatug scan` connects to a database, introspects its schema (tables, views, columns, primary keys, foreign keys), and writes the resulting metadata into the DataTug project on disk. Re-running `scan` updates the metadata in place — this is the primary path for keeping a DataTug project synchronized with a live database.

## Synopsis

```
datatug scan --project <id> --driver <driver> --server <host> --db <name> --env <env> \
  [--port <n>] [--user <name>] [--password <pw>] [--dbmodel <id>]
```

## Problem

DataTug projects encode database schemas as versionable on-disk files. Authoring those files by hand is infeasible for any non-trivial database. The agent needs a one-command path to read the live `information_schema` (or driver equivalent), normalize the result, and write a deterministic file set that diffs cleanly under Git.

`scan` is intentionally a separate command from `serve` so users can run it as a CI job (against a staging environment, for example) and commit the result.

## Behavior

### Required project context

#### REQ: requires-project

`scan` MUST be invoked inside a project context — either via the cwd being a DataTug project, via `--project <id>`, or via `--dir <path>` (the shared CLI conventions, [REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution)). If no project context can be resolved, the command MUST exit `3` (NotFound).

### Required connection details

#### REQ: required-db-flag

`--db <name>` MUST be required. It identifies the database (catalog) to scan.

#### REQ: required-env-flag

`--env <env>` MUST be required. It identifies the environment within the project the scanned metadata belongs to (e.g., `LOCAL`, `DEV`, `SIT`, `UAT`, `PROD`).

#### REQ: driver-selection

`--driver`/`-D` MUST specify the database driver. Supported values today: `sqlserver`. SQLite scanning currently `panic`s (placeholder). The set of supported drivers MUST match the set linked into the binary via `_ "..."` imports in `main.go`.

#### REQ: connection-string-construction

The connection string MUST be built via `pkg/datatug-core/dbconnection.NewConnectionString(driver, host, user, password, db, options...)`. `port` and `mode=ReadOnly` MUST be appended as options when supplied. The CLI MUST connect in read-only mode for schema introspection.

#### REQ: dbmodel-default

When `--dbmodel` is omitted, the DB model ID MUST default to the value of `--db`. This makes single-database projects easy to scan while preserving the ability to map multiple physical databases onto one logical model.

### Output to project store

#### REQ: persist-via-project-store

The scan result MUST be written via `pkg/datatug-core/storage` interfaces, using the project's existing store. The scan MUST NOT bypass the storage layer and write files directly.

#### REQ: idempotent-rescan

Re-running `scan` against the same project, environment, and database MUST be idempotent on a database whose schema has not changed: the resulting on-disk files MUST be byte-identical to the prior run. This is the property that makes scans `git diff`-able.

### Sensitive data handling

#### REQ: no-password-in-logs

Database passwords MUST NOT appear in stdout or stderr at any verbosity. The current implementation passes `--password` straight into `NewConnectionString`. Logging of the connection string MUST mask the password (compare the `cmd_execute_sql.go` redaction pattern `password=******`).

## Parameters

| Flag | Aliases | Type | Required | Description |
|---|---|---|---|---|
| `--driver` | `-D` | string | yes | DB driver. Supported: `sqlserver`. |
| `--server` | `-s` | string | yes (network DBs) | Network host. |
| `--port` |  | int | no | Network port; driver-default if omitted. |
| `--user` | `-U` | string | no | DB user. |
| `--password` | `-P` | string | no | DB password. |
| `--db` |  | string | yes | Catalog/database ID to scan. |
| `--dbmodel` |  | string | no | DB model ID. Defaults to `--db`. |
| `--env` |  | string | yes | Environment ID (`LOCAL`, `DEV`, etc.). |
| `--project` / `--dir` |  |  | (one of, or cwd) | Project context. See [parent feature](../README.md). |

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

Given a project at `./proj` and a reachable SQL Server, `datatug scan --dir ./proj --driver sqlserver --server localhost --db sample --env DEV` exits `0` and updates the project files on disk to reflect the database's tables, views, columns, and FK relationships.

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

- SQLite scanning is currently `panic("not implemented yet")`. Should this spec mandate SQLite support or formally defer it to a follow-up feature?
- Should there be a `--dry-run` flag that connects, reads schema, but does not write the project? Useful for CI checks before committing.
- Should the command refuse to scan an `--env PROD` without an additional `--allow-prod` flag, as a foot-gun guard?
- The flag `--server` (`-s`) refers to a database host, not an HTTP server; the parent CLI also has `-s` used differently in other commands. Should there be a shared-flag REQ for what `-s` means?

---
*This document follows the https://specscore.md/feature-specification*
