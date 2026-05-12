# Feature: Dataset Def

> [View in SpecStudio](https://specstudio.synchestra.io/project/features?id=datatug-cli@datatug@github.com&path=spec%2Ffeatures%2Fcli%2Fdataset%2Fdef) — graph, discussions, approvals

**Status:** Implementing

## Summary

`datatug dataset-def` (current name) — to become `datatug dataset def` — prints the definition (column metadata) of a single dataset from a DataTug project as YAML to stdout.

## Synopsis

```
datatug dataset-def --dataset <id> [--project <id> | --dir <path>]
```

## Problem

A dataset's *definition* (its column list, types, constraints) is distinct from its *data* (the row values). Both live in the same dataset artifact on disk but are useful separately. The definition is what a downstream consumer needs to understand the shape of the data before reading it. A small dedicated command makes that lookup scriptable.

## Behavior

### Required dataset selection

#### REQ: requires-dataset

`--dataset <id>` MUST be required. The CLI MUST exit `2` (InvalidArgs) if not supplied.

### Output

#### REQ: yaml-to-stdout

The dataset definition MUST be encoded as YAML and written to stdout. The current implementation uses `yaml.NewEncoder(os.Stdout)`. Output MUST end with a single trailing newline as produced by the encoder.

#### REQ: id-included-in-output

The encoded definition MUST include the dataset's `ID` field set to the value of `--dataset`. The current implementation explicitly assigns `dataset.ID = v.Dataset` before encoding.

### Loading

#### REQ: uses-store-loader

The dataset definition MUST be loaded via `store.LoadRecordsetDefinition(ctx, id)` on the project store. Direct file reads bypassing the store are NOT permitted.

## Parameters

| Flag | Type | Required | Description |
|---|---|---|---|
| `--dataset` | string | yes | Dataset ID to load. |
| `--project` / `--dir` |  | (one of) | Project context. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Definition printed |
| `2` | Missing `--dataset` flag |
| `3` | Project not found, or dataset not found within project |
| `1` | Generic runtime error |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [dataset](../README.md) | Parent feature. |
| [dataset data](../data/README.md) | Sibling. `data` reads a file path; `def` reads by dataset ID. They are complementary. |
| [datasets](../../datasets/README.md) | Provides the list of valid IDs. |

## Acceptance Criteria

### AC: prints-known-dataset

**Requirements:** dataset/def#req:yaml-to-stdout, dataset/def#req:uses-store-loader

`datatug dataset-def --dir ./demo --dataset employees` exits `0` and writes a YAML document with at least an `id: employees` key to stdout.

### AC: missing-dataset-flag-rejected

**Requirements:** dataset/def#req:requires-dataset

`datatug dataset-def --dir ./demo` (no `--dataset`) exits `2`.

### AC: unknown-dataset-not-found

**Requirements:** dataset/def#req:uses-store-loader

`datatug dataset-def --dir ./demo --dataset does-not-exist` exits `3`.

## Outstanding Questions

- Command name MUST eventually be `datatug dataset def`. When does that rename land?
- Should there be a `--format json` mode in addition to YAML? Easy to add — just swap the encoder.
- Should the printed `id` line be omitted when the on-disk definition already contains it, to avoid the current implementation's explicit override?

---
*This document follows the https://specscore.md/feature-specification*
