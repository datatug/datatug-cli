# Feature: DB

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/db?op=request-change) |

**Status:** Planned

## Summary

`datatug db <url>` opens a database viewer for an ad-hoc database URL. Unlike [`scan`](../scan/README.md), this command is not tied to a DataTug project — it is the "open this database in a viewer right now" path. Today the command parses the URL and prints `"Opening database at <url>"` without actually opening a viewer; this spec pins the intended contract.

## Synopsis

```
datatug db <url>
```

## Problem

Users often want to look at a database that has nothing to do with a DataTug project — a one-off SQLite file, a connection string for production, a colleague's URL. Requiring them to `init` a project first is overkill. `datatug db` is the lightweight escape hatch: parse a database URL, open a viewer, no project required.

## Behavior

### URL parsing

#### REQ: parses-dburl

The positional argument MUST be parsed by `github.com/xo/dburl`. Supported schemes (sqlite, mssql, etc.) MUST match the drivers linked into the CLI binary.

#### REQ: missing-url-fails

If no URL is supplied, the CLI MUST exit `2` (InvalidArgs) with a usage message. The current implementation prints `"db url parse error: ..."` on `Args().First() == ""` because `dburl.Parse` rejects the empty string — this is acceptable but the message MUST clearly name the missing argument.

#### REQ: invalid-url-fails

If the URL parses but is not connectable (unknown scheme, malformed credentials), the CLI MUST exit `2` (InvalidArgs). If the URL parses cleanly but the server is unreachable, the CLI MUST exit `4` (connection failure). The current implementation conflates both into a stdout print without exiting — this is a known bug.

### Viewer launch

#### REQ: opens-viewer

On a successful parse and connect, the CLI MUST launch the appropriate viewer — either inline (TUI-style, blocking the terminal until closed) or by handing off to [`ui`](../ui/README.md). The current implementation does not launch any viewer; this is the work this spec authorizes.

#### REQ: no-write-by-default

The viewer MUST connect read-only by default, mirroring [scan REQ: connection-string-construction](../scan/README.md#req-connection-string-construction). A future `--write` flag MAY relax this; the default MUST remain read-only.

### No project context

#### REQ: project-independent

`datatug db` MUST NOT require, resolve, or modify any DataTug project. It MUST work in any directory, regardless of `~/.datatug.yaml`.

## Parameters

| Position | Type | Required | Description |
|---|---|---|---|
| 1 | string | yes | Database URL parseable by `github.com/xo/dburl` (e.g., `sqlite3://./demo.sqlite`, `mssql://user:pw@host/db`). |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Viewer opened and later closed by the user |
| `2` | Missing or unparseable URL |
| `4` | Could not connect to the database |
| `1` | Generic runtime error |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [ui](../ui/README.md) | The viewer this command launches SHOULD be the same DB viewer the TUI uses. |
| [scan](../scan/README.md) | Independent. `scan` updates a project; `db` only opens a viewer. |

## Acceptance Criteria

### AC: missing-url-rejected

**Requirements:** db#req:missing-url-fails

`datatug db` with no positional argument exits `2` with a usage message naming the URL.

### AC: opens-sqlite-viewer

**Requirements:** db#req:parses-dburl, db#req:opens-viewer

`datatug db sqlite3://./demo.sqlite` launches the SQLite viewer and exits `0` when the user closes it. (Not yet met — current implementation only prints.)

### AC: read-only-by-default

**Requirements:** db#req:no-write-by-default

The connection opened by `datatug db` MUST be read-only. Verifiable by attempting an `UPDATE` from inside the viewer (where supported by the viewer's UI) and expecting it to fail.

## Subcommands

| Subcommand | Status | Description |
|---|---|---|
| [`db copy`](copy/README.md) | Approved | `datatug db copy --from <url> --to <url>` — cross-engine database copy primitive. |

## Open Questions

- Should the viewer launched here be the same component as the TUI's DB viewer, or a separate "ad-hoc" mode with different UX?
- Should `datatug db` accept a positional dataset (e.g., `datatug db sqlite3://./demo.sqlite employees`) and open directly into a single table?
- Should this command eventually be renamed `datatug open <url>` for consistency with the verb-subcommand convention? Today `db` is a noun used as a verb.

---
*This document follows the https://specscore.md/feature-specification*
