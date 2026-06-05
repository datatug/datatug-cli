# Feature: Serve-Brokered AI Query Builder

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/serve-brokered-query-builder?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/serve-brokered-query-builder?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/serve-brokered-query-builder?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/serve-brokered-query-builder?op=request-change) |
**Status:** Under Review
**Date:** 2026-06-05
**Owner:** alexander.trakhimenok
**Source Ideas:** —
**Supersedes:** —
**Implements:** specscore:feature/serve-brokered-query-builder@github.com/datatug/datatug
**Grade:** A

## Summary

The **CLI (daemon) Implementation** of the Serve-Brokered AI Query Builder Capability (`specscore:feature/serve-brokered-query-builder@github.com/datatug/datatug`). It extends the existing `datatug serve` daemon ([cli/serve](../cli/serve/README.md)) with two additions on the **same** process, host, port, and lifecycle: an **MCP endpoint** (streamable HTTP) that a terminal AI-agent drives, and a **query-builder session broker** that holds one current query per named-query *tab* and synchronizes it with the hosted Web UI over HTTP + WebSocket. Each tab is in one of two modes: **DTQL mode** (default) holds a canonical dalgo AST whose textual form is a 1:1 DTQL-YAML serialization, rendered to the connection's native query language on run; **native mode** holds the connection's own native query text plus parameters verbatim and executes it as-is. The AST spans relational and nested/document shapes; a tab may convert DTQL→native one-way. This Feature specifies only the daemon-side surface and its deltas; all platform-agnostic behavior is inherited from the Capability it `**Implements:**`.

## Problem

The Capability defines *what* a serve-brokered query builder must do on every surface; the Web realization lives in `datatug-apps` and the agent conversation lives in an external terminal. Neither can function without a local daemon that actually holds the query state, exposes it to the agent over MCP, serves and syncs it to the Web UI, executes it against live connections, and brokers edits both ways. `datatug serve` already bridges the browser at `datatug.app` to on-disk projects and local databases — so the daemon side belongs here, built on that existing server rather than as a new process. Without this Implementation the Capability has no running broker and the other two surfaces have nothing to talk to.

## Behavior

This Implementation inherits every Capability requirement (`tab-current-version`, `query-mode`, `canonical-ast`, `dtql-yaml-form`, `native-mode-representation`, `mode-transition`, `tab-connection-binding`, `mcp-agent-face`, `apply-change-payload`, `terminal-quiet-by-default`, `web-deterministic-edits`, `web-native-edits`, `query-parameters`, `web-history-and-revert`, `results-presentation`, `two-way-sync`, `candidate-options`, `deep-link-onboarding`, `local-loopback-access`, `read-only-queries`, `parity-matrix`). The requirements below specify the daemon-side realization and its deltas. The terminal-presentation requirements (`terminal-quiet-by-default`) and the Web UI's own rendering (`web-history-and-revert` client state) are realized by the agent skill and the `datatug-apps` features respectively; this daemon provides the server-side endpoints they consume.

### Daemon surface (extends `cli/serve`)

#### REQ: serve-adds-mcp-and-builder

`datatug serve` MUST expose, on the same daemon as the existing agent API (same host/port/lifecycle from [cli/serve](../cli/serve/README.md)), an MCP endpoint (streamable HTTP) for the AI-agent and the query-builder session API for the Web UI. Enabling the builder MUST NOT change the existing `cli/serve` defaults or break its current agent-API behavior.

#### REQ: mcp-builder-tools

The MCP endpoint MUST provide, at minimum, the tools `create_tab` (taking the tab's mode — `dtql` (default) or `native` — and its connection), `apply_change`, `inspect`, `convert_to_native`, and `run`, each addressing a tab by an explicit tab id (except `create_tab`, which returns one).

### Query-session state

#### REQ: tab-current-ast

For a `dtql`-mode tab the daemon MUST hold the current query as one canonical dalgo AST, in memory, keyed by tab id, with no prior-version history retained and no persistence across restarts. The AST MUST be rendered to the bound connection's native query language only on run (the SQL dialect for a SQL connection, the native query form for a document store). The "dalgo form" returned by `inspect` is the AST's 1:1 DTQL-YAML serialization. (Realizes the Capability's `tab-current-version` + `canonical-ast` + `dtql-yaml-form`.)

#### REQ: tab-native-text

For a `native`-mode tab the daemon MUST hold the connection's native query text plus its parameter set verbatim, in memory, keyed by tab id, and MUST NOT maintain a dalgo AST, parse, or rewrite the text — `run` executes it as-is against the bound connection. (Realizes the Capability's `native-mode-representation`.)

#### REQ: convert-to-native

`convert_to_native` MUST take a `dtql`-mode tab id, render its current AST to native text in the tab's dialect, set the tab to `native` mode holding that text, and discard the AST. The daemon MUST offer no inverse (`native`→`dtql`) operation. (Realizes the Capability's `mode-transition`.)

