---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Daemon

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/daemon?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/daemon?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/daemon?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/daemon?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`datatug daemon` owns one local, read-only `datatug serve` process that AI-agent mods (the Claude Code DataTug dev panel) use so their cards can link to the DataTug web UI. A global `--client <caller>` flag (or `DATATUG_CLIENT`) makes any command ensure that process is running after the command's own work, without ever changing the command's stdout. `datatug daemon status|start|stop|url` are the explicit primitives; the flag is a thin wrapper over `daemon start`.

## Problem

The web UI reads project data only through the local agent API (`datatug serve`, default port 8989). A mod that wants a card to open "this table" in the web UI needs that API running and needs the exact URL, but a mod must not read project files, build URLs itself, or leave unowned background state on the machine. Founder, 2026-10-15: "When Ai agent start to use datatug for mod purpose datatug cli should start datatug daemon/service to serve API endpoints to serve web ui requests as we'll have link from cards to open cards in web ui. [...] we probably should pass some parameter like --client=claude-mod or something like that and if supplied check if daemon is started and if not try to start. If not able to start we should show a warning."

## Behavior

### Caller flag

#### REQ: client-flag

The root command MUST accept a persistent `--client <caller>` flag, valid on every command. When the flag is absent, `DATATUG_CLIENT` supplies the value; the flag wins when both are set. Known callers are `claude-mod` and `codex-mod`. An unknown non-empty value MUST print one warning line to stderr that does NOT repeat the value (it may be a connection URL typed into the wrong flag) and MUST NOT start anything.

#### REQ: client-ensures-daemon

When a known caller is set, after the command's own work (success or failure) the CLI MUST behave as `datatug daemon start --client <caller>` with its output discarded, except for the failure warning of `warning-on-failure`. It MUST NOT run for `daemon`, `serve`, `version`, `help`, `completion` or `self-update`/`install`/`upgrade`, so `daemon stop` is never undone and the foreground server never spawns a second one.

#### REQ: stdout-untouched

The `--client` wrapper MUST NOT write to stdout and MUST NOT change the command's exit code. All wrapper output goes to stderr.

#### REQ: warning-on-failure

When the daemon is not running and could not be started, the wrapper MUST print exactly one stderr line starting `datatug: warning: ` that states the cause in a fixed phrase and names the log file, and MUST NOT include a connection URL or credential. When the daemon is running or started, the wrapper MUST print nothing.

### Subcommands

#### REQ: status

`datatug daemon status [--format json|text]` MUST NOT start or stop anything. JSON (the default) is one object on stdout with: `running` (bool), `url` (agent API base, e.g. `http://127.0.0.1:8989`, empty when not running), `uiBase` (web UI address of this agent), `port`, `pid`, `startedBy` (caller id, `"external"` for a datatug API this CLI did not start, empty when not running), `startedAt` (RFC 3339), `version`, `logFile`, `idleTimeout` and `error` (fixed phrase, only when the last start failed) and `startError` (stable machine-readable reason, omitted when empty; the one value today is `policy-principal-required`: a policy set exists, `serve` would refuse to start without `--as`/`--role`/`--group`, and the daemon names none; `start` decides this before spawning, with the same policies-folder rule as `serve`, reading nothing beyond whether a valid set exists). Exit code is `0` whether or not it runs.

#### REQ: start

`datatug daemon start [--client <caller>] [--format json|text]` MUST ensure the daemon is running: reuse a running one, otherwise start one, then print the `status` object. It MUST exit `0` when running and `4` when it could not be started (the status object, with `error` and, when named, `startError`, is still printed). On `policy-principal-required`, `daemon start` and the `--client` auto-start print one stderr line saying a policy set exists but no principal was named for the daemon and to run `datatug serve --as <principal>` or remove the policy set; it never names the policy path or contents.

#### REQ: stop

`datatug daemon stop` MUST stop only a daemon this CLI started and recorded: it sends SIGTERM to the recorded pid, waits a bounded time, then removes the pidfile and record. It MUST refuse (exit `3`, "no daemon started by this CLI") for a datatug API it did not start (`startedBy` `external`) and MUST NEVER signal any other process. Stopping an already stopped daemon exits `0`.

