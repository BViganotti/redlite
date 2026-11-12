# Compatibility

Redlite targets wire compatibility, not feature parity. If a command is listed
as supported, it returns the same reply shape as Redis; if it is not listed, the
server answers `unknown command` rather than pretending.

The reference for the differential suite is Redis OSS 8.8. Commands that only
exist in newer releases (the `SET` value conditions) are skipped automatically
when the reference server is older.

## Reply encoding

- RESP2 is the default. `HELLO 3` switches the connection to RESP3.
- Maps (`CONFIG GET`) are encoded as flat arrays in RESP2 and maps in RESP3.
- Nulls are `$-1` in RESP2 and `_` in RESP3.
- Errors are always a single line; embedded newlines are replaced with spaces so
  a reply can never desynchronise the stream.

## Supported commands

| Category | Commands |
| --- | --- |
| Connection | `PING` `ECHO` `QUIT` `SELECT` `HELLO` `RESET` `AUTH` `CLIENT` |
| Introspection | `COMMAND` `CONFIG` `INFO` `TIME` `LASTSAVE` `SLOWLOG` `LATENCY` `DEBUG` `MODULE` |
| Admin | `SHUTDOWN` `BGREWRITEAOF` `MONITOR` |
| Strings | `GET` `SET` `SETNX` `SETEX` `PSETEX` `GETSET` `GETDEL` `GETEX` `MGET` `MSET` `MSETNX` `APPEND` `STRLEN` `GETRANGE` `SUBSTR` `SETRANGE` `INCR` `DECR` `INCRBY` `DECRBY` `INCRBYFLOAT` `LCS` |
| Hashes | `HSET` `HSETNX` `HMSET` `HGET` `HMGET` `HDEL` `HLEN` `HEXISTS` `HKEYS` `HVALS` `HGETALL` `HINCRBY` `HSCAN` `HSTRLEN` `HRANDFIELD` |
| Lists | `LPUSH` `RPUSH` `LPUSHX` `RPUSHX` `LPOP` `RPOP` `LLEN` `LINDEX` `LSET` `LRANGE` `LTRIM` `LREM` `LINSERT` `LPOS` `LMOVE` `RPOPLPUSH` `BLPOP` `BRPOP` `BLMOVE` `BRPOPLPUSH` `LMPOP` `BLMPOP` |
| Sets | `SADD` `SREM` `SCARD` `SISMEMBER` `SMISMEMBER` `SMEMBERS` `SINTER` `SUNION` `SDIFF` `SPOP` `SRANDMEMBER` `SSCAN` `SMOVE` `SINTERSTORE` `SUNIONSTORE` `SDIFFSTORE` `SINTERCARD` |
| Sorted sets | `ZADD` `ZREM` `ZSCORE` `ZMSCORE` `ZINCRBY` `ZCARD` `ZCOUNT` `ZRANGE` `ZREVRANGE` `ZRANGEBYSCORE` `ZREVRANGEBYSCORE` `ZRANGEBYLEX` `ZREVRANGEBYLEX` `ZLEXCOUNT` `ZREMRANGEBYLEX` `ZRANK` `ZREVRANK` `ZREMRANGEBYRANK` `ZREMRANGEBYSCORE` `ZSCAN` `ZRANGESTORE` `ZINTERCARD` `ZMPOP` `BZMPOP` `ZUNION` `ZINTER` `ZDIFF` `ZUNIONSTORE` `ZINTERSTORE` `ZDIFFSTORE` `ZPOPMIN` `ZPOPMAX` `ZRANDMEMBER` |
| Keyspace | `DEL` `UNLINK` `EXISTS` `TYPE` `KEYS` `SCAN` `RANDOMKEY` `RENAME` `RENAMENX` `TOUCH` `DBSIZE` `FLUSHDB` `FLUSHALL` `COPY` `OBJECT` `MEMORY` `SORT` `SORT_RO` `DUMP` `RESTORE` `MIGRATE` |
| Expiry | `EXPIRE` `PEXPIRE` `EXPIREAT` `PEXPIREAT` `TTL` `PTTL` `PERSIST` `EXPIRETIME` `PEXPIRETIME` |
| Bitmaps | `SETBIT` `GETBIT` `BITCOUNT` `BITPOS` `BITOP` `BITFIELD` `BITFIELD_RO` |
| HyperLogLog | `PFADD` `PFCOUNT` `PFMERGE` |
| Geo | `GEOADD` `GEOPOS` `GEODIST` `GEOHASH` `GEOSEARCH` `GEOSEARCHSTORE` `GEORADIUS` `GEORADIUS_RO` `GEORADIUSBYMEMBER` `GEORADIUSBYMEMBER_RO` |
| Transactions | `MULTI` `EXEC` `DISCARD` `WATCH` `UNWATCH` |
| Pub/Sub | `SUBSCRIBE` `UNSUBSCRIBE` `PSUBSCRIBE` `PUNSUBSCRIBE` `SSUBSCRIBE` `SUNSUBSCRIBE` `PUBLISH` `SPUBLISH` `PUBSUB` |
| Replication | `REPLICAOF` `SLAVEOF` `PSYNC` `REPLCONF` `WAIT` `WAITAOF` |
| Scripting/Cluster/Streams | Not implemented (see below) |

