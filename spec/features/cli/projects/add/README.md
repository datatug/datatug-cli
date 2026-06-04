# Feature: Projects Add

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/projects/add?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/projects/add?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/projects/add?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/projects/add?op=request-change) |

**Status:** Implementing

## Summary

`datatug projects add` registers an existing on-disk DataTug project in the user-level registry (`~/.datatug.yaml`). After registering, the project can be addressed by its ID via `--project <id>` in other commands.

## Synopsis

```
datatug projects add --project <id> --dir <path>
```

## Problem

The user-level registry is the lookup table the CLI uses to translate `--project <id>` into a directory path. Without an `add` command, users would have to hand-edit `~/.datatug.yaml` to introduce a new ID — fragile, error-prone, and not scriptable.

## Behavior

### Identification

The command identifies the project to add by ID and path. The ID is normalized to lower case.

#### REQ: id-normalization

The project ID MUST be normalized to lower case before being written to `~/.datatug.yaml`. This matches the current behavior `strings.ToLower(v.ProjectName)`.

#### REQ: requires-id-and-path

Both project ID and project directory MUST be provided. If either is missing, the command MUST exit `2` (InvalidArgs).

The current implementation reads these from `projectBaseCommand` (flags `--project`/`-p` and `--dir`/`-d`). Specific flag wiring is part of the shared CLI conventions ([REQ: shared flags](../../README.md#shared-flags)).

### Idempotence

#### REQ: idempotent-on-same-path

If a project with the same ID is already registered and points at the same path, the command MUST exit `0` without modifying the config. Re-running `add` MUST be safe.

#### REQ: conflict-on-different-path

If a project with the same ID is already registered but points at a different path, the command MUST exit non-zero (`4` — InvalidStateTransition) with a message naming the existing path. It MUST NOT overwrite the existing registration silently.

### Config persistence

#### REQ: append-not-replace

The command MUST append the new project to `Settings.Projects` and persist via the same code path used by all writers (`saveConfig`). It MUST NOT rewrite or reorder unrelated entries.

#### REQ: config-write-is-atomic

The current implementation opens the file with `os.Create` and then encodes YAML directly into it, which is NOT atomic — a crash mid-write can corrupt `~/.datatug.yaml`. This REQ pins the target behavior: writes MUST be performed via `write-to-temp-then-rename` so a crash leaves either the old or new file intact. Remediation tracked under Outstanding Questions.

### Config path bug

The current implementation passes the literal string `~/.datatug.yaml` to `os.Create`, which does NOT expand `~` and writes the file in the current directory under a `~` subdir. This is a known bug tracked under Outstanding Questions; this spec pins the correct behavior.

#### REQ: tilde-expansion

The `~` in the config path MUST be expanded to the current user's home directory before any filesystem call. The implementation MUST resolve the path via `os.UserHomeDir()` (or equivalent), not by passing a literal `~`-prefixed string to `os.Create`/`os.Open`.

## Parameters

| Flag | Aliases | Type | Required | Description |
|---|---|---|---|---|
| `--project` | `-p` | string | yes | Project ID. Normalized to lower case. |
| `--dir` | `-d` | string | yes | Path to the existing DataTug project directory. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Project registered (or already registered with same path) |
| `2` | Missing required flag |
| `4` | ID conflict with existing registration at a different path |
| `1` | Generic runtime error (write failure, YAML encode failure) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [projects](../README.md) | Parent. Adds entries that `projects` lists. |
| [init](../../init/README.md) | `init` creates a project on disk; `projects add` registers it. The user runs both. |
| [config](../../config/README.md) | Reads the file `projects add` writes. |

## Acceptance Criteria

### AC: registers-new-project

**Requirements:** projects/add#req:requires-id-and-path, projects/add#req:append-not-replace

`datatug projects add --project demo --dir ./demo` writes a new entry with `id: demo` and `origin: ./demo` to `~/.datatug.yaml` and exits `0`.

### AC: idempotent-rerun

**Requirements:** projects/add#req:idempotent-on-same-path

Running the same `datatug projects add` invocation twice exits `0` both times and leaves `~/.datatug.yaml` byte-identical between the two states after the first write.

### AC: rejects-id-collision

**Requirements:** projects/add#req:conflict-on-different-path

With `demo` already pointing at `./demo`, running `datatug projects add --project demo --dir ./other` exits `4` and leaves the file unchanged.

### AC: writes-to-home-dir

**Requirements:** projects/add#req:tilde-expansion

Run from any working directory, the command MUST write to `$HOME/.datatug.yaml`, NOT to `./~/.datatug.yaml`.

## Open Questions

- The current code uses `os.Create("~/.datatug.yaml")` — the literal `~` is not expanded by `os.Create`. Should the fix land alongside this spec, or as a separate task?
- Should `projects add` accept a `--title` flag to set the human-readable title at registration time? Today the title is empty until manually edited.
- Should successful registration print the resolved path to stdout for confirmation, or stay silent for scripting?

---
*This document follows the https://specscore.md/feature-specification*
