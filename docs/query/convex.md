# Native Convex public-query reads

`datatug query convex --config convex-native.json --request request.json`
reads existing data through explicitly configured public Convex query functions.
It does not create a storage layout, discover tables, accept arbitrary function
paths from a request, or expose mutations, actions, admin credentials or SQL.
Writes, transactions and generic DALgo/TugQL query translation are not implemented
by this command. Deployment query functions remain responsible for authorization;
DataTug application access policies are not applied to this native route.

The trusted configuration fixes a verified HTTPS deployment root, collection and
query aliases, permitted dynamic argument names and immutable static arguments:

```json
{
  "endpoint": "https://your-deployment.convex.cloud",
  "tokenEnv": "CONVEX_READ_USER_JWT",
  "collections": {
    "tasks": {
      "getFunction": "dalgo:getTask",
      "idArgument": "id",
      "idField": "_id",
      "staticArgs": {"tenant": "trusted-tenant"},
      "fields": ["_id", "text", "done"]
    }
  },
  "queries": {
    "tasksByOwner": {
      "function": "dalgo:listTasks",
      "collection": "tasks",
      "argumentNames": ["owner"],
      "staticArgs": {"tenant": "trusted-tenant"}
    }
  }
}
```

Supply the token through the named environment variable, not argv or a JSON
credential value. The environment is read when the SDK starts the operation.
Explicit `"anonymous": true` can replace `tokenEnv` only when the configured public
functions deliberately authorize anonymous reads; the two modes cannot be mixed.
There is no missing-token anonymous fallback. Optional `caFile` names a bounded
PEM root-CA file. TLS verification remains enabled. The transport forbids redirects
and ambient proxies; the configuration cannot contain URL credentials, a query,
a fragment or a non-root path.

Requests are one strict JSON object, from a regular file or `--request -`:

```json
{"operation":"get","collection":"tasks","id":"opaque-native-document-id"}
```

Other operations are `exists`, `testConnection` (the same authorized point read,
with `requestCompleted` and `exists`), and `getMany` with `ids`. Batch output keeps
input order, duplicates and `null` for missing documents; an empty batch performs
no HTTP request. Native IDs are opaque strings accepted by the pinned SDK, not
table names or resource paths. A configured get function must validate the ID's
table and authorization before returning a document or `null`.

A paginated request selects a configured query alias:

```json
{"operation":"query","query":"tasksByOwner","args":{"owner":"native-user-id"},"pageSize":25,"pages":4}
```

The public function receives ordinary dynamic/static arguments and
`paginationOpts` with `numItems`, `cursor`, `maximumRowsRead` and
`maximumBytesRead`. It must return the native `{page,isDone,continueCursor}`
contract. The command traverses at most the requested number of whole pages,
including empty evaluated pages, under one command deadline. `pages * pageSize`
must not exceed 1,000; page size stays fixed during the journey. Oversized pages,
repeated progress positions, duplicate document identities and malformed replies
fail closed; output is never truncated to fabricate success. Output `hasMore`
means the last returned page was unfinished. Internal instance/auth/function-bound
SDK continuation handles stay inside the process and are closed at completion;
no resumable token is printed. A new invocation starts a new journey. There is no
cross-page snapshot guarantee or promise that `hasMore` implies a nonempty page.

Output identifies `"format":"convex_encoded_json"`. A point result contains
`document: {id,data}` or `null`; batch/query results contain ordered `documents`.
Native argument and document data use Convex's encoded JSON representation:

```json
{"wide":{"$integer":"/////////38="},"binary":{"$bytes":"AP+A"},"negativeZero":{"$float":"AAAAAAAAAIA="}}
```

`$integer` stores signed int64 as base64 little-endian two's-complement bytes;
`$bytes` stores binary as base64. `$float` preserves negative zero, infinities and
NaN bits. Ordinary numbers represent native finite float64 values, not arbitrary
precision decimal lexemes. Do not replace these tagged values with JavaScript
wide JSON number literals. Arrays, nested objects, strings, booleans and null keep
their native meanings. Valid own data fields such as empty names, `_data`,
`__proto__`, `constructor` and `prototype` are copied safely through Go maps;
their wire-codec validity is separate from backend stored-document naming rules.
Control records and argument allowlists remain closed and case sensitive.

`--timeout` defaults to 10 seconds and accepts 1 ms through 2 minutes. The parent
command context and first SIGINT cover input reads, credentials, native codecs,
HTTP, pagination, client close and the final buffered stdout guard. Cancellation
and deadline errors are redacted. Success is serialized before a fresh final
context check and one stdout write; ordinary writer/partial-write errors remain
errors. The operation cannot retract bytes already accepted by an output writer.

Input/config/CA files are bounded at 64 KiB each; at most 100 batch keys, 100 pages,
1,000 returned rows, 1 MiB per response and 64 KiB aggregate SDK request bodies are
admitted. The SDK also bounds physical work and cursor retention. Encoded output
is bounded at 8 MiB with JSON escaping included. These local limits do not prove
backend scan cost, deployment IAM/JWT policy, a hosted service account, an installed
release or browser/CORS behavior. Account-free owned TLS tests exercise the HTTP
contract; a real Convex backend remains a separate qualification gate.
