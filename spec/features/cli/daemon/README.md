---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Daemon

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/daemon?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/daemon?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/daemon?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/daemon?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug daemon` owns one local, read-only `datatug serve` process that AI-agent mods (the Claude Code DataTug dev panel) use so their cards can link to the DataTug web UI. A global `--from <caller>` flag (or `DATATUG_FROM`) makes any command ensure that process is running after the command's own work, without ever changing the command's stdout. `datatug daemon status|start|stop|url` are the explicit primitives; the flag is a thin wrapper over `daemon start`.

## Problem

The web UI reads project data only through the local agent API (`datatug serve`, default port 8989). A mod that wants a card to open "this table" in the web UI needs that API running and needs the exact URL, but a mod must not read project files, build URLs itself, or leave unowned background state on the machine. Founder, 2026-10-15: "When Ai agent start to use datatug for mod purpose datatug cli should start datatug daemon/service to serve API endpoints to serve web ui requests as we'll have link from cards to open cards in web ui. [...] we probably should pass some parameter like --from=claude-mod or something like that and if supplied check if daemon is started and if not try to start. If not able to start we should show a warning."

## Behavior

### Caller flag

#### REQ: from-flag

The root command MUST accept a persistent `--from <caller>` flag, valid on every command. When the flag is absent, `DATATUG_FROM` supplies the value; the flag wins when both are set. Known callers are `claude-mod` and `codex-mod`. An unknown non-empty value MUST print one warning line to stderr naming the value and MUST NOT start anything.

#### REQ: from-ensures-daemon

When a known caller is set, after the command's own work (success or failure) the CLI MUST behave as `datatug daemon start --from <caller>` with its output discarded, except for the failure warning of `warning-on-failure`. It MUST NOT run for `daemon`, `serve`, `version`, `help`, `completion` or `self-update`/`install`/`upgrade`, so `daemon stop` is never undone and the foreground server never spawns a second one.

#### REQ: stdout-untouched

The `--from` wrapper MUST NOT write to stdout and MUST NOT change the command's exit code. All wrapper output goes to stderr.

#### REQ: warning-on-failure

When the daemon is not running and could not be started, the wrapper MUST print exactly one stderr line starting `datatug: warning: ` that states the cause in a fixed phrase and names the log file, and MUST NOT include a connection URL or credential. When the daemon is running or started, the wrapper MUST print nothing.

### Subcommands

#### REQ: status

`datatug daemon status [--format json|text]` MUST NOT start or stop anything. JSON (the default) is one object on stdout with: `running` (bool), `url` (agent API base, e.g. `http://127.0.0.1:8989`, empty when not running), `uiBase` (web UI address of this agent), `port`, `pid`, `startedBy` (caller id, `"external"` for a datatug API this CLI did not start, empty when not running), `startedAt` (RFC 3339), `version`, `logFile`, `idleTimeout` and `error` (fixed phrase, only when the last start failed). Exit code is `0` whether or not it runs.

#### REQ: start

`datatug daemon start [--from <caller>] [--format json|text]` MUST ensure the daemon is running: reuse a running one, otherwise start one, then print the `status` object. It MUST exit `0` when running and `4` when it could not be started (the status object, with `error`, is still printed).

#### REQ: stop

`datatug daemon stop` MUST stop only a daemon this CLI started and recorded: it sends SIGTERM to the recorded pid, waits a bounded time, then removes the pidfile and record. It MUST refuse (exit `3`, "no daemon started by this CLI") for a datatug API it did not start (`startedBy` `external`) and MUST NEVER signal any other process. Stopping an already stopped daemon exits `0`.

#### REQ: url

`datatug daemon url --project <id> --env <env> --db <db> --table <name> [--view] [--schema <s>] [--records [--limit N] | --record <pk-json>] [--format json|text]` MUST print the web UI deep link of `deep-link-contract` and MUST NOT start the daemon. JSON fields: `url`, `kind` (`table-metadata`, `table-records`, `record`), `running`, `uiSupport` (`available` or `pending`). `--records` and `--record` are mutually exclusive (exit `2`). Any identifier containing `://` or `@` MUST be refused with exit `2` so a connection URL can never reach a link.

### Ownership and lifetime

#### REQ: owner-and-teardown

The state directory is `~/datatug/daemon/` (`$DATATUG_DAEMON_DIR` overrides it). It holds `daemon.pid`, `daemon.json` (pid, port, url, startedAt, startedBy, version, binary path, idle timeout), `daemon.log` and a short-lived `daemon.lock`. Owner is the CLI itself under the caller recorded in `startedBy`; the teardown verb is `datatug daemon stop`. No launchd job, service or tunnel is ever created.

