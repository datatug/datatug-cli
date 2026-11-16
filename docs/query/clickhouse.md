# ClickHouse reads

This implementation connects one existing ClickHouse database through the released
DALgo adapter. It returns anonymous, ordered typed recordsets: repeated column
labels and duplicate rows remain separate. MergeTree ordering is not a unique
record key. Copy destinations, point lookups, writes, transactions, joins, local
aggregation and keyed federation are unsupported. Existing map-row HTTP endpoints
refuse this source; the typed CLI query route is the supported consumer here.

Validation status: this CLI procedure and its new journey have **not been executed
end to end** yet. Source, tests and mandatory CI are prepared. The upstream Go
adapter's own native/HTTP real-server proof is separate from CLI command, release
and installed-binary acceptance. Run the checks below in the governed Linux
environment before making an operational claim.

## Operator configuration

Use a least-privilege ClickHouse user with `readonly=1` locked by its server
profile. Lock read/result overflow modes, row/byte/memory/thread budgets and the
permitted execution-time range at the server too. The adapter probes read-only
status on its leased connection and sends finite bounds. It cannot make an
administrator credential safe or turn native SQL into a per-table allowlist.

Only the endpoint/database belongs in the DSN environment variable. URL userinfo,
queries, fragments, encoded names and arbitrary settings are refused. Native TLS
is `clickhouse://host:9440/database`; HTTPS is
`clickhouse+https://host:8443/database`. Plain HTTP is permitted only with
`clickhouse+http://LITERAL_LOOPBACK_IP:PORT/database`, for a disposable fixture.
An explicit port and plain SQL database identifier are required.

```sh
export DATATUG_WAREHOUSE='clickhouse+https://warehouse.example:8443/explore'
export DATATUG_CLICKHOUSE_USERNAME='explorer'
# Supply DATATUG_CLICKHOUSE_PASSWORD through your environment/secret manager.
export DATATUG_CLICKHOUSE_RELATIONS='{"events":["id","amount","at"]}'
datatug db add --project demo --db env:DATATUG_WAREHOUSE --env local --name warehouse
datatug scan -d ./demo -D clickhouse --dsn-env DATATUG_WAREHOUSE --env local --db warehouse
datatug query run --db 'datatug://demo/warehouse?env=local' --from events --limit 10 --no-policies --format json
```

`db add` identifies ClickHouse from the set variable; an unset variable retains
the existing PostgreSQL registration default. Set it before registering ClickHouse.
The strict saved descriptor contains only `{"dsnEnv":"DATATUG_WAREHOUSE"}`.
Only `DATATUG_*` variables or the existing operator allowlist may be named.
The endpoint, user, password and relation grants are not persisted in the project.
Schema scanning lists only permitted visible relations/columns of that database,
retains native types/index metadata and invents no unique keys or constraints.
An empty column list (`"events":[]`) permits all columns visible to the credential
of that table; an empty relation map does not authorize a structured read or scan.

Each read is bounded to 10 seconds, 1000 rows and 4,000,000 native result bytes.
The CLI's typed encoding additionally refuses cells over 64 KiB and encoded
results over 4,000,000 bytes. Overflow fails without printing a partial result.
`--from` defaults visibly to 100 rows; saved/file queries require their own positive
limit. Offset is supported without a generic stable-key/page promise. Use an
explicit supported ordering when the application requires one.

## Policies and SQL

TugQL supports the adapter's flat single-relation field projection/filter/order/
limit subset. Whole original and policy-effective ASTs pass the same adapter
compiler before I/O. Policy-injected unsupported fields, key conditions, nested
sources, joins and unsupported expressions fail rather than falling back to
keyed RecordsReader execution. Use the bare relation name (or DALgo's explicit
qualified reference with the actual configured database), not a dotted literal
relation name. Saved TugQL/SQL resolve the descriptor again on every run.

