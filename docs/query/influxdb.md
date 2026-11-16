# Native InfluxDB v3 mapped reads

`query influxdb` reads explicitly mapped existing tables through the public
`github.com/dal-go/dalgo2influxdb` native client. This is a partial SQL read
surface for a v3-compatible JSON query endpoint, with scoped token authorization.
DataTug application policy is not applied to this explicit native command.

```sh
export INFLUX_READ_TOKEN='read-only-token-from-your-secret-manager'
datatug query influxdb --config mapping.json --request request.json --timeout 10s
```

The trusted mapping contains only the token's environment variable **name**:

```json
{
  "endpoint": "https://influx.example.test",
  "database": "existing-database",
  "tokenEnv": "INFLUX_READ_TOKEN",
  "tables": {
    "events": {
      "Name": "existing_events",
      "Key": {"Name": "event_id", "Kind": "string"},
      "Columns": {
        "amount": {"Name": "amount_native", "Kind": "number"},
        "label": {"Name": "label_native", "Kind": "string", "Nullable": true},
        "active": {"Name": "active_native", "Kind": "boolean"}
      }
    }
  }
}
```

`caFile` may name a PEM CA file for a private verified HTTPS endpoint. Only a
root HTTPS URL is accepted: no embedded credentials, query, fragment, alternate
path, proxy, redirect or TLS bypass. The token must be 1–8192 printable non-space
ASCII bytes and should have only read access to the intended database/tables.
The mapping is not a replacement for server authorization. No token value is
accepted in a flag or mapping/request file. Error messages omit raw transport
causes, response bodies, SQL, tokens and URLs.

One strict request object may come from a regular file or `--request -` (stdin):

```json
{"operation":"get","collection":"events","key":"event-1"}
{"operation":"exists","collection":"events","key":"event-1"}
{"operation":"getMany","collection":"events","keys":["event-1","missing","event-1"]}
```

Each line above is a separate request example, not JSON Lines input. Get returns
`{"key":"event-1","data":{...}}` or `null`; exists returns a boolean.
GetMany preserves input order, duplicates and `null` missing positions. It
executes sequentially under one deadline and aggregate response/request/row
budgets; a large duplicate list can exhaust the aggregate budget.

```json
{
  "operation": "query",
  "collection": "events",
  "projection": ["amount", "active"],
  "filters": [
    {"field":"amount","operator":">=","value":{"kind":"number","value":"9007199254740993.000"}}
  ],
  "order": [{"Field":"amount","Descending":false}],
  "limit": 10
}
```

Queries support mapped projection, AND scalar comparisons (`== != < <= > >=`),
mapped ordering and a positive limit. `__name__` refers to the configured unique,
non-null string key. Key ordering is appended as a stable tiebreaker. Ordering by
a nullable column is refused. Returned output contains `rows` and, when a
lookahead row exists, `nextCursor`. Copy that cursor array unchanged into the
same query's `startAfter` field. Numbers in both filters and cursor positions
use `{"kind":"number","value":"decimal lexeme"}`; bare numeric literals are
refused as data values. Strings, booleans and null remain native JSON scalars.
Numbers in output use the same wrapper, preserving the received decimal lexeme,
including wide integers, fractional zeroes and exponent spelling. This does not
recover precision already lost by a server or claim arbitrary Influx logical types.

The provider validates returned projection, key identity/order, filters and cursor
ordering. Cursor positions are not opaque authorization tokens or snapshots:
only trusted callers should supply them; concurrent table changes can affect
later pages. Use the same mapping, filters, projection and ordering when resuming.

The whole command deadline (1 ms–2 min, default 10 s) starts before mapping,
request, CA and token access. First SIGINT/SIGTERM cancels the active read and
returns normally through cleanup. Output is buffered and guarded after encoding,
then written once; a cancelled read emits no partial JSON prefix. A writer can
still fail partially during the final write. Input is limited to 64 KiB per
document, duplicate members/unknown fields/trailing documents/invalid Unicode
are refused, and outstanding input-read workers are bounded. The provider has
1000 parsed rows (including lookahead and repeated GetMany rows), 4 MiB aggregate
response bytes, 64 KiB aggregate request bytes and one concurrent operation.
Query has at most 64 filters, 16 order fields, 128 projections and 17 cursor
positions; output is capped at 8 MiB after lossless tagging.

No raw SQL, aggregate/join/group query, composite key, autodiscovery, schema,
DDL, write, transaction, Flux or InfluxQL path is supplied. These missing command
semantics are not implemented; they are not claims that the engine lacks them.
Compatibility with a hosted Cloud product, its IAM/endpoint differences, live
CORS, installed CLI release and deployment are unverified by this source branch.
