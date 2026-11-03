# Query Redis keys

Use native read commands directly from DataTug CLI:

```sh
export REDIS_URL='rediss://reader:password@redis.example.com:6379/0'
datatug query redis --db env:REDIS_URL -- GET customer:42
datatug query redis --db env:REDIS_URL -- MGET customer:42 customer:43
datatug query redis --db env:REDIS_URL -- HMGET customer:42 name email
```

The URL path selects the numeric Redis database. `rediss://` verifies TLS;
`redis://` is plaintext and suits a trusted local connection. Use a dedicated
Redis ACL user restricted to intended key patterns and allowed reads.
Environment variables keep connection credentials out of shell arguments.

Output is one JSON value: strings for GET/HGET, ordered arrays for MGET/HMGET,
integers for metadata, and `null` for absent values. WRONGTYPE, ACL denials and
connection failures exit nonzero. URLs, passwords and server errors are omitted
from diagnostics. This default output remains intended for text data.

Use `--lossless` for arbitrary binary data or exact 64-bit integer replies:

```sh
datatug query redis --db env:REDIS_URL --lossless -- GET customer:42
datatug query redis --db env:REDIS_URL --lossless -- PTTL customer:42
```

Bulk replies become `{"kind":"bytes","base64":"..."}` and integers become
`{"kind":"integer","value":"9223372036854775807"}`. Arrays become
`{"kind":"array","values":[...]}` recursively; null stays null and TYPE's
simple status stays a string. This matches the independent TypeScript RESP
adapter's native value contract. Default output retains legacy strings/numbers;
JavaScript consumers must select lossless mode to avoid byte/int64 loss.

Supported commands: GET, MGET, HGET, HMGET, HEXISTS, EXISTS, TYPE, TTL, PTTL,
STRLEN, HLEN, LLEN, SCARD, ZCARD and ZSCORE. Writes, Lua, administrative operations,
KEYS, blocking commands and unbounded collection reads are rejected
before connecting. Calls accept at most 128 key/field arguments, 64 KiB of
command text and a ten-second deadline. Each complete Redis reply is capped at
1 MiB before driver allocation. Larger replies fail safely and close that
connection; later queries reconnect. Each array is capped at 256 elements and
nesting at 16 levels.

SCAN is opt-in and returns exactly one page:

```sh
datatug query redis --db env:REDIS_URL --lossless --scan-prefix 'customer:' -- SCAN 0 COUNT 100
```

The configured prefix is literal: glob/backslash characters are escaped and the
client forces `<escaped prefix>*` MATCH. A conflicting user MATCH is refused;
without a nonempty prefix SCAN fails before connecting. The entire response fails
if its shape/cursor is malformed or any key falls outside the literal prefix.
COUNT is a hint (1 to 1,000, default 100), not a result bound: a page may exceed
the reply/array cap and fail without truncation. Duplicate keys and empty pages
with nonzero cursor are legal; pass the exact returned cursor for a later page.
The caller must bound total work across pages; SCAN is not a snapshot.

`--scan-prefix` is trusted local operator configuration, not a substitute for
Redis credentials or per-user server authorization. ACL key patterns alone do
not filter SCAN enumeration, and the prefix does not scope point reads.
The Go transport owns a separate connection/driver per read so caller cancellation
can close only that read promptly. This adds reconnect/authentication latency;
admission retains the configured connection limit. Client shutdown waits for
active reads, and cancellation/deadline checks run before success output.

This command uses Redis's native flat key namespace and ACLs. It does not run
SQL, TugQL, DataTug application access policies, saved project queries or
cluster routing. Multi-key reads are not a snapshot across calls. Use `--`
before the command to preserve keys beginning with a dash.
