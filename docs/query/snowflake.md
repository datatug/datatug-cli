# Native Snowflake reads

```sh
datatug query snowflake --config snowflake.json --request request.json --timeout 10s
```

This command reads existing tables through the public DALgo native Snowflake
SQL API adapter. It has a closed structured JSON interface; it is not arbitrary
SQL, general TugQL compatibility, a managed document layout, discovery or a write
route. Snowflake role/token permissions govern access. DataTug application
policies are not applied, and this command does not certify hosted authorization.

Configuration contains trusted resource mappings and credential **environment
variable names**, not credential values:

```json
{
  "endpoint": "https://your-account.snowflakecomputing.com",
  "database": "EXISTING_DB",
  "schema": "PUBLIC",
  "warehouse": "READ_WH",
  "role": "READER",
  "tables": {"users": {"name": "EXISTING_USERS", "idColumn": "ID"}},
  "authKind": "oauth",
  "tokenEnv": "SNOWFLAKE_READ_TOKEN",
  "caFile": "optional-owned-ca.pem"
}
```

`authKind` is exactly `oauth` or `keypairJwt`. Supply a least-privilege OAuth
token or pre-generated key-pair JWT in the named environment variable through
trusted application infrastructure. The value is read when each SDK operation
requests a credential, so rotation is not captured at command construction.
No signing/private keys, admin credentials, anonymous fallback, hosted login or
credential values in argv/config/URLs are supported.

The endpoint is a fixed HTTPS `*.snowflakecomputing.com` account origin: no port,
userinfo, path, query token or fragment. TLS is verified, optionally against a
bounded regular CA file. Normal production DNS/dialing is used. There is no
insecure, test-DNS, alternate status origin or loopback flag. The owned SDK
transport disables ambient proxies, redirects, cookies, compression and reuse
that could cause implicit GET retries. Table/database/schema/column spellings
are exact; the SDK quotes native identifiers and binds values separately.

## Requests

Supply one strict JSON object from a regular file or `--request -` for stdin.
Config/request inputs are limited to 64KiB, with duplicate-key, malformed Unicode,
depth/node, unknown-field and case-sensitive presence checks. Trailing documents
and secret, SQL, endpoint, session, statement-handle or cursor fields are refused.
Optional fields must be absent or correctly typed; explicit null is not a default.

```json
{"operation":"get","collection":"users","id":"9007199254740993"}
```

`get`, `exists` and `testConnection` require a mapped collection alias and an
original nonempty UTF8 string ID. Native IDs including percent characters remain
unchanged; they are not converted through the core key-path representation.
`testConnection` performs only that supplied point SELECT and reports
`requestCompleted`/`exists`, not schema discovery or a permission certification.
A successful empty SELECT is missing. HTTP/auth/transport failures remain errors.

```json
{"operation":"getMany","collection":"users","ids":["a","missing","a"]}
```

Batch IDs are copied and admitted in full before auth. At most100 are accepted;
duplicates, order and missing positions are retained. An empty array returns an
empty result without credential or HTTP calls. Reads share one original deadline
and aggregate request/row/response/work bounds; they are not a shared snapshot.

```json
{
  "operation": "query",
  "collection": "users",
  "projection": ["ID", "AMOUNT"],
  "where": {
    "and": [
      {"documentId":true,"operator":"!=","value":null},
      {"field":"AMOUNT","operator":">=","value":{"kind":"number","value":"9007199254740993.0001"}}
    ]
  },
  "order": [{"field":"AMOUNT","direction":"desc"}],
  "limit": 20,
  "offset": 0
}
```

The CLI constructs concrete public DALgo AST objects and delegates SQL rendering
and bindings to the SDK. It does not render SQL itself. `limit` is required,
1..1000; optional `offset` is0..1,000,000,000. Projection, when supplied, is a
nonempty list of at most128 exact native field names. Omitting it selects all
columns; an empty projection does not silently become `*`. Query projections do
not need an ID and do not gain a hidden column. Ordering, when present, has1..16
unique native fields with exactly `asc`/`desc`; the SDK adds `NULLS LAST` and a
configured-ID tie-breaker. Separate OFFSET queries do not establish a snapshot.

