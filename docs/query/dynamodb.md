# Native DynamoDB reads

`datatug query dynamodb` reads existing DynamoDB tables through the public Go
adapter and official AWS SDK v2. A trusted JSON config fixes the HTTPS endpoint,
region, collection-to-table mapping and native partition/sort key names:

```json
{
  "endpoint": "https://dynamodb.us-east-1.amazonaws.com",
  "region": "us-east-1",
  "tables": {
    "orders": {
      "name": "existing-orders",
      "partitionKey": "account",
      "sortKey": "id",
      "consistentRead": false
    }
  }
}
```

The config is local trusted operator input. Credential values never belong in
it or in a URL. The command reads only `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY` and optional `AWS_SESSION_TOKEN` under its operation
context. Use narrowly scoped temporary IAM credentials allowing GetItem/Query
on the intended tables. Profile, IMDS, SSO and account discovery are not used.
DataTug application access policies are **not applied** to this native command.

Only verified HTTPS root endpoints are accepted. URL userinfo, paths, query
strings, fragments, redirects and ambient HTTP proxies are refused. An optional
`caFile` config field names a PEM CA file for an owned TLS endpoint; it adds no
insecure certificate bypass. Both input documents and the CA file are limited
to 64 KiB. Keep the mapping separate from an untrusted read request.

```sh
datatug query dynamodb --config mapping.json --request get.json
datatug query dynamodb --config mapping.json --request - --timeout 10s < query.json
```

Each request is one JSON object, with no unknown fields, duplicate object keys
or trailing document. Invalid UTF-8 and lone UTF-16 surrogate escapes are
rejected instead of silently changing a key. Well-formed Unicode is preserved.
Field matching follows Go encoding/json case folding. Use the canonical
camelCase names shown here.

Read one exact native key:

```json
{
  "operation": "get",
  "collection": "orders",
  "key": {
    "account": {"S": "tenant-one"},
    "id": {"N": "9007199254740993"}
  }
}
```

`exists` takes the same key and returns an `exists` boolean. `getMany` takes
`keys: [...]` instead of `key`, preserving order, duplicates and missing null
items. It uses sequential reads under one aggregate budget, not a transaction.
Keys must have exactly the configured names, with one S, N or B tag per value.
S and B keys are nonempty; N is an exact decimal string, B is standard base64.

A bounded native partition Query uses a literal partition value:

```json
{
  "operation": "query",
  "collection": "orders",
  "partition": {"S": "tenant-one"},
  "sort": {
    "operator": "between",
    "values": [{"N": "1"}, {"N": "100"}]
  },
  "limit": 20,
  "descending": false
}
```

Sort conditions are optional and require a mapped sort key. Supported operators
are `=`, `<`, `<=`, `>`, `>=`, `between` and string/binary `begins_with`.
Expressions and attribute names are bound by the adapter, never taken as raw
request expressions. There is no Scan, PartiQL, index, SQL, write, schema or
transaction fallback. Missing adapter semantics are not claims that DynamoDB
intrinsically lacks those features.

Output is one JSON object with a trailing newline. Item values retain their AWS
tags: S/N/B/BOOL/NULL/M/L/SS/NS/BS. N and NS remain decimal strings; B and BS use
base64. Native sets remain sets, nested values remain tagged, and a missing item
is null. Decimal and wide-integer values are never coerced to float64.

Query output includes `items`, `pages`, `evaluated` and `hasMore`. Empty
pages with a continuation are followed within the operation limits.
`hasMore: true` means the provider returned a continuation after the requested
limit; **no portable cursor is emitted**. Current provider cursors belong to the
in-process client and cannot be resumed by another CLI invocation. This output
does not claim the partition was fully exhausted or read as a snapshot.

Limits per command: 10 seconds by default (configurable from 1 ms to 2 minutes),
1,000 requested records including GetMany duplicates, 1 MiB aggregate parsed
key/result/cursor work, 100 SDK Query pages, 10,000 evaluated records, and 100
evaluated records per SDK page. Output has an additional 8 MiB JSON limit.
Malformed, foreign, unordered, oversize or non-progressing replies fail closed
without partial JSON. Parsed-result limits apply **after the SDK decoded the
response**; they are not a wire-allocation or AWS billing guarantee.

The deadline begins before input, credentials, signing and requests. Bounded
input admission allows at most two outstanding file/stdin readers across command
instances. A borrowed stdin reader that ignores cancellation retains its slot
until it finishes; cancellation does not close someone else's stdin. The CLI
owns its HTTP transport and closes idle connections on exit. Results are encoded
before a final caller/deadline check immediately before stdout delivery. Ordinary
partial-write errors propagate; output already committed is not relabeled as a
cancellation.

Tests use the actual official SDK with owned loopback TLS protocol fixtures.
They do not prove DynamoDB Local semantics, hosted IAM/STS, capacity/billing,
registry releases, application integration, or a deployment.
