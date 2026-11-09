# Compatibility

Redlite targets wire compatibility, not feature parity. If a command is listed
as supported, it returns the same reply shape as Redis; if it is not listed, the
server answers `unknown command` rather than pretending.

## Reply encoding

- RESP2 is the default. `HELLO 3` switches the connection to RESP3.
- Maps (`CONFIG GET`) are encoded as flat arrays in RESP2 and maps in RESP3.
- Nulls are `$-1` in RESP2 and `_` in RESP3.
- Errors are always a single line; embedded newlines are replaced with spaces so
  a reply can never desynchronise the stream.

## Supported commands

| Category | Commands |
| --- | --- |
| Connection | `PING` `ECHO` `QUIT` `SELECT` `HELLO` |
| Introspection | `CLIENT` `COMMAND` `CONFIG` `INFO` `TIME` `LASTSAVE` |
| Admin | `SHUTDOWN` `BGREWRITEAOF` |
| Strings | `GET` `SET` `SETNX` `SETEX` `PSETEX` `GETSET` `GETDEL` `GETEX` `MGET` `MSET` `MSETNX` `APPEND` `STRLEN` `INCR` `DECR` `INCRBY` `DECRBY` |
| Hashes | `HSET` `HSETNX` `HMSET` `HGET` `HMGET` `HDEL` `HLEN` `HEXISTS` `HKEYS` `HVALS` `HGETALL` `HINCRBY` `HSCAN` |
| Lists | `LPUSH` `RPUSH` `LPUSHX` `RPUSHX` `LPOP` `RPOP` `LLEN` `LINDEX` `LSET` `LRANGE` `LTRIM` `LREM` |
| Sets | `SADD` `SREM` `SCARD` `SISMEMBER` `SMISMEMBER` `SMEMBERS` `SINTER` `SUNION` `SDIFF` `SPOP` `SRANDMEMBER` `SSCAN` |
| Sorted sets | `ZADD` `ZREM` `ZSCORE` `ZMSCORE` `ZINCRBY` `ZCARD` `ZCOUNT` `ZRANGE` `ZREVRANGE` `ZRANGEBYSCORE` `ZREVRANGEBYSCORE` `ZRANK` `ZREVRANK` `ZREMRANGEBYRANK` `ZREMRANGEBYSCORE` `ZSCAN` |
| Keyspace | `DEL` `UNLINK` `EXISTS` `TYPE` `KEYS` `SCAN` `RANDOMKEY` `RENAME` `RENAMENX` `TOUCH` `DBSIZE` `FLUSHDB` `FLUSHALL` |
| Expiry | `EXPIRE` `PEXPIRE` `EXPIREAT` `PEXPIREAT` `TTL` `PTTL` `PERSIST` `EXPIRETIME` `PEXPIRETIME` |

## Known differences

### Data model

- Five value types exist: string, hash, list, set and sorted set. Streams,
  bitmaps and hyperloglogs are not implemented yet (`ROADMAP.md`).
- Every command checks the type and returns
  `WRONGTYPE Operation against a key holding the wrong kind of value` instead of
  coercing. `MGET` is the one exception Redis defines: a non-string value is
  reported as nil.
- `SET`, `MSET` and `GETSET` replace a value of any type. `SET ... GET` on a
  non-string key returns `WRONGTYPE` without writing.
- Empty hashes, lists, sets and sorted sets are removed from the keyspace.
- One database. `SELECT 0` succeeds; any other index returns
  `ERR DB index is out of range`.

### RESP3 reply shapes

- `HGETALL` returns a map in RESP3 and a flat array in RESP2, matching Redis.
- `ZRANGE`, `ZREVRANGE`, `ZRANGEBYSCORE` and `ZREVRANGEBYSCORE` with
  `WITHSCORES` return an array of `[member, score]` pairs in RESP3 and a flat
  `member, score, ...` array in RESP2.
- `CONFIG GET` returns a map in RESP3 and a flat array in RESP2.
- `HSCAN`, `SSCAN` and `ZSCAN` return `[cursor, flat pairs]` in both protocols.

