# Idea: db copy filtering and subsetting

**Status:** Approved
**Date:** 2026-05-14
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we let `datatug db copy` produce subsets of source data — filtering by table, row, and column with a portable predicate language — so callers like `db snapshot` can capture meaningful slices without owning their own copy engine?

## Context

`datatug db copy --from <url> --to <url>` is implemented (spec/features/cli/db/copy/, Implemented) but operates only on whole databases: every table, every row, every column. The sibling Idea `spec/ideas/db-snapshot-command.md` (Approved) makes config-file-driven snapshots its MVP bar, and that config file's only meaningful content is *which subset to capture* — recent customers, exclude audit_log, drop create_time columns. Without subsetting in `db copy`, snapshot has nothing useful to forward. Today, DALgo's `dal.StructuredQuery` surface already exposes `WhereField(name, op, value)` and `GroupCondition` (AND/OR composition), and the inGitDB backend executes structured queries natively; the SQL-text fallback path (`dal.NewTextQuery`) used by `dalgo2sql` for Postgres/SQLite handles the cases where StructuredQuery doesn't compile. Filtering is therefore not a green-field feature — the DALgo seam exists, and this Idea pins how `db copy` exposes it as a CLI surface and a config-file schema.

## Recommended Direction

Extend `db copy` with four orthogonal subsetting axes, all expressed as CLI flags AND mirrored 1:1 in a config-file schema for `db snapshot`'s forwarder. The four axes: (1) **Table selection** — `--include <t1,t2>` and `--exclude <t1,t2>` (mutually exclusive on a single invocation); table names not present on source are an error (exit 2). (2) **Row filters** — `--where <table>:<field>:<op>:<value>` (repeatable; multiple flags on the same table AND together), with a small fixed operator vocabulary (`=, !=, <, <=, >, >=, in, not_in, is_null, is_not_null`). Push-down to source via DALgo's `dal.WhereField`; the engine compiles structured predicates to either StructuredQuery (inGitDB) or SQL text (`dalgo2sql` for SQLite/Postgres). Complex OR-groups are NOT supported on the CLI — config-file YAML is the path for those, expressed as structured AND/OR groups. (3) **Row limits** — `--limit <table>:<N>` (repeatable); compiles to DALgo's Limit clause. No global default. (4) **Column subsetting** — per-table `--columns <table>:<c1,c2>` (whitelist) OR `--exclude-columns <table>:<c1,c2>` (blacklist, mutually exclusive on the same table), PLUS a global `--exclude-columns-global <c1,c2>` that silently no-ops on tables not having those columns (motivating use: drop `create_time,update_time` everywhere). Column subsetting is push-down: only the requested columns are SELECTed. **Referential integrity is explicitly out of scope** — if a row filter excludes parents whose children remain, the children may dangle. The user is responsible; this is a 'subset capture' tool, not a 'consistent subset' tool. **Pull-down filtering is also out of scope for MVP**: every supported backend (SQLite, Postgres, inGitDB) can push the structured surface; pull-down only matters when a future backend can't, and we'll address it then.

## Alternatives Considered

- **Raw SQL WHERE passthrough (`--raw-where users:"created_at > date('2025-01-01') AND active = TRUE"`).** Maximum expressivity. Lost because (a) it ties every snapshot to the source engine's SQL dialect (Postgres `NOW() - interval '7 days'` ≠ SQLite `date('now', '-7 days')`), (b) inGitDB-as-source has no SQL surface and would require its own path anyway, and (c) the parent `db-snapshot-command` Idea explicitly aims for *portable, versionable* snapshots — engine-locked WHERE clauses contradict that. A raw-SQL escape hatch may land as a follow-up if operator-vocabulary limits bite real users.
- **Table-include only (defer rows/columns/limit).** The 10×-simpler MVP. Lost because the parent Idea's `latest_customers.yaml` example requires row filters, and CI fixture generators almost always need limits. Table-only would unblock half of snapshot's MVP and force a follow-up Idea before snapshot can ship — net longer path than bundling all four axes here.
- **Pull-down filtering (read all rows from source, filter in copy engine).** Backend-agnostic. Lost because (a) every MVP backend (SQLite, Postgres, inGitDB) supports push-down via DALgo's existing surfaces, so pull-down is strictly slower with no portability win for the MVP set; (b) the canonical use case is "1000 recent rows from a million-row table" — pulling all million only to discard 999,000 is unacceptable.
- **Referential-integrity-aware subsetting (auto-include parent rows when their FK-referenced children are selected).** "Real" subsetting like Tonic.ai or jailer. Lost because it requires schema-graph traversal, transitive closure of FK relationships, and decision rules for cycles — an order of magnitude larger than this Idea. The MVP user accepts dangling references and resolves them out-of-band (or restructures the filter to keep parents).
- **Custom DSL for filters (jsonnet, CEL, expressions).** Familiar to ops tooling users (kubectl-style). Lost because YAML structured form + a small CLI mini-syntax covers the MVP cases without inventing a new language for users to learn.

