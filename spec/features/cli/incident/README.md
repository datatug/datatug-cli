---
format: https://specscore.md/feature-specification
status: Amending
---

# Feature: Incident

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/incident?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/incident?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/incident?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/incident?op=request-change) |
**Status:** Amending
**Date:** 2026-09-11
**Owner:** alex
**Source Ideas:** —
**Supersedes:** —
**Implements:** specscore://github.com/datatug/datatug/feature/incidents

## Summary

The **CLI Implementation** of the Incidents Capability (`specscore://github.com/datatug/datatug/feature/incidents`): first-class incident access for humans, scripts and AI agents through one singular resource namespace, `datatug incident`, on the same domain model and through the same server the Incidentius web UI uses. This Feature specifies only the CLI-specific surface — verbs, flags, output shapes, exit codes, id handling — not the event stream, projection semantics, assertion kinds or lifecycle rules, which are inherited unchanged from the Capability it `**Implements:**`.

## Problem

Vision §50 states the architectural rule directly: *"Every important incident-resolution capability should be API-first and, where appropriate, exposed through the DataTug CLI so humans, scripts and AI agents operate on the same model."* Without a pinned CLI contract, a dispatched agent (Synchestra, vision §52) and a human on-call responder would each need their own integration path, the two could drift, and a script consuming `watch` output would have no stable shape to parse. This Feature exists so `datatug incident` is exactly as reliable a surface as `/datatug/incidents/*` — same authorization, same data, same error semantics — and so the umbrella CLI's own conventions (singular namespace, explicit verbs, standard exit codes) are not re-litigated per command.

## Synopsis

```
datatug incident list [--status <status>]... [--query <id>] [--check <id>] [--board <id>] [--project <id>] [--store <storeId>] [--format grid|json|yaml] [--json]
datatug incident search <text> [--entity <Entity>.<Field>=<value>]... [--project <id>] [--store <storeId>] [--format ...] [--json]
datatug incident show <id> [--at <RFC3339>] [--project <id>] [--store <storeId>] [--format ...] [--json]
datatug incident similar <id> [--project <id>] [--store <storeId>] [--format ...] [--json]
datatug incident create --title <text> [--description <text>] [--from-investigation <id>] [--project <id>] [--store <storeId>]
datatug incident update <id> (--status <status> | --note <text>) [--project <id>] [--store <storeId>]
datatug incident context <id> add|promote|reject --entity <Entity> --field <field> --value <value> [--store <storeId>] \
    [--role affected|healthy_control|suspected|excluded|recovered] [--layer <layer>] [--project <id>]
datatug incident hypothesis <id> add --text <text> [--store <storeId>]
datatug incident hypothesis <id> set-state <hypothesisId> --state <state> [--store <storeId>]
datatug incident evidence <id> attach (--execution <recId> | --annotation <id> | --check-run <recId>) [--store <storeId>]
datatug incident link <id> (--recurrence-of <id> | --related <id>) [--store <storeId>]
datatug incident resolve <id> --outcome resolved|false-alarm|accepted|handed-off|unresolved [--store <storeId>]
datatug incident merge <duplicate-id> --into <id> [--project <id>] [--store <storeId>] [--json]
datatug incident access request <id> --source <sourceId> [--collection <name>]... --reason <text> [--until <RFC3339>] [--project <id>] [--store <storeId>] [--json]
datatug incident access approve|decline <id> <requestId> [--reason <text>] [--store <storeId>] [--json]
datatug incident access revoke <id> <grantId> [--reason <text>] [--store <storeId>] [--json]
datatug incident access list <id> [--store <storeId>] [--format grid|json|yaml] [--json]
datatug incident watch [<id>] [--json] [--since <cursor>] [--project <id>] [--store <storeId>]
```

`--project` follows the umbrella's `--project`/`--dir` resolution (`cli#req:project-or-dir-resolution`); every verb above accepts it. `--store <storeId>` selects the incident store when more than one is configured (default: the store configured for the current project; hub REQ:incident-store); every verb accepts it — `create`, `list`, `search`, `watch` to choose the store, and every verb that takes an `<id>` (`show`, `similar`, `update`, `context`, `hypothesis`, `evidence`, `link`, `resolve`, `merge`) to resolve a short id within that store. The incident store is routed by configuration, never by `--project`: `--project` instead scopes which DataTug project's evidence is read, and one incident MAY reference several projects (hub REQ:incident-store, REQ:multi-project-incidents).

