# Plan: full Redis parity

This is the work required for redlite to be a drop-in replacement for Redis —
not "most apps", but "no observable difference for the Redis core". It is a
large, multi-month plan; this document is the map, not a promise that the work
is done.

## 1. Define the target

"100% compatible" needs a pinned target, because Redis adds commands every
release. The plan targets **Redis OSS 8.x** (pin one exact version in CI, e.g.
8.8), including core data types, streams, scripting, cluster, ACL, replication
and persistence.

### In scope

- Every command in `COMMAND DOCS` for the pinned release, including arity,
  flags, ACL categories and key specs.
- RESP2 and RESP3 (including push frames, attributes, doubles, big numbers,
  verbatim strings and maps).
- Observable semantics: expiry, eviction, blocking, `WATCH`, `SCAN`, keyspace
  notifications, client-side caching, multi-database behaviour.
- Persistence: RDB for every type and encoding Redis writes, AOF (including
  Redis 7 multi-part manifests and the RDB preamble), `DUMP`/`RESTORE`.
- Replication: real Redis as replica of redlite and redlite as replica of real
  Redis, full and partial resync, ACKs, `WAIT`/`WAITAOF`.
- Cluster: slot routing, `MOVED`/`ASK`, cluster bus, resharding, replicas.
- Security and operations: full ACL, TLS, unix sockets, protected mode,
  `CONFIG`, `INFO`, `SLOWLOG`, `LATENCY`, `DEBUG`, `CLIENT`.

### Out of scope (and why)

| Area | Why |
| --- | --- |
| Redis Modules ABI | Executing C modules requires embedding the Redis module API and the C ABI. A Go server cannot support arbitrary modules without CGo and a large compatibility shim. `MODULE LIST` stays empty and module commands return unknown-command. |
| Redis Stack (Search, JSON, TimeSeries, Bloom) | Separate products built on modules, not part of the core server. |
| Sentinel | A separate process and protocol; can be planned as an add-on after cluster. |
| Bit-for-bit memory layout / performance | Functional parity is the goal; memory efficiency and throughput are a separate track. |

If the target release includes newer core types (for example vector sets), they
are in scope; the inventory step in Phase 0 decides.

## 2. Baseline at v0.13.0

203 of 295 commands in Redis 8.8. The authoritative gap list is generated from
Redis itself and lives in
`internal/server/testdata/command-gaps.txt` (92 names); the conformance test
fails if that list drifts.

| Family | Status |
| --- | --- |
| Strings | Complete for the core set; gaps: `MSETEX`, `INCREX`, digest helpers |
| Lists | Complete including `LMPOP`/`BLMPOP` |
| Hashes | Missing the hash-field-expiry family and `HGETEX`/`HSETEX`/`HGETDEL`/`HINCRBYFLOAT` |
| Sets | Complete |
| Sorted sets | Complete except `BZPOPMIN`/`BZPOPMAX` |
| Bitmaps | Complete (`BITFIELD` included) |
| HyperLogLog | Complete; Redis-compatible `HYLL` payloads |
| Geo | Complete |
| Streams | None |
| Pub/Sub | Complete including sharded pub/sub |
| Transactions | Present; `WATCH` still touch-based divergence |
| Scripting | None |
| Server / client / config | Broad: `DEBUG`, `MODULE`, `SLOWLOG`, `LATENCY`, `MONITOR`, `CLIENT KILL/REPLY`, `COMMAND DOCS`; gaps: `ROLE`, `FAILOVER`, `READONLY`/`READWRITE`, `ASKING` |
| ACL | Selectors, `DRYRUN`, `LOG`, `GENPASS`; categories still limited |
| Persistence | RDB + `DUMP`/`RESTORE`; missing multi-part AOF, stream encodings |
| Replication | Redlite-to-redlite only |
| Cluster | None |

## 3. Gap register

Effort: S (≤2 days), M (≤1 week), L (2–4 weeks), XL (1–3 months).

### 3.1 Protocol and connection

- [ ] RESP3 attributes (`|`), big numbers (`(`), verbatim strings (`=`), doubles
      (`,`), booleans in every reply where Redis uses them (M)
- [ ] Exact `HELLO` fields, including `modules` and proto negotiation errors (S)
- [ ] `CLIENT REPLY OFF|ON|SKIP` (M)
- [ ] `RESET` full semantics: protocol, auth, tracking state (S)
- [ ] `MONITOR` (M)
- [ ] `SYNC` legacy replication handshake (S, depends on 3.7)
- [ ] Inline command edge cases and protocol error text parity (S)

