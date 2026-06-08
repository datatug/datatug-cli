---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Datasets

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/datasets?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/datasets?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/datasets?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/datasets?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug datasets` lists every dataset (recordset) defined in the resolved DataTug project. Output is one dataset ID per line on stdout.

## Synopsis

```
datatug datasets [--project <id> | --dir <path>]
```

## Problem

Users need a scriptable way to enumerate the datasets in a project so they can pipe IDs into [`dataset-def`](../dataset/def/README.md) or [`dataset-data`](../dataset/data/README.md). The TUI shows the same list interactively; this command is the CLI equivalent.

## Behavior

### Project resolution

#### REQ: requires-project-context

`datasets` MUST resolve a project context via the shared CLI conventions ([REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution)). If none is found, the command MUST exit `3`.

### Output

#### REQ: one-id-per-line

The command MUST print exactly one dataset ID per line on stdout. Order MUST match the order returned by `store.LoadRecordsetDefinitions(ctx)` — no implicit sorting.

#### REQ: empty-project-prints-nothing

If the project contains zero datasets, the command MUST exit `0` and write nothing to stdout. Empty output is the correct signal of an empty set.

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
| `1` | Generic runtime error (load failure) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [dataset](../dataset/README.md) | Once renamed, this command should become `datatug dataset list`. |
| [dataset def](../dataset/def/README.md) | Consumes IDs from this list. |
| [dataset data](../dataset/data/README.md) | Consumes IDs / file paths derived from datasets in this list. |

## Acceptance Criteria

### AC: lists-known-datasets

**Requirements:** datasets#req:one-id-per-line

Against the demo project with N datasets, `datatug datasets --dir ./demo` exits `0` and prints exactly N lines on stdout.

### AC: empty-project-zero-lines

**Requirements:** datasets#req:empty-project-prints-nothing

A project with no datasets produces zero stdout lines and exits `0`.

## Open Questions

- Should this command be renamed to `dataset list` per [parent REQ: singular-resource-names](../README.md#req-singular-resource-names) and [REQ: verb-subcommands](../README.md#req-verb-subcommands)?
- Should there be a `--format yaml|json` mode that emits structured output (id + name + column count, for example)? Today it is plain IDs only.
- Should the list be sorted alphabetically by default, with `--no-sort` to preserve insertion order? Users typing one line per dataset usually want stable, sorted output.

---
*This document follows the https://specscore.md/feature-specification*
