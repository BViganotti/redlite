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
- `INCRBYFLOAT` uses 64-bit-mantissa decimal arithmetic and formats like Redis
  7's long double. Redis 8 prints more digits for some fractions (for example
  `10.6` as `10.59999999999999964`).
- `DEL` of a key that has expired but has not been reaped yet returns `0`, and
  `EXISTS`/`TYPE` behave as if it were gone.
- `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT` accept `NX`, `XX`, `GT`, `LT`.
- Expired keys are reaped by a sampler every 100 ms, in addition to lazy checks
  on read.

### Authentication and ACLs

- `--requirepass` (or `CONFIG SET requirepass`) enables a single default user.
  Unauthenticated connections get `NOAUTH` for every command except `AUTH`,
  `HELLO` and `QUIT`.
- `AUTH <password>` and `AUTH default <password>` are supported, with a
  constant-time comparison. `HELLO ... AUTH default <password>` also works.
- `ACL WHOAMI`, `ACL LIST` and `ACL CAT` are implemented. Per-user ACLs, command
  categories and key patterns are not; there is one `default` user with full
  access.
- A replica connecting to a protected primary needs `--masterauth`.
- The server binds to `127.0.0.1` by default and has no TLS. Put it behind a
  proxy that terminates TLS if it must cross an untrusted network.

### Replication

- `REPLICAOF host port` (and the `SLAVEOF` alias) performs a full resync and
  then applies the primary's command stream. `REPLICAOF NO ONE` detaches and
  makes the instance writable again.
- `PSYNC` always answers `FULLRESYNC`; there is no partial resync, backlog or
  `master_repl_offset` tracking yet, so every reconnect re-sends the snapshot.
- The snapshot travels in redlite's own format, not Redis' RDB, so a real Redis
  replica cannot sync from redlite and vice versa.
- Replicas are read-only (`READONLY You can't write against a read only
  replica.`). Transactions arriving from the primary apply as a unit.
- Cascading replication works: a replica propagates the stream it receives.
- `INFO replication` reports the role, link status, primary address and the
  number of attached replicas.

### Keyspace notifications

- Enabled with `notify-keyspace-events` (at startup or `CONFIG SET`). `K`
  publishes to `__keyspace@0__:<key>`, `E` to `__keyevent@0__:<event>`, `A`
  enables every category.
- Supported categories: `g` (generic), `$` (string), `l` (list), `s` (set),
  `h` (hash), `z` (sorted set), `x` (expired). The `m` (key miss) flag is
  accepted but ignored.
- Delivery is best effort. Messages share the pub/sub path, which writes
  synchronously with a deadline; a subscriber that cannot keep up loses
  messages rather than growing an unbounded buffer.
- Expired events come from both the background sampler and lazy expiration.
- Events are not propagated to replicas; each instance emits its own.

### Transactions

- `MULTI`, `EXEC`, `DISCARD`, `WATCH` and `UNWATCH` are supported.
- Queued commands are validated for name and arity at queue time. Any error
  before `EXEC` aborts the whole transaction with `EXECABORT`.
- Runtime errors (for example `INCR` on a non-integer) are returned as elements
  of the `EXEC` array; the rest of the transaction still applies.
- `EXEC` is atomic with respect to other clients: the server takes a write gate
  for the whole batch.
- Blocking commands (`BLPOP`, `BRPOP`, `BLMOVE`, `BRPOPLPUSH`) are supported.
  They do not hold the transaction gate while waiting, so they cannot block
  another client's `EXEC`. Inside `MULTI` they return immediately instead of
  blocking, as Redis does.
- `WATCH` compares a key's existence, value version and flush epoch. It aborts
  when a watched key's state changed, which covers the usual read-modify-write
  pattern. Unlike Redis, creating and then deleting a key between `WATCH` and
  `EXEC` does not abort if the final state is identical.
- Blocked clients are woken in registration order, but the element is not
  reserved for them: a non-blocking `LPOP` can still take it first.

### Pub/Sub

- `SUBSCRIBE`, `UNSUBSCRIBE`, `PSUBSCRIBE`, `PUNSUBSCRIBE`, `PUBLISH` and
  `PUBSUB CHANNELS|NUMSUB|NUMPAT` are supported.
- RESP2 subscribers receive arrays; RESP3 subscribers receive push frames (`>`).
- In RESP2, a subscribed connection may only run the subscribe family, `PING`,
  `QUIT` or `RESET`. RESP3 connections may run any command while subscribed.
- Delivery is synchronous with a write deadline per subscriber. A subscriber
  that stops reading cannot block publishers indefinitely, but it is not
  buffered without bound either.
- `PUBLISH` counts deliveries: a client subscribed to both a channel and a
  matching pattern is counted twice.
- Keyspace notifications (`notify-keyspace-events`) are not implemented.

### Persistence

- The append-only file is a sequence of RESP commands, which is the same shape
  Redis uses, but redlite does not read Redis' RDB files.
- Snapshots use a redlite-specific format: an 8-byte magic header, a format
  version, the entries, and a CRC-32 checksum. They cannot be opened by Redis,
  and real RDB files cannot be loaded here.
- `SAVE` writes synchronously; `BGSAVE` copies the keyspace and writes in the
  background. Both use a temporary file, fsync and an atomic rename.
- On startup, if `appendonly` is enabled the AOF is authoritative. Otherwise
  `dir/dbfilename` is loaded when present, and a corrupt snapshot refuses to
  start rather than silently losing data.
- `SHUTDOWN SAVE` writes a snapshot before stopping.
- A rewrite emits typed commands: `SET`/`PXAT` for strings, `HSET` for hashes,
  `RPUSH` for lists, `SADD` for sets and `ZADD` for sorted sets, in chunks of
  128 elements. Every type round-trips through a restart.
- `BGREWRITEAOF` runs in the background: appends during the rewrite are
  buffered (up to 64 MiB) and folded into the new file before the atomic swap.
  If the buffer overflows, the rewrite aborts and the old log stays
  authoritative.
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