## Known differences

### Data model

- Five value types exist: string, hash, list, set and sorted set. Bitmaps and
  HyperLogLog are encoded on top of strings, as in Redis. Streams are not
  implemented yet (`ROADMAP.md`).
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
- Set-shaped replies (`SMEMBERS`, `SINTER`, `SUNION`, `SDIFF`, `SPOP` with a
  count) use RESP3 sets (`~`).
- Scores are RESP3 doubles (`,`) in `ZSCORE`, `ZINCRBY`, `ZMSCORE`, `ZADD
  INCR` and `WITHSCORES` replies; RESP2 keeps the bulk-string form.
- `ZMPOP`/`BZMPOP` return `[key, [[member, score], ...]]` in both protocols,
  matching Redis. `ZPOPMIN`/`ZPOPMAX` stay flat in RESP2 and paired in RESP3.
- RESP3 attributes, big numbers and verbatim strings have encoder support; no
  command emits them yet.

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

- `--requirepass` (or `CONFIG SET requirepass`) sets the default user's
  password. Unauthenticated connections get `NOAUTH` for every command except
  `AUTH`, `HELLO` and `QUIT`.
- `AUTH <password>` and `AUTH <user> <password>` are supported, with a
  constant-time comparison. `HELLO ... AUTH <user> <password>` also works.
- `ACL SETUSER` defines users with `on`/`off`, `>password`/`nopass`,
  `~key-pattern`/`allkeys`, `&channel-pattern`/`allchannels`, `+@read`,
  `+@write`, `+command`, `-command`, `+@all` and `reset`. Parenthesised
  selectors (`(+set ~prefix:*)`) are supported, and a command is allowed when
  any selector permits the command, keys and channels together.
- `ACL DRYRUN`, `ACL LOG [count|RESET]`, `ACL CAT [category]` and `ACL GENPASS`
  are implemented. `ACL LOG` merges repeated failures by user, reason, object
  and context; its `client-info` field has the same shape as `CLIENT LIST`.
  Denied commands and keys return `NOPERM`.
- Key patterns are checked with the same glob matcher as `KEYS`; commands that
  take no keys skip the check.
- There is no TLS on the plain port and no per-command ACL categories beyond
  read/write. Use the TLS listener for untrusted networks.

### Replication

- `REPLICAOF host port` (and the `SLAVEOF` alias) performs a full resync and
  then applies the primary's command stream. `REPLICAOF NO ONE` detaches and
  makes the instance writable again.
- A reconnect sends `PSYNC <replid> <offset>`. When the primary still has that
  offset in its backlog (1 MiB by default) it answers `+CONTINUE` and streams
  only the missing bytes; otherwise it re-sends the snapshot.
- The snapshot travels in redlite's own format, not Redis' RDB, so a real Redis
  replica cannot sync from redlite and vice versa.
- Replicas are read-only (`READONLY You can't write against a read only
  replica.`). Transactions arriving from the primary apply as a unit.
- Cascading replication works: a replica propagates the stream it receives.
- `INFO replication` reports the role, link status, primary address, offset and
  the number of attached replicas.

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
- Newer commands emit their Redis event names where implemented
  (`sinterstore`, `sunionstore`, `sdiffstore`, `smove`, `zrangestore`, `zrem`,
  `lpop`/`rpop` for `LMPOP`, `setbit` for `BITFIELD`, `restore`, `sortstore`).