### Ordering and randomness

Redis leaves the order of `HGETALL`, `HKEYS`, `HVALS`, `SMEMBERS`, `SINTER`,
`SUNION`, `SDIFF`, and `HSCAN`/`SSCAN`/`ZSCAN` pages unspecified. Redlite returns
the set-like results sorted; `ZSCAN` walks in rank order. That is compatible
with the contract (callers may not depend on order) and makes tests
reproducible, but do not treat the order as portable.

`SPOP` and `SRANDMEMBER` use Go's process-local PRNG. The selection is uniform
enough for application use but is not cryptographically secure.

`SRANDMEMBER` with a negative count returns exactly that many elements and may
repeat members; with a positive count it returns unique members, at most the
cardinality. `SPOP` with a count removes and returns that many, or everything
when the count exceeds the cardinality.

### Scanning

`SCAN` cursors are indexes into a sorted snapshot of the keyspace, not hash
buckets. `HSCAN`, `SSCAN` and `ZSCAN` use the same approach over sorted field
names, members and rank order. Consequences:

- Pagination is correct and deterministic for an unchanged keyspace.
- `COUNT` limits how many keys are *examined*, not how many are returned —
  the same contract Redis documents, but the cursor value itself is not
  compatible with a real Redis instance.
- A key added or removed between calls can shift a page. Do not rely on the
  cursor across mutations.

`KEYS` takes the read lock for the whole scan, so it blocks writers while it
runs. It is a debugging command, exactly as in Redis.

### Expiration

- `TTL` rounds to the nearest second (`(ms + 500) / 1000`); `PTTL` is exact.
- `DEL` of a key that has expired but has not been reaped yet returns `0`, and
  `EXISTS`/`TYPE` behave as if it were gone.
- `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT` accept `NX`, `XX`, `GT`, `LT`.
- Expired keys are reaped by a sampler every 100 ms, in addition to lazy checks
  on read.

### Authentication and security

- No password or ACL support. `HELLO ... AUTH` returns `WRONGPASS`.
- The server binds to `127.0.0.1` by default. Do not expose it publicly.

### Transactions and pub/sub

- `MULTI`, `EXEC`, `WATCH`, `SUBSCRIBE`, `PUBLISH` and blocking commands are not
  implemented. They are tracked for v0.3.

### Persistence

- The append-only file is a sequence of RESP commands, which is the same shape
  Redis uses, but redlite does not read Redis' RDB files.
- A rewrite emits typed commands: `SET`/`PXAT` for strings, `HSET` for hashes,
  `RPUSH` for lists, `SADD` for sets and `ZADD` for sorted sets, in chunks of
  128 elements. Every type round-trips through a restart.
- `BGREWRITEAOF` is synchronous today; the reply string is the Redis-compatible
  one so clients do not break.
- `appendfsync everysec` can lose up to one second of acknowledged writes on a
  hard crash. `always` does not.
- There is no `SAVE`/`BGSAVE` yet.

### Introspection

- `INFO` reports the sections that exist. Fields that would describe features
  redlite does not have (for example `rdb_last_save_time`, connected replicas)
  are either omitted or fixed at zero.
- `CONFIG GET` covers `bind`, `port`, `dir`, `maxclients`, `appendonly`,
  `appendfsync`, `loglevel`, `timeout` and `databases`.
- `CONFIG SET` changes `maxclients`, `appendfsync` and `loglevel`; everything
  else returns an error instead of pretending to apply.
- `COMMAND` returns name, arity, flags and the first/last/step key positions.
  `COMMAND DOCS` is empty.
- `CLIENT LIST` reports the fields clients commonly parse, with `fd=-1`.

### Limits

- `proto-max-bulk-len` is 512 MiB.
- A request may contain at most 1,048,576 arguments.
- An inline command may be at most 64 KiB.

Anything not described above and not in the supported table is absent. When in
doubt, run `COMMAND COUNT` and compare, or read the command table in
`internal/server`.