## MVP Scope

A three-week spike landing all four subsetting axes against Chinook on both supported source backends (SQLite, inGitDB), with end-to-end E2E tests for: (a) `db copy --include Customer,Invoice --from sqlite:///chinook.db --to ingitdb://./out` (table-only); (b) `db copy --where Customer:Country:=:USA --where Customer:SupportRepId:=:3 --from ... --to ...` (row filter, AND); (c) `db copy --limit Invoice:100 --from ... --to ...` (limit); (d) `db copy --exclude-columns-global CreatedAt,UpdatedAt --columns Customer:CustomerId,FirstName,LastName --from ... --to ...` (column subsetting, global + per-table); (e) the four axes combined in one invocation. Both directions of the SQLite↔inGitDB pair MUST E2E. Postgres source/target is deferred (matches `db copy`'s current deferral). The DALgo `Limit` and field-projection coverage in `dalgo2sql` and `dalgo2ingitdb` are pre-MVP plan-time audits; missing methods land as upstream PRs in DALgo, not as local shims here.

## Not Doing (and Why)

- Referential integrity / consistent subsetting (auto-include rows referenced by selected children) — user-responsible; this is a capture tool, not a consistent-subset tool
- OR-groups on the CLI `--where` flag — only AND across repeated flags on a table; complex predicates go via config file YAML
- Raw SQL passthrough (`--raw-where`) — chosen against in favor of structured predicates for portability; revisit if real users hit operator-vocabulary limits
- Pull-down filtering (read all, filter in copy engine) — every MVP backend pushes; only relevant when a non-pushable backend lands
- Anonymization / data masking / column transformation — separate concern, distinct Idea if pursued
- Subqueries, joins, computed columns in WHERE — the structured surface doesn't support them, and we're not building a SQL frontend
- Global `--include` / `--exclude` defaults beyond columns — for tables, `--include` and `--exclude` are CLI-level, not config-vs-CLI-layered
- Wildcards or regex in table names (`--include 'log_*'`) — explicit list only; revisit if real users have many tables
- Operator extensions beyond the fixed vocabulary (e.g. `like`, `between`, `regex`) — explicitly deferred; the listed ten cover the MVP fixture use cases

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | DALgo's `dal.StructuredQuery` surface — `WhereField`, `GroupCondition` (AND/OR), `Limit`, and explicit field projection — compiles correctly to each MVP backend (`dalgo2sqlite`, `dalgo2sql` for Postgres, `dalgo2ingitdb`). | Plan-time audit: write a minimal `Query{Where: ..., Limit: N, Fields: [...]}` against each driver against the Chinook fixture; assert returned rows match the expected SQL semantics. Missing driver methods land as upstream DALgo PRs (no local shims). |
| Must-be-true | The fixed operator vocabulary (`=, !=, <, <=, >, >=, in, not_in, is_null, is_not_null`) covers ≥95% of real-world fixture-subsetting needs. | Walk the dominant snapshot use cases (latest-N-by-date, by-country, by-tenant, exclude-deleted, by-tag-membership) on Chinook plus one or two real DataTug projects; flag any case the vocabulary can't express. |
| Must-be-true | The CLI mini-syntax `--where <table>:<field>:<op>:<value>` parses unambiguously for the MVP operator set without requiring full shell-grammar escaping. | Prototype the parser; round-trip 20 representative cases including string values with spaces, dates, numerics, null/not-null forms; verify shell quoting on bash/zsh/PowerShell. |
| Must-be-true | The config-file YAML schema is a faithful 1:1 mirror of the CLI flag surface PLUS structured OR-group support — no semantic difference between "snapshot via flags" and "snapshot via config" except OR support. | Sketch `latest_customers.yaml` end-to-end; show its equivalent flag-only command; verify behavior identity except for OR-groups (CLI-only path goes through config). |
| Should-be-true | Push-down filtering is fast enough that "1000 latest customers from a 100k-row Customer table" completes in under 2 seconds on a developer laptop. | Benchmark on a scaled-up Chinook (10× or 100× rows) with `--where Customer:CustomerId:>:10000 --limit Customer:1000`; record wall-time. |
| Should-be-true | Column projection in `dalgo2sql` (Postgres) preserves type fidelity for the MVP Chinook fixture (no silent NULL-isation or coercion when only a subset of columns is SELECTed). | Plan-time spot-check: SELECT a subset, assert types match a full-row SELECT for each Chinook table. |
| Should-be-true | Global column exclusion (`--exclude-columns-global CreatedAt,UpdatedAt`) is the correct ergonomic — silently no-op on tables lacking the column, no error. | User-test the latest_customers use case end-to-end; if "silent no-op" produces surprise ("why didn't this column drop?"), add `--strict-columns` later. |
| Might-be-true | Users will hit the operator-vocabulary ceiling and request `like`, `between`, `regex`, or raw-SQL escape hatch. | Defer; revisit when a real user reports the limit. |
| Might-be-true | Referential-integrity-aware subsetting will be the dominant follow-up request. | Defer; file as a separate Idea only when fixture-orphan complaints arrive. |
| Might-be-true | Users will want filter expressions stored as reusable named profiles (`db copy --profile latest-customers`). | Defer; the config-file *is* a named profile, just stored as a file path. |


## SpecScore Integration

- **New Features this would create:**
  - **Plan-time choice between two layouts:** either extend `spec/features/cli/db/copy/README.md` inline with a new "Filtering and Subsetting" section (REQs `filter-tables`, `filter-rows`, `filter-columns`, `filter-limit`, plus shared `filter-cli-syntax` and `filter-config-schema`), OR create `spec/features/cli/db/copy/filtering/` as a child sub-feature. The Idea is agnostic; either landing satisfies the dependency from `db-snapshot-command`.
- **Existing Features affected:**
  - `spec/features/cli/db/copy/README.md` — Implementation Status note will change: rows are no longer always "all rows from source"; schema for what subsetting opts the engine accepts will be added. The five copy ACs (`sqlite-to-ingitdb-chinook-roundtrip` etc.) need amendment to clarify they assume *no* subsetting flags.
  - `spec/features/cli/db/README.md` — parent index gets a one-line note that `copy` now supports subsetting.
- **Dependencies:**
  - **`spec/ideas/db-snapshot-command.md`** (Approved) — this Idea is its hard-blocking dependency. snapshot's MVP config-file ACs cannot be met until this Idea promotes to Feature and ships.
  - **`dal-go/dalgo` `StructuredQuery` coverage** — `WhereField`, `GroupCondition`, `Limit`, field projection. Plan-time audit; upstream PRs land in `dal-go/dalgo` if any method is missing on a target driver. Sequencing mirrors the `cross-engine-db-copy` Idea's playbook: spec-and-prototype-first, replace-directive, tag-and-release.
  - **`dalgo2sql` structured-query-to-SQL compiler** — must compile `Limit` and field projection to Postgres/SQLite text. Plan-time verification; upstream PR if gaps exist.
  - **`dalgo2ingitdb`** — already executes StructuredQuery natively; coverage of `Where` + `Limit` + projection needs plan-time spot-check.

## Open Questions

- **CLI mini-syntax for `--where`.** Direction picks `<table>:<field>:<op>:<value>` (colon-delimited). Alternatives: `<table>.<field><op><value>` (no separator, harder to parse for multi-char ops like `!=`, `is_null`), JSON-per-flag (`--where '{"table":"users","field":"id","op":">","value":1}'` — verbose but bulletproof). Confirm at Feature-spec time.
- **Value escaping in `--where`.** Strings with `:` or shell-special characters need a documented escaping rule. URL-encoding? Backslash? Single-quote-enclosed? Pin at Feature-spec time.
- **Operator vocabulary scope creep.** The MVP lists ten. Adding `like`, `between`, `regex` post-MVP is easy on the structured side but may not push down portably (Postgres regex ≠ SQLite regex). Confirm the deferral wording in the Feature spec.
- **Config-file YAML schema.** Concrete shape (top-level `include`/`exclude`/`where`/`limit`/`columns` keys; nested `where:` as list-of-conditions per table with optional `or:` groupings). Owned by Feature spec.
- **Behavior when `--include` names a non-existent table.** Exit 2 with "table not found on source", or silently no-op (treat include as "intersect with available")? Direction is exit 2 (fail-loud); confirm at Feature-spec time.
- **Column subset interaction with primary key.** If `--columns Customer:FirstName,LastName` excludes the PK, what happens? Likely: PK is implicitly included even if excluded (target needs it for keying); pin at Feature-spec time.
- **Filter forwarding from `db snapshot --config`.** Owned by the parent snapshot Feature, but this Idea must commit to a stable flag surface so the forwarder is straightforward.
- **Where this Feature lands.** Inline extension of `db copy` Feature vs new child sub-feature `cli/db/copy/filtering/`. Plan-time decision; the Idea is agnostic.
- **Spec status of `db copy`'s existing ACs after this lands.** They become "with no filtering flags" baseline ACs; the new ACs cover the filtered cases. Cross-reference cleanup at Feature-spec time.

---
*This document follows the https://specscore.md/idea-specification*
