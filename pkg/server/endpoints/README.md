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
  (`IsSupportedOrigin`: `localhost` and `127.0.0.1` pages, `https://datatug.app` and its
  subdomains, `https://app.incidentius.com`);
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

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
|  **Executor** |
| POST   | /exec/execute | Executes a batch of commands |
| GET | /exec/select | Executes a single non mutating SELECT command |
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

The text of `pkg/dbcopy`, which holds the display form of the source (the path of its file, the
host, the port and the database of a PostgreSQL one), is in the log of the server. The fixed
sentences that name no source (the preview of PostgreSQL sources is off, a read through policies
is not available, a URL that turns the read-only session off, a connection that was lost) are
answered as they are. The CLI's own commands (`datatug query run`, `datatug db copy`) show the
sentences of `pkg/dbcopy`: a person at their own terminal sees their own paths.

### Endpoint: POST /execute

Executes a batch of commands

### Endpoint: GET /select

Executes a single non mutating SELECT command