### 3.2 Command families

Strings: `LCS`, `SUBSTR` alias, `SET` option parity (`IFEQ`? if present in the
target), error text (M).

Lists: `LMPOP`, `BLMPOP` (M), exact `LPOS`/`LINSERT` edge cases (S).

Hashes: hash field expiration (`HEXPIRE`, `HPEXPIRE`, `HEXPIREAT`,
`HPEXPIREAT`, `HTTL`, `HPTTL`, `HEXPIRETIME`, `HPEXPIRETIME`, `HPERSIST`,
`HGETEX`, `HGETDEL`, `HSETEX`), `HSCAN NOVALUES` if present (L).

Sets: `SINTERSTORE`, `SUNIONSTORE`, `SDIFFSTORE`, `SMOVE`, `SINTERCARD` (M).

Sorted sets: `ZRANGEBYLEX`, `ZREVRANGEBYLEX`, `ZLEXCOUNT`, `ZREMRANGEBYLEX`,
`ZRANGESTORE`, `ZINTERCARD`, `ZMPOP`, `BZMPOP`, and the unified
`ZRANGE ... BYSCORE|BYLEX REV LIMIT` (L).

Bitmaps: `BITFIELD`, `BITFIELD_RO` including signed/unsigned overflow modes
(L).

HyperLogLog: replace the exact-set payload with the Redis dense/sparse HLL
format and Redis' estimator so `PFCOUNT` matches within Redis' own tolerance and
values survive a move to Redis (L).

