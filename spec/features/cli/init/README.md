# Feature: Init

> [View in SpecStudio](https://specstudio.synchestra.io/project/features?id=datatug-cli@datatug@github.com&path=spec%2Ffeatures%2Fcli%2Finit) — graph, discussions, approvals

**Status:** Implementing

## Summary

`datatug init` scaffolds a new DataTug project on disk. It creates the project directory (if missing), writes the project root file under a `datatug/` subdirectory, and records the creator and creation timestamp.

## Synopsis

```
datatug init <project-id> <project-path>
```

## Problem

Without a scaffold command, users have to hand-author the on-disk format of a DataTug project: the `datatug/` directory layout, the root file with its `id`/`access`/`created` keys, and the conventions about where environments, DB models, and datasets live. Hand-authoring is fragile (typos in keys, missing files, wrong nesting) and the contents drift across the user base.

`datatug init` produces a lint-clean baseline so the project is immediately openable by [`show`](../show/README.md), [`serve`](../serve/README.md), and the TUI.

## Behavior

### Positional arguments

The command takes two positional arguments in order: a project ID and a project path.

#### REQ: project-id-arg

The first positional argument MUST be the project ID. It is used as the in-file `id`, stored under `ProjItemBrief.ID`. The CLI MUST NOT auto-derive it from the directory name — explicit naming makes scripted creation deterministic.

#### REQ: project-path-arg

The second positional argument MUST be the absolute or relative path to the project directory. The CLI MUST create the directory (with `0777` mode, subject to umask) if it does not exist.

### Refusal to clobber

#### REQ: refuse-existing-project

If `<project-path>/datatug` already exists and is a directory, `datatug init` MUST exit non-zero with a message stating that a DataTug project already exists at that path. It MUST NOT overwrite the existing files.

The current implementation surfaces this via a returned error containing `"the folder already contains datatug project"`. The exit code mapping is governed by [cli REQ: standard-exit-codes](../README.md#req-standard-exit-codes) — value `4` (InvalidStateTransition) for refusing to clobber.

### Creator metadata

#### REQ: record-creator

The created project file MUST record creation metadata in `Created` with at least the timestamp (`At`). When `user.Current()` succeeds, the implementation MAY populate creator name / username fields; current code paths are commented out and only the timestamp is set. This MUST NOT block on slow user-lookup calls (>1s).

### Single-project store

#### REQ: use-single-project-store

`datatug init` MUST use the single-project filestore (`filestore.NewSingleProjectStore`) so the resulting tree contains exactly one project. Multi-project bootstraps go through [`projects add`](../projects/add/README.md), not `init`.

## Parameters

| Position | Name | Type | Required | Description |
|---|---|---|---|---|
| 1 | `project` | string | yes | DataTug project ID. |
| 2 | `projectPath` | string | yes | Directory in which to create the project. Created if missing. |

The command currently accepts no flags. A future `--force` flag for clobbering, and a `--from` flag for cloning, are tracked under Outstanding Questions.

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Project created successfully |
| `2` | Missing or invalid positional arguments |
| `4` | A DataTug project already exists at the target path (cannot transition empty → init) |
| `1` | Generic runtime error (filesystem write, store save) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. Inherits exit-code and stderr conventions. |
| [show](../show/README.md) | Consumes the project layout `init` produces. |
| [validate](../validate/README.md) | A freshly-`init`ed project MUST pass `datatug validate`. |
| [projects add](../projects/add/README.md) | Registers an existing on-disk project in the user-level registry; `init` does NOT register the project — the user must `projects add` afterwards. |
| [demo](../demo/README.md) | Bypasses `init` and clones an external repository to produce its project. |

## Acceptance Criteria

### AC: creates-empty-project

**Requirements:** init#req:project-id-arg, init#req:project-path-arg, init#req:use-single-project-store

`datatug init my-proj ./my-proj-dir` exits `0`, creates `./my-proj-dir/datatug/...`, and the resulting project's `id` field equals `my-proj`.

### AC: refuses-clobber

**Requirements:** init#req:refuse-existing-project

Running `datatug init my-proj ./my-proj-dir` twice in a row exits non-zero on the second run, with a stderr message naming the path.

### AC: records-creation-timestamp

**Requirements:** init#req:record-creator

The created project file contains a `Created.At` timestamp within a small delta (e.g., ±10s) of when the command was run.

## Outstanding Questions

- Should `init` automatically call [`projects add`](../projects/add/README.md) so the new project is immediately addressable by ID from `~/.datatug.yaml`? Today the user must run two commands.
- Should there be a `--force` flag to overwrite an existing `datatug/` directory? Current behavior is strict refusal, which is safer but blocks legitimate re-init.
- Should `init` accept a database connection string and seed the project with one environment + scanned DB schema in a single step? Today the user runs `init` then [`scan`](../scan/README.md).
- The implementation contains a large block of commented-out database-bootstrap code dating from earlier behavior. Should the spec retain a placeholder for that flow or formally remove it?

---
*This document follows the https://specscore.md/feature-specification*
