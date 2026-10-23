# Local MCP server

`datatug mcp` exposes read-only tools over stdio using the official MCP Go SDK.
It connects to a separately running `datatug serve` agent; it does not start or
stop that agent. The agent chooses the effective principal, policies and native
SQL grant. The MCP client cannot override them.

The agent URL defaults to `http://127.0.0.1:8989`. `--agent-url` accepts only an
HTTP origin using `localhost`, `127.0.0.1` or `[::1]` and an explicit port.
Redirects and HTTP proxies are disabled. Authentication uses the existing local
agent key: both processes must run under the same OS account and use the same
`DATATUG_DAEMON_DIR` when that directory is overridden. No token belongs in MCP
configuration.

Start the agent with your existing project and its intended policy session.
For a policy-governed SQL session, native SQL needs both `--allow-opaque-sql`
and a policy granting opaque queries. Without policy files, the existing agent
runs as an unrestricted principal. `datatug serve --help` describes its flags.
The MCP process keeps stdout exclusively for protocol JSON; diagnostics use
stderr.

An MCP client's stdio configuration can launch the installed executable:

```json
{
  "mcpServers": {
    "datatug": {
      "command": "/absolute/path/to/datatug",
      "args": ["mcp", "--agent-url", "http://127.0.0.1:8989"]
    }
  }
}
```

Use the absolute path of the executable that supports `datatug mcp --help`.
Installation and upgrade options are in the [CLI installation guide](../README.md#installation).
Homebrew promotion can lag the directly downloadable release.

| Tool | Arguments | Result |
|---|---|---|
| `datatug_agent_info` | none | Effective principal and agent capabilities |
| `datatug_list_projects` | none | Configured project IDs |
| `datatug_describe_project` | `project` | Environment, source and entity IDs |
| `datatug_list_queries` | `project` | Saved SQL query IDs and parameter declarations |
| `datatug_get_query` | `project`, `queryId` | SQL text and parameter declarations |
| `datatug_list_tables` | `project`, `environment`, `source` | Scanned table and view names |
| `datatug_describe_table` | `project`, `environment`, `source`, `schema`, `table` | Live columns, primary key and allowed relationships |
| `datatug_run_query` | `project`, `environment`, `source`, `queryId`, `parameters`, optional `limit` | Typed rows, applied bindings, limitations and provenance |

Discover identifiers before using them. Query IDs include their folders, such
as `customers/customer-purchases-by-genre`. For SQLite, `schema` is an empty
string. An unscanned source can use `datatug_describe_table` with a known table
name even when no scanned table list exists.

`parameters` maps declared parameter IDs to text values. For example,
`{"CustomerId":"1"}` binds integer 1 when the saved declaration says integer.
Strings bind literally; integer and decimal text retains precision. Boolean
text is `true` or `false`; dates use `YYYY-MM-DD` and datetimes use UTC RFC3339.
Multi-value and credential parameters are not supported by this release.
Parameter defaults and raw connection targets are omitted from metadata.

Execution supports saved SQL queries against SQLite through the existing
agent API. It provides no arbitrary SQL, project mutation, query-builder UI,
MCP Views, or TugQL execution tools. SQLite opens the source read-only and its
native authorizer blocks writes, attachments and changes that disable the
read-only guard. Native SQL has `opaque-privileged` provenance: source-level
policy decisions apply, while row and column policies do not rewrite SQL.

Each tool has a 30-second deadline. Input frames and agent request bodies are
limited to 64 KiB; agent responses and tool payloads are limited to 2 MiB.
`limit` defaults to 100 returned rows and accepts 1–500. It limits returned
rows, not database work. Closing the client's stdin shuts down the MCP process
and leaves the separately owned agent running.

The real-project acceptance test uses the official SDK in a child process,
authenticated local HTTP transport and the committed Chinook database. It
checks saved SQL parameter effects, live schema, denied grants/authentication,
write/attachment rejection, an older protocol handshake, JSON-only stdout and
clean shutdown. To test a built or released executable with that fixture:

```sh
DATATUG_DEMO_PROJECT_DIR=/absolute/path/to/datatug-demo-projects/demo-project-1 \
DATATUG_MCP_TEST_BINARY=/absolute/path/to/datatug \
  wb run -- go test ./pkg/server -run '^TestMCPRealSavedSQLChildProcess$' -count=1 -v
```

Run this from a datatug-cli checkout. The test requires a real demo-project-1
checkout at `DATATUG_DEMO_PROJECT_DIR`. It copies the project and
SQLite data into temporary directories and removes its child processes,
temporary agent key and loopback fixture on completion.
