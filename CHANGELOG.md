# Changelog

All notable changes to redlite are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/).

## [0.14.0] - 2026-11-12

### Added

- `BZPOPMIN` and `BZPOPMAX`.
- `HINCRBYFLOAT`.
- `ROLE` on primaries and replicas.
- `READONLY`, `READWRITE` and `ASKING`, which return Redis' explicit
  "cluster support disabled" error, and `PFSELFTEST`.

### Changed

- The command inventory now reports 211 of 295 Redis 8.8 commands, with 84
  tracked gaps.

## [0.13.0] - 2026-11-10

### Added

- Geo command family: `GEOADD`, `GEOPOS`, `GEODIST`, `GEOHASH`, `GEOSEARCH`,
  `GEOSEARCHSTORE`, `GEORADIUS[_RO]` and `GEORADIUSBYMEMBER[_RO]`, backed by
  Redis' 52-bit geohash scores in ordinary sorted sets.
- `MIGRATE` with `COPY`, `REPLACE`, `AUTH`, `AUTH2`, `KEYS`, TTL preservation
  and Redis-compatible `NOKEY`/count replies. Includes a full RESP2/RESP3 reply
  decoder in `internal/resp`.
- Redis-compatible HyperLogLog payloads (`HYLL`): MurmurHash64A hashing, dense
  and sparse representations, cardinality cache and the Ertl estimator. Values
  move both ways between redlite and Redis.
- ACL selectors (`ACL SETUSER user (+set ~prefix:*) ...`), `ACL DRYRUN`,
  `ACL LOG [count|RESET]`, `ACL CAT <category>` and `ACL GENPASS`.
- `acllog-max-len` and richer `ACL GETUSER`/`ACL LIST` output.

### Changed

- The differential suite now covers geo, HyperLogLog and ACL selector behavior
  against Redis 8.8.

## [0.12.0] - 2026-11-09

### Added

- Set stores and algebra: `SINTERSTORE`, `SUNIONSTORE`, `SDIFFSTORE`, `SMOVE`
  and `SINTERCARD`.
- Multi-key pops: `LMPOP`, `BLMPOP`, `ZMPOP`, `BZMPOP`.
- Sorted-set lex family (`ZRANGEBYLEX`, `ZREVRANGEBYLEX`, `ZLEXCOUNT`,
  `ZREMRANGEBYLEX`), `ZRANGESTORE`, `ZINTERCARD`, and the unified
  `ZRANGE ... BYSCORE|BYLEX [REV] [LIMIT]`.
- `BITFIELD` and `BITFIELD_RO` with `GET`, `SET`, `INCRBY`, unsigned/signed
  1-63/64-bit fields and `WRAP`/`SAT`/`FAIL` overflow modes.
- `LCS`, `SUBSTR`, and `SET` value conditions `IFEQ`, `IFNE`, `IFDEQ`, `IFDNE`
  (XXH3-64 digests).
- `SORT` / `SORT_RO` with `BY`, `GET`, `LIMIT`, `ALPHA`, `ASC`/`DESC` and
  `STORE`.
- `DUMP` / `RESTORE` using Redis' payload format (CRC-64), interoperable with
  Redis for strings, hashes, lists, sets and sorted sets.
- Sharded pub/sub: `SSUBSCRIBE`, `SUNSUBSCRIBE`, `SPUBLISH`, `PUBSUB
  SHARDCHANNELS` and `PUBSUB SHARDNUMSUB`.
- Operations: `SLOWLOG`, `LATENCY`, `DEBUG` subcommands, `MODULE LIST`,
  `MONITOR`, `CLIENT KILL`, `CLIENT REPLY`, `WAITAOF`, `COMMAND DOCS` and
  `COMMAND LIST FILTERBY`.
- RESP3 sets and doubles, plus encoder support for attributes, big numbers and
  verbatim strings.
- `INFO` sections for commandstats, latencystats, errorstats, cpu and cluster.
- Randomized differential checks against Redis 8.8 for bitfields, LCS,
  sorted-set ranges, SORT and DUMP/RESTORE.

### Changed

- `SMEMBERS`, `SINTER`, `SUNION`, `SDIFF` and `SPOP` with a count now return
  RESP3 sets; scores are RESP3 doubles.
- `ZMPOP`/`BZMPOP` use Redis' nested `[member, score]` reply shape.
- `CONFIG GET` exposes the slowlog and latency monitor parameters.

## [0.11.0] - 2026-11-09

### Added

- RDB interoperability: `SAVE`/`BGSAVE` write standard Redis RDB files
  (`REDIS0010`, plain encodings, CRC-64), and startup loads RDB files written
  by Redis 7/8, including intset, listpack and quicklist-of-listpacks values.
- Bitmaps: `SETBIT`, `GETBIT`, `BITCOUNT` (BYTE/BIT), `BITPOS`, `BITOP`.
- HyperLogLog: `PFADD`, `PFCOUNT`, `PFMERGE` (exact counts, opaque string).
- Sorted-set algebra: `ZUNION`, `ZINTER`, `ZDIFF` and their `STORE` variants
  with `WEIGHTS`/`AGGREGATE`, plus `ZPOPMIN`, `ZPOPMAX`, `ZRANDMEMBER`.
- `HSTRLEN`, `HRANDFIELD`, `LINSERT`, `LPOS`, `COPY`, `OBJECT`, `MEMORY`,
  `WAIT`, `LOLWUT`, `RESET`.

## [0.10.0] - 2026-11-09

### Changed

- The keyspace is split into 16 shards. Single-key commands take one shard
  lock, multi-key commands lock the touched shards in order, and whole-keyspace
  operations lock all of them. WATCH versions and all command semantics are
  unchanged.