Geo: `GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, `GEOSEARCHSTORE`,
`GEORADIUS*` aliases (L).

Streams: `XADD`, `XLEN`, `XRANGE`, `XREVRANGE`, `XDEL`, `XTRIM`, `XREAD`,
`XREADGROUP`, `XGROUP`, `XACK`, `XPENDING`, `XCLAIM`, `XAUTOCLAIM`, `XINFO`,
`XSETID`, and the newer stream commands in the target release (XL).

Pub/Sub: `SSUBSCRIBE`, `SUNSUBSCRIBE`, `SPUBLISH`, `PUBSUB SHARDCHANNELS`,
`PUBSUB SHARDNUMSUB` (M).

Keyspace: `SORT`, `SORT_RO` with `BY`, `GET`, `LIMIT`, `STORE`, `ALPHA` (L);
`DUMP`, `RESTORE` with `REPLACE`, `ABSTTL`, `IDLETIME`, `FREQ` (L); `MIGRATE`
(L); `MOVE`, `SWAPDB` (depends on multi-DB); `WAITAOF` (depends on AOF).

Server: `DEBUG` subcommands used by test suites and frameworks (`JMAP`,
`SLEEP`, `SET-ACTIVE-EXPIRE`, `OBJECT`, `QUICKLIST-PACKED-THRESHOLD`,
`STRINGMATCH-LEN`, `CHANGE-REPL-ID`) (M), `SLOWLOG` (M), `LATENCY` (M),
`COMMAND DOCS` with real metadata (M), `COMMAND LIST FILTERBY` (S),
`FAILOVER` (L), `MODULE LIST` returning an empty list (S).

### 3.3 Semantics

- [ ] `SCAN` cursor must use the same hash-table walk as Redis so cursors are
      portable and guarantees hold (L)
- [ ] `WATCH` must use Redis' touch-based dirty-key tracking (M)
- [ ] Multi-key reads (`MGET`, `EXISTS`, …) atomic with respect to writes (M)
- [ ] Expiry: active sampler tuned to `hz`, `ACTIVE-EXPIRE` debug control,
      exact expired-key event timing (M)
- [ ] Eviction: `maxmemory`, all `maxmemory-policy` values, `OBJECT FREQ`,
      `OBJECT IDLETIME`, eviction events (L)
- [ ] Keyspace notifications: every event class including `m` key-miss and `n`
      new-key, exact event names per command (M)
- [ ] Client-side caching: `CLIENT TRACKING`, `TRACKINGINFO`, `CACHING`,
      `GETREDIR`, invalidation push frames (L)
- [ ] Blocking commands: exact `BLPOP` timeout rounding, `BLMOVE` wake ordering,
      blocked-client `CLIENT LIST` flags (M)
- [ ] `MULTI`/`EXEC` error and `EXECABORT` semantics, `DISCARD` edge cases (S)

### 3.4 Persistence

- [ ] RDB writer for streams, modules-aux, functions, and every type; version
      byte matching the target Redis (L)
- [ ] RDB reader for legacy encodings still written/accepted by Redis: ziplist,
      zipmap, list quicklist v1, zipmap, module values (L)
- [ ] RDB AUX fields, `SELECTDB` for all databases, LRU/LFU metadata (M)
- [ ] Redis 7 multi-part AOF: manifest, base/incr files, truncation semantics,
      `aof-use-rdb-preamble` hybrid (L)
- [ ] `DUMP`/`RESTORE` payload format with version and CRC64 (M)
- [ ] `MIGRATE` (L)
- [ ] Cross-version fixtures: RDB/AOF produced by Redis 6, 7 and 8 load
      correctly; our files load in all three (M, mostly fixtures)

### 3.5 Replication

- [ ] Send a real RDB in the full-resync bulk transfer (M)
- [ ] `REPLCONF ACK <offset>`, `REPLCONF GETACK`, offsets, `master_repl_offset`
      exactness (M)
- [ ] Partial resync with the Redis offset/backlog semantics (M)
- [ ] `WAIT` and `WAITAOF` (M)
- [ ] Diskless sync, `repl-diskless-sync`, `repl-diskless-load` (L)
- [ ] Replica chaining and `min-replicas-to-write` (M)
- [ ] `FAILOVER` and manual failover handshake (L)
- [ ] Interop tests: real `redis-server` as a replica of redlite and vice
      versa, including a mid-stream disconnect (L for the lab, then S per run)

### 3.6 Cluster

- [ ] Cluster-enabled mode, 16384 slots, slot map persistence (XL)
- [ ] Cluster bus protocol on `cluster-port` (gossip, failure detection) (XL)
- [ ] `CLUSTER INFO|NODES|SLOTS|SHARDS|MYID|KEYSLOT|COUNTKEYSINSLOT|GETKEYSINSLOT`
      (L)
- [ ] `MOVED`/`ASK` redirection and `ASKING` (M)
- [ ] Resharding via `CLUSTER SETSLOT` and `MIGRATE` (L)
- [ ] Cluster replicas and failover (L)
- [ ] Interop tests with a real 3-node Redis cluster (L)

### 3.7 Security and operations

- [ ] Full ACL: users, `+@category`, `-@category`, selectors `(...)`,
      `ACL DRYRUN`, `ACL LOG`, `ACL SAVE/LOAD`, `ACL GENPASS`, ACL file (L)
- [ ] TLS parity: `tls-replication`, `tls-cluster`, certificate reload (M)
- [ ] Unix socket (`unixsocket`, `unixsocketperm`) (S)
- [ ] `protected-mode`, `bind`, `requirepass`, `masterauth` semantics (S)
- [ ] `CONFIG` full parameter surface: `GET *` matches Redis' keys; `SET`
      honours or explicitly rejects each; `REWRITE` with a config file (L)
- [ ] `INFO` all sections and fields, including `commandstats`, `latencystats`,
      `errorstats`, `cpu`, `cluster` (L)
- [ ] `SLOWLOG`, `LATENCY`, `LATENCY DOCTOR/HISTORY/RESET` (M)
- [ ] Logging: log levels, `logfile`, syslog if required (S)

### 3.8 Scripting

- [ ] Embed a Lua 5.1-compatible interpreter (dependency; Redis uses Lua 5.1) (L)
- [ ] `EVAL`, `EVALSHA`, `EVAL_RO`, `EVALSHA_RO`, `SCRIPT LOAD/EXISTS/FLUSH/KILL`
      (L)
- [ ] `redis.call`, `redis.pcall`, `redis.status_reply`, `redis.error_reply`,
      `redis.sha1hex`, `redis.log`, `redis.setresp`, `redis.breakpoint` (L)
- [ ] Command restrictions inside scripts, `lua-time-limit`, `BUSY` errors (M)
- [ ] Effects replication (or verbatim replication) compatibility (L)
- [ ] Functions: `FUNCTION LOAD/DUMP/LIST/STATS/FLUSH`, `FCALL`, `FCALL_RO` (L)

## 4. Delivery phases

| Phase | Scope | Exit criterion | Estimate |
| --- | --- | --- | --- |
| 0 | Command inventory + conformance harness (**done**) | `COMMAND DOCS` diff is empty except known gaps; differential runner can compare any command with seeds | done |
| 1 | Long-tail commands: SORT, set stores/SMOVE/SINTERCARD, BITFIELD, unified ZRANGE, DUMP/RESTORE, LMPOP/ZMPOP, sharded pub/sub, DEBUG/SLOWLOG/LATENCY/CLIENT KILL (**done**) | Apps using core types no longer hit unknown-command | done |
| 2 | Streams | Full stream family + consumer groups + RDB encodings | 3–5 w |
| 3 | Scripting | Lua + functions, including replication mode | 3–5 w |
| 4 | Multi-DB, full ACL, client tracking | `SELECT` 0–15, `MOVE`/`SWAPDB`, ACL users/files, tracking invalidations | 3–4 w |
| 5 | Persistence parity | All RDB encodings, multi-part AOF, DUMP/RESTORE, fixtures for Redis 6/7/8 | 3–5 w |
| 6 | Replication wire parity | Real Redis ⇄ redlite replication, partial resync, WAIT | 3–5 w |
| 7 | Cluster | 3-node interop, MOVED/ASK, resharding, replicas | 8–12 w |
| 8 | Ops surface | INFO/CONFIG/DEBUG/SLOWLOG/LATENCY/eviction/maxmemory | 3–4 w |
| 9 | Hardening | Official Redis Tcl test suite subset, client suites, fuzz, performance | 4–8 w |

Total: roughly **30–50 engineer-weeks** for one person, i.e. 7–12 months
full-time, longer part-time. Cluster alone is 2–3 months. Phases 1–3 deliver
most of the practical value.

## 5. Testing strategy

1. **Inventory as code.** A tool captures `COMMAND DOCS` and `CONFIG GET *`
   from the pinned Redis and writes a JSON fixture. Tests fail when redlite's
   table differs (missing command, arity, key spec, ACL categories).
2. **Differential corpus.** A declarative file per command: setup, command,
   expected replies in RESP2 and RESP3, error cases. Run against both servers,
   byte-for-byte, with randomized arguments for edge cases.
3. **Persistence fixtures.** Checked-in RDB/AOF files from Redis 6, 7 and 8 for
   every type and encoding. Load → dump → load → compare. Validate our output
   with `redis-check-rdb` and a real `redis-server`.
4. **Client matrix.** Run the test suites of `redis-py`, `go-redis`,
   `node-redis`, `ioredis`, and Jedis/Lettuce smoke tests against redlite.
5. **Application smoke tests.** BullMQ (Lua-heavy), Sidekiq, Celery, Rails
   cache, Spring Data Redis. These find gaps that command lists miss.
6. **Replication and cluster labs.** Scripts that start real Redis peers and
   verify sync, failover, redirection and resharding.
7. **Official Redis tests.** Track the upstream Tcl suite, run the subsets that
   apply. This is the strongest external gate.
8. **Performance.** `redis-benchmark` and `memtier` for throughput and latency;
   `MEMORY USAGE` accuracy; no regressions per release.

## 6. Definition of done

- [ ] `COMMAND` output matches Redis for names, arity, flags, first/last/step
      keys, and ACL categories
- [ ] Every command passes the differential corpus in RESP2 and RESP3,
      including error text
- [ ] RDB and AOF fixtures from Redis 6, 7 and 8 load; our files load in all
      three; `redis-check-rdb` passes
- [ ] `DUMP`/`RESTORE` payloads are interchangeable
- [ ] Real Redis replicates from redlite and redlite from real Redis, full and
      partial
- [ ] A 3-node real Redis cluster and a 3-node redlite cluster interoperate
      through resharding and failover
- [ ] The client matrix and the application smoke tests pass
- [ ] `INFO`/`CONFIG`/`DEBUG`/`SLOWLOG`/`LATENCY` match well enough for
      frameworks that parse them
- [ ] `docs/compatibility.md` lists only the declared exclusions (modules,
      Stack, Sentinel)

## 7. Risks and non-goals

- **Moving target.** Redis adds commands every release. Pin a version and state
  "parity with Redis 8.8"; re-baseline deliberately.
- **Cluster is a network protocol, not a feature.** The bus and failure
  detector are as much work as the slot routing.
- **Modules are a hard boundary.** Without CGo and the module ABI, arbitrary
  modules cannot run. This is the one place where "100%" is not achievable in
  pure Go; the plan is an explicit, documented exclusion.
- **Performance is not compatibility.** A correct server can still be slower;
  keep the benchmark track separate.

## 8. Recommended path for the actual goal

If the goal is "develop locally against redlite, deploy on Redis", the useful
milestone is **Phase 0 + 1 + 2**, plus multi-DB and the eviction/persistence
parity items that affect the app. That covers essentially every application
using stock data structures, streams, pub/sub and transactions.

If the goal is to publish "100% Redis compatible", the full plan applies, and
Phases 4–7 (ACL, persistence, replication, cluster) are what reviewers and
users will test.
