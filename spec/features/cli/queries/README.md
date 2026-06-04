# Feature: Queries

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/queries?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/queries?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/queries?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/queries?op=request-change) |

**Status:** Planned

## Summary

`datatug queries` lists the named queries stored in a DataTug project (and, in future, manages them — create, rename, delete). Today the command is a placeholder — its action `panic`s with `"not implemented"`. This spec pins the intended contract so the placeholder can be replaced with a real implementation without re-inventing the surface.

## Synopsis

```
datatug queries [--project <id> | --dir <path>]
```

## Problem

DataTug projects store reusable parameterized queries as first-class artifacts (alongside datasets, schemas, environments). Users need:

- A scriptable enumeration of queries in a project.
- A future path to `new`, `rename`, `delete` queries from the CLI.

The current placeholder is a footgun (running `datatug queries` crashes the binary). A spec'd placeholder lets the team prioritize implementation against a known contract.

## Behavior

### Project resolution

#### REQ: requires-project-context

`queries` MUST resolve a project context via the shared CLI conventions ([REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution)). If none is found, the command MUST exit `3`.

### Output

#### REQ: one-id-per-line

The command MUST print exactly one query ID per line on stdout. Order MUST match the order the project store returns. (Mirrors [datasets REQ: one-id-per-line](../datasets/README.md#req-one-id-per-line) for consistency.)

#### REQ: empty-project-prints-nothing

If the project contains zero named queries, the command MUST exit `0` and write nothing to stdout.

### Placeholder behavior

#### REQ: no-panic

The command MUST NOT call `panic`. The current implementation does — fixing it is the first step of making this feature `Implementing`.

## Parameters

| Flag | Aliases | Type | Description |
|---|---|---|---|
| `--project` | `-p` | string | Project ID. |
| `--dir` | `-d` | string | Project directory. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Listing succeeded (or empty) |
| `3` | Project not resolved |
| `1` | Generic runtime error |

(Until implemented, the current command crashes with code `10` via the panic-recovery path.)

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [execute](../execute/README.md) | Future: a `queries run <id>` subcommand could replace some [`execute`](../execute/README.md) invocations. |
| [dataset](../dataset/README.md) | Independent — datasets are stored data; queries are stored SQL. |

## Acceptance Criteria

### AC: lists-known-queries

**Requirements:** queries#req:one-id-per-line

Against a project with N queries, `datatug queries` exits `0` and prints exactly N lines. (Not yet met — current implementation panics.)

### AC: no-panic

**Requirements:** queries#req:no-panic

`datatug queries` does NOT panic. (Not yet met.)

## Open Questions

- Should this be renamed `datatug query list` per [parent REQ: singular-resource-names](../README.md#req-singular-resource-names) and [REQ: verb-subcommands](../README.md#req-verb-subcommands)?
- Which sub-commands belong in the same release as the rename? Candidates: `new`, `rm`, `rename`, `run`.
- Should `queries run <id>` execute the query and stream rows, or should that go through [`execute`](../execute/README.md)?

---
*This document follows the https://specscore.md/feature-specification*