#### REQ: idle-lifetime

The daemon is started with `serve --idle-timeout 2h` ([serve#req:idle-timeout](../serve/README.md)): the server MUST shut down gracefully (exit `0`) after that long with no HTTP request, and removes nothing else. Every `status` or `start` probe is a request, so an active agent keeps it alive. `datatug serve --idle-timeout` defaults to `0` (never), so a hand-started server is unaffected.

#### REQ: detached

The daemon MUST be started in its own process group with stdin from the null device and stdout and stderr appended to `daemon.log`, so it survives the command that started it.

#### REQ: localhost-read-only

The daemon MUST bind `127.0.0.1` and MUST be started without `--allow-writes`, `--allow-live-connections` and `--allow-opaque-sql`. Project selection is `serve`'s default (every registered project).

#### REQ: port-choice

The preferred port is 8989. If it is bound by a datatug API (answers `agent-info`), that server is adopted as running with `startedBy` `external`. If it is bound by anything else, the CLI MUST pick the next free port up to 9009, record it, and MUST NEVER signal or kill the foreign process. If none is free, start fails.

#### REQ: bounded-start

Starting MUST wait for `GET /datatug/agent-info` to answer for at most 5 seconds and fail early if the child exits. Concurrent starters are serialized by `daemon.lock`; a loser only waits for readiness.

#### REQ: no-secrets

`status`, `url`, the wrapper warning and the log-file path MUST never contain a credential, token or connection URL.

### Deep-link contract

#### REQ: deep-link-contract

The web UI address of an agent is `<uiBase>` = `https://datatug.app/store/http-<host>:<port>` (the existing `/store/http-<host>:<port>` convention of `serve`). Links, with every path segment percent-encoded:

| Card | URL |
|---|---|
| Table metadata | `<uiBase>/project/<project>/env/<env>/db/<db>/table/<table>[?schema=<s>]` (`view/<name>` for a view) |
| Table records (top N) | `<uiBase>/project/<project>/env/<env>/db/<db>/table/<table>/records?limit=<N>` |
| Single record | `<uiBase>/project/<project>/env/<env>/db/<db>/table/<table>/record?pk=<url-encoded JSON object column→value>` |

`DATATUG_UI_BASE` MAY replace `https://datatug.app` (self-hosted or local build). The metadata route exists in the UI today (`env/:environmentId` → `db/:dbCatalogId` → `table|view/:tableType`, datatug-apps `datatug-routing-proj-env*.ts`); the `records` and `record` child routes do not, so `url` reports `uiSupport` `pending` for them until the UI ships them.

## Acceptance Criteria

### AC: wrapper-keeps-stdout

**Requirements:** daemon#req:from-ensures-daemon, daemon#req:stdout-untouched, daemon#req:warning-on-failure

`datatug <cmd> --from=claude-mod` prints byte-identical stdout and the same exit code as without the flag; a failed start adds exactly one `datatug: warning:` line on stderr.

### AC: start-is-owned-and-stoppable

**Requirements:** daemon#req:start, daemon#req:stop, daemon#req:owner-and-teardown, daemon#req:detached

`daemon start` leaves a running server with a pidfile and record; `daemon stop` ends exactly that process and removes both.

### AC: foreign-port-never-killed

**Requirements:** daemon#req:port-choice, daemon#req:stop

With a non-datatug listener on 8989, `daemon start` binds the next free port and the listener is untouched; with a datatug API on 8989, it is adopted and `daemon stop` refuses.

### AC: read-only-localhost

**Requirements:** daemon#req:localhost-read-only, daemon#req:idle-lifetime

The spawned command line has `--host 127.0.0.1`, `--idle-timeout 2h` and none of the `--allow-*` flags.

### AC: link-shapes

**Requirements:** daemon#req:url, daemon#req:deep-link-contract, daemon#req:no-secrets

`daemon url` returns the three shapes above, refuses `://` and `@` in identifiers, and flags records links `pending`.

## Follow-up (UI repository, datatug/datatug-apps — not done here)

1. Add child routes `records` and `record` under `table/:tableType` and `view/:tableType` in `datatug-routing-proj-env-db.ts`, reading `limit` and `pk` query params.
2. Flip `uiSupport` to `available` in `pkg/daemon` once shipped.
3. Confirm `https://datatug.app` can call a plain-HTTP `127.0.0.1` agent (CORS allow-list in the request guard) from a browser page.

## Open Questions

- A policy set under `~/.datatug/policies/` makes `serve` refuse to start without `--as`; the daemon then fails with its log pointing at that. Should `daemon start` accept `--as/--role/--group` passthrough?
