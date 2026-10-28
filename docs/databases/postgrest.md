# Query PostgREST

> Post-release source documentation. The PostgREST source is wired into the CLI
> in this checkout and is not part of a released `datatug` binary yet, and it is
> not listed as a supported database on the website. Hosted Supabase is a
> separate, manual acceptance gate (see [Hosted Supabase](#hosted-supabase)).

DataTug reads the native PostgreSQL tables and views a [PostgREST](https://postgrest.org/)
API exposes, through the [dalgo2postgrest](https://github.com/dal-go/dalgo2postgrest)
adapter. It does not speak the PostgreSQL wire protocol, does not create a DALgo
storage layout and never writes. Supabase REST is the same protocol: a Supabase
project's `/rest/v1` endpoint is read with the same source and adapter.

## Endpoint and credentials

```sh
export DATATUG_POSTGREST_BEARER='<short-lived access token>'
export DATATUG_POSTGREST_ITEMS='postgrest://api.example.com/rest/v1?schema=app'

datatug query run --db env:DATATUG_POSTGREST_ITEMS -f items.tugql.yaml --no-policies --format json
```

The URI contract is:

| URI | Meaning |
| --- | --- |
| `postgrest://HOST[:PORT][/API-PATH]` | PostgREST over HTTPS (the default) |
| `postgrest+https://HOST[:PORT][/API-PATH]` | The same, spelled explicitly |
| `postgrest+http://HOST[:PORT][/API-PATH]` | Plain HTTP, accepted only for `localhost`, `127.0.0.1` or `::1` |

`API-PATH` is the API root below the host: empty for a local PostgREST server,
`/rest/v1` for a Supabase project (or any deployment mounted under a prefix). It
must be plain path segments with no trailing slash, no `.`/`..` segment and no
percent-encoding. Credentials, a fragment and an empty query are refused before
any request. `http://` and `https://` keep their existing meaning in the CLI (a
local DataTug project directory), so they are not a PostgREST endpoint.

Two non-secret options may be given in the URL:

| Option | Meaning | Default |
| --- | --- | --- |
| `schema=NAME` | The PostgREST schema profile sent as `Accept-Profile` | the server's exposed schema |
| `id=COLUMN` | The unique column that carries a record's identity | `id` |

Both must be simple SQL identifiers (`[A-Za-z_][A-Za-z0-9_]*`, at most 63
bytes). An unknown option, an option given twice, or an empty value is refused
before the endpoint is opened: no option is silently ignored.

Credentials never belong in the URI, a project file, a catalog or an error. The
adapter reads them for every request from the operator's environment:

- `DATATUG_POSTGREST_BEARER`: sent as `Authorization: Bearer <token>`.
- `DATATUG_POSTGREST_API_KEY`: sent as `apikey: <key>`, which Supabase requires
  in addition to the Bearer token.

Either may be absent. With none set the request is anonymous and PostgREST's own
`401`/`403` (or the anonymous role's grants) decides. Both may be set together.
A value containing CR, LF or NUL is refused before the first request; an error
names the variable and never its value.

Use a database role with only the `SELECT` grants the source needs, and the
matching row-level-security policies. The adapter exposes no write path, but the
database role remains the authority for what can be read, and PostgREST applies
that role to every request.

## Queries

One root relation is read per query; PostgreSQL tables and views are `from`
names. A structured TugQL file can filter, project and order native columns:

```yaml
from: {name: items}
columns:
  - {field: title}
  - {field: qty}
  - {field: owner}
where:
  op: '>='
  left: {field: qty}
  right: {value: 10}
orderBy:
  - {field: qty}
limit: 50
```

```sh
datatug query run --db env:DATATUG_POSTGREST_ITEMS -f items.tugql.yaml --no-policies --format json
```

Supported: one root relation; top-level comparisons (`==`, `>`, `>=`, `<`,
`<=`, `IN`, `NOT IN`), explicit `IS NULL` / `IS NOT NULL`, and nested `AND`/`OR`;
projection with plain fields and aliases; ordering; offset; and a positive
limit. The configured ID column is appended as an id tie-breaker when the query
does not order by it, so offset paging is stable across pages. A physical column
named like a PostgREST control (`limit`, `offset`, `select`, `order`, `or`, ...)
is targeted with PostgREST's quoted filter syntax instead of being consumed as a
control.

PostgREST cannot answer an unbounded read: a source requires a positive limit
for every query, and DataTug never sends `count=exact`, never hides a count and
never asks for a resource embedding. Because of that, an exploration `--from`
read with no `--limit` gets a visible default bound of 100 (stated on stderr)
instead of a full scan. An explicit `--limit 0` stays explicit and fails.

A page may be shorter than requested when the server's `db-max-rows` setting is
lower than the requested page size. That is not the end of the data: the adapter
verifies `Content-Range` and continues from the next offset, so a limit larger
than the server's page size is read as several physical pages. If a query's page
count exceeds the adapter's request budget it fails rather than returning a short
success.

Not supported: joins, grouping, aggregation, `HAVING`, subqueries, cursors, raw
SQL, RPC functions, resource embedding, full-text search and writes. DALgo's
local aggregation fallback removes the caller's limit and asks for unbounded
source rows; the adapter refuses that rather than computing a partial aggregate,
so an aggregate query fails explicitly. Copying into PostgREST is refused before
any request.

Numeric and null semantics are the adapter's: JSON integers that fit `int64`
stay `int64`, larger integers and decimals stay exact JSON numbers, and an
explicit JSON `null` is distinct from a missing field and from an empty string.
The CLI's existing result writer normalizes record values through its JSON
output path, so an exact decimal or an integer beyond 2^53 can print in the
CLI's chosen format as a JSON number rather than the adapter's typed value; this
is the same behavior every existing provider has in `query run`.

## Saved queries and access policies

A PostgREST catalog records a connection descriptor, never an API root and never
a credential. The descriptor names the operator's environment variable, and the
API root itself is read only from that variable when the source is opened:

```json
{
  "driver": "postgrest",
  "path": "connections/local/items.json",
  "dbModel": "items"
}
```

```json
{
  "dsnEnv": "DATATUG_POSTGREST_ITEMS"
}
```

`datatug query run --project <dir> --query <id>` resolves that catalog through
the same `api.ResolveCatalogSourceURL` path as the other providers: it reads the
descriptor from inside the project, rejects a literal endpoint or an
`env:`/URL-shaped path, applies the same `DATATUG_*`/`DATATUG_DSN_ENV_ALLOW`
variable-name rule as a PostgreSQL descriptor, and refuses a variable that is
not set or that holds another scheme before any request is made. A cloned
project therefore cannot point the operator's PostgREST credentials at a host of
its own choosing. Credentials still come from the operator's
`DATATUG_POSTGREST_*` variables; the project never holds them.

DataTug access policies work on the relation name; a policy can allow rows by a
condition and allow-list the fields a query may select, filter or sort. There is
no PostgREST-specific policy bypass.

PostgREST catalogs do not change the hosted API's admission or SSRF rules; this
is trusted native CLI configuration, not a new hosted proxy.

## Metadata

The generic `dbschema.SchemaReader` reads the bounded PostgREST OpenAPI document
at the API root, with the same credentials and `Accept-Profile` schema. It lists
the relations the role can see, their fields and portable types, and a primary
key only where the document declares one. PostgREST exposes no index, constraint
or foreign-key metadata, so a scan reports none instead of inventing any. A
missing, unauthorized or malformed document is an error, never an empty
relation list.

When a scan saves a catalog it writes only `connections/<env>/<db>.json` naming
the operator's variable, exactly as a PostgreSQL scan does, so the saved catalog
is the same safe descriptor a saved query resolves:

```sh
export DATATUG_POSTGREST_ITEMS='postgrest://api.example.com/rest/v1?schema=app'
datatug scan -D postgrest --dsn-env DATATUG_POSTGREST_ITEMS --db items --env LOCAL -d ./project
```

The scan records the driver and the descriptor path, never the endpoint's host,
port, API path or credentials.

## Bounds

The adapter's defaults apply: at most 1000 rows accepted, 200 rows per page, 20
pages per query, 4 MiB of response bytes, an 8 MiB OpenAPI document, offset up to
100000, 100 keys in a multi-read, and one 15-second deadline per operation
covering authentication, every HTTP exchange and all pages. Exceeding a budget
fails the read; it never truncates a result.

## Hosted Supabase

Local PostgREST proves the protocol and the adapter's bounded behavior. A hosted
Supabase project is a separate gate: its JWT verifier, grants, row-level-security
policies, CORS settings and network path are deployment-specific, so the
repository's checked-in tests prove the owned local fixture, not a hosted
project. This checkout does not claim verified hosted Supabase support.

To run an acceptance pass against your own project: use a table the project
allows you to read, grant `SELECT` to a scoped role (or use Supabase RLS with the
`anon`/authenticated role), deny write grants, and supply a short-lived access
token as `DATATUG_POSTGREST_BEARER` plus the project's anon key as
`DATATUG_POSTGREST_API_KEY`. Never use the service-role key and never commit a
secret. The URL is `postgrest+https://<ref>.supabase.co/rest/v1`.

## Test fixture

```sh
./scripts/check-journey-postgrest.sh
```

The script starts its own PostgreSQL 17 + PostgREST 16.5 Compose project on two
random loopback ports under a unique `datatug-cli-postgrest-<run>` name, seeds
native rows through PostgreSQL's init hook, creates a least-privilege reader and
a role without grants, and mints short-lived read-only JWTs. It proves the reader
has `SELECT` but not `INSERT` and the no-grants role cannot read, then runs the
Go command-path journey and the built `datatug` binary against the same service:
a saved query that resolves the descriptor, a default-bounded `--from` read,
five matching rows read as several server-capped pages (the server caps a page
at two rows and four rows share the sort value), explicit projection with the
native identity kept as the record key, an unbounded read and an aggregate
refused explicitly, a bad token refused with neither the token nor the endpoint
in the message, and a schema scan that records only the descriptor. The Compose
project and its volume are removed when the script ends.
