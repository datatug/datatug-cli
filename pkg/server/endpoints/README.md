# API end-points of DataTug agent

When DataTug agent is started with a `serve` command it listens on HTTP port (*by default 8989*).

> datatug serve -p=./example

## Who is answered

A serve route answers a request only when it comes from one of the server's own pages or from a
tool on the same machine, and an answer names a source by its ID. One check stands in front of
every route (the routes of the table below, the page at `/` and the answer to every `OPTIONS`
request; see `RequestGuard` in `request_guard.go`):

- the `Host` of the request is one that the server was started for: `localhost`, `127.0.0.1`
  or `::1`, or the host given with `--host`, on the port that the server listens on;
- the `Origin` of the request, when it has one, is on the list of the `OPTIONS` handler
  (`IsSupportedOrigin`: exactly `https://datatug.app`, and the host `datatug.localhost` over
  `http` or `https` with no port or a numeric one, for local UI development; nothing else, no
  flag adds one);
- unless the server was built without an access secret (tests only), the request carries the
  session token (`Authorization: Bearer`), except a preflight and `POST /datatug/session/exchange`,
  which trades the one-time code of a link for the token (see `pkg/agentaccess`); a request without
  it is a `401`;
- the `Sec-Fetch-Site` of the request, when it has one, is `same-origin`, `same-site` or `none`,
  or its `Origin` is on the list.

A request that has neither an `Origin` nor a `Sec-Fetch-Site` (the CLI, `curl`, a script) is
answered when its `Host` passes. Any other request is refused with a `403` whose body is one fixed
sentence: it holds nothing the request said, and it names no `Access-Control-Allow-Origin`. An
answer names an origin in `Access-Control-Allow-Origin` only when it is on the list.

A server that listens on a wildcard address (`--host 0.0.0.0`) answers `Host: 0.0.0.0:<port>` and
the loopback names, and no other name: the host a client reaches it by has to be given with
`--host`.

The live connection to a database server is a capability that is off by default (see
`GET /dbserver-databases` below): `datatug serve --allow-live-connections`.

## Record lookup

`POST /datatug/records/lookup` reads at most one row of a table of a registered source by its primary key,
through the same policy-enforced executor as every other read (a denied principal gets `403 ACCESS_DENIED`
and no data). It is not registered in write-only mode and needs no write capability.

Request: `{"project","env","source","schema","table","key":{"<pkCol>":<value>}}`. `schema` may be `""`;
no other field may be left out; unknown fields are refused. `project`, `env` and `source` are plain
names and the source is looked up in the registry of the environment (there is no default source);
`schema` and `table` hold no `://`, path separator or control character. A refused field is a `400`
that does not echo its value, and nothing is looked up.

`key` names exactly the declared primary-key columns of the table, read live from the source (a source
that was never scanned works), with a string, or a number or string that is an exact integer literal for
an integer column (an optional `-` and digits, no `+`, space, leading zero, fraction or exponent; it must
fit int64). An integer is read from its literal text, never through a float, so `9007199254740993` opens
that record and not its neighbour. Values are bound as query parameters; identifiers come from the live
schema only. A key that does not fit (unknown, non-key, missing or extra column, `null`, object, array,
boolean, wrong type, a key type that is not integer or string, a table with no primary key) is
`400 INVALID_REQUEST` with `field: "key"` and a message that does not quote the request, and no query is
issued.

