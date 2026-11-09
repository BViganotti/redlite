# Roadmap

This is the work I want to do on redlite, roughly in order. Items map to
GitHub issues; if you want to take one, comment there first so we do not
duplicate effort.

## v0.1 — the core (done)

- [x] RESP2 parsing and reply encoding
- [x] RESP3 handshake (`HELLO 3`), maps and nulls
- [x] String commands, conditional `SET`, `GETEX`
- [x] Expiry: lazy reads, background sampler, `NX`/`XX`/`GT`/`LT`
- [x] `SCAN` with `MATCH`, `COUNT` and `TYPE`
- [x] Append-only file with `always`/`everysec`/`no` fsync policies
- [x] Crash recovery: truncate a partial trailing command
- [x] `BGREWRITEAOF`
- [x] Graceful shutdown, `maxclients`, idle timeout
- [x] Docker image, CI, unit and integration tests

## v0.2 — the other data types

- [x] Value model that is not `[]byte`: sealed interface with type-safe dispatch
- [x] Hash commands (`HSET`, `HGET`, `HDEL`, `HGETALL`, `HINCRBY`, `HSCAN`)
- [x] List commands (`LPUSH`, `RPUSH`, `LPOP`, `RPOP`, `LRANGE`, `LLEN`, `LINDEX`)
- [x] Set commands (`SADD`, `SREM`, `SMEMBERS`, `SISMEMBER`, `SINTER`, `SUNION`)
- [x] Sorted sets (`ZADD`, `ZRANGE`, `ZRANGEBYSCORE`, `ZRANK`, `ZINCRBY`) on a
      skiplist with spans
- [x] `WRONGTYPE` errors on every cross-type access
- [ ] `OBJECT ENCODING` for the encodings we can honestly report

## v0.3 — concurrency semantics

- [x] `MULTI` / `EXEC` / `DISCARD` / `WATCH`
- [x] Blocking variants (`BLPOP`, `BRPOP`, `BLMOVE`, `BRPOPLPUSH`) with timeouts
- [x] `PUBLISH` / `SUBSCRIBE` / `PSUBSCRIBE`, including RESP3 push frames
- [x] Keyspace notifications (`CONFIG SET notify-keyspace-events`)

## v0.4 — durability and replication

- [x] Snapshots (`SAVE`, `BGSAVE`) with a versioned, checksummed file format
- [x] Background AOF rewrite that does not block writers
- [x] `REPLICAOF` with full resync and command propagation
- [ ] Replication backlog and partial resync
- [x] `INFO replication` fields that reflect reality

## v0.5 — operations

- [ ] Sharded keyspace instead of one `RWMutex`
- [ ] Batched AOF writes for throughput
- [ ] `SLOWLOG`, `LATENCY`, command statistics in `INFO`
- [ ] Prometheus metrics endpoint
- [ ] Structured audit log for admin commands
- [ ] Graceful drain: stop accepting, finish in-flight commands, then exit
- [ ] Windows support for `BGREWRITEAOF` (atomic rename over an open file)

## Quality track

- [x] Differential test harness: run the same command script against redlite and
      real Redis, compare replies byte for byte (`make differential`)
- [x] Fuzz the RESP parser and the glob matcher with `go test -fuzz`
- [ ] Property tests for expiry and scan pagination
- [x] Benchmarks for the core store operations (`-bench .`)
- [ ] Mutation testing to prove the suite actually catches regressions

## Tooling and distribution

- [x] Go embed API (`redlite.Start` returning a server with `Addr` and `Close`)
- [ ] Homebrew tap and `go install` instructions
- [ ] Signed releases with `goreleaser`
- [ ] Documentation site generated from `docs/`

## Security

- [x] Password authentication (`requirepass`, `AUTH`, `NOAUTH`)
- [x] `HELLO ... AUTH` and `--masterauth` for replication
- [x] Minimal ACL introspection (`ACL WHOAMI`, `ACL LIST`, `ACL CAT`)
- [ ] Per-user ACLs with command categories and key patterns
- [ ] TLS

## Good first issues

These are intentionally small and self-contained:

- Add `COPY` with the `REPLACE` option
- Make `CONFIG REWRITE` persist to a config file
- Accept `CLIENT KILL` with an `ID` filter
- Add `HSTRLEN` and `LPOS`
- Add `SINTERCARD`
- Report command statistics in `INFO`
