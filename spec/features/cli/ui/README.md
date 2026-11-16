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
datatug ui --deep-link <datatug.app address>
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

`datatug ui --result <id>` MUST open the unified workspace on a result written by `query run --result-id <id>` (see [query](../query/README.md)), as an immutable chat RecordSet: its rows come from `data.json`, its TugQL (or, for a saved query, the saved query's id and its text) and parameters from `meta.json`, and no query is run to open it. The project is the one `meta.json` names, found only as a project id of the user's project registry, at the absolute folder registered for it (never a folder of that name in the current directory, nor a relative or remote location), so the command works from any folder; the result appears as a new chat session of that project, under one line that says it was loaded from its files. Join candidates come from the source's schema metadata, as for any chat result; a later refresh, Related preview or JOIN is the person's explicit action through the existing secure executor. `--as`, `--role` and `--group` name the principal for a project with access policies. Opening a result leaves the saved last chat choices unchanged. A result whose `data.json` does not have the key column first and rows as long as its columns, or whose parameters are not each a scalar or a set, is incomplete. A missing, expired, incomplete or unknown-version result, and a project that is not registered on the machine, exit 3 with one line that names no path; a result whose database is not a source of a registered project, a malformed id and `--result` with `--file` exit 2.

### Opening a page of the web app

The web app at `https://datatug.app` shows the command for the page the user is on; pasted into a terminal, it opens the same project and screen in the TUI. The address is a named flag, not a positional argument (founder, 2026-11-04): `datatug ui` takes no positional argument.

#### REQ: deep-link-flag

`datatug ui --deep-link <address>` MUST accept an address of the web app, `https://datatug.app/…` or, for a development build of the web app, `http://localhost:<port>/…` (also `127.0.0.1` and `[::1]`), read in the shapes of the web app's project-address library (`datatug/datatug-apps` `libs/datatug/project-address`): `/project/github.com/<owner>/<repo>[/<dir>…][/-/<page>…][?ref=<ref>]`, its legacy `…/tree/<ref>/…` and `…/blob/<ref>/…/datatug-project.json` forms, `/store/<storeId>/project/<projectId>[/<page>…]` and `/space/<spaceId>/store/firestore/project/<projectId>[/<page>…]`. Any other host or scheme, an address with credentials, and a malformed address MUST exit `2` before the TUI starts, with one line that names the expected form and never repeats the address. `--deep-link` with `--file` or `--result`, and any positional argument, MUST exit `2`.

#### REQ: deep-link-targets

An address that names no project (`/`, `/home`, …) MUST start the TUI as a bare `datatug ui` does. Every project address MUST start on the Projects screen and then:

- a GitHub project MUST open from `~/datatug/github.com/<owner>/<repo>[/<dir>]`, cloning the repository there first when it is missing; a project that is not in the settings yet MUST be added to them once it loads (the demo project, which is never added, opens as the demo project). The terminal opens the checked-out branch: a `?ref=` names a ref the TUI cannot select yet, so the TUI says so after opening the project;
- a project of the local `datatug serve` agent (`http-localhost:<port>`, `localhost:<port>`, `http-127.0.0.1:<port>`) MUST open the project of the settings whose id is the address's project id, or say that no project of that id is registered;
- a DataTug Cloud project of a Space (`/space/<spaceId>/store/firestore/project/<projectId>`) MUST open as [cloud-projects](../cloud-projects/README.md) specifies: sign-in and the first read happen before the TUI starts, and failures exit with that feature's codes; a personal DataTug Cloud project (`/store/firestore/project/<projectId>`) MUST exit `1` with that feature's personal-project line; a project stored in the browser (`local`) MUST show "This project is stored in your browser; export it or push it to GitHub to open it here"; a project of any other store MUST say its store cannot be opened yet. Each kind of project has one resolver.

The page of the address MUST open its section of the project: `queries`, `query/<id>` → Queries; `boards`, `board/<id>` → Dashboards; `database/…`, `browser-db/…` → Databases; `environments`, `env/<id>` → Environments; none, `overview`, `project` → the project. Any other page MUST open the project and say that the page has no terminal screen yet.

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
| `--deep-link` | string | `""` | Address of a page of the web app (`https://datatug.app/…`) to open. |
| `--no-sign-in`, `--insecure-storage`, `--auth-host`, `--api-url` | bool/string | | With `--deep-link` to a DataTug Cloud project; see [cloud-projects](../cloud-projects/README.md). |
| `--tui` (parent) | bool | `false` | Parent-level flag, reserved for forcing TUI mode from other commands. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | TUI exited cleanly |
| `2` | Invalid `--file` value (e.g., non-SQLite file); with `--result`, a malformed id or a result outside a registered project; an address that `--deep-link` does not accept, `--deep-link` with `--file` or `--result`, or a positional argument |
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
**Then** the workspace opens on a session of that project holding the RecordSet with the two rows of the files and the result's TugQL, so no query ran; `datatug ui --result nope`, an expired result and an incomplete one exit 3, and a result of a database opened by path exits 2.

### AC: deep-link-opens-the-page (verifies REQ:deep-link-flag, REQ:deep-link-targets)

**Given** the repository `acme/repo` cloned at `~/datatug/github.com/acme/repo` with a project in `some/dir`
**When** the user runs `datatug ui --deep-link https://datatug.app/project/github.com/acme/repo/some/dir/-/queries`
**Then** the TUI opens that project on its Queries section and adds it to the settings; the same command for a repository that is not cloned yet clones it first; a `--deep-link` to a DataTug Cloud project (`/store/firestore/project/p1`) opens the Projects screen with the DataTug Cloud notice; `--deep-link https://example.com/x`, an address with credentials, `--deep-link <address> --file x.db` and a positional `datatug ui <address>` exit 2 without starting the TUI.

## Open Questions

- The `--file` flag is currently SQLite-only; the error path for non-SQLite files calls `panic` rather than returning an error. Should the implementation be updated to return a clean `InvalidArgs` exit per [cli REQ: no-log-fatal](../README.md#req-no-log-fatal)?
- The parent-level `--tui` flag exists but has no documented effect on non-`ui` subcommands. Should it be removed or pinned to a specific meaning?

---
*This document follows the https://specscore.md/feature-specification*
