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
| `datatug_list_queries` | `project` | Saved SQL or TugQL query IDs and parameter declarations |
| `datatug_get_query` | `project`, `queryId` | SQL or TugQL text and parameter declarations |
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
Client parameters named `currentUser`, `principal.*` or `path.*` are refused;
they cannot replace the fixed policy principal or structural bindings.

Execution supports saved SQL and TugQL queries against SQLite through the existing
agent API. Saved TugQL uses the protected structured-query path and the configured
policies; saved SQL retains its separate opaque-query grant and result profile.
It provides no arbitrary SQL, project mutation, query-builder UI or MCP Views. SQLite opens the source read-only and its
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

## Configured exact-table SQLite reads

This profile is a candidate feature. Use it for security enforcement only with a
named CLI release that has passed this profile's acceptance checks; no published
release is qualified here yet. An older CLI may ignore `security.json`. Launch
with the explicit profile flag so a binary without the feature rejects the flag
instead of silently starting an unprotected session:

```sh
datatug serve --project /absolute/canonical/project --security-profile exact-table-readonly-v1
```

The optional `exact-table-readonly-v1` server profile binds one registered SQLite
catalog and a fixed principal to required project policy files. Its declaration
is `<project>/.datatug/security.json`:

```json
{
  "profile": "exact-table-readonly-v1",
  "project": "my-project",
  "environment": "local",
  "catalog": "my-catalog",
  "sqliteFile": "/absolute/canonical/existing/database.sqlite",
  "requiredPolicies": ["policies/tables.yaml"],
  "principal": {"id": "reader"}
}
```

Required policy sets must be present, nonempty and valid AccessPolicy documents.
They always intersect with other project policies and any explicitly added
`--policy`/`--policies-dir` documents. A global/default policy directory cannot
replace a required set. An explicit `--security-profile exact-table-readonly-v1`
requires the declaration; a present declaration is also loaded without that flag.
Principal flags must agree with it. Writes, raw SQL and live-server capabilities
are incompatible with this profile.

The selected catalog ID is the only accepted source ID. Existing canonical files
are required; symlinks, environment substitution, SQLite URI options and aliases
are refused. Changes to the declaration, required policies, registered catalogs
or target file identity invalidate the session, including cached result context.
A new validated session is required. Table/view listing and column details apply
the same policy session; unavailable authority refuses the complete request.
Table references must use their exact native spelling in the ordinary `main`
namespace. Legacy collection-path policy selectors are also byte-exact: use
`/Invoice` for a native table named `Invoice`; `/invoice` does not constrain it.
Views, virtual/shadow tables and ambiguous source identities are
unsupported, including discovery/detail admission. A listed object whose
admitted identity cannot be verified makes the catalog request refuse rather
than return a partial success.
Only structured TugQL reads and the listed discovery/detail routes are enabled;
subqueries and derived sources are unsupported. Artifact, Watch, live-server,
snapshot and mutation routes refuse.

Named project CLI source paths currently refuse a declared profile because they
do not install this server session. Direct ad-hoc source URLs and legacy projects
without the declaration retain their existing behavior; they are outside this
profile. This is a trusted local configuration boundary, not an OS sandbox or
protection against a credential holder editing files and starting another process.
It does not provide Agent-only identity, enrolled grants, retained-stream expiry,
named-schema rules or an approval workflow.

The configured profile currently qualifies canonical macOS/Linux local paths;
Windows drive-letter paths are unsupported. The cross-platform legacy commands
retain their existing behavior. An implicit global `serve` registry containing a
local security declaration refuses startup and requires explicit `--project`.
Any filesystem error conservatively invalidates a bound session; recovery requires
new validation. Required policy objects and digests come from the same owned
bytes; ordinary registry changes during its shared loader are rejected. This
provides no atomic pathname guarantee against a filesystem owner performing ABA
replacement while a native handle opens.

The existing explicit opaque-SQL grant also permits the read-only `table_list`
metadata pragma. Its native authorizer still refuses writes, attachment and
unsafe pragmas; this does not grant opaque SQL to the configured profile.
