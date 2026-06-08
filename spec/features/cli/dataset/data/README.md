---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Dataset Data

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/dataset/data?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/dataset/data?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/dataset/data?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/dataset/data?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug dataset-data` (current name) — to become `datatug dataset data` — prints the row data of a single dataset from a DataTug project. Supports YAML, JSON, and an interactive GRID display (the GRID format draws a table in the terminal using `tview`).

## Synopsis

```
datatug dataset-data --file <path> [--format yaml|json|grid] [--indent <spaces|TAB>]
```

## Problem

Datasets store row data in files inside the project. Users need to:

- View the rows quickly in the terminal (GRID).
- Pipe them to a script for processing (YAML / JSON).
- Control indentation when piping to a downstream tool that cares.

## Behavior

### Required file flag

#### REQ: requires-file

`--file <path>` MUST be required. It identifies the dataset data file inside the project (the file is loaded via `store.LoadRecordsetData(ctx, file)`). The CLI MUST exit `2` if it is omitted.

The flag is named `--file` (not `--dataset`) because dataset data is stored per-file and a single dataset may span multiple files.

### Format selection

#### REQ: default-format-yaml

When `--format` is unset, the format MUST default to YAML. This matches the current implementation's `if v.Format == "" { v.Format = "yaml" }`.

#### REQ: supported-formats

`--format` MUST accept exactly `yaml`, `json`, and `grid` (case-insensitive). Any other value MUST cause exit `2` (InvalidArgs) with a message listing the supported values.

#### REQ: grid-is-interactive

`--format grid` MUST open an interactive terminal grid (via the implementation's `showRecordsetInGrid`) showing the rows. This format is NOT pipe-safe and MUST NOT be used in scripts.

### Indentation

#### REQ: indent-flag

`--indent` MUST accept a digit string (number of spaces) or the literal `TAB`. The behavior MUST be:

- YAML: a single digit sets `yamlEncoder.SetIndent(<n>)`. `TAB` MUST be treated as 4 spaces (YAML cannot encode literal tabs).
- JSON: a digit sets the JSON indent to `<n>` spaces. `TAB` sets it to a literal tab character. When unset, the JSON indent MUST default to a single space.

The current implementation has a known bug: it parses the digit as a count of *characters* but reuses `v.Indent` as the string. The targeted behavior pinned here is the spec; deviations are tracked under Outstanding Questions.

### Output

#### REQ: rows-as-list-of-maps

YAML and JSON output MUST be encoded as a list of objects, where each object maps column name → cell value. This matches the current `writeRows` implementation.

## Parameters

| Flag | Type | Required | Description |
|---|---|---|---|
| `--file` | string | yes | Path (within the project) to the dataset data file. |
| `--format` | string | no | Output format: `yaml` (default), `json`, or `grid`. |
| `--indent` | string | no | Indentation. A digit (e.g., `2`) or the literal `TAB`. |
| `--dataset` | string | no | Inherited from the parent `dataset` command's flag set. Not currently consumed by `data`. |
| `--project` / `--dir` |  | (one of) | Project context. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Data printed (or GRID closed) |
| `2` | Missing `--file`, or `--format` not in {yaml, json, grid} |
| `3` | Project or file not found |
| `1` | Generic runtime error |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [dataset](../README.md) | Parent feature. |
| [dataset def](../def/README.md) | Sibling. Use `def` for column metadata, `data` for row values. |

## Acceptance Criteria

### AC: prints-yaml-by-default

**Requirements:** dataset/data#req:default-format-yaml, dataset/data#req:rows-as-list-of-maps

`datatug dataset-data --dir ./demo --file employees/data.json` (no `--format`) writes a YAML list-of-maps to stdout.

### AC: json-output

**Requirements:** dataset/data#req:supported-formats, dataset/data#req:rows-as-list-of-maps

`--format json` produces a JSON array of objects.

### AC: unsupported-format-rejected

**Requirements:** dataset/data#req:supported-formats

`--format xml` exits `2` with a stderr message listing `yaml`, `json`, `grid`.

### AC: grid-opens-tui

**Requirements:** dataset/data#req:grid-is-interactive

`--format grid` opens an interactive table the user can close to return to the shell.

## Open Questions

- The indent parsing logic in the current implementation is buggy (compares `strconv.Atoi` error wrong, mixes `v.Indent` as both digit and spaces). Should the spec define a stricter `--indent` grammar (e.g., `"0"`, `"2"`, `"4"`, `"TAB"`) and let the implementation be rewritten cleanly?
- `--dataset` is inherited from the parent base struct but is not consumed by `data`. Should it be removed from this command's flag set, or used to disambiguate when `--file` is relative?
- Should `grid` mode be removed in favor of a dedicated TUI viewer, given it does not compose with pipes and confuses scripting users?
- Command name MUST eventually be `datatug dataset data`. When does that rename land?

---
*This document follows the https://specscore.md/feature-specification*
