---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Serve

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/serve?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/serve?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/serve?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/serve?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug serve` starts an HTTP server that exposes the DataTug agent API. The server makes one or more locally-stored DataTug projects available to the DataTug Web UI (at `datatug.app` or a local PWA build), so users can browse, query, and scan databases through a browser while the data and credentials stay on their machine.

By default the command also opens the user's browser to the Web UI URL pointing at the local agent.

## Synopsis

```
datatug serve
datatug serve --host <host> --port <port> [--local] [--client-url <url>]
datatug serve --project <id>
datatug serve --dir <path>
```

## Problem

DataTug's Web UI is hosted at `datatug.app`, but the data lives on the user's machine (or behind their firewall). The agent process bridges the two: the browser talks to `localhost:8989` (the agent), and the agent reads the on-disk project and connects to local databases on the user's behalf.

Users need:

- A one-command way to start the agent against one or all of their projects.
- A consistent default that "just works" — sensible host/port, automatic browser launch.
- An escape hatch (`--local`) for users who self-host the Web UI build.

## Behavior

### Project selection

The agent serves one or many projects.

#### REQ: serve-all-by-default

When neither `--project` nor `--dir` is supplied, the agent MUST serve every project registered in `~/.datatug.yaml`. Each project is addressable in the Web UI by its `id`.

#### REQ: serve-single-via-dir

When `--dir <path>` is supplied, the agent MUST serve only the project at that path. The project ID is read from the project file in that directory.

#### REQ: dir-no-multi-list

The `--dir` flag MUST NOT accept a list of paths or a semicolon-separated set. Serving multiple specified projects via the command line is out of scope for this MVP; multi-project serving is the implicit behavior when `--dir` is omitted. The current implementation explicitly rejects `;`-separated input.

### Host and port

#### REQ: default-host

The default host MUST be `localhost`. The user-level config (`Settings.Server.Host`) MAY override the default. The `--host`/`-h` flag MUST override both.

#### REQ: default-port

The default port MUST be `8989`. The user-level config (`Settings.Server.Port`) MAY override the default. The `--port`/`-o` flag MUST override both.

### Client URL and browser launch

The agent constructs a URL pointing at the Web UI and opens it in the user's default browser.

#### REQ: default-remote-client

Without `--local`, the default client URL MUST be `https://datatug.app/pwa/repo/<host>:<port>`. The browser MUST be opened to that URL with the agent's host:port appended as `/agent/<host>:<port>`.

#### REQ: local-flag

`--local` MUST switch the default client URL to `http://<host>:<port>` (the agent serves both the API and a bundled local UI when this flag is set). This supports air-gapped use.

#### REQ: client-url-override

`--client-url <url>` MUST override both defaults. The agent appends `/agent/<host>:<port>` to whatever URL is supplied.

#### REQ: browser-failure-non-fatal

Failure to launch the browser MUST log a warning to stdout and MUST NOT abort serving. The HTTP server is the primary deliverable; the browser launch is a convenience.

### HTTP server lifecycle

#### REQ: blocks-until-signal

`datatug serve` MUST block in the foreground until it receives an interrupt signal (Ctrl-C / SIGTERM). It MUST NOT daemonize itself.

#### REQ: graceful-shutdown

On signal receipt, the HTTP server MUST perform a graceful shutdown: stop accepting new connections, allow in-flight requests up to a short bounded timeout, then exit `0`. The current implementation has a TODO for graceful shutdown — remediation is tracked under Outstanding Questions.

### URL agent suffix

#### REQ: agent-suffix-format

The URL the agent navigates the browser to MUST embed the agent's listening address as `<host>:<port>`, except when port equals `0` or `80`, in which case only `<host>` MUST be used (no `:port` suffix). This matches the current implementation.

## Parameters

| Flag | Aliases | Type | Default | Description |
|---|---|---|---|---|
| `--host` | `-h` | string | `localhost` | Bind address. |
| `--port` | `-o` | int | `8989` | Bind port. |
| `--local` |  | bool | `false` | Use `http://<host>:<port>` as the client URL instead of the remote PWA. |
| `--client-url` |  | string | `""` | Override the Web UI URL the browser opens. |
| `--project` | `-p` | string | `""` | Serve only the project with this registry ID. |
| `--dir` | `-d` | string | `""` | Serve only the project rooted at this path. |

## Exit codes

| Exit code | Meaning |
|---|---|
| `0` | Server stopped cleanly (graceful shutdown) |
| `2` | Conflict between flags (e.g., `--dir` with `;`-separated value) |
| `3` | `--project <id>` not found in registry, or `--dir <path>` is not a DataTug project |
| `4` | Failed to bind to host:port |
| `1` | Generic runtime error |

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. Uses shared host/port defaults from the user-level config. |
| [config](../config/README.md) | Reads `Settings.Server.Host` / `Settings.Server.Port` from the same file. |
| [projects](../projects/README.md) | Multi-project mode (no `--dir`) serves every entry in the projects registry. |
| [scan](../scan/README.md) | The Web UI may trigger scans against connected DBs through the agent's API. |
| [ui](../ui/README.md) | Independent. TUI does not consume the HTTP API. |

## Acceptance Criteria

### AC: defaults-bind-localhost-8989

**Requirements:** serve#req:default-host, serve#req:default-port

`datatug serve` with no flags and no server config binds to `localhost:8989` and opens the default browser to `https://datatug.app/pwa/repo/localhost:8989/agent/localhost:8989`.

### AC: local-flag-uses-loopback-client

**Requirements:** serve#req:local-flag

`datatug serve --local` opens the default browser to `http://localhost:8989/agent/localhost:8989`.

### AC: ctrl-c-stops-cleanly

**Requirements:** serve#req:blocks-until-signal, serve#req:graceful-shutdown

Sending SIGINT to a running `datatug serve` causes the process to exit `0` within a bounded grace period.

### AC: missing-dir-fails-clearly

**Requirements:** serve#req:serve-single-via-dir

`datatug serve --dir ./not-a-project` exits `3` with a stderr message naming the missing/invalid project.

## Open Questions

- Graceful shutdown is currently a TODO in the implementation. Should that land alongside this spec or in a follow-up?
- Should the agent emit a structured "ready" line (e.g., a single JSON object) on stdout when it has bound the port, so test harnesses can wait deterministically?
- The default client URL hardcodes `https://datatug.app`. Should that be configurable via `Settings.Client.UrlConfig`?
- Should the browser auto-launch be disable-able via `--no-browser`? Useful in CI and headless setups.
- Today `--host` shadows the parent CLI's `-h`/`--help`. Should the short alias change to something else?

---
*This document follows the https://specscore.md/feature-specification*
