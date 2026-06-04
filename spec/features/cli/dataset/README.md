# Feature: Dataset

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/dataset?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/dataset?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/dataset?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/dataset?op=request-change) |

**Status:** Implementing

## Summary

`datatug dataset` (alias `ds`) is the parent for dataset (recordset) inspection commands. Today it has no useful default behavior — the bare `datatug dataset` invocation prints nothing and exits `0`. The real work happens in its current sibling commands [`dataset-def`](def/README.md) and [`dataset-data`](data/README.md), which this feature specifies as subcommand contracts. The sibling top-level command names are tracked for migration to `dataset def` / `dataset data` under Outstanding Questions.

## Synopsis

```
datatug dataset --project <id>
datatug dataset --dir <path>
datatug ds ...
```

## Contents

| Directory | Description |
|---|---|
| [def/](def/README.md) | Output dataset definition (schema) |
| [data/](data/README.md) | Output dataset row data |

## Problem

Datasets (recordsets) are first-class artifacts inside a DataTug project — they encode example query results, fixture data, and reference tables. Users need:

- A way to list datasets in a project ([`datasets`](../datasets/README.md), specified separately for now).
- A way to print a single dataset's column definition.
- A way to print a single dataset's row data in a chosen format.

The current `datatug dataset` parent command exists but its action does nothing observable beyond initializing the project context. This spec documents the intended future surface and the current real subcommands.

## Behavior

### Bare invocation

#### REQ: bare-invocation-shows-help

`datatug dataset` with no further arguments SHOULD print help and exit `0`. The current implementation silently exits `0`, which violates [parent REQ: verb-subcommands](../README.md#req-verb-subcommands). Remediation tracked under Outstanding Questions.

### Alias

#### REQ: ds-alias

`datatug ds` MUST be accepted as a short alias for `datatug dataset`, matching the current registration `Aliases: []string{"ds"}`.

### Shared flag

#### REQ: dataset-flag

Subcommands that operate on a specific dataset MUST accept `--dataset <id>` to identify it. This is encoded today in `datasetBaseCommand`. The flag MUST be required where a single dataset must be targeted (e.g., [`dataset-def`](def/README.md), [`dataset-data`](data/README.md)).

## Parameters

| Flag | Aliases | Type | Description |
|---|---|---|---|
| `--dataset` |  | string | Dataset (recordset) ID. Required on subcommands that operate on a single dataset. |

(Inherits `--project`/`--dir` from the shared CLI conventions.)

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Help printed (bare invocation) — see Outstanding Questions |
| `3` | Project not resolved |
| `2` | Missing `--dataset` on subcommands that require it |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [datasets](../datasets/README.md) | Lists what `dataset` operates on. `datasets` should eventually become `dataset list`. |
| [`dataset def`](def/README.md) | Specified as a child here; today the command is the separately-registered top-level `datatug dataset-def`. |
| [`dataset data`](data/README.md) | Specified as a child here; today the command is the separately-registered top-level `datatug dataset-data`. |

## Acceptance Criteria

### AC: alias-equivalence

**Requirements:** dataset#req:ds-alias

`datatug ds <any-subcommand>` and `datatug dataset <any-subcommand>` MUST behave identically.

### AC: bare-shows-help

**Requirements:** dataset#req:bare-invocation-shows-help

`datatug dataset` (no further args) prints help and exits `0`. (Not yet met — see Outstanding Questions.)

## Open Questions

- The current code registers `dataset-def` and `dataset-data` as top-level commands (`datatug dataset-def`, `datatug dataset-data`) instead of subcommands of `dataset`. Should this spec require collapsing them to `datatug dataset def` / `datatug dataset data`? Doing so is a breaking change.
- The bare `datatug dataset` action currently calls `initProjectCommand` and then returns nil with a `TODO`. Should this be replaced with a help dump now, or left for the migration mentioned above?
- Should `dataset list` (currently the standalone [`datasets`](../datasets/README.md)) move under this parent in the same migration?

---
*This document follows the https://specscore.md/feature-specification*
