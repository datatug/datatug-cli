# `db mirror` is now `db copy --schema=destination`

`datatug db mirror` was removed: the same copy, with the same policy, flags and
guarantees, is [`datatug db copy --schema=destination`](db-copy.md#copying-from-a-schemaless-source---schemadestination)
(`--collections` is `--include` there). The key encoding is specified in
[Key encoding](db-copy.md#key-encoding).