| Verb | Purpose |
|---|---|
| `list` | List incidents, optionally filtered by `--status` (repeatable) or by derived back-link (`--query`, `--check`, `--board`). |
| `search` | Full-text search plus entity-fact filters (`--entity Customer.ID=5`). |
| `show` | Print one incident's projection, current or `--at` a past instant. |
| `similar` | Deterministic-overlap candidates for the given incident (ranking is NEXT, per the hub Feature — this Feature only pins the verb and output shape). |
| `create` | Open a new incident, optionally seeded `--from-investigation <id>`, with a free-text `--description`. |
| `update` | Change `--status` or append a `--note`; mutually exclusive per call. |
| `context` | Add, promote or reject an Investigation Context fact scoped to the incident, with an optional cohort `--role` and `--layer` (product-family.md §3, investigation-context). |
| `hypothesis` | Add a hypothesis or change an existing one's `--state`. |
| `evidence` | Attach an execution record, an annotation or a check run as evidence for a hypothesis. |
| `link` | Record a recurrence or a related-incident relationship. |
| `resolve` | Close the incident with one of the five outcomes (hub REQ:lifecycle-and-outcomes; vision §23). |
| `merge` | Append the duplicate's events into the surviving incident and close the duplicate with `mergedInto`; hub REQ:lifecycle-and-outcomes. |
| `access` | Request, approve, decline, revoke and list incident-scoped temporary read grants; see REQ:access-verbs. |
| `watch` | Stream the incident's (or the store's) event log; see REQ:event-cursor-watch. |

## Behavior

### Namespace and verbs

#### REQ: singular-namespace-and-verbs

Every incident capability MUST be exposed under one singular resource namespace, `datatug incident` (never `incidents`), matching `cli#req:singular-resource-names`. Each capability MUST be an explicit verb subcommand — `list`, `search`, `show`, `similar`, `create`, `update`, `context`, `hypothesis`, `evidence`, `link`, `resolve`, `merge`, `access`, `watch` — per `cli#req:verb-subcommands`. A bare `datatug incident` with no verb MUST print help and exit `0`; it MUST NOT perform an implicit default action (e.g. it MUST NOT default to `list`).

### Output

#### REQ: json-and-grid-output

Every read verb (`list`, `search`, `show`, `similar`, `access list`) MUST support `--format json|yaml|grid`. The default MUST be `grid` when stdout is a terminal and `json` otherwise, so an unredirected human run is readable and a piped/scripted run is machine-parseable without an explicit flag. **(lead assumption)** This inverts the umbrella's `cli#req:yaml-default-for-structured` default for this namespace specifically, because AI agents and scripts — this Feature's primary non-human consumers per vision §50 — expect JSON, not YAML, and a TTY check already gives humans a readable default. A `--json` boolean MUST be accepted as shorthand for `--format json`; passing `--json` together with `--format` set to anything other than `json` MUST exit `2`. Every `--format json` (or `--json`) output MUST reuse the server's own JSON field names and `TypedValue` shape for facts (`{type, value}`, per the transport appendix), never a CLI-renamed or CLI-flattened shape, so a script that already speaks the `/datatug/incidents/*` wire format can parse CLI output unchanged.

### Access requests

#### REQ: access-verbs

