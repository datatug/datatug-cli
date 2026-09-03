---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Query

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/query?op=explore) | [Edit](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/query?op=edit) | [Ask question](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/query?op=ask) | [Request change](https://specscore.studio/app/github.com/datatug/datatug-cli/spec/features/cli/query?op=request-change) |

**Status:** Implementing
**Source Ideas:** —
**Date:** 2026-09-03
**Owner:** alex
**Supersedes:** —

## Summary

`query` is the resource group the [CLI umbrella](../README.md) reserves for running queries; `query run` is its first verb. It runs one ad-hoc [DTQL](https://github.com/dal-go/dalgo/tree/main/dtql) query against a database URL exactly as a policy-secured application would for a chosen principal: through DALgo's declarative access policies, discovered from the user's `~/.datatug/policies/` directory, so a "global" policy written once applies to every query the user runs. The rows a real caller would receive go to stdout in the chosen format; the limitations the policies imposed — the deciding rule, the row condition, the field allow-list, the binding — go to stderr, so a pipeline consumes clean data while the operator sees why rows or fields are missing. The SQL `execute` command can later join the same verb behind a `--sql` flag.

This is the first consumer of `dalgo/access` row-level conditions, principal bindings and field patterns outside DALgo itself.

## Problem

Policy authors have no way to see what a DALgo access policy will do to a real query for a given principal before the policy is deployed in an application, and analysts have no ad-hoc read path that honours policies at all: `execute` runs raw SQL and ignores them, so whoever can run the CLI sees every row and every column. `query run` executes a query as a secured application would for the principal named on the command line, prints the rows that caller would get, and reports which rule, condition and field list applied — so "customers assigned to you, never the `passwordHash`" can be written once and verified from the terminal.

## Synopsis

```
datatug query run --db <url> (-f <dtql-file>|--from <collection>) \
  [--format grid|json|jsonl|yaml|csv] \
  [--as <user-id>] [--role <role>]... [--group <group>]... [--var <name>=<value>]... \
  [--policy <file>]... [--policies-dir <dir>] [--no-policies] [-q|--quiet]
```

`-f/--file` is the input document, as on `entity add`; `--format` has no short alias on this command. `-q` is `--quiet`.

## Behavior

### Input

#### REQ: query-input

`query run` MUST accept the query either as a DTQL document (`-f <path>`, YAML or JSON; `-f -` reads stdin) or as `--from <collection>`, which selects every row and every field of one root collection. Exactly one of the two MUST be given; otherwise the command MUST exit 2 with a usage error. Parameters in the query document (`param:` nodes on the right-hand side of a `where` comparison) MUST be substituted from `--var` before execution; an unresolved query parameter MUST exit 2 naming `$name`. A parameter anywhere else (columns, the left-hand side, `orderBy`, `groupBy`, `having`) is not supported and MUST exit 2 naming it.

#### REQ: db-url

`--db <url>` MUST be required and MUST accept the same URL schemes as `db copy` (`sqlite://`, `ingitdb://`, `postgres://`). A missing or unparsable URL MUST exit 2. A database that cannot be opened MUST exit 4.

### Policies

#### REQ: policy-discovery

Policies MUST be discovered from a per-user directory: `--policies-dir <dir>` when given, else the `DATATUG_POLICIES_DIR` environment variable, else `~/.datatug/policies/`. Every regular file in that directory whose name ends in `.yaml`, `.yml` or `.json` MUST be loaded, in file-name order; `--policy <file>` MUST add further documents after the discovered ones, in flag order. Only the default `~/.datatug/policies/` MAY be absent; an explicitly configured directory that does not exist MUST exit 2 naming it. When zero policies are loaded and `--no-policies` is not given, the command MUST exit 2 with `no access policies loaded; pass --no-policies to run unrestricted`. `--no-policies` MUST skip discovery; combined with `--policy` it MUST exit 2. To test one document in isolation, point `--policies-dir` at an empty directory and pass `--policy`.

#### REQ: policy-documents

Each file MUST be a DALgo access document — an `AccessPolicy` or a principal-bound policy set (`ruleSets` + `bindings`) — decoded with `access.DecodePolicy`; any other kind (an `AuditPolicy`, for example) or a file that does not decode MUST exit 2 naming the file. All loaded policies MUST apply together, as DALgo intersects them: a row or field is visible only when every policy allows it.

#### REQ: principal-and-variables

`--as <user-id>` MUST set the principal ID (always a string) and the `$currentUser` variable; `--role` and `--group` (repeatable, valid without `--as`) MUST set the principal's roles and groups, so role bindings apply to an anonymous principal. `--var name=value` (repeatable) MUST define further variables for `$name`. A name MUST satisfy DALgo's parameter grammar (`dal.ValidParamName`); the names `currentUser`, `principal.*` and `path.*` are reserved and MUST exit 2, pointing at `--as`, `--role` and `--group`; `now` MAY be overridden to test time-bound rules. A value MUST be parsed as a YAML scalar or flow sequence, so `--var age=18` compares numerically, `--var open=true` as a boolean and `--var ids=[1,2]` feeds an `In` condition; a date stays the literal text; quote to force a string (`--var id="'007'"`); a mapping value MUST exit 2.

### Execution and output

#### REQ: secured-execution

The query MUST run through `access.SecureReadSession` over the opened database, so row conditions are pushed into the query and field allow-lists are projected or redacted by DALgo before any row reaches the command. A query whose columns, `where`, `orderBy`, `groupBy` or `having` reference a field that some deciding rule's `fields` list of some loaded policy refuses MUST be denied naming the field, so a caller cannot read a hidden field under another name or probe it through row counts; a query using any construct the check cannot classify MUST be denied too (fail closed). Because DALgo redacts by output name, column aliases (`as:`) MUST be refused with exit 2 whenever a loaded policy carries a field list. With `--no-policies` the query MUST run unrestricted and `access: running without access policies` MUST be written to stderr even under `--quiet`.

#### REQ: stdout-rows

Result rows MUST be written to stdout only, in the `--format` given: `grid` (default; aligned columns), `json` (one array of objects), `jsonl` (one object per line), `yaml` (a sequence of mappings) or `csv` (header row then rows). The record key MUST be emitted first as `$key`; it identifies the record and is not subject to field allow-lists. Then the record's fields: when the query names columns, those columns in request order, de-duplicated, minus any column that enforcement removed from every returned row (nothing is added in its place; an adapter may still return an unknown column as empty); when the query names no columns, the union of the returned fields in sorted order. Numbers MUST render plainly (`1000000`, never `1e+06`). A record whose data contains a field literally named `$key` MUST exit 1. Nothing else MUST be written to stdout. Row streams default to `grid` rather than the umbrella's YAML because they are looked at more often than parsed; `yaml` remains available.

#### REQ: stderr-report

Before execution, one line per loaded policy MUST be written to stderr, produced by `Policy.Decide` on a one-resource `Request` for the query's base collection, in this form: `access: policy "<name>" (<source>) rule "<rule>" allows|denies query on <resource>: <limitations>[ via <binding>]`, where `<limitations>` is `where <condition> [<var>=<value>, …]; fields [<list>]` — the condition as `access.Decision.Condition` renders it (`ownerID = $currentUser`), followed by the bindings of the variables the caller supplied and never a value the caller did not supply, and every distinct field allow-list a row may be bounded by — or `no limitations`; a denying line ends with the decision's explanation instead. Rule ids from principal-bound documents appear as `<ruleSet>/<rule>`. `-q/--quiet` MUST suppress these lines only.

#### REQ: denial

When a policy denies the query — including a missing variable and a hidden-field reference — nothing MUST be written to stdout, the denial's explanation MUST be written to stderr, and the command MUST exit 5 (the umbrella's code for access denied by policy).

#### REQ: exit-codes

Exit codes: 0 success; 2 usage or invalid input (flags, undecodable query or policy file, bad `--var`, unresolved query parameter, missing explicit policies directory, no policies without `--no-policies`); 4 the database cannot be opened or refuses the query; 5 access denied by policy; 1 writing the output failed or any other failure.

## Acceptance Criteria

### AC: query-from-collection (verifies REQ:query-input)

**Given** an inGitDB database with a `products` collection of three records
**When** the user runs `datatug query run --db ingitdb://<dir> --from products --format json --no-policies`
**Then** stdout is a JSON array of three objects each carrying `$key` and the record's fields, and the exit code is 0.

### AC: query-file-stdin (verifies REQ:query-input)

**Given** a DTQL document on stdin selecting `name` from `products` where `price >= 10`
**When** the user runs `datatug query run --db ingitdb://<dir> -f - --format json --no-policies`
**Then** stdout contains only the matching products with `$key` and `name`, and the exit code is 0.

### AC: input-mutually-exclusive (verifies REQ:query-input)

**Given** both `-f` and `--from`, or neither
**When** the user runs `datatug query run --db ingitdb://<dir> …`
**Then** the command exits 2 with a usage error and writes nothing to stdout.

### AC: query-param-substituted (verifies REQ:query-input)

**Given** a DTQL document whose `where` compares `price` with `{ param: minPrice }`
**When** the user runs it with `--var minPrice=10 --no-policies`, and again without `--var`
**Then** the first run returns the two products priced 10 or more, and the second exits 2 naming `$minPrice`.

### AC: db-url-required (verifies REQ:db-url)

**Given** no `--db` flag
**When** the user runs `datatug query run --from products`
**Then** the command exits 2 saying `--db` is required.

### AC: db-scheme-unsupported (verifies REQ:db-url)

**Given** `--db mongo://x`
**When** the user runs `datatug query run --db mongo://x --from products`
**Then** the command exits 2 naming the unsupported scheme.

### AC: policies-discovered-in-order (verifies REQ:policy-discovery)

**Given** `--policies-dir` holding the valid policies `10-global.yaml` and `20-extra.yml` plus a `notes.txt`
**When** the user runs a products query
**Then** the stderr report names the path of `10-global.yaml` before that of `20-extra.yml` and never mentions `notes.txt`.

### AC: policy-flag-appended (verifies REQ:policy-discovery)

**Given** `--policies-dir` pointing at an empty directory and `--policy pricing.yaml`
**When** the user runs a products query
**Then** the report names `pricing.yaml` and the run succeeds.

### AC: env-dir-used (verifies REQ:policy-discovery)

**Given** `DATATUG_POLICIES_DIR` set to a directory holding the global policy and no `--policies-dir`
**When** the user runs a customers query as alice
**Then** the report names the policy file under that directory.

### AC: explicit-dir-missing-exit-2 (verifies REQ:policy-discovery)

**Given** `--policies-dir` (or `DATATUG_POLICIES_DIR`) pointing at a directory that does not exist
**When** the user runs a query
**Then** the command exits 2 naming the directory and writes nothing to stdout.

### AC: no-policies-required-when-none (verifies REQ:policy-discovery)

**Given** a home directory without `~/.datatug/policies/` and no `--policy`
**When** the user runs a products query without `--no-policies`, and again with it
**Then** the first run exits 2 with `no access policies loaded; pass --no-policies to run unrestricted`, and the second returns every product.

### AC: no-policies-conflicts-with-policy (verifies REQ:policy-discovery)

**Given** `--no-policies` together with `--policy pricing.yaml`
**When** the user runs a query
**Then** the command exits 2 with a usage error.

### AC: invalid-policy-exit-2 (verifies REQ:policy-documents)

**Given** a policies directory containing a file that is not a valid access document
**When** the user runs a query
**Then** the command exits 2, stderr names the file, and stdout is empty.

### AC: var-invalid-exit-2 (verifies REQ:principal-and-variables)

**Given** `--var my-var=1`, `--var currentUser=bob`, `--var novalue` or `--var m={a: 1}`
**When** the user runs a query
**Then** the command exits 2 naming the offending variable.

### AC: var-typed (verifies REQ:principal-and-variables)

**Given** `--policy pricing.yaml` whose rule allows `query` on `/products` where `price >= $minPrice`
**When** the user runs `--from products --var minPrice=10`
**Then** only products priced 10 or more are returned and the report shows `[minPrice=10]`, proving the variable compared as a number.

### AC: rows-filtered-by-principal (verifies REQ:secured-execution)

**Given** the documented global policy (`docs/policies/global.yaml`) in the policies directory and `customers` records with fields `id, name, email, passwordHash, ownerID`, two owned by `alice` and one by `bob`
**When** the user runs `datatug query run --db ingitdb://<dir> --from customers --as alice --format json`
**Then** stdout contains alice's two customers, none of bob's, and the exit code is 0.

### AC: fields-hidden (verifies REQ:secured-execution)

**Given** the same policy and data
**When** the user runs the same query as alice
**Then** no object on stdout carries a `passwordHash` property, while `id`, `name`, `email` and `ownerID` are present.

### AC: hidden-field-filter-denied (verifies REQ:secured-execution)

**Given** the same policy and a DTQL document whose `where` is `passwordHash == "h1"`
**When** the user runs it as alice
**Then** stdout is empty, stderr names `passwordHash`, and the exit code is 5.

### AC: hidden-field-select-denied (verifies REQ:secured-execution)

**Given** the same policy and a DTQL document selecting `name` and `passwordHash`
**When** the user runs it as alice
**Then** stdout is empty, stderr names `passwordHash`, and the exit code is 5.

### AC: alias-refused-under-field-lists (verifies REQ:secured-execution)

**Given** the same policy and a DTQL document selecting `passwordHash` as `public_hash`
**When** the user runs it as alice
**Then** stdout is empty, stderr names `public_hash`, and the exit code is 2.

### AC: param-outside-where-exit-2 (verifies REQ:query-input)

**Given** a DTQL document whose `orderBy` is `{ param: col }`
**When** the user runs it with `--var col=name`
**Then** the command exits 2 naming `$col`.

### AC: no-policies-warns (verifies REQ:secured-execution)

**Given** `--no-policies --quiet`
**When** the user runs `datatug query run --db ingitdb://<dir> --from customers`
**Then** every customer including `passwordHash` is on stdout and stderr contains `running without access policies`.

### AC: report-names-limitations (verifies REQ:stderr-report)

**Given** the global policy and `--as alice`
**When** the user runs the customers query
**Then** stderr contains `access: policy "global" (<path>) rule "list-own" allows query on /customers: where ownerID = $currentUser [currentUser=alice]; fields [id, name, email, ownerID]`, and that line is not on stdout.

### AC: report-no-limitations (verifies REQ:stderr-report)

**Given** the global policy
**When** the user runs `--from products`
**Then** stderr contains `rule "list-all" allows query on /products: no limitations`.

### AC: quiet-suppresses-report (verifies REQ:stderr-report)

**Given** `--quiet`
**When** the user runs the customers query as alice
**Then** stderr is empty and stdout is unchanged.

### AC: denied-exit-5 (verifies REQ:denial)

**Given** the global policy and no `--as`
**When** the user runs `--from customers`
**Then** stdout is empty, stderr explains that `$currentUser` is unresolved, and the exit code is 5.

### AC: formats (verifies REQ:stdout-rows)

**Given** alice's two customers
**When** the user runs the query with `--format csv`, `jsonl`, `yaml` and `grid`
**Then** csv has the header `$key,email,id,name,ownerID` followed by two rows, jsonl has two JSON objects on two lines, yaml is a sequence of two mappings, and grid has a header line and two aligned rows.

### AC: header-follows-request-order (verifies REQ:stdout-rows)

**Given** the global policy and a DTQL document selecting `name` then `email` from `customers`
**When** the user runs it as alice with `--format csv`
**Then** the header is `$key,name,email` (request order, not the sorted union) and no `passwordHash` column appears.

### AC: numbers-plain (verifies REQ:stdout-rows)

**Given** a product priced `1000000`
**When** the user runs `--from products` in each of `csv`, `grid`, `yaml` and `json`
**Then** every output contains `1000000` and none contains `1e+06`.

### AC: db-unopenable-exit-4 (verifies REQ:exit-codes)

**Given** `--db ingitdb://<path-of-a-regular-file>`
**When** the user runs `--from products --no-policies`
**Then** stdout is empty and the exit code is 4.

## Architecture

- `pkg/accesspolicies` is the reusable core, so the TUI and `serve` can adopt it without the cobra layer:
  - `ResolveDir` (`--policies-dir` > `DATATUG_POLICIES_DIR` > `~/.datatug/policies`), `Load` (discovery, `--policy` files, the `--no-policies` rules and the zero-policies refusal), `LoadFile` (`access.DecodePolicy`, source = path).
  - `ParseVariables` (name grammar, reserved names, YAML scalar or sequence values).
  - `Run(ctx, session, query, Options) (Result, error)`: principal and variables on the context, query-parameter placement and substitution (`condeval.Substitute` + `dal.WithWhere`), the alias refusal and the hidden-field reference check over columns and clauses against every field list DALgo's decisions expose (`Decision.Writes`, mirroring DALgo's pattern matcher), `access.SecureReadSession`, and `Result{Reader, Lines}`; running with no policies requires `Options.Unrestricted`.
  - `Line{Policy, Source, Resource, Rule, Allowed, Condition, Bindings, FieldLists, Via, Explanation}` with `String()` rendering the pinned report format; `Explain` produces the lines from one `Decide` per policy.
