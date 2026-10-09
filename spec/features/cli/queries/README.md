---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Queries

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/queries?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/queries?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/queries?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/queries?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug queries` lists the named queries stored in a DataTug project (and, in future, manages them — create, rename, delete). The listing is implemented, as one ID per line or, with `--format json`, as one JSON document that also carries each query's title and type; creating, renaming and deleting queries from the CLI is not. The command used to be a placeholder whose action `panic`ked with `"not implemented"`.

## Synopsis

```
datatug queries [--project <id> | --dir <path>]
datatug queries [--project <id> | --dir <path>] --format json
```

## Problem

DataTug projects store reusable parameterized queries as first-class artifacts (alongside datasets, schemas, environments). Users need:

- A scriptable enumeration of queries in a project.
- A future path to `new`, `rename`, `delete` queries from the CLI.

Running `datatug queries` used to crash the binary; it now lists the queries, or exits `3` outside a project.

## Behavior

### Project resolution

#### REQ: requires-project-context

`queries` MUST resolve a project context via the shared CLI conventions ([REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution)). If none is found, the command MUST exit `3`.

### Output

#### REQ: one-id-per-line

In the `text` format (the default) the command MUST print exactly one query ID per line on stdout. Order is by ID, so the output is stable. A query whose ID is not made of plain names (letters, digits, `.`, `_` and `-`, with `/` between folders) is not printed: stdout holds only IDs that `query run` can address, and one line on stderr says how many queries were skipped. (Mirrors [datasets REQ: one-id-per-line](../datasets/README.md#req-one-id-per-line) for consistency.)

#### REQ: empty-project-prints-nothing

If the project contains zero named queries, the command MUST exit `0` and write nothing to stdout in the `text` format, and `[]` in the `json` format.

#### REQ: json-format

`--format json` MUST print one JSON document on stdout: an array, in the order of the `text` format, of one object for each query the `text` format prints. An object has `id` (the ID the `text` format prints), `title` and `type` (those of the query's file, each left out when the file holds none), and `parameters` (left out when the file declares none): an array, in the file's order, of one object for each declared parameter that has an `id`, with `id`, `type` (left out when the file holds none, or one that is not a string) and `required` (`true` only when the file's `isRequired` is the JSON value `true`, left out otherwise). A parameter entry that is not an object, or whose `id` is not a string or is empty, is left out; entries with the same `id` are all listed, as the file holds them. Key names are matched as Go's JSON decoder matches them (without regard to case), as for `title` and `type`. A query whose ID is not made of plain names is left out and counted on stderr, as in the `text` format. A query whose file cannot be read, is not a regular file (a symbolic link is not followed), or does not hold a JSON object is listed with its `id` only, and one line on stderr says how many could not be read; it is no failure. The listing does not validate a query as the project store does. Any other `--format` than `text` and `json` MUST exit `2`. The `text` format MUST NOT change: it reads no query file.

### Placeholder behavior

#### REQ: no-panic

The command MUST NOT call `panic`: not in an empty folder, not in a folder that is not a project and not in a project with no query. (A panic is also a telemetry event.)

## Parameters

| Flag | Aliases | Type | Description |
|---|---|---|---|
| `--project` | `-p` | string | Project ID. |
| `--dir` | `-d` | string | Project directory. |
| `--format` | none | string | `text` (default) or `json`. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Listing succeeded (or empty) |
| `2` | `--project` and `--dir` both given; an unsupported `--format` |
| `3` | Project not resolved |
| `1` | Generic runtime error, or a write to stdout failed |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [execute](../execute/README.md) | Future: a `queries run <id>` subcommand could replace some [`execute`](../execute/README.md) invocations. |
| [dataset](../dataset/README.md) | Independent — datasets are stored data; queries are stored SQL. |

## Acceptance Criteria

### AC: lists-known-queries

**Requirements:** queries#req:one-id-per-line

Against a project with N queries with plain IDs, `datatug queries` exits `0` and prints exactly N lines; a query with a non-plain ID is skipped and counted on stderr.

### AC: json-lists-queries

**Requirements:** queries#req:json-format, queries#req:empty-project-prints-nothing

Against a project with queries in folders, one with a title and a type and one whose file holds neither, `datatug queries --format json` exits `0` and prints one JSON array in ID order whose objects carry `id`, and `title` and `type` only where the file holds them; a query with a non-plain ID is left out and counted on stderr; a query file that is not JSON is listed with its `id` only and counted on stderr; so is a query file that is a symbolic link, whose target is not read; a project with no query prints `[]`; `--format yaml` exits `2`; and `datatug queries` with no `--format` prints what it printed before.

### AC: json-lists-parameters

**Requirements:** queries#req:json-format

Against a query whose file declares a required integer parameter and an optional one with no type, `datatug queries --format json` prints for it `parameters` of `{"id", "type", "required": true}` and `{"id"}` in the file's order; a query with no parameters has no `parameters` key; a malformed parameter entry (not an object, or with an `id` that is not a string or is empty) is left out and the query is still listed with its title; an `isRequired` that is not the JSON `true` gives no `required`; and the `text` format is unchanged.

### AC: no-panic

**Requirements:** queries#req:no-panic

`datatug queries` does NOT panic, in an empty folder, in a folder that is not a project and in a project with no query.

## Open Questions

- Should this be renamed `datatug query list` per [parent REQ: singular-resource-names](../README.md#req-singular-resource-names) and [REQ: verb-subcommands](../README.md#req-verb-subcommands)?
- Which sub-commands belong in the same release as the rename? Candidates: `new`, `rm`, `rename`, `run`.
- Should `queries run <id>` execute the query and stream rows, or should that go through [`execute`](../execute/README.md)?

---
*This document follows the https://specscore.md/feature-specification*