- Added `NewWithShards` for benchmarks and tests.

## [0.9.0] - 2026-11-09

### Added

- Per-user ACLs: `ACL SETUSER`, `DELUSER`, `GETUSER`, `USERS`, with
  `on`/`off`, passwords, key patterns, channel patterns, `+@read`, `+@write`,
  `+command`/`-command` and `+@all`/`reset`.
- TLS listener (`--tls-port`, certificate, key, optional client CA) and
  `--tls-replication` for encrypted replica links.
- Replication backlog and partial resync (`PSYNC <replid> <offset>` /
  `+CONTINUE`), bounded by `--repl-backlog-size`.
- `INFO replication` exposes the real offset and full-resync count.

## [0.8.0] - 2026-11-09

### Added

- Keyspace notifications: `--notify-keyspace-events` / `CONFIG SET`, the
  `__keyspace@0__` and `__keyevent@0__` channels, category filtering, expiry
  events, and the standard Redis flags.

## [0.7.0] - 2026-11-09

### Added

- Authentication: `--requirepass`, `AUTH` (one and two argument forms),
  `HELLO ... AUTH`, `CONFIG SET requirepass`, and `NOAUTH` on every
  unauthenticated command. `--masterauth` authenticates replicas.
- Minimal ACL introspection: `ACL WHOAMI`, `ACL LIST`, `ACL CAT`.
- `GETRANGE`, `SETRANGE`, `INCRBYFLOAT` and `COMMAND GETKEYS`.

### Changed

- `BGREWRITEAOF` is now asynchronous: appends during the rewrite are buffered
  and folded into the new file, up to a 64 MiB bound.

## [0.6.0] - 2026-11-09

### Added

- Replication: `REPLICAOF`/`SLAVEOF` with full resync and command streaming,
  `PSYNC` and `REPLCONF` on the primary, read-only replicas, cascading
  replication, and real `INFO replication` fields.

### Changed

- Write commands are propagated to attached replicas, including transactions as
  a `MULTI`/`EXEC` unit.

## [0.5.0] - 2026-11-09

### Added

- Blocking list commands: `BLPOP`, `BRPOP`, `BLMOVE`, `BRPOPLPUSH`, with
  fractional timeouts, FIFO waiter wakeups, and no blocking inside `MULTI`.
- `LMOVE` and `RPOPLPUSH`.
- Snapshots: `SAVE`, `BGSAVE` and `SHUTDOWN SAVE`, with a versioned,
  checksummed file, atomic writes, startup loading, and a `--dbfilename`
  setting.

### Changed

- `INFO persistence` reports the RDB fields (`rdb_last_save_time`,
  `rdb_bgsave_in_progress`, `rdb_last_bgsave_status`).

## [0.4.0] - 2026-11-09

### Added

- Transactions: `MULTI`, `EXEC`, `DISCARD`, `WATCH`, `UNWATCH`. Queued commands
  are validated at queue time, `EXEC` is atomic with respect to other clients,
  and transactions are persisted as a `MULTI`/`EXEC` block.
- Pub/Sub: `SUBSCRIBE`, `UNSUBSCRIBE`, `PSUBSCRIBE`, `PUNSUBSCRIBE`, `PUBLISH`,
  `PUBSUB CHANNELS|NUMSUB|NUMPAT`, with RESP3 push frames.
- Public `redlite` package for running a server inside a Go test process.

### Changed

- `HGETALL` returns a map under RESP3 (unchanged in RESP2).
- Subscribed RESP2 connections are restricted to the subscribe family, `PING`,
  `QUIT` and `RESET`, as Redis does.

## [0.3.0] - 2026-11-09

### Added

- Sorted sets backed by a span-based skiplist: `ZADD` (with `NX`, `XX`, `GT`,
  `LT`, `CH`, `INCR`), `ZREM`, `ZSCORE`, `ZMSCORE`, `ZINCRBY`, `ZCARD`,
  `ZCOUNT`, rank and score ranges with `WITHSCORES`/`LIMIT`, `ZRANK`,
  `ZREVRANK`, `ZREMRANGEBYRANK`, `ZREMRANGEBYSCORE`, `ZSCAN`.

### Fixed

- `HGETALL` returns a map in RESP3.

## [0.2.0] - 2026-11-09

### Added

- A typed value model with `WRONGTYPE` errors.
- Hashes: `HSET`, `HSETNX`, `HMSET`, `HGET`, `HMGET`, `HDEL`, `HLEN`,
  `HEXISTS`, `HKEYS`, `HVALS`, `HGETALL`, `HINCRBY`, `HSCAN`.
- Lists: `LPUSH`, `RPUSH`, `LPUSHX`, `RPUSHX`, `LPOP`, `RPOP`, `LLEN`,
  `LINDEX`, `LSET`, `LRANGE`, `LTRIM`, `LREM` on a ring buffer.
- Sets: `SADD`, `SREM`, `SCARD`, `SISMEMBER`, `SMISMEMBER`, `SMEMBERS`,
  `SINTER`, `SUNION`, `SDIFF`, `SPOP`, `SRANDMEMBER`, `SSCAN`.
- A differential harness that compares replies against real Redis, fuzz targets
  for the parser and matcher, and store benchmarks.

## [0.1.0] - 2026-11-09

### Added

- RESP2 and RESP3, 52 commands covering strings, keys, expiry and
  introspection.
- Append-only file with `always`/`everysec`/`no` fsync policies, deterministic
  TTL rewriting, and truncation of a partial trailing command on load.
- Docker image, CI, graceful shutdown, client limits and idle timeouts.
