# API end-points of DataTug agent

When DataTug agent is started with a `serve` command it listens on HTTP port (*by default 8989*).

> datatug serve -p=./example

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
|  **Queries** |
| GET | /queries/all_queries | |
| POST | /queries/create_query | Legacy create-or-replace; needs `--allow-writes` and a project-write grant for the serving principal |
| PUT | /queries/update_query | Legacy create-or-replace; same authorization as create_query |
| DELETE | /queries/delete_query | Needs `--allow-writes` and a project-write grant |
| POST | /queries/capture | Save as project query: an atomic, revision-checked create (`ifNoneMatch`) or update (`ifMatch`) of a DTQL query pair; same authorization |
|  **Recordsets** |
| GET | /data/recordsets | |
| GET | /data/recordset_definition | |
| GET | /data/recordset_data | |
| POST | /data/recordset_add_rows | |
| PUT | /data/recordset_update_rows | |
| DELETE | /data/recordset_delete_rows | |
|  **Folders** |
| PUT | /folders/create_folder | |
| DELETE | /folders/delete_folder | 404 when the project has no such folder; 500 when the delete fails |
|  **DB servers** |
| GET | /dbserver-summary | The db server of the project that the `driver`, `host` and `port` of the query name |
| GET | /dbserver-databases | The databases of a SQL Server server (`driver=sqlserver`) that the served project records: a project that is not served, a server that is not a plain reference and a server that the project does not record are refused with a 400 before anything is connected to |
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

### Endpoint: POST /execute

Executes a batch of commands

### Endpoint: GET /select

Executes a single non mutating SELECT command
