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
from diagnostics. Redis values are strings, so this command suits text data.

Supported commands: GET, MGET, HGET, HMGET, HEXISTS, EXISTS, TYPE, TTL, PTTL,
STRLEN, HLEN, LLEN, SCARD and ZCARD. Writes, Lua, administrative operations,
key enumeration, blocking commands and unbounded collection reads are rejected
before connecting. Calls accept at most 128 key/field arguments, 64 KiB of
command text and a ten-second deadline. Each complete Redis reply is capped at
1 MiB before driver allocation. Larger replies fail safely and close that
connection; later queries reconnect.

This command uses Redis's native flat key namespace and ACLs. It does not run
SQL, TugQL, DataTug application access policies, saved project queries or
cluster routing. Multi-key reads are not a snapshot across calls. Use `--`
before the command to preserve keys beginning with a dash.