`datatug incident access` MUST expose the hub's incident-scoped read grants (hub `incidents` REQ:access-requests-in-incident; `server-enforced-access` REQ:incident-scoped-grants; the founder's 2026-09-11 words are quoted there — MVP inclusion is the founder's, the mechanism is the lead's) as five verbs and nothing more. `request <id> --source <sourceId> [--collection <name>]... --reason <text> [--until <RFC3339>]` MUST append `access.requested` for the calling principal and print the request id; `approve <id> <requestId>` and `decline <id> <requestId>` MUST append `access.approved` or `access.declined` and, when the caller is not an approver for the referenced project or the request id does not exist, MUST exit `1` with stderr and the echoed code indistinguishable between the two cases (REQ:exit-codes; the server decides, and the CLI never infers approver status); `revoke <id> <grantId>` MUST append `access.revoked`; `list <id>` MUST print pending requests and active grants with scope, requester, approver, expiry and grant id from the projection, never from a CLI-side cache. `request` MUST reject a malformed or past `--until` with exit `2`. Every verb proxies the server (`POST /datatug/incidents/{id}/access/*`, `GET …/access`); no verb evaluates policy locally. A read that wants the grant carries the incident: `datatug query run … --incident <id>` (umbrella `cli/query`, lead assumption for the flag name; the flag's own REQ and AC are a follow-up in that Feature, not this one) or a check run started from the incident sets `ExecutionRequest.incident`; a read without it is evaluated as before the grant. The grant id appears in the resulting execution record, never as a CLI flag of its own.

### Event stream

#### REQ: event-cursor-watch

`watch <id>` MUST stream the named incident's events; `watch` with no id MUST stream every incident event visible in the selected incident store (`--store`; vision §51). Mechanically, both MUST consume NDJSON from the server's own event stream — `GET /datatug/incidents/{id}/events?since=<cursor>` for a single incident, `GET /datatug/incidents/events?since=<cursor>` for the whole store (hub REQ:watch-event-cursor fixes both routes) — rather than the CLI polling a snapshot endpoint in a loop it invents itself. Each event on the stream MUST carry an opaque cursor; on a clean exit (EOF or Ctrl-C) the command MUST print the cursor of the last event it consumed on **stderr**, never stdout, so stdout stays event-only and a piped `--json` output stays parseable without the cursor line mixed in; a subsequent `--since <cursor>` resumes exactly after that event without re-emitting anything already seen. Default (no `--json`) rendering MUST be one line per event in the human-readable form from vision §51 (`HH:MM:SS  KIND  detail`, e.g. `10:43:02  HYPOTHESIS  H17 created: "FedEx acknowledgement failure"`); `--json` MUST print the raw event objects instead, one per line.

### Historical projection

#### REQ: show-at-replays-projection

`show <id>` with no `--at` MUST print the incident's current projection. `show <id> --at <RFC3339>` MUST instead reconstruct the projection by folding every event with a timestamp at or before `<RFC3339>`, using the identical deterministic fold the server's live projection uses (product-family.md D3) — never a separately maintained history table or a CLI-side approximation. A malformed `--at` value MUST exit `2`. This is how the CLI realizes vision §39's timeline replay without inventing new incident semantics.

### Server-backed execution

#### REQ: same-server-same-policy

Every verb MUST execute through the same trusted server boundary `datatug serve` exposes (product-family.md §2, "one execution and access boundary") — either an already-running `datatug serve` over HTTP, or, when none is running, the same in-process executor package `datatug serve` itself calls (mirroring how `query run --project --query` calls `secureread.Executor` directly rather than shelling out to HTTP, `apps/datatugapp/commands/cmd_query_run_saved.go`). **(lead assumption)** Which of those two modes is the CLI's default, and whether both are always available, is not fixed by this Feature and is an implementation choice for the Plan phase — the REQ only pins that no third path exists. No verb MUST write directly to an incident's on-disk event log (`incidents/<id>/events.jsonl`) or any other project file; every mutation is an event appended by the server's own incident-event API, so the append-only invariant (product-family.md D3) and every access policy the server enforces apply identically to a CLI caller and a browser caller. `--project`/`--dir` resolution MUST follow the umbrella's existing conventions (`cli#req:project-or-dir-resolution`, `cli#req:project-and-dir-mutually-exclusive`); no incident verb introduces a second project-selection mechanism.

### Exit codes

#### REQ: exit-codes

Every verb MUST use exactly three exit codes: `0` success; `2` usage error (bad flags, invalid enum value, mutually exclusive flags together, malformed `--at`); `1` every other failure — not found, access denied, and any server-side error — with the server's structured error `code` (the transport appendix's `error.code`, e.g. `NOT_FOUND`, `ACCESS_DENIED`) echoed on stderr so a script or agent can branch on the code string rather than the numeric exit alone. **(lead assumption)** This collapses the umbrella's separate `3`/`4`/`5`/`10` codes (`cli#req:standard-exit-codes`) into one `1` for this namespace: since every incident read and write is a proxied server call, the CLI cannot always tell a policy denial from a genuine not-found without itself disclosing which one it is (see `AC:denied-incident-exit-1-no-disclosure`), so a single generic-failure code paired with the echoed structured code carries the same information more honestly than guessing a numeric bucket. On any non-zero exit, stdout MUST stay empty (`cli#req:error-on-stderr`).

### Identifiers

#### REQ: agent-friendly-ids

Every verb that takes an `<id>` argument MUST accept both the store-scoped short form an incident is created with (`INC-12`) and the incident's full id as returned by `create`/`show`/`watch` JSON output, so a human typing a short id and an agent replaying an id it read from a prior JSON response both resolve to the same incident. The full id echoed by `create` is the canonical `IncidentRef` string form, `<storeId>/<incidentId>` (hub REQ:incident-references-and-back-links, which fixes the separator and requires that store ids contain no `/`; the wire spelling of the two fields is reconciled by the hub plan's model task); the short `INC-<n>` form resolves within the store selected by `--store` (default: the store configured for the current project). Resolution follows the same bare-vs-qualified pattern `query run --project --query` already uses for saved query ids (`api.ResolveQueryID`, `apps/datatugapp/commands/cmd_query_run_saved.go`): a short id that is ambiguous across more than one store in scope MUST exit `2` naming every candidate, never guess one.

