# Query Typesense

DataTug CLI reads one existing [Typesense](https://typesense.org/) collection
through the [dalgo2typesense](https://github.com/dal-go/dalgo2typesense)
adapter. There are two paths, and they are deliberately different:

* a **structured query** through the normal `datatug query run` path, where
  DataTug application access policies are enforced;
* an **explicit native search** through `datatug query typesense`, where the
  scoped Typesense key's ACL is the only authorization and DataTug policies are
  not applied.

The two are never mixed, and neither is SQL or TugQL over a Typesense collection.

## The source URL and the key

```
typesense://HOST[:PORT]/COLLECTION
typesense+https://HOST[:PORT]/COLLECTION
typesense+http://127.0.0.1:8108/COLLECTION   # loopback only
```

`typesense://` is HTTPS. Plain HTTP must be asked for by name and is accepted
only for a loopback endpoint (`localhost`, `127.0.0.1` or `::1`). The path is
exactly one collection name; the endpoint the adapter receives is the origin
only. A user name, password, base path, API suffix, second path segment, query,
fragment or percent-encoded collection is refused before anything is opened.
`env:NAME` names an environment variable that holds one of these URLs.

The scoped Typesense API key comes from the environment, never from the URL:

```sh
export DATATUG_TYPESENSE_API_KEY='...scoped key...'
```

It is read for **every** request, so rotating it needs no restart. A missing,
empty or unsendable value is refused with a fixed message that names the
variable and no part of the value. A key is never written to a project, a log
line or an error message.

## Structured queries (policy-enforced)

```sh
datatug query run --db env:DATATUG_TYPESENSE_BOOKS --from books --limit 10
datatug query run --db typesense://search.example.com:8108/books -f report.tugql.yaml
```

`--from`/`-f` run through the same access-policy layer as every other source
(`accesspolicies.Run` + DALgo). A missing `--limit` on an exploratory `--from`
read defaults visibly to 100 rows; an explicit `--limit 0` is refused by the
adapter rather than replaced. The adapter's hard row budget (1000 rows) and its
page, offset, request, response and deadline bounds also apply: a result that
would need more is refused, never silently truncated.

A Typesense collection has an implicit non-null string `id` but no sortable
`id` field, and a search-only key cannot read collection metadata. `--from`
therefore keeps the engine's native relevance order and makes **no generic
stable-primary-key guarantee** across pages. Explicit ordering is expressed in
the TugQL document and is validated by the adapter's sort fields.

Structured writes are refused before any request: the adapter is read-only.
Joins, grouping, aggregation, cursors, transactions and writes are refused as
unsupported; no aggregate result and no facet result is presented as the other.

### Structured limitations (honest)

These are properties of the pinned adapter and are not worked around in the CLI:

* **`NotIn` is unsupported before I/O.** The read is refused, not approximated.
* **A filtered query validates the whole predicate over the entire bounded
  `offset + limit` candidate prefix before the first row is exposed.** The
  matching candidates are buffered; a false positive is a typed semantic
  refusal, never a silently discarded row, scan-around, local filter or a
  whole-collection fallback.
* **A semantic mismatch refuses the entire query.** Native string folding,
  array matching and normalized text make `filter_by` a candidate producer, not
  the final semantic authority. If the exact predicate cannot be proven against
  the candidate, the adapter refuses the query.
* **Strict `>` and `<` are widened to inclusive `>=` and `<=`** so a quantised
  numeric index cannot omit an eligible raw value. The exact verifier then
  rejects a boundary candidate, so a strict comparison can explicitly refuse at
  the boundary. Inclusive integer predicates and equality remain useful when
  every candidate satisfies the exact predicate.

### Arrays, ids and numbers

Collections are scanned through the generic `dbschema.SchemaReader`, keeping
each field's native declared type. A Typesense `string[]` stays a native array
in the project (`Options.SourceDefinitions`); it is not flattened into a scalar
portable type. The implicit `id` is a non-null string and the primary key.
Integers above 2^53 are preserved as 64-bit values in query output.

## Schema scan and saved queries

A scan reads one collection's declared field types and its implicit string `id`
primary key, and records only a connection descriptor that names the operator's
environment variable:

```sh
datatug scan -d ./shop -D typesense --dsn-env DATATUG_TYPESENSE_BOOKS --db books --env local
```

The descriptor (`connections/<env>/books.json`) holds `{"dsnEnv": "..."}` and
nothing else: no host, port, endpoint or key is persisted. The collection is
recorded under the logical `default` grouping used for CouchDB; Typesense has no
SQL namespace, so a bare `books` reference and `default.books` resolve to the
same collection and the same policy name. `--counts` is refused for a Typesense
source, because Typesense has no bounded native count and DataTug does not read
a whole collection to count it.

A **search-only** key (`documents:search`) runs structured and native reads but
cannot list collections or describe a schema. A scan or `db schema` then fails
with a clear metadata-forbidden message instead of an empty catalog. Discovering
the schema needs the same key configuration to also grant `collections:get` and
`collections:list`; there is no separate admin credential option.

```sh
datatug query run --project ./shop --query books/top --env local --as alice
```

Saved TugQL queries and their `policies/` rules use the ordinary
policy-enforced path; the Typesense key stays the operator's environment.

## Explicit native search

```sh
datatug query typesense --db env:DATATUG_TYPESENSE_BOOKS --request search.json
echo '{"q":"*","queryBy":["title"],"limit":5}' |
  datatug query typesense --db env:DATATUG_TYPESENSE_BOOKS --request -
```

This command is the native Typesense boundary, modeled on `datatug query redis`:

* **Authorization:** the scoped Typesense API key's ACL decides what can be
  read. DataTug application access policies are **not** applied.
* It accepts only an explicit `typesense://`, `typesense+https://` or loopback
  `typesense+http://` source (or `env:NAME` holding one). It never resolves a
  project, a `datatug://` alias or a saved query, so it can never imply that
  DataTug policies were enforced.
* The command prints the fixed access-boundary sentence on stderr before it
  executes anything, and the same sentence is in its help.
* It is **not** SQL, TugQL or a SQL aggregate. For policy-enforced structured
  access, use `datatug query run`.

### Request document

The request is one JSON object of at most **64 KiB** (the bound is checked on the
raw bytes before whitespace is trimmed). Unknown fields are refused — including
snake_case names such as `query_by`, which are **never** silently ignored — and
`limit` must be positive and explicit. The decode is strict about one object and
EOF, and every refusal happens before any provider request.

The pinned adapter's `SearchRequest` has no JSON tags, so `encoding/json` binds
these documented camelCase names by case-folded field name:

| field | meaning |
|---|---|
| `collection` | an existing collection; must match the source's collection when given |
| `q` | native query text; `"*"` is the wildcard |
| `queryBy` | array of full-text fields; required unless `q` is `"*"` |
| `filterBy` | bounded native filter expression |
| `sortBy` | array of `{"field": "...", "descending": true\|false}` |
| `facetBy` | array of facet-enabled fields |
| `maxFacetValues` | bound on returned facet values |
| `limit` | positive number of hits (required) |
| `offset` | non-negative hit offset |

### Output

The result is one JSON object, preserved field for field from the adapter's
native `SearchResult`: `collection`, `found`, `outOf`, `page`, `returned`,
`searchTimeMs`, `partial`, `complete`, `truncated`, `provenance`, and the native
`hits` and `facets`. Hits keep the Typesense string `id` and the document as
stored (arrays, `null`s and 64-bit integers included). Facets keep their native
counts and stats. A capped or cut-off result keeps `partial`/`truncated`; it is
never presented as a clean end of data. The native result is never flattened
into a SQL aggregate, and native facets are not SQL aggregate results.

Native search intentionally keeps the engine's own ANY/normalized-string,
negative and full-text semantics. It is outside DataTug policy enforcement; that
is exactly why it is a separate command.
