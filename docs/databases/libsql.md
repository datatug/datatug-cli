# Query libSQL

> Post-release source documentation. The libSQL source is wired into the Go CLI
> in this checkout and is not part of a released `datatug` binary yet. It is a
> Go CLI provider only: no hosted Turso account, no TypeScript/web execution and
> no Go-backed web substitute is claimed here.

DataTug reads the native SQLite tables of a [libSQL](https://github.com/tursodatabase/libsql)
server through the [dalgo2libsql](https://github.com/dal-go/dalgo2libsql)
adapter. libSQL speaks the SQLite dialect, so a query collection is a relation of
the database's single implicit `main` schema: a table a scan records, or another
relation such as a view that a structured read names. The connection is HTTPS
(Hrana over HTTP); it is not a local file and not a Turso account API.

## Endpoint and credentials

```sh
export DATATUG_LIBSQL_BEARER='<short-lived read-only token>'
export DATATUG_LIBSQL_DB='libsql://db.example.com'

datatug query run --db env:DATATUG_LIBSQL_DB --from items --limit 20 --no-policies --format json
```

The URI contract is:

| URI | Meaning |
| --- | --- |
| `libsql://HOST[:PORT][/BASE-PATH]` | libSQL over HTTPS (the default) |
| `libsql+https://HOST[:PORT][/BASE-PATH]` | The same, spelled explicitly |
| `libsql+http://HOST[:PORT][/BASE-PATH]` | Plain HTTP, accepted only for `localhost`, `127.0.0.1` or `::1` |

`BASE-PATH` is the server's base path when it is mounted under a prefix. It must
be plain path segments with no trailing slash, no `.`/`..` segment and no
percent-encoding, and it must not name an API endpoint (`/v2`, `/v3`,
`/pipeline`, `/health`, `/version` are refused). Credentials, a query and a
fragment are refused before any request. `http://` and `https://` keep their
existing meaning in the CLI (a local DataTug project directory), so they are not
a libSQL endpoint even though `libsql://` itself becomes HTTPS on the wire.

Credentials never belong in the URI, a project file, a catalog, a log line or an
error. The adapter reads the operator's environment **for every request**, so a
rotating token needs no restart:

- `DATATUG_LIBSQL_BEARER`: sent as `Authorization: Bearer <token>`.

With no variable set the request is anonymous and the server's own policy
decides (a read-only server usually refuses it). A value containing CR, LF or
NUL is refused before the first request; the error names the variable and never
its value.

## Projects, scans and credentials

A saved project never holds an endpoint or a token. A libSQL catalog records a
connection descriptor whose `dsnEnv` names an operator-controlled variable,
exactly as a PostgreSQL, CouchDB or PostgREST catalog does:

```sh
datatug scan -d ./project -D libsql --dsn-env DATATUG_LIBSQL_DB --db main --env local
```

`connections/local/main.json` then holds only `{"dsnEnv":"DATATUG_LIBSQL_DB"}`,
and `environments/local/catalogs/main/main.db.json` holds only the driver and the
descriptor path. The server base address is read from the variable when a query
or a rescan opens the source. A project that records a literal endpoint is
refused before anything is opened, so a cloned project cannot point the
operator's bearer token at a host of its own choosing. A variable is only
accepted when it starts with `DATATUG_` or is listed in `DATATUG_DSN_ENV_ALLOW`.

An ad-hoc `datatug query run --db libsql://...` names the endpoint directly. That
form is for the person typing the command; it is never written to a project.

## Reads, limits and policies

- All five schema-reader methods (`ListCollections`, `DescribeCollection`,
  `ListIndexes`, `ListConstraints`, `ListReferrers`) come from the adapter's
  generic SQLite catalog reader, so `datatug scan` records real columns, primary
  keys, indexes, foreign keys and constraints under SQLite's `main` schema. No
  `id` column is invented, and a `WITHOUT ROWID` table is not assumed to have a
  rowid key.
- A scan reports **tables only**. The adapter implements the five strict
  `SchemaReader` methods and no `ViewLister`, and its strict `DescribeCollection`
  refuses a view, so `ListCollections` lists tables and a view is never
  discovered by a scan. A structured read can still name a view as a relation
  when the query orders explicitly (a view has no declared primary key, so
  `--from` cannot page it in key order).
- Keyed reads (`Get`/`GetMulti`) need a live-validated single non-null
  `INTEGER`/`TEXT` primary key declared in the adapter's `Recordsets` map.
  `datatug query run` and `datatug scan` use structured reads, which discover
  identity from the live catalog, so neither needs that map.
- An exploratory `--from` read defaults visibly to 100 rows when no `--limit` is
  given; an explicit `--limit` (including `0`) and a `-f` or saved query's own
  limit are never replaced. That output limit is separate from the adapter's
  hard request, response, row, column, value and deadline budgets. A result cut
  by a hard bound is an error, never a successful partial aggregate.
- `--limit`/`--offset` page by the declared primary key, so a page is repeatable
  on unchanged data. It is not a snapshot: a concurrent writer can change what
  the next page sees.
- Access policies are enforced by the outer `pkg/accesspolicies` wrapper. The
  adapter is sealed and read-only: it exposes no write, raw-SQL or handle
  accessor, and a copy into a libSQL target is refused before I/O.

## Native values and JSON

The adapter preserves native SQLite/libSQL values through both readers: an
integer is an `int64` (all 19 digits, including values beyond 2^53 and both
extrema), a real is a `float64`, text is a string, a blob is a copied `[]byte`
and SQL is `NULL`. One column may hold several storage classes in different rows,
because libSQL's declared affinity does not convert a stored value.

The CLI does not stringify or reinterpret those values. In `--format json`:

- `int64` is written as a JSON number with every digit (so
  `9223372036854775807` stays exact);
- `float64` is a JSON number (fractional values keep their fraction);
- text is a JSON string;
- a blob is a JSON string holding its **standard base64** encoding (`x'00ff10'`
  becomes `"AP8Q"`); an empty blob is `""`, which is a different value from
  `NULL`'s `null`;
- `NULL` is `null`.

A JSON consumer that parses every number as an IEEE-754 double loses precision
beyond 2^53; that is a property of the consumer, not of the CLI output. Use
`--format csv` or the grid when a value's exact digits matter to a downstream
tool.

## Supported and unsupported operations

Native SQL text, joins, transactions and opaque cursors are **not** provided by
this adapter and fail honestly; DataTug never answers them with a `SELECT`
prefix heuristic or a local scan that pretends to be the server. Structured
TugQL reads are compiled to SQLite SQL, and these native aggregates run as SQL:
`count`/`count-distinct`, `sum`/`sum-distinct`, `avg`/`avg-distinct`, `min`,
`max`, with `GROUP BY`, `HAVING` and `ORDER BY`. `FIRST`/`LAST`, aggregate-local
ordering and a stable row order are not advertised.

Write operations are refused before any I/O: `datatug db copy --to libsql://...`
fails with a read-only refusal.

## What is not claimed

- A released `datatug` binary does not include this provider yet; until a
  release carries it, only a build from this branch can use it.
- Hosted Turso (an account, a platform token, a management API) is out of scope.
  Only a libSQL server reachable at its HTTPS base address is read.
- The TypeScript and web UI providers, and any Go-backed web substitute, are
  separate work.