#### REQ: url

`datatug daemon url --project <id> --env <env> --db <db> --table <name> [--view] [--schema <s>] [--records [--limit N] | --record <pk-json>] [--format json|text]` (or `--db datatug://<project>/<db>[?env=<ENV>]` in place of `--project`, `--env` and `--db <id>`; giving both is exit `2`) MUST print the web UI deep link of `deep-link-contract` and MUST NOT start the daemon. JSON fields: `contract` (int, the shape version of this object, currently `1`; bumped on any incompatible change to the shape), `url`, `kind` (`table-metadata`, `table-records`, `record`), `running`, `uiSupport` (`available` or `pending`), `warnings` (always present, `[]` when none; see `url-validates-metadata`). `--records` and `--record` are mutually exclusive (exit `2`). Any identifier containing `://` or `@`, and any path identifier equal to `.` or `..`, MUST be refused with exit `2` so a connection URL can never reach a link or climb the path; `--db` is a plain source id or exactly a `datatug://` reference, any other scheme or a path is exit `2`. A reference with no `?env=` takes the one environment of the project registry on disk that lists the source (several: exit `2` naming them; none: exit `3`), with no network and no database connection; `datatug:///<db>` (no project) is exit `2`. A link longer than 2048 characters, a key value that is null, an object, an array, a boolean, a non-integer number (the error names the kind, never the value; the record lookup binds strings and exact integers only) or a string over 256 characters, are exit `2`. Every link's fragment starts with `code=<one-time code>` minted locally from the daemon's access key (see [serve](../serve/README.md) `origins-and-token`); a daemon that left no key gets a link without one. `running` is decided from the record, the pid and a TCP connect, without an HTTP request, so a check never resets the idle timer; a datatug API this CLI did not start (`external`) costs one `agent-info` request.

#### REQ: url-validates-metadata

`daemon url` MUST check the link against the project's scanned metadata (the same files `datatug show` lists, read from the registered project folder; no network and no database connection) when the project holds it for the source and environment. Every cheap check of the request itself (required flags, `--record` JSON and key kinds, `--limit`, the 2048-character bound) comes first, so such a refusal is exit `2` whatever the project holds and the project is not read; only then is the metadata read, and only the columns file of the one table asked for (not every table). Then:

