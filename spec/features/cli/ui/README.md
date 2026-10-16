---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: UI

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/ui?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/ui?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/ui?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/ui?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug ui` (and the bare `datatug` invocation) launches the terminal UI — a Bubble Tea application that lets the user browse DataTug projects, viewers, settings, and the API service monitor. The TUI is the CLI's default surface for human users; scripts and CI workflows use the other commands.

## Synopsis

```
datatug
datatug ui [--file <path>]
datatug ui -f <path>
datatug ui --result <id> [--as <user>] [--role <role>]... [--group <group>]...
```

## Problem

A data-exploration tool that ships only a Web UI requires running a server, opening a browser, and switching context away from the terminal. A pure-CLI tool covers scripting but cannot show schema diagrams, paginated tables, or interactive forms. The TUI bridges both: it stays in the terminal, supports keyboard-driven navigation, and shows DataTug viewers (project, DB viewer, cloud viewers) without spinning up a browser.

The TUI is also the default surface — running `datatug` with no arguments MUST open it. Users learn the tool by typing `datatug` and exploring; the surface-area docs are inside the TUI, not on the command line.

## Behavior

### Default launch

#### REQ: default-command

`datatug` with no subcommand MUST launch the TUI. This is wired in `apps/datatugapp/commands/command.go` via `DefaultCommand: "ui"` and MUST NOT regress.

### Initial screen selection

The TUI restores the last-viewed screen by reading `dtstate.CurrentScreenPath` on startup.

#### REQ: restore-last-screen

On launch, the TUI MUST read the persisted state via `pkg/dtstate.GetDatatugState()` and route the user to the screen named in `CurrentScreenPath`. Supported top-level paths are `viewers`, `settings`, `api_monitor`, and the default `projects`. Unknown values MUST fall back to `projects`.

#### REQ: state-load-failure-non-fatal

If reading persisted state fails, the TUI MUST log the error and continue to the `projects` screen. The user MUST NOT see a stack trace or an early exit.

### File-open shortcut

The `--file` flag opens a database file directly, bypassing the project selector.

#### REQ: file-flag

`--file <path>` (alias `-f`) MUST open the file in the appropriate viewer. The current implementation supports SQLite files only and dispatches via `dtio.IsSQLite`. Non-SQLite paths MUST cause the TUI to exit with a clear stderr message and a non-zero exit code (`2` — InvalidArgs).

### Opening a stored query result

#### REQ: result-flag

`datatug ui --result <id>` MUST open the unified workspace on a result written by `query run --result-id <id>` (see [query](../query/README.md)), as an immutable chat RecordSet: its rows come from `data.json`, its DTQL (or, for a saved query, the saved query's id and its text) and parameters from `meta.json`, and no query is run to open it. The project is the one `meta.json` names, found only as a project id of the user's project registry, at the absolute folder registered for it (never a folder of that name in the current directory, nor a relative or remote location), so the command works from any folder; the result appears as a new chat session of that project, under one line that says it was loaded from its files. Join candidates come from the source's schema metadata, as for any chat result; a later refresh, Related preview or JOIN is the person's explicit action through the existing secure executor. `--as`, `--role` and `--group` name the principal for a project with access policies. Opening a result leaves the saved last chat choices unchanged. A result whose `data.json` does not have the key column first and rows as long as its columns, or whose parameters are not each a scalar or a set, is incomplete. A missing, expired, incomplete or unknown-version result, and a project that is not registered on the machine, exit 3 with one line that names no path; a result whose database is not a source of a registered project, a malformed id and `--result` with `--file` exit 2.

### Terminal restoration on panic

#### REQ: restore-terminal-on-panic

A panic anywhere in the TUI MUST result in `global.App.Stop()` being called from the top-level recovery in `main.go`, so the terminal is returned to a usable state before the panic message and stack are printed to stderr. See [parent feature REQ: panic-recovery](../README.md#req-panic-recovery).

### Module registration

The TUI registers viewers (database, GCloud, AWS, Azure), settings, and the API service monitor at startup.

#### REQ: module-registration

`registerModules()` MUST be called exactly once per TUI launch, before `tui.App.Run()`. Adding a new viewer is done by appending a registration call; viewers MUST NOT register themselves implicitly via `init()` to keep boot order explicit.

## Parameters

| Flag | Type | Default | Description |
|---|---|---|---|
| `--file`, `-f` | string | `""` | Path to a database file to open directly. Currently SQLite-only. |
| `--result` | string | `""` | Id of a result of `query run --result-id` to open with its loaded rows. |
| `--as`, `--role`, `--group` | string | `""` | Principal for a project with access policies (with `--result`). |
| `--tui` (parent) | bool | `false` | Parent-level flag, reserved for forcing TUI mode from other commands. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | TUI exited cleanly |
| `2` | Invalid `--file` value (e.g., non-SQLite file); with `--result`, a malformed id or a result outside a registered project |
| `3` | With `--result`, the result is missing, expired, incomplete or of an unknown version, or its project is not registered |
| `10` | Unhandled panic (caught by top-level recovery) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. Inherits panic recovery and the default-command wiring. |
| [serve](../serve/README.md) | Independent. The TUI does not call `serve` and vice versa. |
| [init](../init/README.md) | A future TUI flow MAY invoke project scaffolding via the same logic as `datatug init`. Not in scope today. |

## Acceptance Criteria

### AC: bare-invocation-opens-tui

**Requirements:** ui#req:default-command

`datatug` with no arguments launches the TUI and lands on the projects screen (or the last-viewed screen if state exists).

### AC: file-flag-opens-sqlite

**Requirements:** ui#req:file-flag

`datatug ui --file ./demo.sqlite` opens the SQLite DB viewer pointed at that file. Exits `0` on clean shutdown.

### AC: corrupt-state-still-launches

**Requirements:** ui#req:state-load-failure-non-fatal

If `dtstate` cannot be read (missing file, corrupt YAML), the TUI still launches and shows the projects screen, with a non-fatal error logged.

### AC: result-opens-the-loaded-records (verifies REQ:result-flag)

**Given** a result written by `query run --db datatug://<project>/<db> --from Item --result-id t4`, after which the table's rows are deleted
**When** the user runs `datatug ui --result t4` from a folder that is not a project
**Then** the workspace opens on a session of that project holding the RecordSet with the two rows of the files and the result's DTQL, so no query ran; `datatug ui --result nope`, an expired result and an incomplete one exit 3, and a result of a database opened by path exits 2.

## Open Questions

- The `--file` flag is currently SQLite-only; the error path for non-SQLite files calls `panic` rather than returning an error. Should the implementation be updated to return a clean `InvalidArgs` exit per [cli REQ: no-log-fatal](../README.md#req-no-log-fatal)?
- The parent-level `--tui` flag exists but has no documented effect on non-`ui` subcommands. Should it be removed or pinned to a specific meaning?

---
*This document follows the https://specscore.md/feature-specification*
