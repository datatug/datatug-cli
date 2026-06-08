---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Execute

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/execute?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/execute?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/execute?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/execute?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug` exposes ad-hoc SQL execution against a connected database, optionally writing the result rows to CSV. The command opens a connection from supplied flags, runs a single query, and either pretty-prints the result table to stdout or writes a CSV file. The command is currently registered under the misnamed identifier `updateUrlConfig`; this spec pins the intended name `execute` and the migration path.

## Synopsis

```
datatug execute --driver <driver> --host <host> --consoleCommand-text "<sql>" \
  [--port <n>] [--user <name>] [--password <pw>] [--schema <name>] \
  [--output-path <file>] [--output-format csv]
```

> The CLI binary today registers this command as `datatug updateUrlConfig`. The name above is the target after rename.

## Problem

For quick troubleshooting, users need to run an SQL query without going through a project, an environment, and the Web UI. They want:

- A one-liner that takes a connection string and a query.
- Tabular output by default, CSV when piped to a file.
- Clear handling of passwords (not logged in plaintext).

## Behavior

### Connection construction

#### REQ: driver-required

`--driver`/`-D` MUST identify a linked-in SQL driver. If unset, the CLI MUST exit `2`.

#### REQ: host-default-localhost

`--host`/`-h` defaults to `localhost`, matching the current implementation.

#### REQ: mode-flag

`--mode` MUST accept `rw` (ReadWrite) or `ro` (ReadOnly). When unset, the default MUST be `ro` for SQLite and unset (driver default) for other drivers.

#### REQ: password-masked-in-logs

When the implementation logs the connection string, the password MUST be replaced with `password=******`. The current implementation already does this via a regex; the REQ pins it as contract.

### Query selection

The command accepts either `--query`/`-q` or `--consoleCommand-text`/`-t`, but not both.

#### REQ: query-xor-command-text

If both `--query` and `--consoleCommand-text` are supplied, the CLI MUST exit `2` (InvalidArgs). The current implementation enforces this via `validation.NewBadRequestError`.

#### REQ: star-equals-shortcut

If `--consoleCommand-text` starts with `*=`, the implementation MUST treat it as a shorthand for `SELECT * FROM <rest>`. The current implementation does this with `strings.TrimLeft(v.CommandText, "*=")` — note the trim-set semantics means `*==foo` also becomes `foo`. The shorthand MUST be preserved.

### Output

#### REQ: default-output-table

When `--output-path` is unset, the CLI MUST pretty-print the result rows as a terminal-aligned table on stdout, including a header row and a separator row of dashes.

#### REQ: csv-when-path-supplied

When `--output-path` is supplied, the CLI MUST write rows as CSV to that file. The default format `csv` is the only currently supported value of `--output-format`. Other values MUST cause exit `2`.

#### REQ: uniqueidentifier-as-string

When a result column's database type name is `UNIQUEIDENTIFIER` (SQL Server's GUID type), the implementation MUST decode the bytes via `uuid.FromBytes` and render the string form. Bad input (length != 16) MUST cause exit `1`.

### Resource cleanup

#### REQ: closes-connection-on-exit

The DB connection pool MUST be closed on command exit, success or failure. The current implementation already does this via `defer db.Close()`.

#### REQ: closes-rows-on-exit

The `sql.Rows` reader MUST be closed on command exit. The current implementation already does this via `defer rows.Close()`.

## Parameters

| Flag | Aliases | Type | Required | Description |
|---|---|---|---|---|
| `--driver` | `-D` | string | yes | SQL driver name. |
| `--host` | `-h` | string | no (`localhost`) | DB host. |
| `--port` |  | string | no | DB port. |
| `--mode` |  | string | no | `rw` or `ro`. |
| `--user` | `-U` | string | no | DB user. |
| `--password` | `-P` | string | no | DB password. |
| `--project` | `-p` | string | no | DataTug project ID (reserved; not currently used). |
| `--schema` | `-s` | string | no | DB schema/catalog. |
| `--query` | `-q` | string | one of | SQL query. |
| `--consoleCommand-text` | `-t` | string | one of | SQL command text (DDL, multi-statement, etc.). |
| `--output-path` | `-o` | string | no | Path to write CSV. If unset, prints to stdout. |
| `--output-format` | `-f` | string | no (`csv`) | Output format. Currently only `csv` is supported. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Query executed and rows printed/written |
| `2` | Invalid argument combination (e.g., both `--query` and `--consoleCommand-text`) |
| `4` | DB connection or query execution failure |
| `1` | Generic runtime error (UUID decode, file write) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [scan](../scan/README.md) | Both open DB connections via `pkg/datatug-core/dbconnection`. Shared connection-string logic MUST stay in one place. |
| [queries](../queries/README.md) | Future: `queries run <id>` may share this command's executor. |

## Acceptance Criteria

### AC: runs-query-prints-table

**Requirements:** execute#req:driver-required, execute#req:default-output-table

`datatug execute --driver sqlite3 --host ./demo.sqlite --query "SELECT 1"` (after the rename lands) exits `0` and prints a one-cell table.

### AC: query-or-command-text-mutually-exclusive

**Requirements:** execute#req:query-xor-command-text

Supplying both `--query` and `--consoleCommand-text` exits `2` with a stderr message.

### AC: csv-output

**Requirements:** execute#req:csv-when-path-supplied

`--output-path ./out.csv` writes a CSV file containing the result rows; stdout receives no row data.

### AC: password-not-logged

**Requirements:** execute#req:password-masked-in-logs

Running with `--password secret123` does NOT produce any line containing `secret123` on stdout or stderr.

## Open Questions

- The current command name is `updateUrlConfig` (the Go function is `updateUrlConfigCommandArgs`, the Usage string is "Executes query or a consoleCommand"). The implementation is clearly an executor, not a config-updater. Rename to `execute` is REQUIRED; this spec defines the target. When does the rename land — and is the old name kept as an alias?
- The flag `--consoleCommand-text` ships with the literal string `consoleCommand` (a leftover from a global rename of `command` → `consoleCommand` that hit places it shouldn't). It MUST be renamed `--command-text` / `-t`.
- The `--project` / `-p` flag is accepted but unused. Should it be removed, or wired up so that supplying it pulls connection details from the project's environment config?
- `--output-format` currently accepts only `csv`. Should `json` and `yaml` be added?
- Should the command support `--file <path-to-sql>` to read the SQL from disk instead of a flag value?

---
*This document follows the https://specscore.md/feature-specification*