- a `--table` that is neither a table nor a view of the source is exit `3` ("the table or view is not in the project's scanned metadata"; the name is never echoed);
- with no `--schema`, a name that several schemas hold is exit `2` ("ambiguous: several schemas hold that name, give --schema"); `--schema` matches exactly (SQLite's one schema is `main`; an empty `--schema` matches the only schema that holds the name);
- `--view` MUST match the object's kind: `--view` for a table, and a plain `--table` for a view, are exit `2` (the message names the kind only). The scan keeps tables and views only, so a materialized view is not distinguished (it is whatever the scan filed it as);
- `--record` on a view is exit `2` ("a view has no primary key");
- `--record` keys MUST be exactly the table's primary-key column names: a key that is not one is exit `2` ("not a primary-key column"), a primary-key column that is missing is exit `2` ("missing primary-key column"); no value is ever echoed. When the scan holds no primary-key information of the table (no `pkPosition` at all) the key is not checked and `warnings` gets `"primary key not in scanned metadata: key not validated"`.

Names (table, schema, key columns) are compared exactly and case-sensitively, as `datatug db schema` prints them; `--table customer` does not match `Customer`.

When the project holds no usable metadata for the source (never scanned, a scan with no table and no view at all, a model folder that is missing, an unregistered project, an unknown environment or source, or a table's columns file that is unreadable, not a regular file (links are never followed) or larger than 4 MiB) the link is built as before and `warnings` is `["table metadata not scanned: link not validated"]`; the exit code is `0` and nothing goes to stderr. With the text format the warnings are not printed.

#### REQ: result-link

`datatug daemon url --result <id> [--view=table|record|columns|sources|related] [--row <n>] [--format json|text]` MUST print the link of a result stored by `query run --result-id` (see [query](../query/README.md)) and MUST NOT start the daemon or send a request. `--view` takes its value after `=` (a bare `--view` keeps meaning a view of a table link); the default view is `table`. `--row` is a 1-based position, at most 10000, never a key; `--row 0` and `--row` without `--result` are exit `2`. The id follows the result id grammar (exit `2` otherwise); any table-link flag with `--result`, a view outside the list, and a view name on a table link are exit `2`. JSON fields: `contract` (as for every `daemon url` object), `url`, `kind` (`query-result`), `running`, `uiSupport`, `resultExists`, `warnings` (always present, `[]`). Whether the result exists is read from the store on disk (a complete, unexpired result whose directory is not a link and is the user's); when it does not, `url` is absent and no code is minted. The link carries no project, database, table, column, value or query text: the page reads them from the agent.

### Ownership and lifetime

#### REQ: owner-and-teardown

The state directory is `~/datatug/daemon/` (`$DATATUG_DAEMON_DIR` overrides it). It holds `daemon.pid`, `daemon.json` (pid, port, url, startedAt, startedBy, version, binary path, idle timeout), `daemon.log` and a short-lived `daemon.lock`. Owner is the CLI itself under the caller recorded in `startedBy`; the teardown verb is `datatug daemon stop`. No launchd job, service or tunnel is ever created.

#### REQ: idle-lifetime

The daemon is started with `serve --idle-timeout 2h` ([serve#req:idle-timeout](../serve/README.md)): the server MUST shut down gracefully (exit `0`) after that long with no HTTP request, and removes nothing else. `status` and `url` decide `running` from the record, the pid and a TCP connect and send no HTTP request to a daemon this CLI started, so they do not keep it alive; only a real request does (an external datatug API on the preferred port costs one `agent-info` request). `datatug serve --idle-timeout` defaults to `0` (never), so a hand-started server is unaffected.

#### REQ: detached

The daemon MUST be started in its own process group with stdin from the null device and stdout and stderr appended to `daemon.log`, so it survives the command that started it.

#### REQ: scrubbed-environment

The daemon MUST be started with an allow-listed environment only: `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `TMPDIR`/`TMP`/`TEMP`, `TZ`, `TERM`, `LANG`, `LANGUAGE`, `LC_*`, `XDG_*`, the Windows system variables, and the non-secret directory settings `DATATUG_DAEMON_DIR`, `DATATUG_EVIDENCE_DIR`, `DATATUG_POLICIES_DIR`, `DATATUG_PERSONAL_DIR`, `DATATUG_SESSION_RESULTS_DIR`, `DATATUG_CHAT_STORE_DIR`, `DATATUG_CHAT_PREFERENCES_DIR`, and the telemetry opt-outs `DATATUG_TELEMETRY`, `DO_NOT_TRACK`, `CI` (the child is the same binary and MUST honour them). Everything else (`DATATUG_OPEN_DB`, tokens, passwords) MUST NOT reach the long-lived process. A consequence: the AI profile key of `--detective-ai-profile` (read from a named variable) is not available to the daemon, which never starts it with that flag.

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
| Table metadata | `<uiBase>/project/<project>/env/<env>/db/<db>/table/<table>?schema=<s>` (`view/<name>` for a view; `schema` is always present, empty for a schemaless source) |
| Table records (top N) | `<uiBase>/project/<project>/env/<env>/db/<db>/table/<table>/records?limit=<N>&schema=<s>` |
| Single record | `<uiBase>/project/<project>/env/<env>/db/<db>/table/<table>/record?schema=<s>#pk=<encodeURIComponent of a JSON object column→value>` |
| Query result | `<uiBase>/result/<id>?view=<view>[&row=<n>]` |

The key is in the URL fragment so it is never sent to the web host. It is one JSON object with sorted keys and typed values; an integer above 2^53 − 1 in absolute value is written as a string so the UI never rounds it. The fragment is encoded like JavaScript's `encodeURIComponent`.

`DATATUG_UI_BASE` MAY replace `https://datatug.app` (self-hosted or local build). The deployed UI serves all three kinds on both `table` and `view` (datatug-apps `libs/datatug/main/src/lib/routes/datatug-routing-proj-env-db.ts`: `table|view/:tableType`, `…/records`, `…/record`; `schema` is authoritative in `agent-links/table-identity.ts`, `limit` is read in `link-route.ts`, `pk` is the last part of the fragment in `record-key.ts`, the code is taken out first by `main.ts`), so `url` reports `uiSupport` `available` for all three kinds. A kind is flipped only when both its `table` and `view` routes are served. The `result/<id>` route of a query result is not served yet, so `query-result` is `pending`; the page will read the result from the agent's `GET /datatug/results/<id>` (see [serve](../serve/README.md) `result-route`).

## Acceptance Criteria

### AC: wrapper-keeps-stdout

**Requirements:** daemon#req:client-ensures-daemon, daemon#req:stdout-untouched, daemon#req:warning-on-failure

`datatug <cmd> --client=claude-mod` prints byte-identical stdout and the same exit code as without the flag; a failed start adds exactly one `datatug: warning:` line on stderr.

### AC: start-is-owned-and-stoppable

**Requirements:** daemon#req:start, daemon#req:stop, daemon#req:owner-and-teardown, daemon#req:detached

`daemon start` leaves a running server with a pidfile and record; `daemon stop` ends exactly that process and removes both.

### AC: foreign-port-never-killed

**Requirements:** daemon#req:port-choice, daemon#req:stop

With a non-datatug listener on 8989, `daemon start` binds the next free port and the listener is untouched; with a datatug API on 8989, it is adopted and `daemon stop` refuses.

### AC: read-only-localhost

**Requirements:** daemon#req:localhost-read-only, daemon#req:idle-lifetime, daemon#req:scrubbed-environment

The spawned command line has `--host 127.0.0.1`, `--idle-timeout 2h` and none of the `--allow-*` flags.

### AC: link-shapes

**Requirements:** daemon#req:url, daemon#req:deep-link-contract, daemon#req:no-secrets

`daemon url` returns the three shapes above (key in the fragment, `schema` always present), refuses `://`, `@`, `.` and `..` in identifiers, and flags all three kinds `available`.

### AC: link-contract-version

**Requirements:** daemon#req:url

Every `daemon url` JSON object carries `"contract": 1`.

### AC: link-validated

**Requirements:** daemon#req:url-validates-metadata

Against a scanned project, `daemon url` refuses an unknown table (exit 3), `--view` on a table or no `--view` on a view, and a `--record` whose keys are not exactly the primary key (exit 2), and an ambiguous schema (exit 2); for a never-scanned or empty source it still prints the link with the `warnings` entry.

### AC: result-link-shape

**Requirements:** daemon#req:result-link, daemon#req:deep-link-contract, daemon#req:no-secrets

For a stored result, `daemon url --result <id> --view=sources --row 2` prints `<uiBase>/result/<id>?view=sources&row=2` with `resultExists` `true`, `kind` `query-result` and `uiSupport` `pending`, and a running agent's code first in the fragment; for an id with no result it prints `resultExists` `false` and no `url`; a bad id or view, or `--result` with `--table`, is exit `2`.

## Follow-up

1. Confirm `https://datatug.app` can call a plain-HTTP `127.0.0.1` agent (CORS allow-list in the request guard) from a browser page.
2. Serve `result/<id>` in datatug-apps (reading `GET /datatug/results/<id>`), then flip `query-result` to `available` in `pkg/daemon`.

## Decisions

- **Flag name `--client` / `DATATUG_CLIENT` (settled, founder 2026-10-15: "--client is a good name - approved").** The founder first suggested `--from`, but `--from` is already a command-local flag on `query run` (table), `db copy` and `db export` (source URL), where it would shadow a persistent root flag and make the caller id read as a table name. No command registers `client`.

## Open Questions

- How does the daemon obtain a principal when a policy set exists (`--as`/`--role`/`--group` on `daemon start`, remembered in `daemon.json`)? Today it names none, so `start` fails with `startError: policy-principal-required`. Recommendation: `daemon start --as/--role/--group`, persisted in `daemon.json` and passed through to `serve`, with the `--client` wrapper reusing the recorded one.