The access decision (the session's policies, on the collection) is made before the source is opened or
its schema read, on both routes: a principal the policy refuses gets one `403 ACCESS_DENIED` whether the
table exists or not and whatever the key looks like. Only the default schema of the driver (or no schema)
names a table; any other `schema` is `404 NOT_FOUND` with `field: "schema"`, so the queried object is
always the object the policy decided on. A key column that appears twice in `key` is a bad key.

An integer key above 2^53 - 1 reads up to 64 candidates (SQLite compares numbers as floats) and keeps the
row whose key is exactly equal. If none is exact and the candidates cannot be compared (a key column the
policy masks, or returned as a float or bytes) or the 64 are all used (keys above about 2^60), the answer is
`500 INTERNAL`, never `404` and never a neighbour.

Response `200`: the contract `Result` of `semantic/related/rows` and `exec/run_query`
(`recordset` with one row of typed values, `limitations`, `provenance`). Errors are the contract envelope:
`NOT_FOUND` with `field` `project`, `source`, `table` or `key` (record not found), `400
INVALID_REQUEST` / `MISSING_PARAMETER`, `ACCESS_DENIED`, `SOURCE_UNAVAILABLE`, `TIMEOUT`. The body, the
key and the row are never logged.

`POST /datatug/records/schema` takes the same body without `key` and answers
`{"source","schema","table","columns":[{"name","type"}],"primaryKey":[...],"referencedBy":[...]}`.
It reads no row.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
|  **Executor** |
| POST   | /exec/execute | Executes a batch of commands |
| GET | /exec/select | Executes a single non mutating SELECT command |
| POST | /records/lookup | One record by the typed values of its declared primary key; read-only, see [Record lookup](#record-lookup) |
| POST | /records/schema | The live schema (columns and types, primary key, referencing tables) of one table of a registered source, read from the source and not from a scan |
|  **Entities** |
| GET | /entities/all_entities | |
| GET | /entities/entity | |
| POST | /entities/create_entity | |
| PUT | /entities/save_entity | |
| DELETE | /entities/delete_entity | 404 when the project has no such entity; 500 when the delete fails |
|  **Projects** |
| POST | /projects/create_project | `501`: not implemented (behind `--allow-writes` as every write) |
|  **Queries** |
| GET | /queries/get_query | A failure to walk the queries tree of the project is `queries of project "<id>" could not be loaded`, as for `all_queries` |
| GET | /queries/all_queries | `root` is `shared` (the default) or `personal`; any other value is a `400` that does not quote it |
| POST | /queries/create_query | Legacy create-or-replace; needs `--allow-writes` and a project-write grant for the serving principal |
| PUT | /queries/update_query | Legacy create-or-replace; same authorization as create_query |
| DELETE | /queries/delete_query | Needs `--allow-writes` and a project-write grant |
| POST | /queries/capture | Save as project query: an atomic, revision-checked create (`ifNoneMatch`) or update (`ifMatch`) of a DTQL query pair; same authorization |
|  **Recordsets** |
| GET | /data/recordsets | |
| GET | /data/recordset_definition | |
| GET | /data/recordset_data | `501`: not implemented |
| POST | /data/recordset_add_rows | `count`, when given, is a whole number from 0 to 1000; any other is a `400` |
| PUT | /data/recordset_update_rows | |
| DELETE | /data/recordset_delete_rows | |
|  **Folders** |
| PUT | /folders/create_folder | |
| DELETE | /folders/delete_folder | 404 when the project has no such folder; 500 when the delete fails |
|  **DB servers** |
| GET | /dbserver-summary | The db server of the project that the `driver`, `host` and `port` of the query name |
| GET | /dbserver-databases | The databases of a SQL Server server (`driver=sqlserver`) that the served project records. It connects to the server under the identity of the person who runs `datatug serve`, so it answers `403` unless `datatug serve` was started with `--allow-live-connections`. A project that is not served, a server that is not a plain reference and a server that the project does not record are refused with a 400 before anything is connected to; the connection and the query end after 15 seconds, or with the request, with `could not list the databases of db server "<id>"` |
| POST | /dbserver-add | |
| DELETE | /dbserver-delete | 404 when the project does not record the server; 500 when the delete fails |
|  **Boards** |
| GET | /boards/board | |
| POST | /boards/create_board | |
| PUT | /boards/save_board | |
| DELETE | /boards/delete_board | 404 when the project has no such board; 500 when the delete fails |

The routes of the table above that load a board, an entity, a recordset definition, a folder or a
db server, and `environment-summary`, `projects/projects_summary`, `projects/project_summary`,
`projects/project_full`, `catalog-tables`, `queries/all_queries`, the semantic routes, the
resolution of the source of `exec/select`, `exec/execute_commands` and `exec/run_query` (the
connection descriptor of a PostgreSQL catalog included), and the document of a saved query of
`exec/run_query`, answer with a sentence that they build from the kind of what was asked for and
its ID (`board "b1" not found`, `could not delete board "b1"`) when the project store, a driver or
a file of the project cannot be read, and read only what the served project records. What the
store or the driver said, which quotes the paths of the server, is in the log of the server and is
not in the answer.

A source is named by its ID in every answer, for every scheme. Where a route answers that the data
file of a source is not there, or that a source could not be opened (the semantic routes,
`exec/run_query`, `exec/select` and `exec/execute_commands`), the sentence is built at the route
from the ID of the source:

| Answer (`503`, `SOURCE_UNAVAILABLE`) | Built from |
|--------------------------------------|------------|
| `the data file of source "<id>" does not exist (run `datatug demo` to fetch the demo project's data fixtures)` | the ID |
| `source "<id>" could not be opened` | the ID |

The text of `pkg/dbcopy`, which holds the display form of the source (the path of its file), is in
the log of the server. The fixed sentences that name no source (the preview of PostgreSQL sources
is off, a read through policies is not available, a URL that turns the read-only session off) are
answered as they are, and so is the failure of a PostgreSQL source, whether it could not be opened or
lost its connection: the sentence the adapter chose for the failure and the hint of where its connection
string is read from (`the server could not be reached; the PostgreSQL connection string is read from the
environment variable <NAME>`), which hold no host, port, database, user or password. The log says the same. The CLI's own commands (`datatug query run`, `datatug db copy`) show the
sentences of `pkg/dbcopy`: a person at their own terminal sees their own paths.

### Endpoint: POST /execute

Executes a batch of commands

### Endpoint: GET /select

Executes a single non mutating SELECT command