Conditions are either exactly one `and`/`or` group with a nonempty child array,
or a comparison with exactly one selector (`field` or `documentId:true`), one
`operator` and one `value`. Conditions are bounded to64 nodes/depth8. Operators
are `=`, `!=`, `>`, `>=`, `<`, `<=`, `in`, `notIn`. Membership contains1..64
non-null scalars. Null is admitted only for equality/inequality. Strings/bools
are direct JSON values; exact numeric parameters use
`{"kind":"number","value":"<JSON number lexeme>"}`. Bare JSON numbers,
nonfinite values, custom maps, aliases, formulas, joins and nested expressions
are refused. Number lexemes are at most128 bytes with bounded exponents and
become `json.Number` without a float64 round trip. `documentId` is the actual
DALgo ID marker; a literal field `__name__` remains that application field.

```json
{"operation":"metadata"}
```

Metadata reports only copied configured database/schema/optional warehouse/role
and table bindings, with `provenance:"trusted configuration"` and
`dynamicDiscovery:false`. It performs no auth, provider I/O or CA-file read and
does not expose credential ENV names or connection infrastructure. Actual live
schema discovery is not implemented.

## Native output and delivery

The output profile is `snowflake_jsonv2_string_null_v1`. `get` returns
`document:null` or `{id,data,columns}`; getMany returns ordered `documents` with
null missing positions. Query output contains `columns` in native order and
`rows` as arrays in that exact order. Every declared result partition is
materialized and validated; over-return fails the whole command, never truncates.
No portable cursor, invented `hasMore` or statement/session token is printed.

Snowflake JSONv2 cells stay original STRING or SQL NULL. Wide integer/decimal,
exponent, temporal, binary and VARIANT text is not parsed or implicitly coerced.
Empty string and text `"null"` remain distinct from actual null. Own data fields
named `__proto__`, `constructor` and `prototype` remain ordinary native fields.
Native column names/types remain exact. Optional precision/scale/length/byteLength
descriptors use `{kind:"int64",value:"<decimal string>"}`, preserving every int64
through ordinary outer JavaScript JSON parsing. Known nullable bool values remain
bools; unprovided/JSON-null descriptors stay omitted, not fabricated zero/false.

One deadline starts before input, CA loading and auth, and covers SDK reads,
polling, all partitions, conversion, original-context final delivery and first
SIGINT handling. Default10s; allowed1ms..2m. The SDK uses max1 ordinary operation,
100 keys,1000 rows,64KiB aggregate request bodies,4MiB aggregate response bodies,
1MiB pages,256 requests,128 partitions/columns,65536 JSON nodes and120 polls at
ordinary250ms (clamped to the original timeout for deadlines below250ms).
Short valid deadlines reach native admission rather than being rejected by the
SDK constructor's polling-interval validation. These bounds do not cap remote scans, warehouse credits, hosted
memory or total process RAM.

Closed output conversion additionally charges every row, field/key/cell, repeated
document, metadata descriptor/tag and structural occurrence under1000 rows,
65536 nodes and a conservative HTML-escaped8MiB bound before final encoding.
The existing buffered writer checks8MiB again and checks the original context
immediately before one newline-terminated Write. No prefix is emitted on
admission, read, conversion or cleanup failure. A writer's own partial write/error
is propagated; bytes already accepted by an external writer cannot be rolled back.

After reads/conversion, the command calls public SDK `Shutdown` with an independent
maximum2s cleanup context, then closes its original owned transport once before
the original final context check and stdout. Known pending UUID cancellation uses
the captured submission credential at most once. Header acknowledgement is not
body EOF, remote acceptance or proof of termination. The drain waits for locally
retained logical/physical work; a timed-out drain fails a previously successful
command with zero stdout. An original read/caller/deadline failure keeps priority
over cleanup failure. Cleanup may extend wall time by up to2s beyond the original
deadline, without an unconditional sleep, forced termination or credit guarantee.

Writes, callback transactions, arbitrary SQL, universal TugQL, dynamic discovery,
portable snapshots/cursors and wider native typed-ID modes are not implemented
by this route. Snowflake's other capabilities are separate, not intrinsically
unsupported by the engine. Source/unit tests use owned synthetic TLS SQL API
fixtures and deterministic boundaries. Actual default-dial built-CLI acceptance
is a separate gate. No hosted Snowflake account/auth/warehouse/credits, installed
release, browser/CORS, main merge or Apps deployment is implied.