Native SQL is an independently scoped capability. The operator must set
`DATATUG_CLICKHOUSE_NATIVE_SQL=true` and list this source's DSN variable in
`DATATUG_CLICKHOUSE_NATIVE_SQL_SOURCES` (a bounded comma-separated list). Native
execution requires that listed `env:NAME` reference, not a direct endpoint URL.
The session must be unrestricted or explicitly
allow opaque SQL (`--allow-opaque-sql`) **and** its policies must grant an
`opaqueQuery` scope. Ordinary row/column policies do not constrain native SQL; a
limitation is always reported. The user's ClickHouse grants and locked profile
are the native authority, including table functions. The structured relation map
does not restrict arbitrary SQL. SQL is forwarded unchanged, with no sanitizer,
lexer rewrite or transaction wrapper.

```sh
export DATATUG_CLICKHOUSE_NATIVE_SQL=true
export DATATUG_CLICKHOUSE_NATIVE_SQL_SOURCES=DATATUG_WAREHOUSE
datatug query run --db env:DATATUG_WAREHOUSE --no-policies --format json \
  --sql 'SELECT id, amount FROM explore.events WHERE id={id:UInt64}' \
  --var id=18446744073709551615
```

Named scalar bindings preserve names. Plain finite JSON numeric `--var` tokens
retain their exact lexemes; quoted values remain strings. Duplicate names,
malformed/nonfinite/unsafe values and nested native bindings are refused.
Saved SQL uses its declared parameters and the same real named-binding path.
Do not include passwords or other credentials in SQL literals.

## Output

JSON/YAML contain `{recordset:{columns,rows},ordinalColumns}`. Rows are typed cell
arrays, never objects indexed by column label, and no `$key` is added. Native
types/encoding metadata are parallel ordinal columns. UInt64/Int/Decimal values
use exact decimal strings in typed cells; nullable values and nanosecond times
keep their types. Arrays use `json-text` containing recursively typed cells; maps
use `json-text` arrays of typed `{key,value}` pairs. This retains nested exact
numbers and non-string keys without a float round trip. Decimal scale remains
available in the native column type. CSV/grid retain ordered duplicate headers;
JSONL emits one column-metadata header, followed by typed row arrays.

Saved-query `--result-id` files use the existing typed recordset format, retain
ordinal native declarations/encoding and omit bookkeeping keys. The requested
row cap is reported in result metadata; cells are never silently truncated.
An explicit `anonymous:true` discriminator must match in data and metadata.
Reopen with `datatug query results show --id ID`; this reads the validated typed
cells without executing a query. The keyed terminal chat workspace refuses these
results before converting them to maps. Invalid UTF-8 labels, cells and map keys
are refused, rather than replaced during JSON serialization.
Keyed lookup enrichment and federation refuse ClickHouse before any leaf query.
Other providers keep their existing writers and output shapes.

## Prepared real proof

`bash scripts/check-journey-clickhouse.sh` runs only on Linux. It owns one random
loopback fixture container and always removes it. The exact published server is
`clickhouse/clickhouse-server:26.10.3.7` at the digest in
`testdata/clickhouse/provenance.json`. Limits stay 2 GiB memory, zero swap, two CPUs,
128 PIDs, with internal global pool 96. Fixture SQL/config/license are byte copies
from the released adapter revision recorded there, using only synthetic data.
The startup completion barrier, 60 polls, bounded diagnostics and final zero PID
denial/OOM counters remain mandatory. The metrics-only diagnostic listener is
inside the owned container and is never published on the host.

CI builds this checkout's CLI and runs the real command journey: add, scan,
descriptor reopen, ad-hoc/saved queries, structured and opaque policies, ordered
duplicates, exact native values/bindings, cancellation, refused writes/settings,
forbidden relations, invalid credentials and hard result bounds. Missing fixture
configuration is a failure in that tagged test, never a skipped proof. Normal
unit/race/vet checks, this mandatory real-server job, main/release receipts and
actual installed-binary acceptance remain distinct gates.
