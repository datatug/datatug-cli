# Query CouchDB

> Pre-release source documentation. The CouchDB source is wired into the CLI in
> this checkout and is not part of a released `datatug` binary yet, and it is not
> yet listed as a supported database on the website.

DataTug reads native documents from one existing CouchDB 3.5 database as one
logical collection. Documents are read as an application stored them: `_id` is
the record key, `_rev` is metadata, and no `__dalgo_*` or `data` envelope is
required.

## Endpoint and credentials

```sh
export DATATUG_COUCHDB_USERNAME=reader
export DATATUG_COUCHDB_PASSWORD='use-a-secret-manager-or-your-shell'
export DATATUG_COUCHDB_ORDERS='couchdb://couch.example.com:6984/orders'

datatug query run --db env:DATATUG_COUCHDB_ORDERS -f orders.tugql.yaml --no-policies --format json
```

A filtered CouchDB read needs a Mango index, so such a query is normally a `-f`
file with a filter and an order (see [Queries](#queries)). An unfiltered read — a
bare `--from`, or a `-f` file with no `where` — is served by CouchDB's native
`_all_docs` primary index instead and needs no index.

The URI contract is:

| URI | Meaning |
| --- | --- |
| `couchdb://HOST/DATABASE` | CouchDB over HTTPS (the default) |
| `couchdb+https://HOST/DATABASE` | The same, spelled explicitly |
| `couchdb+http://HOST/DATABASE` | Plain HTTP, accepted only for `localhost`, `127.0.0.1` or `::1` |

The endpoint names exactly one database; a trailing slash, a second path
segment, a query string, a fragment, URL credentials and a percent-encoded
database name are refused before any request. `http://` and `https://` keep
their existing meaning (a local DataTug project directory), so they are not a
CouchDB endpoint.

Credentials never belong in the URI, a project file, a catalog or an error.
The adapter reads them for every request from the operator's environment:

- `DATATUG_COUCHDB_USERNAME` and `DATATUG_COUCHDB_PASSWORD` together: Basic
  authentication. One without the other is refused.
- `DATATUG_COUCHDB_BEARER`: Bearer authentication. It cannot be combined with
  the Basic pair; an ambiguous configuration is refused before a request.
- None set: the request is unauthenticated and CouchDB's own `401` is reported.

Use a user with read access to the database. Index metadata is read from
`/{database}/_index`, which does not need server administrator rights. A
database created by an administrator can be restricted to named members; add
the reader to its `_security` members.

## Queries

A structured file query can filter, project and order native fields:

```yaml
from: {name: orders}
columns:
  - field: _id
  - field: name
  - field: total
where:
  op: '>='
  left: {field: total}
  right: {value: 100}
orderBy:
  - field: total
    desc: true
limit: 50
```

```sh
datatug query run --db env:DATATUG_COUCHDB_ORDERS -f orders.tugql.yaml --no-policies --format json
```

Supported: one root collection; top-level field comparisons (`==`, `>`, `>=`,
`<`, `<=`, `IN`, `NOT IN`); `IS NOT NULL`; `AND`/`OR`; projection; one sort
direction; offset; and a positive limit. `_id` is materialized as the record
key and `$key` in output. An explicitly declared `_id` or `_rev` is exposed;
an undeclared `_rev` is not.

A **filtered** CouchDB query must be served by a real Mango index. DataTug never
enables CouchDB's `allow_fallback`, never reroutes a predicate to `_all_docs`, and
never creates an index. Create indexes with your deployment tooling: the index a
query needs is the one whose fields cover its selector and sort (for example a
`score` index for `orderBy` on `score`). An index set that includes a broader
compound index than the query needs can change Mango's choice; check the query
with your actual indexes.

A query with **no filter** is different: it is read deliberately from CouchDB's
native `_all_docs` primary index, so it works on an unindexed database and needs
no Mango index at all. That covers a bare `--from` read, and a `-f` file whose
only structure is a projection, a limit, an offset and (optionally) an `_id`
order:

```sh
datatug query run --db env:DATATUG_COUCHDB_ORDERS --from orders --limit 5 --no-policies --format json
```

- Records come back in native `_id` order (ascending, or descending for
  `orderBy: [{field: _id, desc: true}]`), `_id` as the record key / `$key`.
- Design (`_design/...`) and other management documents, and deleted documents,
  are excluded **before** the offset and the limit, so they never consume a
  result slot.
- Pages use the native `_id` as an inclusive continuation key and are bounded by
  the same page, row and byte budgets as a Mango read.
- This is not a fallback: a query with any `where` predicate stays on Mango and
  is refused when no index serves it.

Because the adapter requires a positive bounded limit, an exploration `--from`
read with no `--limit` gets a visible default bound of 100 instead. An explicit
`--limit 0` stays explicit and fails rather than being replaced.

Pages inside one query stay bounded: a Mango read continues with CouchDB
bookmarks and a primary-index read continues with the native `_id`, so a limit
larger than the adapter's page size (100 by default) is read as several physical
pages. The adapter caps a query at 1000 rows, 20 pages, 10000 offset, 4 MiB per
response and 16 MiB per operation by default. Exceeding any bound fails.

Not supported: joins, grouping, aggregation, `HAVING`, subqueries, wildcard or
aliased projections, nested/dotted fields, mixed sort directions, raw Mango,
raw URLs, management endpoints, `IS NULL` and writes. `IS NULL` is refused
before any request because DALgo also matches documents where the field is
absent while a JSON index only contains documents where the field is present;
use an explicit `field == null` comparison for explicit nulls, or
`IS NOT NULL` for present non-null values. Copying into CouchDB is refused.

## Saved queries and access policies

A CouchDB catalog records a connection descriptor, never an endpoint and never a
credential. The descriptor names the operator's environment variable, and the
endpoint itself is read only from that variable when the source is opened:

```json
{
  "driver": "couchdb",
  "path": "connections/local/orders.json",
  "dbModel": "orders"
}
```

```json
{
  "dsnEnv": "DATATUG_COUCHDB_ORDERS"
}
```

`datatug query run --project <dir> --query <id>` resolves that catalog through
the same `api.ResolveCatalogSourceURL` path as the other providers: it reads the
descriptor from inside the project, rejects a literal endpoint or an
`env:`/URL-shaped path, applies the same `DATATUG_*`/`DATATUG_DSN_ENV_ALLOW`
variable-name rule as a PostgreSQL descriptor, and refuses a variable that is
not set or that holds another scheme before any request is made. A cloned
project therefore cannot point the operator's CouchDB credentials at a host of
its own choosing. Credentials still come from the operator's CouchDB variables;
the project never holds them.

DataTug access policies work on the one logical collection (the database name);
a policy can allow rows by a condition and allow-list the fields a query may
select, filter or sort. There is no CouchDB-specific policy bypass.

CouchDB catalogs do not change the hosted API's admission or SSRF rules; this
is trusted native CLI configuration, not a new hosted proxy.

## Metadata

The generic `dbschema.SchemaReader` reports the one configured collection, its
native `_id` primary key and real Mango indexes. Native CouchDB documents
declare no field schema, so the scan reports the collection with no columns
instead of sampling documents; SQL constraints and foreign keys are reported
as unavailable. A scan needs read access to `_index`. When a scan saves a
catalog it writes only `connections/<env>/<db>.json` naming the operator's
variable, exactly as a PostgreSQL scan does, so the saved catalog is the same
safe descriptor a saved query resolves:

```sh
export DATATUG_COUCHDB_ORDERS='couchdb://couch.example.com:6984/orders'
datatug scan -D couchdb --dsn-env DATATUG_COUCHDB_ORDERS --db orders --env LOCAL -d ./project
```

The scan records the driver and the descriptor path, never the endpoint's host,
port or credentials.

## Test fixture

```sh
./scripts/check-journey-couchdb.sh
```

The script starts its own `apache/couchdb:3.5.3` container (pinned by digest) on
a random loopback port under a unique `datatug-cli-couchdb-<run>` name, creates
a database and a least-privilege reader, and installs a `validate_doc_update`
that makes the database server-enforced read-only for every non-admin writer.
It then proves the reader's write is refused with a real `403`, runs the Go
command-path journey (seeding only through the admin account, which the
validation function lets through), and runs the built `datatug` binary against
the same fixture: a saved query that resolves the descriptor, a filtered query a
seeded Mango index serves, a bounded native primary-index read (whose
`_design` document must not consume a slot), the default-bounded `--from` read,
and a filtered query no index serves, which must be refused. The container is
removed when the script ends.
