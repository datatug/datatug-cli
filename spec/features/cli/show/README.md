# Feature: Show

> [View in SpecStudio](https://specstudio.synchestra.io/project/features?id=datatug-cli@datatug@github.com&path=spec%2Ffeatures%2Fcli%2Fshow) — graph, discussions, approvals

**Status:** Implementing

## Summary

`datatug show` prints a human-readable summary of a DataTug project: its environments, DB servers, databases, schemas, tables, views, columns, primary keys, foreign keys, and reverse references. Intended for quick inspection in a terminal — not for machine consumption.

## Synopsis

```
datatug show --project <id>
datatug show --dir <path>
datatug show
```

## Problem

A DataTug project is a tree of YAML/JSON files on disk. Reading it directly is tedious. Users want a one-shot dump that shows what the project contains — what environments, what databases, what tables — without launching the TUI or the Web UI.

`show` is the simplest possible answer: load the project, walk the tree, print indented prose with a few emoji marker characters for visual scanning.

## Behavior

### Project resolution

#### REQ: requires-project-context

`show` MUST resolve a project context via the shared CLI conventions ([REQ: project-or-dir-resolution](../README.md#req-project-or-dir-resolution)). If no project context is found, the command MUST exit `3` (NotFound).

### Output

#### REQ: human-readable-text

The output MUST be plain text intended for terminals. The current format uses indentation and emoji markers (🌎 🛢️ 📄 🔑 🔗 📎). This format is NOT considered machine-readable and consumers MUST NOT parse it. A `--format json|yaml` mode is tracked under Outstanding Questions.

#### REQ: stable-section-order

Within a single project, the printed order MUST be deterministic: project header, then environments (in project order), then DB drivers and their servers/catalogs (in project order). This makes diffing two `show` outputs meaningful.

#### REQ: writes-to-stdout

All `show` output MUST go to stdout. Progress and informational logs MAY go to stderr. The current implementation uses `fmt.Fprintln(os.Stdout, ...)` and is correct on this axis.

### Coverage

#### REQ: covers-known-elements

The output MUST include, for each environment: environment ID, DB server IDs, and database IDs. For each table or view: schema and name, primary key (name + columns), foreign keys (column lists, target table, FK name), reverse references, and columns (name + DB type, with `(N)` for character-length-bound types except `text`).

This pins the current implementation as the contract.

## Parameters

| Flag | Aliases | Type | Description |
|---|---|---|---|
| `--project` | `-p` | string | Project ID from the registry. |
| `--dir` | `-d` | string | Project directory path. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Project loaded and printed successfully |
| `3` | Project directory or `--project` ID not found |
| `1` | Generic runtime error (load failure, IO error) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [init](../init/README.md) | A freshly-init'd project MUST be printable by `show` (even if mostly empty). |
| [scan](../scan/README.md) | `show` reflects the latest scanned schema. |
| [validate](../validate/README.md) | `show` does NOT validate — invalid projects may print partial output. |

## Acceptance Criteria

### AC: prints-known-project

**Requirements:** show#req:requires-project-context, show#req:covers-known-elements

`datatug show --dir ./demo` against the bundled demo project exits `0` and writes at least the project path, one environment, one DB server, and one table to stdout.

### AC: missing-project-is-not-found

**Requirements:** show#req:requires-project-context

`datatug show --dir ./does-not-exist` exits `3` with a stderr message naming the missing project.

### AC: deterministic-order

**Requirements:** show#req:stable-section-order

Two back-to-back `datatug show` invocations against the same project produce byte-identical stdout.

## Outstanding Questions

- Should `show` gain a `--format yaml|json` mode for machine consumption, satisfying [parent REQ: yaml-default-for-structured](../README.md#req-yaml-default-for-structured)?
- The current output is dense and uses emoji. Should there be a `--no-emoji` flag for terminals or pipelines that mishandle them?
- Should `show` support a `--depth` flag (e.g., environments only, environments + servers, full tree)?
- The leading line `GetProjectStore: <path>` looks like a debug leak (uses the method name as a label). Should this be retitled `Project: <path>` in the same release as this spec?

---
*This document follows the https://specscore.md/feature-specification*
