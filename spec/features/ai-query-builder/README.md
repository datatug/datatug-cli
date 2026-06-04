# Feature: AI Query Builder

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/ai-query-builder?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/ai-query-builder?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/ai-query-builder?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/ai-query-builder?op=request-change) |

**Status:** Under Review
**Date:** 2026-06-04
**Owner:** alexander.trakhimenok
**Source Ideas:** —
**Supersedes:** —
**Implements:** specscore:feature/ai-query-builder@github.com/datatug/datatug

## Summary

The **CLI Implementation** of the AI Query Builder Capability (`specscore:feature/ai-query-builder@github.com/datatug/datatug`): an interactive `datatug` terminal mode that seeds and progressively refines a read-only query from natural language, printing the current query and rendering results in the existing CLI table UI. This Feature specifies only the CLI-specific surface and the deltas/limitations from the Capability; all platform-agnostic behavior is inherited from the Capability it `**Implements:**`.

## Problem

The AI Query Builder Capability defines *what* a conversational query builder must do on every surface. The CLI needs a concrete realization that fits a terminal: line-oriented input, printed SQL, and table-rendered results, reusing the CLI's existing query-execution and rendering machinery rather than inventing new UX. The CLI also leads the MVP, so it must state honestly which refinement operations it guarantees and which ship `Partial`.

## Behavior

This Implementation inherits every Capability requirement (`session-source-binding`, `current-query-state`, `nl-seed`, `nl-transformation-delta`, `query-inspectable`, `execution-mode-toggle`, `results-visible`, `read-only-queries`, `unresolvable-request`). The requirements below specify only the CLI-specific surface and the MVP limitations.

### Terminal surface

#### REQ: cli-interactive-mode

The CLI MUST expose the builder as an interactive mode (a `datatug` subcommand) that binds the session to a data source selected via the existing project/connection flags **before** the first request, then reads natural-language requests one per line until the user exits.

#### REQ: cli-query-display

Each turn, and on explicit request, the CLI MUST print the current query's generated SQL to the terminal under a distinguishing label or separator (e.g. a `-- SQL` header line preceding the query block) so it is unambiguously separable from result rows — this is how the CLI realizes the Capability's `query-inspectable` requirement.

#### REQ: cli-result-rendering

When the current query executes, the CLI MUST render its results using the existing CLI table-rendering used by the `console`/query commands, rather than a bespoke renderer.

### Execution mode in the terminal

#### REQ: cli-execution-mode-control

The CLI MUST realize the Capability's auto-run / manual-apply toggle through terminal affordances: an in-session command to switch modes and, in manual-apply mode, an explicit apply/run command that executes the pending current query. The CLI's default mode is **auto-run**, and this default MUST be stated in the command's help. (The exact form of the apply trigger — a command word, an empty-line submit, or a hotkey — is an Open Question.)

### MVP operation coverage

#### REQ: cli-guaranteed-operations

Against a single source, the CLI MUST support, end to end, these transformations: seeding a query, adding or removing a column, adding or removing a row filter, and changing ordering or row limit.

#### REQ: cli-partial-operations

Operations requiring cross-source or computed columns (e.g. an exchange-rate column) and anti-join filters (e.g. "hide users who made an order") are NOT guaranteed in this MVP. When such a request is made, the CLI MUST treat it under the Capability's `unresolvable-request` rule — reporting it as unsupported/partial and leaving the current query unchanged — rather than silently emitting an incorrect query. This is the `Partial` parity recorded in the Capability's Implementation Matrix.

## Acceptance Criteria

### AC: enter-interactive-mode (verifies REQ:cli-interactive-mode)

**Given** a DataTug project with a selected connection
**When** the user starts the AI query-builder subcommand
**Then** an interactive session is bound to that connection and accepts natural-language requests one per line.

### AC: sql-printed-each-turn (verifies REQ:cli-query-display)

**Given** an active session with a non-empty current query
**When** a turn completes
**Then** the current query's SQL is printed to the terminal under a distinguishing label or separator (e.g. a `-- SQL` header line) that separates it from any result rows.

### AC: results-render-in-table (verifies REQ:cli-result-rendering)

**Given** a current query that executes successfully
**When** results are returned
**Then** they are rendered with the existing CLI table UI used by the console/query commands.

### AC: mode-toggle-and-apply (verifies REQ:cli-execution-mode-control)

**Given** the AI query-builder subcommand
**When** the user inspects its help and uses the mode-switch command
**Then** the help documents auto-run as the default mode, a command to switch between auto-run and manual-apply is available, and an explicit apply/run command is available to execute the pending query.

(The deferral *behavior* of manual-apply mode is verified by the Capability's `AC:manual-mode-defers-execution`; this AC verifies only the CLI-specific affordances and help text.)

### AC: simple-refinements-work (verifies REQ:cli-guaranteed-operations)

**Given** a session seeded with "show the last 10 users who logged in" against one connection
**When** the user says "add a column for country" then "only users created this year" then "order by created date"
**Then** each transformation is applied to the current query and the refined query runs against that single source.

### AC: complex-op-reported-not-faked (verifies REQ:cli-partial-operations)

**Given** a current query over a users source with no exchange-rate data available in that source
**When** the user asks "add a column showing the exchange rate of the user's primary currency to EUR"
**Then** the CLI reports the operation as unsupported/partial and leaves the current query unchanged, rather than emitting an incorrect query.

## Rehearse Integration

Every AC has a concrete CLI surface (an interactive subcommand, printed SQL, table-rendered results, mode commands, refusal paths), so all six are testable — none skipped as subjective. Stub scaffolding under `_tests/` is deferred to the Plan/Implement phase so the stub set tracks the final task breakdown rather than being authored twice, consistent with the approach used by the upstream `capability-and-platform-implementations` Feature.

## Not Doing / Out of Scope

- Cross-source joins and computed columns, and anti-join filters — explicitly `Partial` for this MVP (REQ:cli-partial-operations); the Capability does not require them cross-surface.
- A non-interactive/batch "one-shot NL→SQL" CLI flag — this Implementation is the interactive refinement loop; a batch mode is a separate future Feature.
- The Web realization — tracked as `Planned` in the Capability's Implementation Matrix; not specified here.
- Choice of LLM provider and credential storage — an implementation/config detail resolved at Plan/Implement time (see Open Questions).
- Mutating/DDL queries — refused per the Capability's `read-only-queries` requirement.

## Open Questions

- Which `datatug` subcommand name hosts the mode (a new top-level command vs an extension of `console`)?
- Which LLM provider does the MVP use, and where are credentials read from relative to the DataTug project/config?
- In manual-apply mode, is the apply action a command word, an empty-line submit, or a configurable hotkey?

---
*This document follows the https://specscore.md/feature-specification*
