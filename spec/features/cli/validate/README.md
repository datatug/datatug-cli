# Feature: Validate

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/validate?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/validate?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/validate?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/validate?op=request-change) |

**Status:** Implementing

## Summary

`datatug validate` loads a DataTug project (or a multi-project repo root) from disk and runs the project's structural validation. Reports the first validation error encountered, or exits `0` if the project is structurally sound.

## Synopsis

```
datatug validate [--dir <path>] [-d <path>]
```

## Problem

DataTug projects are versionable file trees. Like any file format, the on-disk representation can become inconsistent — a referenced FK target may have been renamed, an environment may reference a DB server that no longer exists, a JSON file may be missing a required field. A pre-commit / CI check needs a one-liner that says "yes this project is valid" or "no, here's what's wrong".

`datatug validate` is that one-liner. It also supports a multi-project layout: if the directory contains a repo-root file listing several project subdirectories, each is validated in turn.

## Behavior

### Multi-project repo support

#### REQ: repo-root-multi-project

If the supplied directory contains a repo-root file (loaded via `filestore.LoadRootDatatugFile`), `datatug validate` MUST iterate over every project path listed and validate each one. Validation MUST stop and return on the first failure, naming the project index and path.

#### REQ: single-project-fallback

If no repo-root file is present (the loader returns `os.IsNotExist`), `datatug validate` MUST validate the directory as a single project.

### Project loading

#### REQ: loads-via-project-store

Each project MUST be loaded via the standard project store (`v.store.GetProjectStore(v.projectID).LoadProject`). Direct file reads bypassing the store are NOT permitted.

### Validation

#### REQ: delegates-to-project-validate

Validation logic MUST be delegated to `project.Validate()` on the loaded `datatug.Project` value. The CLI command does NOT implement validation rules itself; it is a thin entry point. Rules are owned by `pkg/datatug-core`.

#### REQ: first-failure-wins

On the first validation error, the command MUST exit non-zero (`1` — runtime error, or `2` if rules introduce an InvalidArgs-class — the project-side rule set today only emits generic errors). It MUST NOT continue validating subsequent projects.

### Logging

The current implementation writes progress lines via `log.Println` (which defaults to stderr in Go's `log` package). This is acceptable, as long as success/failure determination remains via the exit code, not by string-matching the logs.

#### REQ: structured-exit

Callers MUST be able to determine success purely from the exit code. The log lines (e.g., `Project: ID=..., path=...`, `Loading DataTug project...`, `DataTug project is valid.`) are informational and MUST NOT be relied upon by automation.

## Parameters

| Flag | Aliases | Type | Description |
|---|---|---|---|
| `--dir` | `-d` | string | Project or repo-root directory to validate. Defaults to cwd if omitted. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | All projects validated cleanly |
| `1` | Validation failed (returned error from `project.Validate()` or load) |
| `3` | The supplied `--dir` does not exist |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [init](../init/README.md) | A freshly-init'd project MUST pass `validate`. |
| [scan](../scan/README.md) | After a successful `scan`, the project MUST still validate. |
| [render](../render/README.md) | Independent. Re-rendering READMEs MUST NOT introduce validation errors. |

## Acceptance Criteria

### AC: valid-project-exits-zero

**Requirements:** validate#req:delegates-to-project-validate

`datatug validate --dir ./valid-project` exits `0`.

### AC: invalid-project-exits-non-zero

**Requirements:** validate#req:first-failure-wins, validate#req:structured-exit

A project with a known structural error exits non-zero, and the error is described on stderr.

### AC: multi-project-stops-on-first-failure

**Requirements:** validate#req:repo-root-multi-project, validate#req:first-failure-wins

In a repo-root layout with three projects where the second is invalid, `datatug validate` validates the first, fails on the second, and does NOT attempt to validate the third.

## Open Questions

- The command is named `validate` in CLI but is constructed by a Go function called `testCommandArgs` and its description still says "Runs validation scripts" / "The `test` consoleCommand executes validation scripts." Should the Go-level naming be cleaned up?
- Should there be a `--all` flag that continues past the first failure and reports every problem, vs. the current fail-fast behavior?
- Should `validate` emit a structured (`yaml` / `json`) report listing all problems, per [parent REQ: yaml-default-for-structured](../README.md#req-yaml-default-for-structured)? Today output is unstructured text.
- The CLI flag is `--dir`/`-d` rather than the shared `--project`/`-p` / `--dir`/`-d` pair other commands use. Should `validate` accept `--project <id>` as well, for symmetry?

---
*This document follows the https://specscore.md/feature-specification*
