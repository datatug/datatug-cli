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
datatug query run --db <url|datatug://<project>/<db>[?env=<ENV>]> (-f <dtql-file>|--from <collection> [--limit <n>] [--offset <n>]) \
  [--format grid|json|jsonl|yaml|csv] \
  [--as <user-id>] [--role <role>]... [--group <group>]... [--var <name>=<value>]... \
  [--policy <file>]... [--policies-dir <dir>] [--no-policies] [-q|--quiet] \
  [--result-id <id> [--result-rows <n>]]

datatug query results locate --id <id> [--format json]
datatug query results check [--format json]
datatug query results prune [--all]
datatug query results rm --id <id>
datatug query results annotate --id <id> --prompt-stdin
```

`-f/--file` is the input document, as on `entity add`; `--format` has no short alias on this command. `-q` is `--quiet`.

## Behavior

### Input

#### REQ: query-input

`query run` MUST accept the query either as a DTQL document (`-f <path>`, YAML or JSON; `-f -` reads stdin) or as `--from <collection>`, which selects every row and every field of one root collection. Exactly one of the two MUST be given; otherwise the command MUST exit 2 with a usage error. Parameters in the query document (`param:` nodes on the right-hand side of a `where` comparison) MUST be substituted from `--var` before execution; an unresolved query parameter MUST exit 2 naming `$name`. A parameter anywhere else (columns, the left-hand side, `orderBy`, `groupBy`, `having`) is not supported and MUST exit 2 naming it.

#### REQ: limit-flag

`--limit <n>` MUST read at most `n` rows of a `--from` query (`n` > 0). With `-f` or a saved query it MUST exit 2, because those state their own limit; a negative value MUST exit 2. `0`, the default, means no limit.

#### REQ: offset-flag

`--offset <n>` MUST skip the first `n` rows of a `--from` query, with the same refusals as `--limit` (`-f` or saved query: exit 2; negative: exit 2). When `--limit` or `--offset` is given, rows MUST be ordered so that pages do not move: by the table's primary key columns (in key order) when it has one, otherwise by `rowid` for a SQLite table. A SQLite view and every other source (no primary key reported, or no way to describe it) keep the order the source gives, which is not guaranteed stable; callers paging such a source cannot rely on it.

#### REQ: db-url

`--db <url>` MUST be required and MUST accept the same URL schemes as `db copy` (`sqlite://`, `ingitdb://`, `postgres://`). A missing or unparsable URL MUST exit 2. A database that cannot be opened MUST exit 4. A PostgreSQL source that cannot be reached, whether at the open or in the middle of the read, MUST exit 4 with one line on stderr: the sentence the adapter chose for the failure, then where the connection string is read from, by the name of the variable (`the PostgreSQL connection string is read from the environment variable NAME` for an `env:NAME` source) or of the flag (`from the --db flag` for a URL), and nothing of the connection: no host, port, database, user, password, text of a driver or path of the machine. (`TestQuery_APostgresSourceIsOpenedReadOnlyAndItsFailuresAreClassified`, `TestQuery_AReadThatLosesItsConnectionShowsOneFixedSentence`.) A query on a PostgreSQL source orders NULLs first when ascending and last when descending, as every other source does, including over a column whose not-null constraint is not validated (PostgreSQL 18), where an `orderBy` with a `limit` returns the same rows as on a column with no such constraint; the job `Journey (PostgreSQL <major>)` runs it against a real server of each major version, 17 and 18. (`TestPostgresQueryJourneyOrdersNullsByDALgosRule`.)

#### REQ: db-project-reference

