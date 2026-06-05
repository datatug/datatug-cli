# TEST-COVERAGE.md — firestoreschema

## Coverage metrics

| Run | Coverage % | Uncovered statements |
|-----|-----------|----------------------|
| Pre-run (no tests) | 0.0% | ~42 |
| Post-run | 90.5% | 4 |

## Seams added

All seams are package-level `var` function variables in `simple_provider.go`.
Each replaces a direct external call with an overridable indirection:

| Seam var | Replaces | File | Lines |
|----------|----------|------|-------|
| `firestoreDoc` | `client.Doc(path)` | simple_provider.go | 16–18 |
| `firestoreCollections` | `p.Collections(ctx)` | simple_provider.go | 19–21 |
| `iterCollectionNext` | `iter.Next()` | simple_provider.go | 22–24 |
| `closeFirestoreClient` | `c.Close()` | simple_provider.go | 25–27 |

## Documented gaps (uncovered branches)

### external-io — default seam bodies (4 statements)

The four seam variables declared at lines 16–27 each have a one-statement default
body that delegates to a real `*firestore.Client` API. Tests always override these
seams via package-level var assignment, so the default bodies are never executed
during testing.

| Function / location | Uncovered statement | Reason | Refactor required |
|--------------------|---------------------|--------|-------------------|
| `firestoreDoc` default body (line 17) | `return client.Doc(path)` | Requires a live `*firestore.Client`; nil client panics | Firestore emulator integration test, or replace `*firestore.Client` throughout with an interface |
| `firestoreCollections` default body (line 20) | `return p.Collections(ctx)` | Same: needs live client to create a non-nil iterator | Same as above |
| `iterCollectionNext` default body (line 23) | `return iter.Next()` | `*firestore.CollectionIterator` has unexported fields; cannot be constructed without a live client | Firestore emulator, or change `firestoreCollectionsProvider` to return a `CollectionIteratorNext` interface instead of `*firestore.CollectionIterator` |
| `closeFirestoreClient` default body (line 26) | `return c.Close()` | Nil `*firestore.Client` panics on `c.Close()` | Firestore emulator integration test, or wrap the entire `GetCollections` flow in a higher-level injectable interface |
