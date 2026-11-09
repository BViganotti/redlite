# Changelog

All notable changes to redlite are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[semantic versioning](https://semver.org/).

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
