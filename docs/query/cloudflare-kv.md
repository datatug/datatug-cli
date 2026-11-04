# Native Cloudflare Workers KV REST reads

```sh
datatug query cloudflare-kv --config cloudflare-kv.json --request request.json
```

This direct native route reads an existing ordinary Workers KV namespace. It is
separate from D1, KV Instant, a Workers binding and a DALgo-managed storage layout.
Token/account/namespace permissions and configured literal prefixes govern
access; DataTug application policies are not applied. The command does not create
accounts, namespaces, keys or hosted credentials.

Trusted configuration fixes one namespace and a local collection alias:

```json
{
  "endpoint": "https://api.cloudflare.com",
  "accountId": "existing-account-id",
  "namespaceId": "existing-namespace-id",
  "collectionAlias": "cache",
  "allowedPrefixes": ["tenant-a/"],
  "tokenEnv": "CLOUDFLARE_KV_READ_TOKEN"
}
```

`endpoint` defaults to the shown HTTPS origin. It accepts only a root origin;
the command appends the fixed `/client/v4` API path. A descriptor cannot supply
an endpoint, account, namespace or credentials. Optional `caFile` names a bounded
PEM root-CA file; certificate verification remains enabled. URL credentials,
query, fragment and non-root paths are rejected. The command owns its HTTP/1-only
transport, disables keep-alives, ambient proxies and compression, follows no
redirects, and makes no retry or admin/global-key fallback.

`tokenEnv` is the **name** of an environment variable holding a read-scoped bearer
token. It is read when the SDK starts an operation, not cached at construction.
Do not put token values in argv or the configuration. Empty, unset and malformed
tokens fail without anonymous fallback. A namespace-wide empty prefix requires
explicit `"allowNamespaceAccess": true`; ordinary empty prefix grants are
rejected. Prefix checks are a local boundary, not proof of a hosted prefix ACL.
The alias defaults to `kv`; it is not a discovered Cloudflare table or namespace
name, and requests must select the configured alias.

## Point and batch reads

The request is one strict JSON object, from a regular file or `--request -`:

```json
{"operation":"get","collection":"cache","key":"tenant-a/original-key"}
```

Keys retain exact valid UTF8 spelling, including slash, literal percent escapes,
astral characters and whitespace. Invalid UTF8/surrogate input, empty keys, exact
`.` or `..`, keys longer than 512 bytes and keys outside literal grants are
refused. The full key is encoded once as one path segment; a literal `%2F` is sent
as `%252F`. No path cleaning or managed key rewrite occurs. Native binding and
REST documentation differ on accepted character wording; local wire proof does
not establish hosted acceptance of every key spelling.

`exists` uses the same bounded authorized point read. `testConnection` also
requires a key and reports `requestCompleted: true` and `exists: true` only after
that read succeeds. It does not enumerate accounts/namespaces or certify broader
authorization. **All REST 404s remain errors.** There is no invented numeric
missing-key code, error-message matching, successful `exists: false`, missing
`null` position, or conversion of namespace/authorization errors into absence.

```json
{"operation":"getMany","collection":"cache","keys":["tenant-a/a","tenant-a/b","tenant-a/a"]}
```

Batches retain input order and duplicates under one operation deadline and
aggregate budget. An empty array succeeds without token lookup or HTTP. A failure
aborts the entire result without partial stdout or dispatching later keys.

## Lossless output

Output identifies `"format":"cloudflare_kv_raw_v1"`. A point read contains an
`entry`; a batch contains ordered `entries`. Each entry retains its original key:

```json
{
  "key": "tenant-a/original-key",
  "value": {"kind":"bytes","base64":"AP+AbnVsbA=="},
  "expiration": {"kind":"uint64","value":"18446744073709551615"}
}
```