- The `n` (new key) and `m` (key miss) classes are accepted but not emitted.

### TLS

- Setting `--tls-port` runs a TLS listener alongside the plain one.
  `--tls-ca-cert-file` makes client certificates mandatory.
- `--tls-replication` makes a replica dial its primary over TLS, verifying the
  server against `--tls-ca-cert-file` (or the system pool when unset).
- The plain port stays unencrypted. Bind it to loopback or firewall it if the
  TLS listener is the only intended entry point.

### Keyspace sharding

- Keys are distributed over 16 shards with FNV-1a. Single-key commands take one
  shard lock. Multi-key commands (`MSET`, `DEL`, `EXISTS`, `RENAME`, `MOVE`,
  set algebra) lock the touched shards in ascending index order.
- Whole-keyspace operations (`KEYS`, `SCAN`, `DBSIZE`, `FLUSHALL`, `RANDOMKEY`,
  `SAVE`, AOF rewrite) lock every shard.
- Commands that read several keys through separate lookups (for example
  `MGET`) are not atomic with respect to concurrent writes. Wrap them in
  `MULTI`/`EXEC` when a consistent view matters.

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
- Sharded pub/sub (`SSUBSCRIBE`, `SUNSUBSCRIBE`, `SPUBLISH`, `PUBSUB
  SHARDCHANNELS|SHARDNUMSUB`) works, but sharding is logical only: there is no
  cluster and every shard lives on the same node. Pattern subscriptions do not
  receive shard messages, as in Redis.
- RESP2 subscribers receive arrays; RESP3 subscribers receive push frames (`>`).
- In RESP2, a subscribed connection may only run the subscribe family, `PING`,
  `QUIT` or `RESET`. RESP3 connections may run any command while subscribed.
- Delivery is synchronous with a write deadline per subscriber. A subscriber
  that stops reading cannot block publishers indefinitely, but it is not
  buffered without bound either.
- `PUBLISH` counts deliveries: a client subscribed to both a channel and a
  matching pattern is counted twice.
- `MONITOR` streams every command executed by other connections in Redis'
  timestamp/address/argument format.

### Persistence

- `DUMP`/`RESTORE` use Redis' payload format (RDB object encoding, two-byte
  version, CRC-64) for all five types. `RESTORE` supports `REPLACE`, `ABSTTL`,
  `IDLETIME` and `FREQ`. Payloads produced by Redis 8 load in redlite and
  vice versa; redlite emits object version 9 so Redis 5+ accepts them.
- `SAVE`/`BGSAVE` write a standard Redis RDB file (magic `REDIS0010`, plain
  encodings for every type). `redis-check-rdb` accepts it and `redis-server`
  loads it, which makes migration to Redis a file copy.
- Loading accepts RDB versions 1–20 and the compact encodings Redis 7/8 emit:
  intset, listpack, and quicklist-of-listpacks, including LZF-compressed
  strings. Streams and module values are rejected with an error.
- The checksum is CRC-64 (Jones polynomial). A corrupt file refuses to load.
- `BGSAVE` copies the keyspace and writes in the background through a temporary
  file and an atomic rename.
- On startup, if `appendonly` is enabled the AOF is authoritative. Otherwise
  `dir/dbfilename` is loaded when present.
- `SHUTDOWN SAVE` writes a snapshot before stopping.
- The AOF is RESP commands: `SET`/`PXAT`, `HSET`, `RPUSH`, `SADD`, `ZADD`,
  `PEXPIREAT` and `MULTI`/`EXEC`. `redis-server --appendonly yes` can load it.
- `BGREWRITEAOF` runs in the background: appends during the rewrite are
  buffered (up to 64 MiB) and folded into the new file before the atomic swap.
  If the buffer overflows, the rewrite aborts and the old log stays
  authoritative.
- `appendfsync everysec` can lose up to one second of acknowledged writes on a
  hard crash. `always` does not.
- Replication still uses redlite's internal snapshot format, so a real Redis
  replica cannot sync from redlite yet.

### HyperLogLog

- `PFADD`/`PFCOUNT`/`PFMERGE` use Redis' real `HYLL` representation: the same
  64-bit MurmurHash64A hashing, 16384 six-bit registers, sparse-opcode decoder,
  cardinality cache and Ertl estimator. Values produced by Redis (sparse or
  dense) are readable here, and values produced here are readable by Redis.
