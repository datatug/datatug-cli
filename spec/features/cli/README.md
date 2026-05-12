# Feature: CLI

> [View in SpecStudio](https://specstudio.synchestra.io/project/features?id=datatug-cli@datatug@github.com&path=spec%2Ffeatures%2Fcli) — graph, discussions, approvals

**Status:** Implementing

## Summary

The `datatug` CLI is the local agent for the DataTug data exploration platform. It scaffolds DataTug projects, scans database schemas into versionable project files, serves an HTTP API for the DataTug Web UI, opens an interactive terminal UI (TUI) over the project, executes queries against connected databases, and manages a per-user registry of DataTug projects.

This feature is the umbrella for command-level specifications. Each command or command group owns a child feature directory with its own contract — flags, output, exit codes, and behavior.

## Problem

DataTug spans a TUI, a Web UI agent, a project file format, and connectors to several database engines. The CLI is the single entry point for all of those surfaces. Without a written contract:

- Flag names drift between releases (e.g., `--project` vs `-p` vs `--dir` vs `-d` already coexist across commands today).
- Default behaviors are non-obvious. `datatug` with no subcommand currently launches the TUI; `datatug serve` opens a browser to a remote URL by default.
- Exit codes are inconsistent. Several commands `log.Fatal` or `panic` rather than return a structured error.
- Output shapes (`projects` emits CSV; `dataset-data` emits YAML/JSON/GRID; `show` emits decorated text) are not pinned, so script authors cannot rely on them.

Pinning the contract surface lets the implementation evolve without breaking users' scripts, dashboards, and CI checks.

## Contents

| Directory | Description |
|---|---|
| [ui/](ui/README.md) | TUI — terminal UI over a DataTug project (default command) |
| [init/](init/README.md) | Scaffold a new DataTug project on disk |
| [config/](config/README.md) | Print user-level CLI configuration |
| [projects/](projects/README.md) | List & manage user-level project registry (with [`add`](projects/add/README.md)) |
| [serve/](serve/README.md) | Run HTTP server providing the DataTug Web UI's API |
| [scan/](scan/README.md) | Scan a database and update project metadata |
| [show/](show/README.md) | Print a human-readable summary of a project |
| [validate/](validate/README.md) | Validate a DataTug project on disk |
| [render/](render/README.md) | Re-render `README.md` files inside a project |
| [dataset/](dataset/README.md) | Inspect datasets — with [`def`](dataset/def/README.md) and [`data`](dataset/data/README.md) subcommands |
| [datasets/](datasets/README.md) | List datasets in a project |
| [demo/](demo/README.md) | Install and run the bundled demo (Chinook) project |
| [console/](console/README.md) | Interactive console (placeholder; not implemented) |
| [db/](db/README.md) | Open a database viewer by URL |
| [queries/](queries/README.md) | Queries management (placeholder; not implemented) |
| [execute/](execute/README.md) | Execute an SQL query/command (currently exposed under the name `updateUrlConfig`) |
| [version/](version/README.md) | CLI version reporting |

External command groups live in their own packages and are referenced here but not specified in this tree:

| External group | Source | Notes |
|---|---|---|
| `auth` | [`pkg/auth`](../../../pkg/auth) | Authentication subcommands |
| `gcloud` | [`apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudcmds`](../../../apps/datatugapp/datatugui/dtviewers/clouds/gcloud/gcloudcmds) | Google Cloud integration subcommands |

These groups MUST observe the shared conventions defined below even though their per-command contracts live elsewhere.

## Behavior

### Default command

#### REQ: default-command-is-ui

When invoked with no subcommand (`datatug`), the CLI MUST launch the TUI defined in [ui/](ui/README.md). This matches the existing implementation (`DefaultCommand: "ui"` in `apps/datatugapp/commands/command.go`).

### Command-naming conventions

Commands SHOULD follow a `datatug <resource> <action>` pattern with singular nouns and verb subcommands, matching the style of `gh`, `kubectl`, and `docker`. The current command set predates this convention; new command groups MUST adopt it, and existing commands that violate it (e.g., `projects` should be `project list`; `datasets` should be `dataset list`; `updateUrlConfig` should be `execute` or `query run`) are tracked under Outstanding Questions for a deprecation pass.

#### REQ: singular-resource-names

New resource names introduced after this spec lands MUST be singular (`project`, `dataset`, `query`), never plural. Pluralization is an output-shape concern, not a command-name one.

#### REQ: verb-subcommands

Every action MUST be an explicit subcommand verb (`list`, `info`, `new`, `add`, `scan`, `serve`). A bare resource name (e.g., `datatug dataset`) MUST show help — it MUST NOT perform an implicit default action.

#### REQ: prefer-new-over-create

Commands that create new artifacts MUST use the verb `new`, never `create`. `datatug init` is grandfathered because the artifact it creates is the project root itself.

### Shared exit-code contract

Every `datatug` command MUST observe the following exit-code contract:

| Exit code | Meaning |
|---|---|
| `0` | Success |
| `1` | Generic runtime error (catch-all; current implementation uses `log.Fatal` which produces `1`) |
| `2` | Invalid arguments (missing required flag, bad flag value, malformed input) |
| `3` | Resource not found (project, dataset, file, database) |
| `4` | Connection or I/O failure against an external system (database, HTTP, filesystem) |
| `10` | Unexpected / catch-all panic |

Exit codes `5–9` and `11–19` are reserved for future standard codes and MUST NOT be used by individual commands.

#### REQ: standard-exit-codes

Commands MUST map errors to the standard code with the matching semantics. A command that has no notion of "connection failure" simply never returns code `4`; it does not repurpose it.

#### REQ: error-on-stderr

On any non-zero exit, a human-readable explanation MUST be written to stderr. stdout MUST remain free of error prose so that pipelines consuming structured output (YAML/JSON/CSV) are not corrupted by error messages.

#### REQ: no-log-fatal

New commands MUST NOT call `log.Fatal` or `os.Exit` directly. They MUST return an `error` from their action and let the root command map it to an exit code. The current implementation violates this in several places; remediation is tracked under Outstanding Questions.

### Panic recovery and telemetry

#### REQ: panic-recovery

A panic from any command MUST be caught by the top-level recovery in `main.go`, MUST restore the terminal if a TUI was active (`global.App.Stop()`), MUST print the panic message and stack to stderr, and MUST exit with code `10`.

#### REQ: telemetry-events

The CLI emits PostHog telemetry events (`DataTug CLI started`, `DataTug CLI exited`, panic exceptions) via `pkg/dtlog`. Telemetry MUST be enqueued asynchronously and MUST NOT block command exit beyond the existing `dtlog.Close()` flush. Telemetry MUST NOT include database contents, query text, or user-supplied credentials.

### Output format conventions

#### REQ: yaml-default-for-structured

Read commands that return structured data SHOULD default to YAML output. Where alternatives are documented on the individual command (`text`, `json`, `csv`, `grid`), they MUST be selected via a `--format` flag.

The current implementation defaults to CSV for `projects` and YAML for `dataset-data`; this REQ pins the target convention. Existing defaults are grandfathered until the next major version.

#### REQ: stable-output-keys

YAML and JSON output keys are part of each command's contract. Renaming or removing a key is a breaking change and MUST follow the deprecation path (announce in release notes, keep the old key for at least one release cycle). Adding new keys is always allowed.

### Shared flags

| Flag | Aliases | Semantics |
|---|---|---|
| `--project` | `-p` | DataTug project ID (resolved against the user-level registry at `~/.datatug.yaml`). |
| `--dir` | `-d` | Path to a DataTug project directory. Mutually exclusive with `--project`. |
| `--format` | `-f` | Output format. Allowed values vary by command (subset of `yaml`, `json`, `text`, `csv`, `grid`). |
| `--tui` |  | Force TUI mode where supported (parent command flag). |
| `-h`, `--help` |  | Print help and exit `0`. Provided by urfave/cli; commands MUST NOT override it. |

#### REQ: project-or-dir-resolution

When both `--project` and `--dir` are unset, commands that operate on a project MUST attempt to use the current working directory as the project directory. If the cwd is not a DataTug project, commands MUST exit `3` (NotFound) with a clear message.

#### REQ: project-and-dir-mutually-exclusive

If both `--project` and `--dir` are supplied, the CLI MUST exit `2` (InvalidArgs) with a message naming both flags.

### User-level configuration

The CLI maintains a per-user configuration file at `~/.datatug.yaml` that holds the project registry, server defaults, and client defaults.

#### REQ: config-file-location

The user-level config MUST live at `~/.datatug.yaml`. The CLI MUST NOT silently relocate it. Tests and CI MAY override the location via a future `DATATUG_CONFIG` environment variable (not yet implemented; tracked under Outstanding Questions).

#### REQ: config-format-yaml

The user-level config file MUST be valid YAML. Comments are preserved on read where the underlying YAML library permits.

## Acceptance Criteria

### AC: bare-invocation-launches-tui

**Requirements:** cli#req:default-command-is-ui

`datatug` with no arguments launches the TUI defined in [ui/](ui/README.md) and exits `0` on clean shutdown.

### AC: unknown-subcommand-fails

**Requirements:** cli#req:standard-exit-codes, cli#req:error-on-stderr

`datatug not-a-real-command` exits non-zero, writes an explanation to stderr, and writes nothing to stdout.

### AC: project-resolution-rejects-conflict

**Requirements:** cli#req:project-and-dir-mutually-exclusive

`datatug serve --project demo --dir ./somewhere` exits `2` with a message naming both `--project` and `--dir`.

### AC: panic-produces-stack-and-exits-cleanly

**Requirements:** cli#req:panic-recovery

A panic inside any command exits the process with code `10`, leaves the terminal in a usable state (no leftover TUI screen), and writes the panic message plus a stack trace to stderr.

## Outstanding Questions

- Several existing command names violate the [REQ: singular-resource-names](#req-singular-resource-names) / [REQ: verb-subcommands](#req-verb-subcommands) conventions: `projects` (should be `project list`), `datasets` (should be `dataset list`), `queries` (should be `query list`), `updateUrlConfig` (should be `execute` or `query run`), `dataset-def` and `dataset-data` (should be `dataset def` / `dataset data` subcommands of [`dataset`](dataset/README.md)). Should this spec define a deprecation path with aliases, or wait for a 1.0 break?
- The implementation currently mixes `log.Fatal`, returned errors, and `panic` for failure paths. Should this spec mandate the migration to returned errors in scope, or split that into a separate refactor feature?
- Telemetry is opt-out today (no flag). Should there be a `--no-telemetry` flag and/or a `DATATUG_TELEMETRY=0` env var pinned in this feature?
- Should the user-level config path be overridable via `DATATUG_CONFIG` to support reproducible CI?
- `datatug --version` / `-v` is not currently wired (the urfave/cli v3 default would expose them, but the build does not inject version metadata via ldflags). Should the [version/](version/README.md) feature spec drive that wiring?

---
*This document follows the https://specscore.md/feature-specification*
