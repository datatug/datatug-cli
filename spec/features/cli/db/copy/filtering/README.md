# Feature: `datatug db copy` filtering and subsetting

> [View in SpecStudio](https://specstudio.synchestra.io/project/features?id=datatug-cli@datatug@github.com&path=spec%2Ffeatures%2Fcli%2Fdb%2Fcopy%2Ffiltering) — graph, discussions, approvals

**Status:** Approved
**Source Idea:** [`db-copy-filtering`](../../../../../ideas/db-copy-filtering.md)
**Parent Feature:** [`cli/db/copy`](../README.md)

## Summary

Extends [`datatug db copy`](../README.md) with four orthogonal subsetting axes — table include/exclude, structured row predicates, per-table row limits, and column subsetting — all exposed both as CLI flags and as a YAML config-file schema. Row predicates compile to DALgo's `dal.WhereField` / `GroupCondition` and push down to the source backend's query engine; pull-down filtering is out of scope for MVP because every supported source backend (SQLite, inGitDB) can push. The CLI surface uses a colon-delimited mini-syntax (`--where <table>:<field>:<op>:<value>`) with a fixed ten-operator vocabulary; complex OR-groups are config-file-only. This Feature is what makes [`db snapshot`'s](../../../../../ideas/db-snapshot-command.md) config-file forwarder useful — without subsetting, snapshots can only capture whole databases.

## Synopsis

```
datatug db copy --from <url> --to <url>
                [--include <t1,t2,…>]        | [--exclude <t1,t2,…>]
                [--where <table>:<field>:<op>:<value>]…
                [--limit <table>:<N>]…
                [--columns <table>:<c1,c2,…>] | [--exclude-columns <table>:<c1,c2,…>]
                [--exclude-columns-global <c1,c2,…>]
                [--filter-config <path-to-yaml>]
                [<existing copy flags: --overwrite, --parallel-streams, --progress>]
```

## Problem

`datatug db copy` today copies whole databases: every table, every row, every column. The parent [`db-snapshot-command`](../../../../../ideas/db-snapshot-command.md) Idea makes config-file-driven snapshots its MVP, and that config file's only meaningful content is *which subset to capture* — recent customers, exclude `audit_log`, drop `create_time`/`update_time` columns globally. Without subsetting in `db copy`, snapshot's config-file surface has nothing useful to forward.

DALgo already exposes the surfaces needed: `dal.WhereField(name, op, value)` for field-op-value predicates, `dal.GroupCondition` for AND/OR composition, structured queries with limits and field projection. The inGitDB backend executes structured queries natively; the SQL-text fallback path (`dal.NewTextQuery`) used by `dalgo2sql` for Postgres/SQLite handles the cases where StructuredQuery doesn't compile. Filtering is therefore not a green-field feature — the seam exists. This Feature pins how `db copy` exposes it as a CLI surface and a config-file schema.

## Behavior

### Table selection

#### REQ: include-flag

The `--include` flag MUST accept a comma-separated list of source table names. When present, exactly those tables are copied; any source table not in the list is skipped. Table names MUST match source-table names exactly (case-sensitive per the source backend's collation rules); the implementation MUST NOT lowercase, normalize, or fuzzy-match.

#### REQ: exclude-flag

The `--exclude` flag MUST accept a comma-separated list of source table names. When present, every source table is copied EXCEPT those in the list. Matching rules are the same as REQ:include-flag (exact, case-sensitive).

#### REQ: include-exclude-mutex

`--include` and `--exclude` MUST be mutually exclusive on the same invocation. Supplying both MUST exit `2` (InvalidArgs) with a stderr message naming both flags. Combining them in the same config file (top-level `include:` AND `exclude:` keys both populated) MUST exit `2` with the analogous message.

#### REQ: table-not-found

If any name in `--include` or `--exclude` does not exist on the source after introspection, the command MUST exit `2` BEFORE any write, with stderr naming the unknown table(s). The check happens after source introspection (REQ:source-schema-via-dbschema in the parent Feature) and before any target writes.

### Row filters

#### REQ: where-cli-syntax

The `--where` flag MUST accept a colon-delimited 4-tuple `<table>:<field>:<op>:<value>` and MUST be repeatable. Field names follow source-side casing (REQ:include-flag). Values are decoded as strings; type coercion to the column's native type happens at predicate-compile time (REQ:where-type-coercion). A literal colon inside the value MUST be escapable with `\:`. Whitespace inside the value is preserved verbatim — shells are expected to quote values containing spaces.

#### REQ: where-and-semantics

When multiple `--where` flags target the same `<table>`, the predicates AND together. There is no way to express OR-groups on the CLI; users needing OR composition MUST switch to `--filter-config <path>` (REQ:config-file-schema).

#### REQ: operator-vocabulary

The fixed MVP operator vocabulary MUST be exactly:

| Token | Semantic | DALgo `dal.Operator` mapping |
|---|---|---|
| `=` | equals | `Equal` |
| `!=` | not equals | `NotEqual` |
| `<` | less than | `LessThan` |
| `<=` | less than or equal | `LessThanOrEqual` |
| `>` | greater than | `GreaterThan` |
| `>=` | greater than or equal | `GreaterThanOrEqual` |
| `in` | value-in-comma-list | `In` (value is comma-split inside the 4-tuple's `<value>` slot) |
| `not_in` | value-not-in-comma-list | `NotIn` |
| `is_null` | field IS NULL | `IsNull` (value slot is empty / ignored) |
| `is_not_null` | field IS NOT NULL | `IsNotNull` |

Any other operator token in `--where` MUST exit `2` with stderr listing the supported set.

#### REQ: where-type-coercion

Values are decoded by attempting type coercion against the column's introspected `dbschema.Type` in this order: integer, float, boolean (`true`/`false` case-insensitive), date (ISO-8601 `YYYY-MM-DD`), datetime (ISO-8601), string fallback. If coercion fails for the column's expected type, the command MUST exit `2` with stderr naming the table, field, attempted value, and expected type. `is_null` / `is_not_null` skip coercion entirely.

#### REQ: where-unknown-field

If the field name in a `--where` 4-tuple does not exist on the introspected source table, the command MUST exit `2` BEFORE any target write, with stderr naming the table, field, and offering the closest source-side field name (Levenshtein distance ≤ 2) if one exists.

### Row limits

#### REQ: limit-flag

The `--limit` flag MUST accept a colon-delimited 2-tuple `<table>:<N>` where `N` is a positive integer, and MUST be repeatable. At most one `--limit` per table is allowed; duplicates MUST exit `2`. Tables without a `--limit` entry have no row limit applied.

#### REQ: limit-compiles-to-dalgo-limit

`--limit <table>:<N>` MUST compile to DALgo's structured `Limit(N)` clause on the per-table read query. The implementation MUST NOT achieve limiting by reading more rows than requested and discarding the remainder (REQ:push-down-only).

### Column subsetting

#### REQ: columns-include-flag

The `--columns` flag MUST accept a colon-delimited form `<table>:<c1,c2,…>` and MUST be repeatable. When present for a table, only the listed columns AND the table's primary-key columns (REQ:columns-pk-implicit) are SELECTed from the source.

#### REQ: columns-exclude-flag

The `--exclude-columns` flag MUST accept the same `<table>:<c1,c2,…>` form. When present, the listed columns are excluded from the SELECT; all other columns (plus PK) are kept.

#### REQ: columns-mutex-per-table

For any single table, `--columns` and `--exclude-columns` MUST NOT both be specified. Conflict MUST exit `2` naming the conflicting table.

#### REQ: columns-pk-implicit

A table's primary-key column(s) are ALWAYS included in the SELECT, regardless of `--columns` whitelist contents or `--exclude-columns` blacklist contents. If a user explicitly lists a PK column in `--exclude-columns` for that table OR omits it from a `--columns` whitelist that names other columns, the implementation MUST exit `2` with stderr naming the table and the PK column, explaining that PKs cannot be subsetted (target writes need them as record keys).

#### REQ: columns-global-exclude

The `--exclude-columns-global` flag MUST accept a comma-separated column-name list. For each source table being copied, any column whose name matches an entry in this list is excluded from the SELECT. Tables that do not have any of the named columns are NOT an error — the global exclude silently no-ops per-table. PK columns named in `--exclude-columns-global` MUST be silently excluded from the global rule (PK protection per REQ:columns-pk-implicit overrides).

#### REQ: columns-global-interaction-with-per-table

`--exclude-columns-global` and per-table `--columns` / `--exclude-columns` compose by intersection: a column is SELECTed for a table if and only if (a) it survives the per-table rule (whitelist contains it, OR blacklist does not contain it, OR no per-table rule applies) AND (b) it is not in the global exclude list (unless it is a PK). The order of evaluation is per-table first, then global exclude trims.

#### REQ: columns-unknown-field

If `--columns` or `--exclude-columns` names a column not present on the introspected source table, the command MUST exit `2` BEFORE any write, with stderr naming the table and unknown column. `--exclude-columns-global` is exempt — unknown columns in the global list are silently ignored per REQ:columns-global-exclude.

### Push-down semantics

#### REQ: push-down-only

All filtering (table include/exclude, row WHERE, row LIMIT, column projection) MUST be applied at the source query level — compiled into the `dal.StructuredQuery` (or its `dal.NewTextQuery` fallback for `dalgo2sql`) that the source `dal.Adapter` executes. The implementation MUST NOT read full rows from the source and discard rows or columns in the copy engine. This rule is what makes "1000 latest customers from a 100k-row table" cheap; violating it would re-introduce the inefficiency the Idea explicitly rejected.

#### REQ: backend-coverage

For MVP, the implementation MUST support push-down for all four filter axes on both source backends in the parent Feature's E2E pair: `sqlite` (via `dalgo2sqlite`) and `ingitdb` (via `dalgo2ingitdb`). If a future backend's driver lacks coverage for an axis, the command MUST exit `1` BEFORE any write, with stderr naming the source backend and the unsupported axis. (Pull-down fallback is explicitly deferred per the Out of Scope section.)

### Config-file forwarding

#### REQ: filter-config-flag

The `--filter-config <path>` flag MUST accept a path to a YAML file containing filter directives. When present, the file is parsed and each directive is translated into the equivalent CLI-flag effect (REQ:config-cli-equivalence) before any other filtering takes effect.

#### REQ: config-cli-equivalence

A config file's effect MUST be identical to the equivalent flags-only invocation, with one exception: the YAML form additionally supports OR-groups in the `where:` section per REQ:config-or-groups. For every config key with a flag equivalent, the resolved (post-parse, pre-compile) `dal.Query` MUST be byte-identical to the flag-form. Mixing `--filter-config` with flag overrides of the same axis MUST exit `2` (no flag-vs-config merging in MVP).

#### REQ: config-file-schema

The config file's YAML schema MUST be exactly:

```yaml
# All keys optional. Empty/missing = no constraint.
include: [<table>, …]                    # mutex with `exclude`
exclude: [<table>, …]                    # mutex with `include`
where:
  <table>:
    - {field: <name>, op: <op>, value: <v>}    # AND across list entries
    - or:                                       # OR-group (config-only)
        - {field: …, op: …, value: …}
        - {field: …, op: …, value: …}
limit:
  <table>: <positive-int>
columns:
  global_exclude: [<column>, …]                 # mirrors --exclude-columns-global
  per_table:
    <table>:
      include: [<column>, …]                    # mutex with `exclude`
      exclude: [<column>, …]                    # mutex with `include`
```

Any unrecognized top-level key MUST exit `2`. Inside `where:<table>:`, each list entry MUST be either an object with `{field, op, value}` keys OR an object with a single `or:` key whose value is a list of `{field, op, value}` entries (max one level of OR nesting in MVP).

#### REQ: config-or-groups

OR-groups in `where:<table>:` MUST compile to `dal.GroupCondition{operator: Or, conditions: [...]}` wrapping the inner `WhereField` conditions, joined to peer (non-OR) entries on the same table via the outer AND (`GroupCondition{operator: And, conditions: [whereField, whereField, orGroup, …]}`).

### Interaction with existing `db copy` ACs

#### REQ: copy-acs-no-filter-baseline

The existing `db copy` ACs (`sqlite-to-ingitdb-chinook-roundtrip` and siblings) MUST remain valid as "no filtering flags" baseline behavior. When this Feature lands, the parent `cli/db/copy` Feature MUST be amended to note that those ACs assume no `--include` / `--exclude` / `--where` / `--limit` / `--columns` / `--exclude-columns` / `--exclude-columns-global` / `--filter-config` flag is present.

### Error handling and exit codes

#### REQ: exit-codes

| Exit code | Meaning |
|---|---|
| `0` | All copy work completed; filters applied as specified |
| `1` | Push-down unsupported on a source backend for an axis used (REQ:backend-coverage), or generic runtime error |
| `2` | Invalid filter flag (mutex violation, unknown operator, unknown table, unknown field, unknown column, type-coercion failure, PK in exclusion, malformed config, mixed config+flag) |

Exit codes `4` (connection failure) carry over unchanged from the parent Feature.

## Architecture

### Components

| Component | Responsibility | Lives in |
|---|---|---|
| `pkg/dbcopy/filter` (proposed) | CLI flag parsing for `--include`/`--exclude`/`--where`/`--limit`/`--columns`/`--exclude-columns`/`--exclude-columns-global`; YAML config parsing for `--filter-config`; compilation to `dal.Query` per source table | this repo (new package) |
| `pkg/dbcopy/filter/operator.go` (proposed) | Fixed operator vocabulary (REQ:operator-vocabulary); token → `dal.Operator` map; rejection of unknown tokens | this repo |
| `pkg/dbcopy/filter/coercion.go` (proposed) | Value-to-type coercion (REQ:where-type-coercion); uses introspected `dbschema.Type` | this repo |
| `pkg/dbcopy/engine.go` (existing) | Calls into the filter package to build per-table `dal.Query`; passes the resulting query to source `dal.Adapter.ExecuteQueryToRecordsReader` | this repo (modified) |
| `dal.QueryBuilder.Where` / `WhereField` / `Limit` / field-projection | DALgo structured-query construction primitives | `dal-go/dalgo/dal` (existing) |
| `dalgo2sqlite` query compiler | StructuredQuery → SQLite text for cases where StructuredQuery isn't executed natively | `dal-go/dalgo2sqlite` (existing; plan-time audit for completeness) |
| `dalgo2ingitdb` query executor | Native StructuredQuery execution against the file tree | `ingitdb/ingitdb-cli/pkg/dalgo2ingitdb` (existing; plan-time audit for Where + Limit + projection coverage) |

### Data flow

```mermaid
flowchart TD
    cli["CLI flags +<br/>--filter-config"]
    schema[("source DB schema<br/>(via dbschema introspection)")]
    parsed["filter directives<br/>(table-keyed)"]
    validate["validate against source schema<br/>(REQ:where-type-coercion,<br/>REQ:where-unknown-field,<br/>REQ:columns-unknown-field,<br/>REQ:table-not-found,<br/>REQ:columns-pk-implicit)"]
    compile["compile to dal.Query per table<br/>(Where + Limit + field projection)"]
    worker["per-table copy worker<br/>(existing engine — REQ:push-down-only)"]
    exit2(["exit 2<br/>(invalid filter)"])

    cli -->|parse| parsed
    schema -->|introspect| validate
    parsed --> validate
    validate -->|invalid| exit2
    validate -->|valid| compile
    compile -->|one Query per included table| worker
```

### Dependencies

- **`cli/db/copy`** (parent Feature, Implemented) — extends its CLI surface and its copy engine. This Feature does NOT change `db copy`'s URL-parsing, schema-introspection, target-DDL, or overwrite/concurrency rules; those continue to apply unchanged.
- **DALgo `dal.QueryBuilder` / `WhereField` / `Limit` / field projection** — `dal-go/dalgo` — **Plan-time audit required.** `WhereField` and `GroupCondition` exist (verified at Idea time). `Limit` and explicit field projection coverage across `dalgo2sqlite`, `dalgo2sql`, `dalgo2ingitdb` is the plan-time bar.
- **`dalgo2sqlite` StructuredQuery → SQLite SQL compiler** — `dal-go/dalgo2sqlite` — **Plan-time audit required.** Verify Where + Limit + projection compile to correct SQLite SQL for the Chinook fixture.
- **`dalgo2ingitdb` query execution** — `ingitdb/ingitdb-cli/pkg/dalgo2ingitdb` — **Plan-time audit required.** Verify Where + Limit + projection execute correctly against an inGitDB tree.

## Testing Strategy

E2E tests against the canonical Chinook fixture, exactly mirroring the parent Feature's bar: SQLite ↔ inGitDB in both directions, Postgres deferred. New test classes:

| Filter axis | E2E test target |
|---|---|
| `--include` (table whitelist) | `db copy --include Customer,Invoice` produces a target with exactly those two collections, row counts matching source |
| `--exclude` (table blacklist) | `db copy --exclude Genre` produces a target missing exactly that collection |
| `--where` (single condition) | `db copy --where Customer:Country:=:USA` produces a target Customer collection with exactly source's `WHERE Country = 'USA'` row count |
| `--where` (AND, multiple flags) | `db copy --where Customer:Country:=:USA --where Customer:SupportRepId:=:3` AND-composes |
| `--limit` | `db copy --limit Invoice:50` produces a target Invoice collection with exactly 50 rows |
| `--columns` (whitelist) | `db copy --columns Customer:CustomerId,FirstName,LastName` produces target Customer rows containing only those columns plus PK |
| `--exclude-columns` (blacklist) | `db copy --exclude-columns Customer:Email,Phone` produces target Customer rows missing those columns; PK preserved |
| `--exclude-columns-global` | `db copy --exclude-columns-global LastEditedTime` drops `LastEditedTime` from any source table that has it; tables without it are unaffected |
| Combined axes (REQ:config-cli-equivalence equivalence) | One invocation combines all four axes and a `--filter-config` invocation with the YAML equivalent — outputs MUST be byte-identical |
| OR-groups via config | A `--filter-config` with `or:` group in `where:Customer:` produces target rows matching `(condition1 OR condition2)`, AND-composed with peer conditions |

Unit tests cover: operator vocabulary (REQ:operator-vocabulary), type coercion (REQ:where-type-coercion), CLI mini-syntax parser including `\:` escape (REQ:where-cli-syntax), YAML schema parser including OR-group nesting limits (REQ:config-file-schema), mutex enforcement (REQ:include-exclude-mutex, REQ:columns-mutex-per-table), PK protection (REQ:columns-pk-implicit), unknown-field/column/table error paths (REQ:table-not-found, REQ:where-unknown-field, REQ:columns-unknown-field).

## Rehearse Integration

All ACs below are testable via `go test ./...` plus shell-driven E2E runs invoking the built `datatug` binary. No new external scaffolding beyond what the parent `cli/db/copy` Feature already requires. Per-AC Rehearse stubs will be scaffolded under `spec/features/cli/db/copy/filtering/tests/` as part of plan-time work.

## Out of Scope

Inherited from the source Idea, reinforced at Feature-spec time:

- **Referential-integrity-aware subsetting** — auto-including parent rows referenced by selected children. User-responsible; this is a "capture subset" tool, not a "consistent subset" tool. Filtered children may dangle.
- **OR-groups on the CLI `--where` flag** — config-file-only. REQ:where-and-semantics formalizes the CLI restriction.
- **Raw SQL passthrough** (`--raw-where`) — chosen against in favor of structured predicates for portability. Future Idea if real users hit the operator-vocabulary limit.
- **Pull-down filtering** (read all, filter in copy engine) — every MVP backend pushes; REQ:backend-coverage exits `1` on unsupported axes, no fallback.
- **Anonymization / data masking / column transformation** — column subsetting drops columns; it does NOT redact or transform values. Distinct concern.
- **Subqueries, joins, computed columns in WHERE** — the structured surface doesn't support them.
- **Wildcards or regex in table names** (`--include 'log_*'`) — explicit list only.
- **Operator extensions beyond the fixed ten** — `like`, `between`, `regex`, et al. deferred.
- **Postgres source/target** — deferred to match the parent Feature's current MVP scope (parent `db copy` defers Postgres until a Postgres DALgo driver lands `dbschema.SchemaReader` + `ddl.SchemaModifier` + `dal.ConcurrencyAware`).
- **Flag overrides over config (`--filter-config base.yaml --where extra:…`)** — REQ:config-cli-equivalence rejects mixing in MVP. Layered configs may revisit.
- **Mutating filter behavior at the target** — filters only narrow what is *read* from source; target writes use the standard parent-Feature path. No target-side filtering (e.g. INSERT-then-DELETE) is added.

## Assumption Carryover

From the source Idea:

| Idea assumption | Status at Feature time |
|---|---|
| Must-be-true: DALgo's `dal.StructuredQuery` surface — `WhereField`, `GroupCondition`, `Limit`, and explicit field projection — compiles correctly across MVP backends | Carried; plan-time audit work, NOT a REQ contract (REQ:backend-coverage codifies the runtime check) |
| Must-be-true: The fixed operator vocabulary covers ≥95% of real-world fixture-subsetting needs | Resolved; ten operators frozen in REQ:operator-vocabulary |
| Must-be-true: The CLI mini-syntax `<table>:<field>:<op>:<value>` parses unambiguously | Resolved; REQ:where-cli-syntax pins the colon-delimiter + `\:` escape |
| Must-be-true: Config-file YAML schema is a 1:1 mirror of CLI flags plus OR-groups | Resolved; REQ:config-file-schema + REQ:config-cli-equivalence pin both rules |
| Should-be-true: Push-down filtering is fast enough that "1000 latest customers from a 100k-row Customer table" completes in under 2 seconds | Carried; plan-time benchmark, NOT a REQ contract |
| Should-be-true: Column projection in `dalgo2sql` (Postgres) preserves type fidelity | Carried; plan-time audit on Postgres-deferred timing |
| Should-be-true: Global column exclusion silently no-ops on tables lacking the column | Resolved; REQ:columns-global-exclude pins silent behavior; unknown columns in `--exclude-columns-global` are also silent |
| Might-be-true: Users will hit the operator-vocabulary ceiling | Open (Out of Scope until evidence) |
| Might-be-true: Referential-integrity-aware subsetting will be the dominant follow-up request | Open (Out of Scope until evidence) |
| Might-be-true: Users will want filter expressions stored as reusable named profiles | Resolved: a config-file path IS the profile; no separate `--profile` flag needed |

## Acceptance Criteria

### AC: include-flag-narrows-to-listed-tables

**Requirements:** filtering#req:include-flag

**Given** a SQLite Chinook source at `./chinook.db` (11 tables) and an empty inGitDB target at `./out/`
**When** the user runs `datatug db copy --from sqlite:///./chinook.db --to ingitdb://./out --include Customer,Invoice`
**Then** the command exits `0`; `./out/` contains exactly two collections (`Customer/` and `Invoice/`); their row counts match source.

### AC: exclude-flag-skips-listed-tables

**Requirements:** filtering#req:exclude-flag

**Given** a SQLite Chinook source and an empty inGitDB target
**When** the user runs `datatug db copy --from sqlite:///./chinook.db --to ingitdb://./out --exclude Genre,MediaType`
**Then** the command exits `0`; `./out/` contains 9 collections (the 11 Chinook tables minus `Genre` and `MediaType`); row counts on remaining tables match source.

### AC: include-exclude-mutex-rejected

**Requirements:** filtering#req:include-exclude-mutex

**Given** any working directory
**When** the user runs `datatug db copy --from ... --to ... --include Customer --exclude Genre`
**Then** the command exits `2`; stderr names both `--include` and `--exclude` and explains they are mutually exclusive; no connection is attempted.

### AC: unknown-table-in-include-rejected

**Requirements:** filtering#req:table-not-found

**Given** a SQLite Chinook source (no table named `Users`)
**When** the user runs `datatug db copy --from sqlite:///./chinook.db --to ingitdb://./out --include Customer,Users`
**Then** the command exits `2` AFTER source introspection but BEFORE any target write; stderr names `Users` as the unknown table; `./out/` is unchanged.

### AC: where-single-condition

**Requirements:** filtering#req:where-cli-syntax, filtering#req:operator-vocabulary, filtering#req:push-down-only

**Given** a SQLite Chinook source where the `Customer` table has 13 rows with `Country = 'USA'`
**When** the user runs `datatug db copy --from sqlite:///./chinook.db --to ingitdb://./out --include Customer --where Customer:Country:=:USA`
**Then** the command exits `0`; the target `Customer/` collection contains exactly 13 records; the source query executed was a single SELECT with `WHERE Country = 'USA'` (push-down — verifiable by query log).

### AC: where-and-composition

**Requirements:** filtering#req:where-cli-syntax, filtering#req:where-and-semantics

**Given** a SQLite Chinook source where 4 customers satisfy both `Country = 'USA'` AND `SupportRepId = 3`
**When** the user runs `datatug db copy --from … --include Customer --where Customer:Country:=:USA --where Customer:SupportRepId:=:3`
**Then** the command exits `0`; the target `Customer/` collection contains exactly those 4 records.

### AC: unknown-operator-rejected

**Requirements:** filtering#req:operator-vocabulary

**Given** any source
**When** the user runs `datatug db copy --from … --include Customer --where Customer:Country:like:USA`
**Then** the command exits `2`; stderr names `like` as the unsupported operator AND lists the ten valid operators.

### AC: where-type-coercion-failure-rejected

**Requirements:** filtering#req:where-type-coercion

**Given** a SQLite Chinook source where `Invoice.Total` is `NUMERIC(10,2)`
**When** the user runs `datatug db copy --from … --include Invoice --where Invoice:Total:>:not-a-number`
**Then** the command exits `2` BEFORE any target write; stderr names the table `Invoice`, field `Total`, value `not-a-number`, and expected type `Decimal`.

### AC: where-unknown-field-rejected

**Requirements:** filtering#req:where-unknown-field

**Given** a SQLite Chinook source where the `Customer` table has no column named `CustomerName` (it has `FirstName` and `LastName`)
**When** the user runs `datatug db copy --from … --include Customer --where Customer:CustomerName:=:Alice`
**Then** the command exits `2`; stderr names the unknown field `CustomerName` and suggests `FirstName` (Levenshtein-closest source field).

### AC: limit-applies-per-table

**Requirements:** filtering#req:limit-flag, filtering#req:limit-compiles-to-dalgo-limit

**Given** a SQLite Chinook source where `Invoice` has 412 rows
**When** the user runs `datatug db copy --from … --include Invoice --limit Invoice:50`
**Then** the command exits `0`; the target `Invoice/` collection contains exactly 50 records; the source query executed used `LIMIT 50` (push-down).

### AC: duplicate-limit-rejected

**Requirements:** filtering#req:limit-flag

**Given** any source
**When** the user runs `datatug db copy --from … --include Invoice --limit Invoice:50 --limit Invoice:100`
**Then** the command exits `2`; stderr names the duplicated table `Invoice` and the conflicting values `50` / `100`.

### AC: columns-whitelist-narrows-projection

**Requirements:** filtering#req:columns-include-flag, filtering#req:columns-pk-implicit

**Given** a SQLite Chinook source where `Customer` has 13 columns including PK `CustomerId`
**When** the user runs `datatug db copy --from … --include Customer --columns Customer:FirstName,LastName`
**Then** the command exits `0`; target `Customer/` records contain exactly `CustomerId` (implicit PK), `FirstName`, `LastName` — three fields, no others.

### AC: columns-blacklist-drops-listed

**Requirements:** filtering#req:columns-exclude-flag

**Given** a SQLite Chinook source where `Customer` has 13 columns
**When** the user runs `datatug db copy --from … --include Customer --exclude-columns Customer:Phone,Email`
**Then** the command exits `0`; target `Customer/` records contain 11 fields (13 minus 2); `Phone` and `Email` are absent; PK is present.

### AC: columns-pk-explicit-exclusion-rejected

**Requirements:** filtering#req:columns-pk-implicit

**Given** a SQLite Chinook source where `Customer.CustomerId` is the primary key
**When** the user runs `datatug db copy --from … --include Customer --exclude-columns Customer:CustomerId`
**Then** the command exits `2`; stderr names the table `Customer`, the PK column `CustomerId`, and explains that PKs cannot be excluded.

### AC: columns-global-trims-per-table-whitelist

**Requirements:** filtering#req:columns-global-interaction-with-per-table

**Given** a SQLite Chinook source where `Customer` has columns including `CustomerId` (PK), `FirstName`, `Email`, and `LastEditedTime`
**When** the user runs `datatug db copy --from … --include Customer --columns Customer:FirstName,Email --exclude-columns-global Email,LastEditedTime`
**Then** the command exits `0`; target `Customer/` records contain exactly `CustomerId` (PK, implicit) AND `FirstName`; `Email` is excluded because the global rule trims it AFTER the per-table whitelist selects it; `LastEditedTime` is excluded by global (it would have been excluded anyway by the per-table whitelist, but the global rule applies).

### AC: columns-global-exclude-applies-where-present

**Requirements:** filtering#req:columns-global-exclude

**Given** a synthetic SQLite source where tables `T1` and `T2` both have a `CreatedAt` column but `T3` does not
**When** the user runs `datatug db copy --from … --exclude-columns-global CreatedAt`
**Then** the command exits `0`; target `T1/` and `T2/` records lack the `CreatedAt` field; target `T3/` is structurally unchanged (no error).

### AC: columns-unknown-rejected

**Requirements:** filtering#req:columns-unknown-field

**Given** a SQLite Chinook source where `Customer` has no column `NickName`
**When** the user runs `datatug db copy --from … --include Customer --columns Customer:NickName`
**Then** the command exits `2`; stderr names the unknown column.

### AC: config-cli-equivalence-byte-identical

**Requirements:** filtering#req:filter-config-flag, filtering#req:config-cli-equivalence

**Given** a SQLite Chinook source, a `filter.yaml` containing `include: [Customer]`, `where: {Customer: [{field: Country, op: '=', value: USA}]}`, `limit: {Customer: 5}`, `columns: {per_table: {Customer: {include: [FirstName, LastName]}}}` AND an equivalent flag form
**When** the user runs both forms against the same source, writing to two separate target directories
**Then** both invocations exit `0`; the two target directories are byte-identical (same files, same record contents, same row order modulo the parent Feature's per-table ordering rules).

### AC: config-and-flags-mixed-rejected

**Requirements:** filtering#req:config-cli-equivalence

**Given** a valid `filter.yaml` and any source
**When** the user runs `datatug db copy --from … --filter-config ./filter.yaml --where Customer:Country:=:USA`
**Then** the command exits `2`; stderr names both `--filter-config` and `--where` and explains that flag and config cannot mix in MVP.

### AC: config-or-group-composes

**Requirements:** filtering#req:config-or-groups

**Given** a SQLite Chinook source where 16 customers satisfy `Country = 'USA' OR Country = 'Canada'`
**When** the user runs `datatug db copy --from … --filter-config ./or.yaml` where `or.yaml` has `where: {Customer: [{or: [{field: Country, op: '=', value: USA}, {field: Country, op: '=', value: Canada}]}]}`
**Then** the command exits `0`; the target `Customer/` collection contains exactly those 16 records.

### AC: malformed-config-rejected

**Requirements:** filtering#req:config-file-schema

**Given** a `bad.yaml` containing an unrecognized top-level key (e.g. `subset: [Customer]` instead of `include:`)
**When** the user runs `datatug db copy --from … --filter-config ./bad.yaml`
**Then** the command exits `2`; stderr names the unknown key `subset`.

### AC: backend-without-pushdown-exits-1

**Requirements:** filtering#req:backend-coverage

**Given** a hypothetical or test-injected source backend that advertises support for table-list filtering but NOT for push-down `WHERE` (verifiable by stubbing the source `dal.Adapter` to return `dal.ErrFilterNotSupported` from `ExecuteQueryToRecordsReader` when the structured query contains a `Where` clause)
**When** the user runs `datatug db copy --from <stub-backend>://… --to … --where Customer:Country:=:USA`
**Then** the command exits `1` BEFORE any target write; stderr names the source backend AND the unsupported axis (`where`); the existing parent-Feature contract for unsupported-scheme rejection (exit `2`) is NOT triggered (this is a runtime capability gap, not a parse-time rejection).

### AC: existing-copy-acs-no-filter-still-pass

**Requirements:** filtering#req:copy-acs-no-filter-baseline

**Given** the parent Feature's `sqlite-to-ingitdb-chinook-roundtrip` AC fixture
**When** the user runs `datatug db copy --from sqlite:///./chinook.db --to ingitdb://./out` with NO filter flag and NO config
**Then** the command behaves identically to the parent Feature's existing AC (every Chinook table copied, all rows, all columns) — no regression introduced by this Feature's CLI surface.

## Outstanding Questions

- **Column ordering in target records.** When `--columns` whitelists a subset, do the target records preserve the listed order or the source's introspected order? Direction: source's introspected order (PK first, then whitelist order). Confirm at plan time.
- **Levenshtein suggestion threshold for `where-unknown-field`.** REQ:where-unknown-field says distance ≤ 2 — confirm this is the right ceiling, and whether to suggest ONLY the closest or ALL within threshold. Plan time.
- **`is_null` / `is_not_null` value-slot behavior.** REQ:operator-vocabulary says the value slot is "empty / ignored". Should `--where Customer:Country:is_null:foo` be silently accepted (value ignored) or rejected (exit 2 for extraneous value)? Direction: reject. Confirm at plan time.
- **YAML `value` type coercion.** When YAML scalars decode as native types (`5` → int, `true` → bool, `2025-01-01` → date), should the config-path skip the CLI's string-then-coerce pipeline OR funnel through it for behavior parity? Direction: funnel through coercion for parity. Confirm at plan time.
- **Verbose echo of compiled query under `--progress`.** When `--progress` is enabled, should stderr include the compiled `dal.Query` (or its SQL-text equivalent) for each table? Useful for debugging filters; would be a new progress-line type. Plan time.
- **Test fixture for `columns-global-exclude-applies-where-present` AC.** Chinook doesn't naturally have a `CreatedAt`/`UpdatedAt` pattern. The AC describes a "synthetic SQLite source". Plan time: build the fixture or rewrite the AC against an existing Chinook column that appears in some but not all tables.

---
*This document follows the https://specscore.md/feature-specification*