`--db` MUST also accept `datatug://<project>/<db>[?env=<ENV>]` (or `datatug:///<db>` for the current project), a reference to a database that is a source of a registered project: the grammar, the resolution (one function shared with `db schema`, `db table`, `db column`, `db copy` and `db export`), the exit codes (`3` for a project, environment or database that is not there; `2` for a malformed reference, a database several environments list, or a source that cannot be opened) and the rule that no path, URL or variable value is shown are those of [`db schema` REQ: project-reference](../db/schema/README.md#req-project-reference). The query is otherwise an ad-hoc one: `-f` or `--from`, policies and `--no-policies` apply as for any `--db`; the saved-query form (`--project` with `--query`) is unchanged and is mutually exclusive with `--db`. A PostgreSQL source resolved this way keeps the PostgreSQL rules of [REQ: db-url](#req-db-url).

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

### Result files

#### REQ: result-id

`query run` MUST accept `--result-id <id>` on the ad-hoc and the saved form. With it the rows MUST NOT be printed: the result goes to `~/.datatug/query-results/<id>/data.json` (the rows) and `meta.json` (everything else a viewer needs). The root is fixed, with no environment override, so a shell, a terminal and the daemon of one user always find the same files. `<id>` MUST match `^[A-Za-z0-9_-]{1,128}$`; an id that is already used MUST exit 2 (`result-id-in-use`), so a result is never overwritten. `--format` MUST be absent or `json`. `--result-rows <n>` (1 to 10000, default 2000) MUST be refused without `--result-id`. `query run` MUST refuse positional arguments.

#### REQ: result-store

The root and each result directory MUST be 0700 and owned by the user, the files 0600. A root inside a Git working tree or a bare repository (`inside-git`), a symlinked root or result directory (`symlink`), one owned by another user (`owner`) or one that cannot be written (`results-store-unwritable`) MUST exit 2 with nothing written. The store is checked before the database is opened. Every store error is a reason code that names no path and no id.

#### REQ: result-files-atomic

Each file MUST be written as an `O_EXCL` 0600 temporary file in the result directory, synced and renamed into place, `data.json` first and `meta.json` last. `meta.json` records the byte length of `data.json`; a result with no `meta.json`, or whose `data.json` has another length, is incomplete. A run that fails removes its result directory.

#### REQ: result-locator

With `--result-id`, stdout MUST be one JSON line with no id, no path and no row: `{"resultFiles":{"version":1},"rowsKept":<n>,"hasMore":<bool>,"columns":<n>}`. Any failure after the flags were parsed MUST print `{"resultFiles":null,"reason":"<code>"}` instead, with the usual exit code; the reason is a store code or `query-failed`, whose details stay on stderr. `query results locate --id <id>` MUST print `{"resultFiles":{"version":1,"id":…,"root":…,"data":…,"meta":…}}` for a complete, unexpired result and `{"resultFiles":null,"reason":"not-found"|"incomplete"|"expired"}` (exit 0) otherwise; `query results check` MUST print `{"usable":true}` or `{"usable":false,"reason":"<code>"}`. Neither opens a database.

#### REQ: result-data

`data.json` is `{"format":"datatug.query-result.data","version":1,"id":…,"recordset":<apicontract.Recordset>}`, with `$key` first and then the columns `stdout-rows` would print. Every cell is a typed value: integers are kept exact as text, binary becomes `<binary N bytes>` and a nested value its JSON text (recorded as the column's `encoding`), and a column whose values have several kinds is a string column listing them in `observedTypes`. The ad-hoc form MUST stop reading after `--result-rows` + 1 records; a saved query keeps the first `--result-rows` rows of its collected result. A cell over 64 KiB is cut on a UTF-8 boundary (counted in `cutCells`), and rows stop before the file would pass 4,000,000 bytes (`truncatedBy: bytes`).

#### REQ: result-meta

`meta.json` is `{"format":"datatug.query-result.meta","version":1,…}` with `id`, `createdAt`, `expiresAt`, `cliVersion`, `dataBytes`; `db` (`ref`, `project`, `env`, `source`: the full `datatug://<project>/<db>?env=<env>` reference of a source of a registered project, else `null`; and `driver`); `query` (`kind` `from`|`dtql`|`saved`, `dtql` as given or the equivalent document of `--from`, `from`, `savedQueryId` and `savedQueryType` for a saved query, typed `parameters`, and `policy` `none`|`discovered`|`dir`|`explicit`|`project`; and the editor-ready members `text`, `language`, `dbRef` and `params`: `text` is the canonical text the query editor accepts (the DTQL of an ad-hoc or `--from`/`--limit` run; for a saved query, the DTQL or native SQL it holds, which is the person's own saved text and is now copied here; none for a saved HTTP query), `language` is `dtql`|`sql`|`http`, `dbRef` is the `db.ref` reference (absent when the run was not against a registered project source; never a path, connection string or credential) and `params` are the run's own parameters as `[{name, value}]` sorted by name, `value` a typed scalar or set exactly as in `parameters`; `kind` stays `from`|`dtql`|`saved`, and a saved native SQL query is `kind: saved`, `language: sql`); `rows` (`kept`, `hasMore`, `truncatedBy`); `provenance` (`mode` and `observedAt` of an HTTP source, else `null`); `prompt` (`text`, `truncated`, `at`; absent until `query results annotate`, see `result-prompt`); and `columns` (`name`, `type`, `declaredType`, `nullable`, `pk`, `source`, `foreignKeys`, `encoding`, `cutCells`, `observedTypes`), `participants` and `related` (see `result-sources`). It MUST hold no row value, connection string, path, host, user, password, environment variable or policy path. `dtql` and `parameters` past 256 KiB together are dropped (`unavailable.query: too-large`); `text` and `params` have their own 256 KiB bound, past which only those two are dropped (no flag); at most 400 columns are described (`unavailable.columns: too-many`); the file stays within 1 MiB.

#### REQ: result-sources

`participants` MUST come from the query's own tree: one per source occurrence of the DTQL (for `--from`, its one-collection document), with `instance` (`root` or the join path), `schema`, `relation`, `alias`, `kind` (`table`|`view`|`unknown`), `role` (`from`|`join`), the `join` type and its `on` field pairs, and for a federated saved query each participant's own `db` reference. A saved native SQL query has none (`unavailable.participants` and `related`: `native-sql`); a saved HTTP query has one, its collection. `related` MUST come from the chat's join-candidate discovery over the foreign keys that DALgo's engine-neutral schema calls (`dbschema.ListCollections` and `DescribeCollection`, shared with the chat in `pkg/joinmeta`) report for each participant's database: `candidateId`, `fromInstance`, `target` (`schema`, `relation`, `kind`), `constraintId`, `direction`, `cardinality`, the ordered `fields` pairs and `evidence: foreign-key`. A relationship the query already joins is not listed. Nothing is filtered by permission. A provider without schema metadata, or a database that declares no foreign keys, gives `related: []` with `unavailable.related: no-relationship-metadata`; a schema that cannot be read gives `schema-unreadable`; over 500 collections gives `too-many`. A column that reads one source's field directly gets that field's `source`, portable `declaredType`, `nullable`, primary-key position and foreign keys, and its data type follows the declared type when every value fits it; where the definition cannot be read they stay `null` with `unavailable.declaredTypes`. At most 32 participants and 200 related are kept (`too-many`). Reading this metadata reads no row of any table.

#### REQ: result-prompt

`query results annotate --id <id> --prompt-stdin` MUST read UTF-8 text from stdin only (`--prompt-stdin` is required; the text is never an argument) and store it as `prompt: {text, truncated, at}` in the result's `meta.json`. It MUST drop control characters other than newline and tab and the Unicode format characters that can disguise text (bidi controls U+202A-202E and U+2066-2069, zero-width U+200B-200D, U+2060, U+FEFF, and the rest of category Cf) and U+2028/2029, replace invalid UTF-8 with U+FFFD, trim surrounding white space, and keep at most 8 KiB, cut on a character boundary (`truncated: true`; input past 1 MiB is also flagged). `at` is RFC 3339 in UTC. It MUST check the id before reading stdin: an id outside the grammar exits 2, an unknown id exits 3 whatever stdin holds, and a store that cannot be used or a result that is expired or incomplete exits 2 with a reason code. It MUST then refuse stdin that is an interactive terminal (exit 2, "pipe the text on stdin") without waiting for input, and empty text (exit 2). It MUST refuse a `meta.json` whose format or version it does not know (exit 2, `unsupported-version`) like every reader. It MUST replace `meta.json` atomically (private temporary file, sync, rename), leave the value of every other member as it was (the members are rewritten in alphabetical key order), overwrite an earlier prompt, and keep `meta.json` within its 1 MiB bound (room is reserved for the prompt when `meta.json` is first written). Nothing locks across processes: two annotations racing leave the last one, and one racing `rm` may exit 2; a failure to sync the directory after the rename is not reported. Its stdout is `{"id":…,"truncated":<bool>,"bytes":<n>}`; it MUST NOT print, log or put the text in an error. The prompt counts in the store's byte accounting, expires with the result, and `results rm` and `prune` remove it with the rest.

#### REQ: result-lifetime

A result lives 7 days. Every `--result-id` write MUST prune expired results, result directories without `meta.json` older than one hour, and then the oldest results while more than 200 results or 512 MiB are kept. `query results prune [--all]` prunes on demand (all results with `--all`) and `query results rm --id <id>` removes one (exit 3 when it is not there). Only directories whose name follows the id grammar are touched, after the symlink and owner checks; a link is never followed.

## Acceptance Criteria

### AC: query-from-collection (verifies REQ:query-input)

**Given** an inGitDB database with a `products` collection of three records
**When** the user runs `datatug query run --db ingitdb://<dir> --from products --format json --no-policies`
**Then** stdout is a JSON array of three objects each carrying `$key` and the record's fields, and the exit code is 0.

### AC: limit-caps-rows (verifies REQ:limit-flag)

**Given** a collection of three or more rows
**When** the user runs `datatug query run --db <url> --from <collection> --limit 2 --no-policies --format jsonl`
**Then** stdout holds exactly two rows; with `-f`, `--limit` exits 2.

### AC: offset-pages-rows (verifies REQ:offset-flag)

**Given** a SQLite table of five rows with primary key `id`
**When** the user runs `--from t --limit 2 --offset 2 --no-policies --format jsonl`
**Then** stdout holds the rows with `id` 3 and 4, in that order; a table with no primary key pages by `rowid`.

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

### AC: db-project-reference (verifies REQ:db-project-reference)

**Given** a registered project `shop` whose environment `local` lists the SQLite source `crm`
**When** the user runs `datatug query run --db datatug://shop/crm --from customers --no-policies`
**Then** the rows of `customers` are printed, as for `--db sqlite://<that file>`, and neither stdout nor stderr holds the path of the file; `--db datatug://nope/crm` exits 3.

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

### AC: result-files-written (verifies REQ:result-id, REQ:result-store, REQ:result-files-atomic, REQ:result-locator, REQ:result-data)

**Given** the Chinook SQLite fixture and an empty home directory
**When** the user runs `datatug query run --db sqlite://<chinook> --from Invoice --no-policies --result-id t1`
**Then** stdout is exactly the path-free locator line with `rowsKept` 412, `~/.datatug/query-results/t1/` is 0700 and holds 0600 `data.json` (412 typed rows) and `meta.json`, `query results locate --id t1` prints the paths and `query results check` answers `usable: true`. The same command again exits 2 with `result-id-in-use`; `--result-id ../x`, `--format csv`, a symlinked root and a home that is a Git working tree each exit 2 and write nothing, and the Git case is refused before the database is opened.

### AC: result-rows-capped (verifies REQ:result-data)

**Given** a source of 1,000,000 records and a table with a 70 KiB text cell
**When** the result is collected with the default `--result-rows`, and `--result-rows 5` is run on the table
**Then** the reader is asked for exactly 2001 records and 2000 are kept with `hasMore`; the table result keeps 5 rows, `truncatedBy: rows`, and the long cell is cut to 64 KiB with `cutCells: 1`.

### AC: result-meta-describes-the-run (verifies REQ:result-meta)

**Given** a DTQL file with a parameter, a `datatug://` reference of a registered project, an HTTP source and saved DTQL, SQL and HTTP queries
**When** each runs with `--result-id`
**Then** `meta.json` holds the query text, the typed parameters and the policy mode (never the policy path), the full reference with project, environment and source for the reference (and `null` for a path or an unregistered project), the provenance mode of the HTTP source (never its URL), and `kind: saved` with `savedQueryId`, `savedQueryType` and `policy: project` for the saved queries.

### AC: result-prompt-annotated (verifies REQ:result-prompt, REQ:result-meta)

**Given** a stored result and, for the old-reader case, one whose `meta.json` has no `text`, `language`, `dbRef`, `params` or `prompt`
**When** `printf '…' | datatug query results annotate --id t1 --prompt-stdin` runs, then again with 4000 three-byte characters, and with empty stdin, an unknown id and a bad id
**Then** the first answers `{"id":"t1","truncated":false,"bytes":<n>}` and `meta.json` gains `prompt` with every other member unchanged and mode 0600; the second keeps 8190 bytes with `truncated: true`; the others exit 2, 3 and 2; no output holds the text; the old `meta.json` still loads and `GET /datatug/results/<id>` answers its `query` and, when annotated, the `prompt` as text.

### AC: result-sources-and-related (verifies REQ:result-sources)

**Given** the Chinook SQLite fixture and the same tables on a PostgreSQL server
**When** `--from Invoice` and the DTQL `Invoice JOIN Customer ON Invoice.CustomerId = Customer.CustomerId` run with `--result-id`
**Then** `--from Invoice` relates `Customer` (outgoing, many-to-one) and `InvoiceLine` (incoming, one-to-many); the join lists `Invoice` (from) and `Customer` (join, with its `on` pair) and relates `InvoiceLine` and `Employee` (outgoing from Customer by `SupportRepId`), never the Invoice-Customer key it already joins; PostgreSQL gives the same lists; an HTTP source gives `related: []` with `no-relationship-metadata`; a saved native SQL query gives `native-sql`; a federated saved query's participants carry their databases' references; and no `meta.json` holds a row value, the database path, or the PostgreSQL user, host or password.

### AC: results-pruned-and-removed (verifies REQ:result-lifetime)

**Given** results that are fresh, expired, incomplete for over an hour, incomplete for a minute, more than 200, a link and a directory named outside the grammar
**When** `query results prune`, `prune --all` and `rm --id` run
**Then** expired and stale incomplete results and the oldest past the bounds are removed, the recent incomplete one, the link (and what it points to) and the other directory stay, and `rm` removes only the result named.

## Architecture

- `pkg/accesspolicies` is the reusable core, so the TUI and `serve` can adopt it without the cobra layer:
  - `ResolveDir` (`--policies-dir` > `DATATUG_POLICIES_DIR` > `~/.datatug/policies`), `Load` (discovery, `--policy` files, the `--no-policies` rules and the zero-policies refusal), `LoadFile` (`access.DecodePolicy`, source = path).
  - `ParseVariables` (name grammar, reserved names, YAML scalar or sequence values).
  - `Run(ctx, session, query, Options) (Result, error)`: principal and variables on the context, query-parameter placement and substitution (`condeval.Substitute` + `dal.WithWhere`), the alias refusal and the hidden-field reference check over columns and clauses against every field list DALgo's decisions expose (`Decision.Writes`, mirroring DALgo's pattern matcher), `access.SecureReadSession`, and `Result{Reader, Lines}`; running with no policies requires `Options.Unrestricted`.
  - `Line{Policy, Source, Resource, Rule, Allowed, Condition, Bindings, FieldLists, Via, Explanation}` with `String()` rendering the pinned report format; `Explain` produces the lines from one `Decide` per policy.
- `apps/datatugapp/commands/cmd_query.go`: the `query` group (help only) and `run`: flags, DTQL/`--from` query construction, `pkg/dbcopy` URL backend, exit-code mapping, and row formatting (`query_output.go`: rows normalised to JSON-shaped maps; `grid`, `json`, `jsonl`, `yaml`, `csv`).
- `pkg/queryresults`: the result store (root checks, atomic writes, locate, prune, remove), the collector that builds `data.json` within its bounds, and the `meta.json` types; `apps/datatugapp/commands/query_result_files.go` connects it to both forms of `query run`, and `cmd_query_results.go` is `query results`. The Git checks are `pkg/gitpath`, shared with `pkg/executionstore`.
- `~/.datatug/` sits beside the umbrella's proposed `~/.datatug.yaml`; `DATATUG_POLICIES_DIR` follows its `DATATUG_*` naming.
- No change to DALgo.

## Error Handling and Failure Modes

- Undecodable query or policy document, unsupported document kind, bad `--var`, unresolved query parameter, missing explicit policies directory, zero policies without `--no-policies`, `--no-policies` with `--policy`: exit 2, naming the file, variable or directory.
- Denied by policy, including a missing variable, a hidden-field reference or a query the check cannot classify: exit 5; stdout stays empty because the denial is raised before any row is written.
- Column alias under a field-restricted policy, or a parameter outside a where right-hand side: exit 2.
- Database cannot be opened: exit 4, naming the source as it is displayed (scheme, host, port and path, never a user name, a password or a query string) and a fixed reason; the driver's own text is never shown, because a driver can quote the connection string. A PostgreSQL source is the exception: it is not named by its display form (that holds the host and the port) and the line is the adapter's sentence and where the connection string is read from (see the `--db` requirement above). The adapter refuses the rewritten query (unsupported operator): exit 4 with the adapter's error.
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