- `apps/datatugapp/commands/cmd_query.go`: the `query` group (help only) and `run`: flags, DTQL/`--from` query construction, `pkg/dbcopy` URL backend, exit-code mapping, and row formatting (`query_output.go`: rows normalised to JSON-shaped maps; `grid`, `json`, `jsonl`, `yaml`, `csv`).
- `~/.datatug/` sits beside the umbrella's proposed `~/.datatug.yaml`; `DATATUG_POLICIES_DIR` follows its `DATATUG_*` naming.
- No change to DALgo.

## Error Handling and Failure Modes

- Undecodable query or policy document, unsupported document kind, bad `--var`, unresolved query parameter, missing explicit policies directory, zero policies without `--no-policies`, `--no-policies` with `--policy`: exit 2, naming the file, variable or directory.
- Denied by policy, including a missing variable, a hidden-field reference or a query the check cannot classify: exit 5; stdout stays empty because the denial is raised before any row is written.
- Column alias under a field-restricted policy, or a parameter outside a where right-hand side: exit 2.
- Database cannot be opened, or the adapter refuses the rewritten query (unsupported operator): exit 4 with the adapter's error.
- A data field named `$key`, or a write to stdout failing: exit 1.

## Rehearse Integration

Deferred — no `specscore rehearse` scenarios yet; the command tests below are the executable contract until a Rehearse corpus exists for this repository.

## Testing Strategy

- Command tests run the cobra command in-process against a temporary inGitDB database (pure Go, so they run without cgo) with the documented `docs/policies/global.yaml` copied into a temporary policies directory — proving the shipped example works verbatim.
- Unit tests for discovery order and filtering, the zero-policies and explicit-directory rules, `--var` grammar and typing, the hidden-field check, `Line.String()`, and each output format.
- SQLite is covered by the same command tests when cgo is available (CI), skipped otherwise.

## Out of Scope

- Writing data through policies (the command is read-only).
- Project-scoped policies (`<project>/policies/`) and policy management verbs (`datatug policy list|check`).
- Pagination and cursors; the query runs once and the rows are buffered before output.
- Joins: DTQL documents do not express them, so the report covers the base collection only.

## Open Questions

- Should the report also be available as JSON (`--report json`)? `Line` already carries the structure; expose it once a consumer asks.