Values are original raw bytes in canonical RFC 4648 base64. Present empty bytes
use an empty base64 string, not JSON null. Bytes that happen to contain JSON,
JSON `null`, invalid UTF8 or NULs are never automatically decoded. Expiration is
an optional observed unsigned integer number of Unix seconds, represented as a
decimal string to avoid rounding in ordinary JSON consumers. Missing expiration
is distinct from an observed zero; the illustrative maximum is a fidelity test,
not a claim Cloudflare emits every possible uint64 timestamp.

## Metadata-only whole-page listing

```json
{"operation":"list","collection":"cache","prefix":"tenant-a/","pageSize":10,"pages":2}
```

The REST page size must be 10 through 1,000, pages 1 through 100, with
`pageSize * pages <= 1000`. A list request requires an explicit prefix within
the configured grant. Listing returns `keys` with original names, optional
expiration and metadata. It does not fetch values or pretend to run SQL.

Metadata uses a raw JSON **text** tag:

```json
{"name":"tenant-a/a","metadata":{"kind":"json","value":"{\"wide\":9007199254740993.000,\"__proto__\":{\"constructor\":\"own\"}}"}}
```

This retains original JSON number lexemes, whitespace and own field names even
when the outer output is parsed by ordinary JavaScript `JSON.parse`. No metadata
value is decoded through float64 or rewritten into a managed object. Interpreting
wide metadata numbers still requires a lossless parser. Absent metadata omits the
field; present JSON null uses `{"kind":"json","value":"null"}`. A listing's
metadata observation is not atomic with a separate value read.

The command traverses whole original pages, including empty or short unfinished
pages, under the original command deadline. Completion follows the REST cursor
contract, not result count. Malformed envelopes, over-return, wrong scope,
duplicate/backward UTF8-byte key order and nonprogressing positions fail the whole
command. Results are never filtered or truncated to fabricate success.

Output includes page count and advisory `hasMore`. SDK cursor handles and raw
provider cursor text remain internal to this one process; no cursor/session or
portable resume token is accepted or printed. If a later page, conversion or
cancellation fails, the command emits no result and closes its whole internal
journey. A new invocation starts again. Ordinary KV eventual consistency means
listing is not a snapshot and cannot promise complete coverage under concurrent
mutation, authoritative absence, or exactly-once network delivery.

## Bounds and cancellation

`--timeout` defaults to 10 seconds and accepts 1 ms through 2 minutes. Its scope
starts before configuration/request/CA reads and covers credentials, all native
requests, conversion, owned client and transport cleanup, serialization and the
final stdout guard. Per-page SDK calls share that original parent deadline.
First SIGINT uses the command's graceful cancellation path. Cancellation and
deadline failures are redacted; stdout remains empty on refusal.

Configuration, request and CA files are individually capped at 64 KiB. The route
admits at most 100 batch keys, 100 pages and 1,000 listed keys. The SDK caps each
raw value at 1 MiB, aggregate raw responses at 4 MiB, escaped request URLs at
64 KiB, and requested-row work at 1,000. Its one internal cursor journey also has
bounded retained bytes/nodes and an original fixed lifetime. The CLI performs
additional conservative escaped-size admission and retains the shared final
8 MiB JSON output cap. An over-budget result fails whole, without a partial page.
These are local bounds, not provider billing or server scan-work guarantees.

The owned client and transport are closed exactly once before success is
serialized and freshly admitted for one stdout write. The command preserves the
shared encoder's newline/escaping and ordinary writer/partial-write errors. It
does not turn already-written output into cancellation afterward or claim it can
retract bytes an output writer accepted. Local cancellation does not prove remote
Cloudflare execution was killed.

Writes and generic DALgo query compilation are not implemented by this route.
The exact native KV REST protocol has no SQL interface or cross-key atomic
snapshot/read-write transaction; this does not mean the provider cannot write a
single key. Dynamic schema/account/namespace discovery is not implemented.

The provider source and clean-public-module consumer are separately qualified
with owned TLS; new CLI source, full coverage/lint, new built-binary TLS/SIGINT
acceptance and publication each need their own controller receipts. This document
does not claim a hosted account, real token IAM, actual Workers/browser/CORS,
installed CLI release, Apps integration, registry tag or main-branch merge.