### Lifecycle

#### REQ: status-and-outcome-vocabulary

Status and outcome values are not CLI-invented; they are exactly the hub Feature's vocabulary (hub REQ:lifecycle-and-outcomes). `datatug incident update <id> --status <status>` MUST accept exactly the seven hub statuses — `open`, `investigating`, `mitigating`, `recovering`, `resolved`, `watching`, `closed` — and an unrecognized value MUST exit `2` naming the allowed statuses. `datatug incident resolve <id> --outcome <outcome>` MUST accept exactly the five hub outcomes — `resolved`, `false-alarm`, `accepted`, `handed-off`, `unresolved`. `duplicate` is not a hub outcome: `--outcome duplicate` MUST exit `2` with a message pointing at `datatug incident merge <duplicate-id> --into <id>` instead. `merge` MUST append the duplicate's events into the surviving incident and close the duplicate with `mergedInto` set and no outcome (hub REQ:lifecycle-and-outcomes); merging an incident that is already merged a second time MUST exit `1` with the transport appendix's `INVALID_REQUEST` echoed (REQ:exit-codes; hub REQ:lifecycle-and-outcomes).

### Back-links

#### REQ: list-filters-by-back-link

`datatug incident list` MUST accept `--query <id>`, `--check <id>` and `--board <id>` as derived back-link filters, each restricting the listed incidents to those whose events reference the given query, check or board id (hub REQ:incident-references-and-back-links, `GET /datatug/incidents?query=<id>|check=<id>|board=<id>`). These are derived filters only, answering "which incidents touched this asset" from event history; recorded provenance (`origin.incident` on a promoted knowledge asset) is a hub-owned file field, not a separate CLI filter. Each filter MAY be combined with `--status` and `--store`.

## Acceptance Criteria

### AC: create-then-show-round-trips (verifies REQ:agent-friendly-ids)

**Given** a project with no incidents yet
**When** `datatug incident create --project demo --title "Checkout errors spike" --description "5xx rate above baseline" --json` runs, its stdout JSON `id` field is captured (e.g. `INC-1`), and then `datatug incident show INC-1 --project demo --json` runs using that short id
**Then** `show`'s JSON `title` and `description` match what `create` was given, `status` is `open`, and running `show` again with the full id echoed in `create`'s own output returns the identical projection.

### AC: access-request-approve-list (verifies REQ:access-verbs)

**Given** `INC-1` references the demo project, `support` may not read `Invoice`, and `admin` holds the project's incident-approver role (founder, 2026-09-11, verbatim: *"By default project admin has this role but can move it to someone else"*)
**When** `datatug incident access request INC-1 --source chinook --collection Invoice --reason "stuck invoices" --json` runs as `support` and prints `requestId`, then `datatug incident access approve INC-1 <requestId> --json` runs as `admin`, then `datatug incident access list INC-1 --json` runs
**Then** the list shows one active grant with scope `chinook/Invoice`, requester `support`, approver `admin` and an expiry; `datatug incident show INC-1 --json` reports the `access.requested` and `access.approved` events with those actors; and the same `approve` run as `support`, and an `approve` with an unknown request id, both exit `1` with stdout empty and stderr indistinguishable from each other.

### AC: status-follows-hub-sequence (verifies REQ:status-and-outcome-vocabulary)

**Given** an existing incident `INC-1`
**When** `datatug incident update INC-1 --status bogus-status` runs, and separately `datatug incident resolve INC-1 --outcome duplicate` runs
**Then** the first exits `2` naming all seven hub statuses (`open`, `investigating`, `mitigating`, `recovering`, `resolved`, `watching`, `closed`), and the second exits `2` naming the five hub outcomes and pointing at `datatug incident merge`.

### AC: merge-appends-events-and-closes-duplicate (verifies REQ:status-and-outcome-vocabulary)

**Given** `INC-2` is a duplicate of `INC-1` with two recorded events
**When** `datatug incident merge INC-2 --into INC-1 --json` runs
**Then** `INC-1`'s event stream contains `INC-2`'s two events tagged `incident.merged`, `datatug incident show INC-2 --json` reports `status` `closed` and `mergedInto` equal to `INC-1`'s `IncidentRef` (`{storeId, incidentId}`), and running the identical `merge` a second time exits `1` with error code `INVALID_REQUEST` (the hub defines no merge-specific code).

### AC: list-by-asset-back-link (verifies REQ:list-filters-by-back-link)