#### REQ: tab-connection-bind

`create_tab` MUST bind the new tab to exactly one project connection chosen at creation; that connection fixes the tab's native query language (SQL dialect or document-store form) for the life of the tab and MUST NOT change thereafter.

#### REQ: apply-change-full-query

`apply_change` MUST accept a tab id, the prose description, and the full resulting query, and MUST set the tab's current query to the supplied full query verbatim — the daemon MUST NOT re-derive it from the prose. In `dtql` mode the full query is the dalgo AST and the call SHOULD also carry the structured delta; in `native` mode it is the native text plus parameters and the delta is omitted. An `apply_change` MUST match the tab's mode. The prose and delta MUST be forwarded to subscribed Web UI clients (for their history) but MUST NOT be retained by the daemon.

#### REQ: tab-parameters

The daemon MUST hold a tab's named parameters and bind their values at run time, in both modes — as AST nodes in `dtql` mode, alongside the native text in `native` mode. Parameter values arriving from the Web UI MUST be applied to the tab's current query. (Realizes the Capability's `query-parameters`.)

### Web-facing API

#### REQ: http-structured-edits

For a `dtql`-mode tab the daemon MUST expose HTTP endpoints that accept structured deterministic edits (such as add/remove/select field-or-column, add filter, set ordering, select nested/sub-collection) and apply them to that tab's current AST, never accepting prose on these endpoints.

#### REQ: http-native-text-edit

For a `native`-mode tab the daemon MUST expose an HTTP endpoint that accepts replacement native query text (and parameter values) for a given tab id and adopts it verbatim as the tab's current query, without parsing or interpreting it.

#### REQ: ws-two-way-sync

The daemon MUST stream a tab's current query and results to subscribed Web UI clients over WebSocket, and on any change originating from the Web UI it MUST emit a "changed" signal that the agent observes by re-reading the tab via MCP. A change from either face MUST become visible to the other.

#### REQ: revert-set-current

The daemon MUST accept a Web-initiated operation that sets a tab's current AST to a caller-supplied prior version (the revert target). The daemon does not store history; it only adopts the supplied version as the new current AST.

#### REQ: run-row-limit

`run` (and Web-triggered execution) MUST honor a caller-supplied row limit, defaulting to 1000 when unspecified, and MUST support continued retrieval (a "load next N" cursor and a "load all" request) rather than server-side page numbers. The executed query MUST be re-runnable from its tab id without re-stating the conversation.

### Conversational disambiguation

#### REQ: candidate-options

`apply_change` MUST support submitting multiple candidate options for a tab. The daemon MUST hold the options as uncommitted candidates, execute a selected option's query on demand, and on commit set the tab's current AST to the chosen option and discard the rest.

### Onboarding and access

#### REQ: create-tab-deep-link

`create_tab` MUST return a deep link containing the query (tab) id, the daemon's reachable host, and a one-time code placed in the link fragment; the daemon MUST allow that code to be exchanged exactly once for the tab's session token.

#### REQ: loopback-token-cors

The daemon MUST bind to loopback, MUST require the valid session token on builder requests, and MUST permit the configured Web UI origin (`https://datatug.app` by default, overridable) via CORS so the hosted page can reach `http://localhost`.

### Safety and reuse

#### REQ: read-only-enforced

For `dtql`-mode tabs the daemon MUST reject any request whose evident intent mutates data or schema and MUST leave the tab's current AST unchanged (the AST cannot express mutation). For `native`-mode tabs, where the text is opaque, the daemon MUST execute every query through a read-only session/transaction on the connection so the engine itself rejects any mutating or DDL statement (insert/update/delete/DDL) — without parsing the native text. In neither case is a mutation allowed to take effect.

#### REQ: reuse-existing-execution

The daemon MUST execute queries through the CLI's existing query-execution and result path (the DAL / dalgo backend used by the `execute`/`console`/`queries` commands), not a parallel engine.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [cli/serve](../cli/serve/README.md) | Extends. The MCP endpoint and builder session API are added to the same `datatug serve` daemon, reusing its host/port/lifecycle and browser-launch. |
| [cli/execute](../cli/execute/README.md), [cli/console](../cli/console/README.md), [cli/queries](../cli/queries/README.md) | Reused for query execution and result data (REQ:reuse-existing-execution). |
| [cli/config](../cli/config/README.md) | Host/port defaults shared with `cli/serve`. |
| [cli/projects](../cli/projects/README.md) | Tabs bind to connections drawn from the served project(s). |
| Capability `serve-brokered-query-builder@github.com/datatug/datatug` | This Feature is its CLI (daemon) Implementation; the Web Implementation lives in `datatug-apps`, driven by the `query-builder` skill in `datatug-ai-skills`. |

## Acceptance Criteria

### AC: serve-exposes-mcp-and-builder (verifies REQ:serve-adds-mcp-and-builder)

**Given** a `datatug serve` daemon started against a project
**When** a client inspects the daemon's endpoints
**Then** the existing agent API is unchanged and both an MCP endpoint and the query-builder session API are reachable on the same host and port.

### AC: mcp-tools-available (verifies REQ:mcp-builder-tools)

**Given** a running daemon
**When** an MCP client lists tools
**Then** `create_tab` (accepting a mode and a connection), `apply_change`, `inspect`, `convert_to_native`, and `run` are present, and each (except `create_tab`) requires a tab id.

### AC: current-ast-no-history (verifies REQ:tab-current-ast)

**Given** a `dtql`-mode tab whose query has been changed several times
**When** the tab is inspected and then the daemon is restarted
**Then** before restart only the latest AST is returned (its dalgo form is DTQL-YAML) with no prior-version history, and after restart the tab no longer exists.

### AC: native-tab-text-verbatim (verifies REQ:tab-native-text)

**Given** a `native`-mode tab created with native query text and parameters
**When** the tab is inspected and run
**Then** the daemon returns and executes the native text unchanged and maintains no dalgo AST for it.

### AC: convert-discards-ast (verifies REQ:convert-to-native)

**Given** a `dtql`-mode tab
**When** `convert_to_native` is called
**Then** the AST is rendered to native text in the tab's dialect, the tab becomes `native` holding that text, the AST is discarded, and no inverse conversion tool exists.

### AC: parameters-bound-at-run (verifies REQ:tab-parameters)

**Given** a tab declaring a named parameter, in either mode
**When** the Web UI sets the parameter value and the tab is run
**Then** the value is bound at execution against the current query.

### AC: tab-bound-to-one-connection (verifies REQ:tab-connection-bind)

**Given** a tab created against a chosen connection
**When** the tab's query is run on any later turn
**Then** it executes against that one connection in that connection's dialect, which has not changed.

### AC: apply-change-adopts-full-query (verifies REQ:apply-change-full-query)

**Given** a `dtql`-mode tab with a current query
**When** `apply_change` is called with a tab id, prose, a structured delta, and a full resulting query (the AST)
**Then** the tab's current AST becomes the supplied full query verbatim, the prose/delta are forwarded to subscribed Web clients, and the daemon retains no history; and for a `native`-mode tab the same holds with the full native text (no delta).

### AC: web-structured-edit-applied (verifies REQ:http-structured-edits)

**Given** a `dtql`-mode tab with a current AST
**When** the Web UI posts a structured "add filter" edit for that tab id
**Then** the daemon applies it to the tab's current AST, and the endpoint rejects a prose payload.

### AC: web-native-text-adopted (verifies REQ:http-native-text-edit)

**Given** a `native`-mode tab
**When** the Web UI posts replacement native query text (and parameter values) for that tab id
**Then** the daemon adopts the text verbatim as the tab's current query without parsing it.

### AC: edit-syncs-both-ways (verifies REQ:ws-two-way-sync)

**Given** a Web UI subscribed over WebSocket and an agent on the same tab
**When** the Web UI makes an edit
**Then** the daemon streams the updated query/results to the Web UI and emits a "changed" signal such that the agent, on re-reading the tab via MCP, sees the update; and an agent `apply_change` likewise streams to the Web UI.

### AC: revert-sets-current (verifies REQ:revert-set-current)

**Given** a tab with a current AST
**When** the Web UI sends a revert operation supplying a prior version
**Then** the daemon sets the tab's current AST to that supplied version.

### AC: run-honors-row-limit (verifies REQ:run-row-limit)

**Given** a query whose result set exceeds the row limit
**When** `run` is invoked without an explicit limit
**Then** at most 1000 rows are returned, a "load next N" cursor and a "load all" request retrieve the remainder, and the same tab id re-runs the query without re-stating the conversation.

### AC: options-held-and-committed (verifies REQ:candidate-options)

**Given** an `apply_change` that submits multiple candidate options for a tab
**When** the user selects one option (executing its query on demand) and commits it
**Then** the tab's current AST becomes the chosen option and the other candidates are discarded.

### AC: create-tab-returns-deep-link (verifies REQ:create-tab-deep-link)

**Given** a running daemon
**When** the agent calls `create_tab`
**Then** the returned deep link contains the tab id, the daemon host, and a one-time code in the fragment that can be exchanged exactly once for the tab's session token.

### AC: loopback-token-and-cors (verifies REQ:loopback-token-cors)

**Given** the daemon bound to loopback
**When** a builder request arrives without a valid session token, and separately when the `datatug.app` origin makes a CORS preflight with the token
**Then** the tokenless request is refused and the configured Web UI origin is permitted.

### AC: mutation-refused (verifies REQ:read-only-enforced)

**Given** a `dtql`-mode tab with a current AST
**When** a request asks to delete or otherwise mutate data/schema
**Then** the daemon refuses it and the tab's current AST is unchanged.

### AC: native-mutation-blocked-at-engine (verifies REQ:read-only-enforced)

**Given** a `native`-mode tab whose text contains a mutating statement (e.g. `DELETE`/`DROP`)
**When** it is run
**Then** the daemon executes it through a read-only session/transaction and the engine rejects the mutation, so no data or schema change occurs and the daemon does not parse the native text.

### AC: execution-reuses-existing-path (verifies REQ:reuse-existing-execution)

**Given** a tab whose query is run
**When** the daemon executes it
**Then** it uses the CLI's existing execute/console query path and result representation, not a separate engine.

## Rehearse Integration

Every AC has a concrete daemon surface (MCP tools, HTTP/WS endpoints, in-memory tab state, token/CORS gating, refusal paths), so all are testable. Stub scaffolding under `_tests/` is deferred to the Plan/Implement phase so the stub set tracks the final task breakdown rather than being authored twice, consistent with the approach used by the `ai-query-builder` CLI Implementation in this repo.

## Not Doing / Out of Scope

- The Web UI itself (screen, controls, client-side history rendering) — realized by the `query-builder` and `ai-terminal-query-builder` features in `datatug-apps`; this daemon only serves the endpoints they consume.
- The agent's natural-language interpretation and terminal presentation (`terminal-quiet-by-default`) — owned by the `query-builder` skill in `datatug-ai-skills`; the daemon exposes structured tools only.
- Which refinement operations the agent guarantees vs reports unsupported — an agent/skill concern; the daemon stores and runs whatever valid read-only AST it is given.
- Query history and session persistence in the daemon — current versions only, in-memory, lost on restart.
- `native`→`dtql` conversion and a bespoke DTQL parser — the daemon renders DTQL-YAML (the AST's serialization) with a standard YAML serializer; native text is never parsed into the AST, and adopting an existing text language (PRQL/Malloy) is deferred.
- Remote/multi-user serve and link-sharing via reverse proxy — local single-session (loopback + token) this cycle; a post-MVP follow-on.
- Concurrency control for simultaneous agent + Web edits — last-write-wins on the tab's current query.
- Mutating/DDL execution — refused in `dtql` mode and blocked at the engine via a read-only session in `native` mode (REQ:read-only-enforced).

## Open Questions

- Is the builder always-on in `datatug serve`, or gated behind a flag (e.g. `--query-builder`) until it stabilizes?
- Transport specifics: SSE vs WebSocket for the Web live feed (the Capability leans WebSocket), and the exact MCP streamable-HTTP path on the daemon.
- Where the per-tab session token and one-time code live in the existing `cli/serve` auth model, and their lifetimes.
- Which native renderers and read-only-session mechanisms exist per driver (SQL read-only transaction vs document-store equivalents), and whether `convert_to_native` and nested/document AST rendering ship fully or Partial in the first cycle.
- Whether the DTQL-YAML schema is owned here or in `datatug-go-models` (shared with the AST definition).

---
*This document follows the https://specscore.md/feature-specification*
