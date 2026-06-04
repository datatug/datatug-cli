# Feature: Demo

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/demo?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/demo?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/demo?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/demo?op=request-change) |

**Status:** Implementing

## Summary

`datatug demo` installs and prepares the bundled DataTug demo project. It downloads two SQLite copies of the public Chinook sample database (for `local` and `prod` environments), clones the [`datatug-demo-project`](https://github.com/datatug/datatug-demo-project) repository, wires the downloaded databases into that project, and registers the project in `~/.datatug.yaml`. After it runs, `datatug serve` reveals a working playground with realistic schema and rows.

## Synopsis

```
datatug demo
datatug demo --reset-db
datatug demo --reset-project
datatug demo --reset-db --reset-project
```

## Problem

DataTug is hard to evaluate from a cold start — to see the Web UI in action a user needs a project, a database, and an environment configuration that connects them. Without a demo command, this is a multi-step setup that varies by platform.

`datatug demo` makes the cold start one command. It is also useful as a smoke test: after upgrading the CLI, run `datatug demo --reset-project` and confirm the project still builds.

## Behavior

### User directory layout

The demo populates `~/datatug/`:

- `~/datatug/dbs/chinook-local.sqlite` — the `local` env database
- `~/datatug/dbs/chinook-prod.sqlite` — the `prod` env database
- `~/datatug/demo-project/` — the demo project clone

#### REQ: fixed-paths

Demo asset paths MUST be exactly the values above. The CLI MUST NOT relocate them based on a flag. (A future `--path` flag is tracked under Outstanding Questions.)

### First-run behavior

#### REQ: download-if-missing

If the demo project directory does not exist, the CLI MUST download both SQLite files (`chinook-local.sqlite` and `chinook-prod.sqlite`) from the canonical Chinook GitHub URL, verify the downloads by running `SELECT COUNT(1)` against a fixed table list (`Employee`, `Customer`, `Invoice`, ..., `PlaylistTrack`), and only then clone the demo project repository.

#### REQ: verify-non-empty

Verification MUST require every checked table to return a count `> 0`. A `0` count MUST be treated as a download failure and MUST trigger a re-download.

### Reset flags

#### REQ: reset-db

`--reset-db` MUST re-download the SQLite files (overwriting in place) and re-verify them. It MUST NOT recreate the project directory.

#### REQ: reset-project

`--reset-project` MUST delete and re-create the demo project directory via `git clone` of the demo project repo. It MUST NOT re-download the SQLite files unless `--reset-db` is also set.

#### REQ: reset-flags-independent

`--reset-db` and `--reset-project` MUST be settable independently and together. Both off (the default) MUST be a safe "ensure installed" operation.

### Registry update

#### REQ: register-in-config

After installing, the CLI MUST register the demo project in `~/.datatug.yaml` under the alias `datatug-demo-project`. If a project with that alias is already registered at a different path, the CLI MUST refuse (current code does this implicitly by returning an error) — the user can manually unregister via the config file.

### Closing summary

#### REQ: prints-next-step

On success, the CLI MUST print a final stdout line instructing the user to run `datatug serve` — current message: `"Run \`./datatug serve\` to see the demo project."` Wording is part of the contract; minor edits are allowed but the directive MUST point at [`serve`](../serve/README.md).

### Network failure handling

#### REQ: download-failure-is-error

A failed download (HTTP error, partial write) MUST cause `datatug demo` to exit non-zero (`4` — connection/IO failure). The partially-written file SHOULD be removed; current implementation leaves it for the next run's verification to catch.

## Parameters

| Flag | Type | Default | Description |
|---|---|---|---|
| `--reset-db` | bool | `false` | Re-download the demo SQLite files. |
| `--reset-project` | bool | `false` | Recreate the demo project directory from git. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Demo installed / updated successfully |
| `2` | Invalid argument combination (none currently possible) |
| `4` | Network or filesystem I/O failure (download, clone, write) |
| `1` | Generic runtime error |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. |
| [config](../config/README.md) | `demo` writes to the same `~/.datatug.yaml` `config` prints. |
| [projects](../projects/README.md) | The demo project shows up in `projects` after install. |
| [serve](../serve/README.md) | The post-install message points at `serve`. |
| [init](../init/README.md) | Independent path — `demo` does NOT use `init` (it clones an external repo instead). |

## Acceptance Criteria

### AC: cold-install

**Requirements:** demo#req:download-if-missing, demo#req:verify-non-empty, demo#req:register-in-config, demo#req:prints-next-step

With no `~/datatug/` directory and no `~/.datatug.yaml` entry, `datatug demo` exits `0`, the demo files exist at the documented paths, the demo project is registered, and the closing message instructs the user to run `datatug serve`.

### AC: reset-db-only

**Requirements:** demo#req:reset-db

Running `datatug demo --reset-db` after an initial install re-downloads `chinook-local.sqlite` and `chinook-prod.sqlite` (file mtimes advance) but leaves the cloned project directory unchanged.

### AC: reset-project-only

**Requirements:** demo#req:reset-project

Running `datatug demo --reset-project` after an initial install removes and re-clones `~/datatug/demo-project/` but does NOT re-download the SQLite files.

### AC: corrupt-db-triggers-redownload

**Requirements:** demo#req:verify-non-empty

If `chinook-local.sqlite` is truncated to zero bytes, the next `datatug demo` run detects the verification failure and re-downloads.

## Open Questions

- Should the demo respect a `DATATUG_HOME` env var instead of hardcoding `~/datatug/`?
- The Chinook download URL is hardcoded against `github.com/datatug/chinook-database`. Should there be a fallback mirror?
- The demo logs use `log.Println` rather than structured logging. Should this command get a `--quiet` flag for CI use?
- The implementation leaks `defer` patterns that close response bodies after writing (correct) but does not check write/close errors on the SQLite files. Should this be fixed in the same release as the spec?

---
*This document follows the https://specscore.md/feature-specification*
