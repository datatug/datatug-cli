---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Projects

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/projects?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/projects?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/projects?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/projects?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug projects` lists DataTug projects registered in the user-level config (`~/.datatug.yaml`). It hosts the [`add`](add/README.md) subcommand for registering an existing on-disk project.

## Synopsis

```
datatug projects [--fields <csv>] [--all]
datatug projects add ...
```

## Problem

The CLI addresses projects by ID, not path. The ID-to-path mapping lives in `~/.datatug.yaml`. Users need a way to:

- See which IDs are registered (so they know what to pass to `--project`).
- Inspect the path each ID maps to (for sanity-checking).
- Scriptably filter to just the fields they need.

## Contents

| Directory | Description |
|---|---|
| [add/](add/README.md) | Register an existing on-disk project in the user-level registry |

## Behavior

### Default output

The current implementation prints a CSV-like line per project, default field is `id`.

#### REQ: one-line-per-project

The command MUST print exactly one line per registered project. Lines MUST be in the order projects appear in `~/.datatug.yaml` (no implicit sort).

#### REQ: default-field-is-id

When `--fields` is not supplied and `--all` is not set, the command MUST print just the project `id` on each line. This is the script-friendly default.

#### REQ: all-flag

`--all` (alias `-a`) MUST select the field set `id,path,title`, joined by commas. It is a shortcut for `--fields id,path,title`.

#### REQ: fields-flag

`--fields` (alias `-f`) MUST accept a comma-separated list (a single argument or repeated occurrences) of field names. Allowed values today: `id`, `origin`, `title`. The current implementation accepts `path` as a documented alias of `origin` — confirmed under Outstanding Questions.

#### REQ: unknown-field-fails

An unknown field name MUST cause exit `2` (InvalidArgs) with a message naming the bad field. The current implementation returns `"unsupported field: %v"` which MUST be preserved.

### Empty registry

#### REQ: empty-registry-prints-nothing

When `~/.datatug.yaml` has no projects (or the file does not exist), the command MUST exit `0` and write nothing to stdout. Empty output is the correct signal of an empty registry; absent files are not an error here.

The current implementation reads the file and would error if it fails to load — that path needs reconciling with this REQ, tracked under Outstanding Questions.

## Parameters

| Flag | Aliases | Type | Default | Description |
|---|---|---|---|---|
| `--fields` | `-f` | string list | `["id"]` | Comma-separated list of fields to output. Values: `id`, `origin`/`path`, `title`. |
| `--all` | `-a` | bool | `false` | Shortcut for `--fields id,path,title`. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Listing succeeded (or empty registry) |
| `2` | Unknown field name in `--fields` |
| `1` | Generic runtime error |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. Output format violates [REQ: yaml-default-for-structured](../README.md#req-yaml-default-for-structured) by defaulting to CSV-like text — grandfathered today. |
| [config](../config/README.md) | Reads the same file. |
| [projects add](add/README.md) | Writes the same file. |
| [serve](../serve/README.md), [show](../show/README.md), [scan](../scan/README.md) | Consume IDs from this registry via `--project`. |

## Acceptance Criteria

### AC: default-is-ids-only

**Requirements:** projects#req:one-line-per-project, projects#req:default-field-is-id

With three projects registered (`a`, `b`, `c`), `datatug projects` prints exactly three lines: `a`, `b`, `c`, in that order.

### AC: all-flag-includes-path-title

**Requirements:** projects#req:all-flag

`datatug projects --all` prints lines of the form `<id>,<path>,<title>` for each project.

### AC: unknown-field-rejected

**Requirements:** projects#req:unknown-field-fails

`datatug projects --fields foo` exits `2` with stderr message naming `foo`.

## Open Questions

- The command name violates [REQ: singular-resource-names](../README.md#req-singular-resource-names) (`projects` vs `project list`). When does the rename land — and should this spec define an alias for the old name?
- Default output is CSV-like text; should it switch to YAML/JSON via [REQ: yaml-default-for-structured](../README.md#req-yaml-default-for-structured)?
- The `--fields` flag currently lists `id`, `path`, `title` in help text but accepts `id`, `origin`, `title` in code. Which is canonical? `origin` matches the on-disk YAML key; `path` is more user-friendly.
- Behavior on missing `~/.datatug.yaml` is currently to return an error; [REQ: empty-registry-prints-nothing](#req-empty-registry-prints-nothing) says it should print nothing. Which wins?

---
*This document follows the https://specscore.md/feature-specification*