- New values are written dense. Counts match Redis for the same inputs because
  the estimator and rounding are identical; distances and stochastic estimates
  are not compared bit-for-bit across platforms.

### Bitmaps and OBJECT

- Bitmap commands operate on ordinary strings with Redis' MSB-first bit order.
- `OBJECT ENCODING` returns approximate names (`int`, `raw`, `listpack`,
  `quicklist`, `hashtable`, `skiplist`, `intset`); it is informational only.

### Introspection

- `MIGRATE` supports `COPY`, `REPLACE`, `AUTH`, `AUTH2`, `KEYS`, relative TTL
  preservation and the single-key/KEYS reply forms. It uses `DUMP`/`RESTORE`
  over a short-lived RESP connection.
- `INFO` reports the sections that exist. Fields that would describe features
  redlite does not have (for example `rdb_last_save_time`, connected replicas)
  are either omitted or fixed at zero.
- `CONFIG GET` covers `bind`, `port`, `dir`, `maxclients`, `appendonly`,
  `appendfsync`, `loglevel`, `timeout`, `databases`, `requirepass`,
  `notify-keyspace-events`, `slowlog-*` and `latency-monitor-threshold`.
- `CONFIG SET` changes `maxclients`, `appendfsync`, `loglevel`,
  `requirepass`, `notify-keyspace-events`, `slowlog-*` and
  `latency-monitor-threshold`; everything else returns an error instead of
  pretending to apply.
- `COMMAND` returns name, arity, flags and the first/last/step key positions.
  `COMMAND DOCS` returns summary/since/group/complexity metadata, and
  `COMMAND LIST FILTERBY PATTERN|ACLCAT|MODULE` filters command names.
- `SLOWLOG GET|LEN|RESET` is implemented with `slowlog-log-slower-than`,
  `slowlog-max-len`, `slowlog-entry-max-argc` and
  `slowlog-entry-max-string-len`. Blocking commands do not count their wait
  time, matching Redis.
- `LATENCY LATEST|HISTORY|RESET|DOCTOR|GRAPH|HISTOGRAM` is backed by the
  `command` event and `latency-monitor-threshold`; it is not a full
  fork/expire-cycle latency monitor.
- `DEBUG SLEEP`, `SET-ACTIVE-EXPIRE`, `OBJECT`, `CHANGE-REPL-ID`, `JMAP`,
  `FLUSHALL` and `RELOAD` are supported. Unknown DEBUG subcommands are rejected.
- `MODULE LIST` is always empty: the module ABI is out of scope.
- `CLIENT LIST` reports the fields clients commonly parse, with `fd=-1`.
  `CLIENT KILL` supports the legacy address form and the ID/TYPE/ADDR/LADDR/
  USER/MAXAGE/SKIPME filters. `CLIENT REPLY ON|OFF|SKIP` is implemented.

### Not yet implemented

These are the declared gaps against the Redis 8.8 target, in rough priority
order. They are absent rather than silently approximated:

- Streams (`XADD`, `XREAD`, consumer groups, stream RDB types).
- Scripting (`EVAL`, `EVALSHA`, `SCRIPT`, `FUNCTION`, `FCALL`).
- Multiple databases (`SELECT` other than 0, `MOVE`, `SWAPDB`) and cluster mode
  (slot routing, `MOVED`/`ASK`, cluster bus).
- Multi-part AOF manifests, AOF/RDB replication to a real Redis, and diskless
  sync.
- Hash field expiration (`HEXPIRE`, `HGETEX`, `HSETEX`, `HSCAN NOVALUES`).
- Client-side caching (`CLIENT TRACKING`, invalidation pushes).
- Eviction (`maxmemory` policies, `OBJECT FREQ`/`IDLETIME`), `FAILOVER`, and
  the full `CONFIG` parameter surface.
- Redis Modules, Redis Stack (Search/JSON/TimeSeries/Bloom) and Sentinel.

### Limits

- `proto-max-bulk-len` is 512 MiB.
- A request may contain at most 1,048,576 arguments.
- An inline command may be at most 64 KiB.

Anything not described above and not in the supported table is absent. When in
doubt, run `COMMAND COUNT` and compare, or read the command table in
`internal/server`.