**Given** the check `stuck-invoices` was run under `INC-1` and never under `INC-3`
**When** `datatug incident list --check stuck-invoices --json` runs
**Then** the output contains `INC-1` and does not contain `INC-3`.

### AC: watch-resumes-from-cursor (verifies REQ:event-cursor-watch)

**Given** an incident with three recorded events and a first `datatug incident watch INC-1 --json` run interrupted after printing the second event, whose final stderr line is a cursor
**When** `datatug incident watch INC-1 --json --since <that-cursor>` runs against the same incident
**Then** only the third event (and any event appended afterward) is printed on stdout — the first two events are not re-emitted.

### AC: show-at-matches-fixture-belief (verifies REQ:show-at-replays-projection)

**Given** the canonical demo incident fixture, whose event log rejects hypothesis `H17` at `2026-09-11T10:43:51Z`
**When** `datatug incident show INC-1 --at 2026-09-11T10:43:40Z --json` runs, and separately `datatug incident show INC-1 --json` runs with no `--at`
**Then** the `--at` run's projection shows `H17` still in state `investigating` (before the rejection event), while the no-`--at` run shows `H17` in state `rejected`.

### AC: json-output-matches-api-envelope (verifies REQ:json-and-grid-output)

**Given** an incident whose Investigation Context carries a typed fact (e.g. `Order.id` equal to a numeric value)
**When** `datatug incident show INC-1 --json` runs with stdout piped to a file (non-TTY)
**Then** the fact appears in the JSON as `{type: "integer", value: "..."}` matching the transport appendix's `TypedValue` shape exactly, with no CLI-only field renaming, and running the same command with a TTY attached instead defaults to aligned `grid` output without `--format` being given.

### AC: similar-output-matches-ranked-signals (verifies REQ:json-and-grid-output)

**Given** the fixture incidents from the hub's `AC:search-finds-similar-by-entities` (INC-1 resolved, INC-2 open, overlapping signals)
**When** `datatug incident similar INC-2 --json` runs
**Then** the ranked incident list and matched-signal fields equal the `GET /datatug/incidents/{id}/similar` envelope byte for byte, with the top-ranked entry the same incident the API ranks first — the CLI pins only the verb and this output shape; the ranking algorithm itself is the hub Feature's `REQ:search-and-similarity`, not retested here.

### AC: denied-incident-exit-1-no-disclosure (verifies REQ:exit-codes)

**Given** two ids under the same project — one that does not exist, and one that exists but the principal `--as bob` is denied access to
**When** `datatug incident show <id> --project demo --as bob` runs for each id in turn
**Then** both runs exit `1`, stdout is empty in both cases, and the stderr message and echoed error code are indistinguishable between the two cases (neither confirms nor denies that the denied id exists).

### AC: usage-error-exit-2 (verifies REQ:exit-codes)

**Given** an existing incident `INC-1`
**When** `datatug incident resolve INC-1 --outcome not-a-real-outcome` runs
**Then** the command exits `2`, stderr names the invalid `--outcome` value and lists the five allowed outcomes, and stdout is empty.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [CLI](../README.md) | Parent. `incident` inherits shared flags, exit-code philosophy, and `--project`/`--dir` resolution; REQ:exit-codes is a documented, named deviation. |
| [query](../query/README.md) | `agent-friendly-ids` mirrors `query run --project --query`'s bare-vs-qualified id resolution; `same-server-same-policy` mirrors its local `secureread.Executor` path as an alternative to HTTP. |
| [serve](../serve/README.md) | The HTTP mode of REQ:same-server-same-policy calls the endpoints `serve` exposes; `watch` is a long-lived client of `serve`'s event stream. |
| Incidents (hub) | Owns identity, lifecycle, hypotheses, evidence relationships, recurrence, outcomes, the event stream and its projection; this Feature only wraps that model in a CLI surface. |
| Investigation Context (hub) | `context add/promote/reject` is a CLI entry point to the hub's cohort roles and layered overlays; this Feature defines no context semantics of its own. |
| Evidence records / Annotations / Checks (hub) | `evidence attach`'s three flags (`--execution`, `--annotation`, `--check-run`) reference artifacts those Features own; this Feature only attaches, never creates them. |

## Open Questions

- Should `datatug incident` also accept a hosted profile URL (`--server <url>`) so a caller can point the CLI at a remote `datatug serve` instead of a local project, ahead of the hosted-collaboration track (product-family.md B2/B3)? Tracked as NEXT, not MVP; this Feature assumes a local project (or a `datatug serve` on `localhost`) for every verb above.

---
*This document follows the https://specscore.md/feature-specification*
