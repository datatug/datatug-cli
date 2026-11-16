# Native Databricks reads

```sh
datatug query databricks --config databricks.json --request request.json --timeout 10s
```

This command reads existing tables through the public DALgo native Databricks SQL API adapter (`dalgo2databricks`). It has a closed structured JSON interface; it is not arbitrary SQL, general TugQL compatibility, a managed document layout, discovery or a write route. Databricks permissions and SQL warehouse settings govern access. DataTug application policies are not applied, and this command does not certify hosted authorization.

Configuration contains trusted resource mappings and credential **environment variable names**, not credential values:

```json
{
  "endpoint": "https://adb-1234567890123456.7.azuredatabricks.net",
  "warehouseId": "0123456789abcdef",
  "catalog": "main",
  "schema": "default",
  "tables": {"users": {"name": "users", "idColumn": "id"}},
  "authKind": "pat",
  "tokenEnv": "DATABRICKS_TOKEN",
  "caFile": "optional-owned-ca.pem"
}
```

`authKind` is `pat` (Personal Access Token), `oauthM2M` (OAuth Machine-to-Machine), or `bearer` (IAM/Service Account dynamic token). Supply a least-privilege token in the named environment variable through trusted application infrastructure. The value is read when each SDK operation requests a credential, so dynamic rotation is supported seamlessly. No signing keys, admin credentials, anonymous fallback, hosted login or credential values in argv/config/URLs are supported.

The endpoint supports Databricks workspaces on Microsoft Azure (`adb-*.azuredatabricks.net`), AWS (`dbc-*.cloud.databricks.com`), and GCP (`*.gcp.databricks.com`), as well as private corporate VPC domains (`*.corp.internal`). TLS is verified, optionally against a verified CA file. Table, catalog, schema, and column spellings are exact; the SDK safely escapes identifiers with backticks and parameterizes values.

## Requests

Supply one strict JSON object from a regular file or `--request -` for stdin. Config/request inputs are limited to 64KiB, with duplicate-key, malformed Unicode, depth/node, unknown-field and case-sensitive presence checks. Trailing documents and secret, SQL, endpoint, session, statement-handle or cursor fields are refused. Optional fields must be absent or correctly typed; explicit null is not a default.

```json
{"operation":"get","collection":"users","id":"9007199254740993"}
```

`get`, `exists` and `testConnection` require a mapped collection alias and an original nonempty UTF8 string ID. Native IDs remain unchanged; they are not converted through the core key-path representation. `testConnection` performs only that supplied point SELECT and reports `requestCompleted`/`exists`, not schema discovery or a permission certification. A successful empty SELECT is missing. HTTP/auth/transport failures remain errors.

```json
{"operation":"getMany","collection":"users","ids":["a","missing","a"]}
```

Batch IDs are copied and admitted in full before auth. At most 100 are accepted; duplicates, order and missing positions are retained. An empty array returns an empty result without credential or HTTP calls. Reads share one original deadline and aggregate request/row/response/work bounds; they are not a shared snapshot.

```json
{
  "operation": "query",
  "collection": "users",
  "projection": ["id", "amount"],
  "where": {
    "and": [
      {"documentId":true,"operator":"!=","value":null},
      {"field":"amount","operator":">=","value":{"kind":"number","value":"9007199254740993.0001"}}
    ]
  },
  "order": [{"field":"amount","direction":"desc"}],
  "limit": 20,
  "offset": 0
}
```

The CLI constructs concrete public DALgo AST objects and delegates SQL rendering and bindings to the SDK. It does not render SQL itself. `limit` is required, 1..1000; optional `offset` is 0..1,000,000,000. Projection, when supplied, is a nonempty list of at most 128 exact native field names. Omitting it selects all columns; an empty projection does not silently become `*`. Query projections do not need an ID and do not gain a hidden column. Ordering, when present, has 1..16 unique native fields with exactly `asc`/`desc`.

Conditions are either exactly one `and`/`or` group with a nonempty child array, or a comparison with exactly one selector (`field` or `documentId:true`), one supported operator (`=`, `!=`, `>`, `>=`, `<`, `<=`, `in`, `notIn`), and a scalar or membership value.

## Output Wire Format

Output format `databricks_jsonv2_string_null_v1` preserves original JSONv2 STRING and NULL cells, including numeric, temporal, binary and complex text. Column int64 descriptors use `{kind:"int64", value:"<decimalString>"}`. All result partitions are returned or the whole command fails; offsets do not establish a snapshot. Owned SDK work is drained before stdout with an independent maximum 2s cleanup scope. Cancellation preserves the original error and zero stdout.
