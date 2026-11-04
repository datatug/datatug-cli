# Native Cosmos DB for NoSQL reads

`datatug query cosmosdb --config cosmos.json --request read.json` reads existing
native documents. It preserves `id`, the configured partition field, `_etag`,
system fields and custom fields. It creates no DALgo envelope or Azure resources.
Access comes from the supplied scoped authorization; DataTug application policies
are not applied to this dedicated native command.

Trusted configuration contains names and endpoint information, never token values:

```json
{"endpoint":"https://example.documents.azure.com","database":"existing-db","container":"existing-container","partitionField":"tenant","authScopeId":"reader-for-tenant-a","authType":"resource","tokenEnv":"COSMOS_READ_RESOURCE_TOKEN"}
```

`authType: "resource"` expects an **unencoded** broker-issued
`type=resource&ver=1.0&sig=...` environment value. `authType: "aad"` expects a raw
AAD access token, without `Bearer` or a Cosmos authorization prefix. The provider
performs the required type-specific encoding; it never guesses token type or
falls back to an account/master/admin key. Tokens are acquired inside each bounded
operation, not during construction. `authScopeId` is a required nonsecret local
scope label; it is not proof of Azure RBAC or token permissions. Optional `caFile`
names a bounded PEM trust file. HTTPS verification stays on. Ambient proxies,
redirects and automatic retries are disabled. Endpoint credentials/query/fragment
and literal token fields are refused. Use environment injection appropriate to
your shell instead of including secret values in command history or JSON.

Requests are one strict JSON object, at most 64 KiB. `--request -` uses stdin.
Unknown/duplicate fields, malformed Unicode and trailing values fail before
authorization. Every key supplies its native ID and an explicit nonempty string
partition:

```json
{"operation":"get","key":{"id":"event-1","partition":"tenant-a"}}
```

Other point operations are `exists` and `testConnection` with the same key.
`testConnection` reports `requestCompleted: true` and `exists`; an authenticated
point read returning a missing document is also a completed request. This is not
an IAM-policy verification, hosted-service certification or account/schema probe.

Ordered batches retain duplicates and missing positions:

```json
{"operation":"getMany","keys":[{"id":"event-1","partition":"tenant-a"},{"id":"missing","partition":"tenant-a"},{"id":"event-1","partition":"tenant-a"}]}
```

Queries pass SQL and named JSON parameters separately. Literal JSON numeric
parameters retain their number lexeme; they are never converted through float64.
Full documents must retain `id` and the configured physical partition field.
Scalar/aggregate/projection results lacking native identity are not implemented.

```json
{"operation":"query","partition":"tenant-a","sql":"SELECT * FROM c WHERE c.amount >= @minimum","parameters":[{"name":"@minimum","value":9007199254740993.000}],"limit":10}
```

Output documents contain `{key:{id,partition},data:{...}}`, preserving nested
arrays, objects, strings, booleans and null. Every JSON number is represented as
`{"kind":"number","value":"9007199254740993.000"}` so JavaScript can retain
the exact returned lexeme. A native object already shaped like that tag remains
an ordinary object; the format does not provide a schema for reversing ambiguous
user objects. This is lossless lexical transport, not an arbitrary-precision
guarantee about numbers stored by Cosmos itself. Get returns `document: null`
for absence; getMany emits positional nulls. Query emits `documents`, `pages`
and `hasMore`. No raw continuation, session token, credential, activity identifier
or service error body is printed. Portable cross-process query continuation is
not implemented: `hasMore` alone cannot resume a later CLI invocation.

The whole command deadline defaults to 10 seconds (1 ms through 2 minutes),
starting before input and authorization. It includes query preparation, body
read/cleanup, value conversion and final buffered output admission. First
SIGINT/SIGTERM follows this cancellation path. The client closes before final
output admission. These bounds cannot retract bytes accepted by an arbitrary
output writer after the write starts.

Provider limits here are 1,000 rows, 100 batch keys, 100 requests/pages, 100 rows
per service page, aggregate 1 MiB raw response bodies, aggregate 64 KiB query
request bytes and one active owned operation. Output conversion additionally
refuses unsupported/deep/cyclic values and excessive aggregate tagging/escaping;
the shared renderer buffers at most an admitted 8 MiB envelope before stdout.
Overflow/error/cancellation refuses the whole result without a stdout prefix.
These limits bound local materialization and request work; they do not bound
Cosmos engine scans, RU charges, account billing, credential-provider allocation
or a single remote computation. No automatic retry or stronger consistency
override occurs. Per-response RU/session metadata is not exposed by this CLI.

This initial mode supports a single top-level nonempty string partition value.
Numeric, boolean, null, empty, hierarchical, undefined and unpartitioned native
keys remain not implemented. Cross-partition querying, writes, transactions,
schema management and automatic account/container creation are not implemented.
Owned TLS wire fixtures and clean public-module builds qualify only their stated
local contracts. Real Cosmos emulator/hosted Azure authentication, RU behavior,
service compatibility and production privileges require separate evidence.
