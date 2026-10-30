---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Board

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/board?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/board?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/board?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/board?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug board list` lists the boards stored in a DataTug project: one ID per line, or with `--format json` one JSON document that also carries each board's title. It is read-only. It is the first verb of the `board` resource; authoring boards from the CLI is not part of this Feature.

## Synopsis

```
datatug board list [--project <id> | --directory <path>]
datatug board list [--project <id> | --directory <path>] --format json
```

## Problem

A project stores boards beside its queries, environments and entities, and the CLI has a listing for each of those and none for boards. A script, an agent skill or a tool that draws a project (the Claude Code mod of `datatug/ai-plugin` is the first) has to read the `boards` folder itself, and so becomes a second reader of a layout that has two forms on disk (a flat file and a folder for each board), free to disagree with what `serve` finds.

## Behavior

### Command shape

#### REQ: verb-subcommand

The resource is `board`, singular, and its action is the explicit verb `list` ([parent REQ: singular-resource-names](../README.md#req-singular-resource-names), [REQ: verb-subcommands](../README.md#req-verb-subcommands)). `datatug board` with no verb MUST show help and perform no action.

### Project resolution

#### REQ: requires-project-context

`board list` MUST resolve its project as the shared CLI conventions say ([REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution)): the folder of `--directory` (`--dir` is the same flag), or the registered project of `--project`, or else the current folder. `--project` and `--directory` together MUST exit `2`. A folder with no project file at its root, and a `--project` that names no registered project, MUST exit `3` with one sentence on stderr and nothing on stdout.

### What it lists

#### REQ: same-reader-as-serve

The boards MUST be read through the project store (`ProjectStore.LoadBoards`), the store through which `serve` reads the same files, so that both layouts of a board on disk are listed and what `board list` lists and what `serve` finds cannot disagree. A board that the store cannot load MUST exit `1` with a message on stderr that names the board, and nothing on stdout. This comes before the skipping of [REQ: one-id-per-line](#req-one-id-per-line): a board that cannot be loaded fails the listing whatever its ID.

#### REQ: read-only

`board list` MUST NOT write, create or change any file.

### Output

#### REQ: one-id-per-line

In the `text` format (the default) the command MUST print exactly one board ID per line on stdout, ordered by ID, so the output is stable. A board whose ID is not a plain name (letters, digits, `.`, `_` and `-`) is not printed, and one line on stderr says how many boards were skipped. (Mirrors [queries REQ: one-id-per-line](../queries/README.md#req-one-id-per-line).) Text, not YAML, is the default, as for `queries`.

#### REQ: json-format

`--format json` MUST print one JSON document on stdout: an array, in the order of the `text` format, of one object for each board the `text` format prints. An object has `id` and `title` (the board's, left out when it has none). Any other `--format` than `text` and `json` MUST exit `2`.

#### REQ: empty-project-prints-nothing

If the project has no board, the command MUST exit `0` and write nothing to stdout in the `text` format, and `[]` in the `json` format.

## Parameters

| Flag | Aliases | Type | Description |
|---|---|---|---|
| `--project` | `-p` | string | Registered project ID. |
| `--directory` | `-d`, `--dir` | string | Project directory path. |
| `--format` | none | string | `text` (default) or `json`. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Listing succeeded (or empty) |
| `2` | `--project` and `--directory` together; an unsupported `--format` |
| `3` | The folder is not a project; `--project` names no registered project |
| `1` | A board cannot be loaded, or a write to stdout failed |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [queries](../queries/README.md) | Same shape of listing: IDs in `text`, objects in `json`. |
| [show](../show/README.md) | `show` lists what a scan wrote and lists no board; `board list` lists boards and nothing a scan wrote. |
| [serve](../serve/README.md) | Reads the same boards through the same store. |

## Acceptance Criteria

### AC: lists-boards

**Requirements:** board#req:one-id-per-line, board#req:same-reader-as-serve, board#req:read-only

Against a project with one board stored as a folder and one stored as a flat file, `datatug board list -d <folder>` exits `0` and prints the two IDs, one per line, in ID order; a board with a non-plain ID is skipped and counted on stderr; two runs are byte-identical and no file of the project is changed.

### AC: json-lists-boards

**Requirements:** board#req:json-format, board#req:empty-project-prints-nothing

On the same project `datatug board list --format json` exits `0` and prints one JSON array in ID order whose objects carry `id`, and `title` only for a board that has one; a project with no board prints `[]` in JSON and nothing in text; `--format yaml` exits `2`.

### AC: not-a-project-is-not-found

**Requirements:** board#req:requires-project-context

`datatug board list` in a folder that is not a project exits `3` with one sentence on stderr and nothing on stdout; `--project` with `--directory` exits `2`; `--project` with a name that is not registered exits `3`.

### AC: unloadable-board-fails

**Requirements:** board#req:same-reader-as-serve

A project with a board file that is not JSON exits `1` with a message that names the board and nothing on stdout.

### AC: bare-resource-shows-help

**Requirements:** board#req:verb-subcommand

`datatug board` exits `0`, prints the help of the `board` resource naming `list`, and lists no board.

## Open Questions

- Should `board show <id>` (a board's cards and parameters) be the next verb, or do boards stay a Web UI concern?

---
*This document follows the https://specscore.md/feature-specification*
