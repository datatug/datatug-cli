---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Config

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/config?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/config?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/config?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/config?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug config` prints the user-level CLI configuration (project registry, server defaults, client defaults) from `~/.datatug.yaml` to stdout in YAML form. The command is read-only.

## Synopsis

```
datatug config
```

## Problem

Users need a quick, scriptable way to see what `datatug` has stored for them — which projects are registered, what the default `serve` host/port is, what the configured Web UI URL is — without opening the YAML file in an editor. Support workflows (bug reports, "what does the CLI think my setup is?") also need a one-line dump that pastes cleanly into an issue.

## Behavior

### Output

#### REQ: yaml-output

`datatug config` MUST print the parsed config as YAML to stdout, terminated by a newline. The output format MUST be valid YAML and MUST round-trip through the same loader that wrote it.

#### REQ: no-secret-redaction

The current implementation prints the config verbatim. If/when secrets land in the config (e.g., credentials), this REQ MUST be revisited. As of today the file contains no secret material; flagged under Outstanding Questions.

### Missing file

#### REQ: missing-config

If `~/.datatug.yaml` does not exist, the CLI MUST exit non-zero (`3` — NotFound) with a clear message naming the expected path. It MUST NOT silently print an empty config.

The current implementation surfaces this via `dtconfig.GetSettings()` returning an `os.ErrNotExist` wrapped error. The mapping to exit code `3` is part of the parent CLI contract.

### No writes

#### REQ: read-only

`datatug config` MUST be read-only. It MUST NOT create, modify, or repair the config file under any circumstance. Setting values is the responsibility of [`projects add`](../projects/add/README.md) and the (future) `config set` subcommand.

## Parameters

None. The command accepts no flags or positional arguments today.

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Config printed successfully |
| `3` | `~/.datatug.yaml` does not exist |
| `1` | Generic runtime error (unreadable file, YAML parse error) |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. Uses the user-level config file pinned in [REQ: config-file-location](../README.md#req-config-file-location). |
| [projects](../projects/README.md) | Reads the same registry that `config` prints. |
| [projects add](../projects/add/README.md) | Writes the same file `config` reads. |
| [serve](../serve/README.md) | Reads `Server.Host` / `Server.Port` defaults from the same file. |

## Acceptance Criteria

### AC: prints-yaml

**Requirements:** config#req:yaml-output

`datatug config` exits `0` and writes a valid YAML document to stdout. The document parses back into the same in-memory `dtconfig.Settings` value.

### AC: missing-config-is-not-found

**Requirements:** config#req:missing-config

With no `~/.datatug.yaml` present, `datatug config` exits `3` and writes a stderr message naming the missing file.

### AC: never-writes

**Requirements:** config#req:read-only

Running `datatug config` MUST NOT create `~/.datatug.yaml` if it does not exist, and MUST NOT modify its mtime if it does. Verifiable by stat before/after.

## Open Questions

- Should this command expand into `datatug config <get|set|list|unset>` subcommands, mirroring `git config` / `gh config`? Today there is no way to set values from the CLI.
- Should output format be selectable via `--format yaml|json` per the parent [REQ: yaml-default-for-structured](../README.md#req-yaml-default-for-structured)?
- The currently-commented-out `cmd_config_client.go` and `cmd_config_server.go` files suggest a former `config client` / `config server` surface. Should those be restored under a `config set` umbrella?

---
*This document follows the https://specscore.md/feature-specification*
