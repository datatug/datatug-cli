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

## Upstash REST

Select the REST transport explicitly; a plain `https://` source is never inferred
to be Upstash. An `env:NAME` source may contain the same `upstash+https://` form.

```sh
export UPSTASH_SOURCE='upstash+https://example.upstash.io'
# Set UPSTASH_READ_TOKEN securely in the environment; do not pass its value as a flag.
datatug query redis --db env:UPSTASH_SOURCE --rest-token-env UPSTASH_READ_TOKEN --lossless -- GET customer:42
```

The endpoint must be a root HTTPS origin, without URL credentials, query,
fragment or command path. TLS certificates are verified. The token is obtained
from the named environment variable inside each bounded operation and sent only
in the Authorization header. Redirects are refused. No raw token argument,
insecure HTTP/TLS mode, URL credential or fallback credential is supported.
The token flags take uppercase environment variable **names**, such as
`UPSTASH_READ_TOKEN`, rather than token values. REST flags are rejected with
`redis://` and `rediss://` sources.

The default Upstash read-only token **does not permit SCAN**. For enumeration,
explicitly supply a separately scoped ACL-user REST token that permits SCAN:

```sh
datatug query redis --db env:UPSTASH_SOURCE --scan-token-env UPSTASH_SCAN_TOKEN --scan-prefix 'customer:' --lossless -- SCAN 0 COUNT 100
```

SCAN uses only `--scan-token-env`; other reads use only `--rest-token-env`.
Neither falls back to the other. SCAN still requires a literal prefix and
validates every returned key: ACL key patterns alone do not filter SCAN output.
Grant only the intended commands and key rights; the CLI cannot certify a token's
server-side grants. There is no KEYS fallback.

The REST path posts one JSON command array and requests raw RESP2 replies to
retain binary values and exact integers. JSON command arguments and prefixes
must be valid UTF-8; binary input keys are refused. Both admitted command text
and the encoded JSON request are capped at 64 KiB, so escaping can make a
request exceed the limit. Complete responses retain the 1 MiB, 256-element and
16-level bounds described below. Token lookup, fetch, body and final output
share the ten-second deadline. A failed or cancelled read emits no JSON prefix.
Caller cancellation closes only the owned HTTP read, and client shutdown closes
its owned transport. This is native read-only compatibility, without DataTug
application policy enforcement or multi-request transactions. Local protocol
fixtures do not establish hosted Upstash or browser CORS qualification.

## Output and command limits

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
