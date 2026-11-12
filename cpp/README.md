# redlite C++ port

A C++17 reimplementation of redlite's Redis-compatible server, built with no
third-party dependencies beyond the C++ standard library and POSIX sockets.

This is a **work in progress port**, not yet at feature parity with the Go
implementation. It is a real, running server: it speaks RESP2 and RESP3, stores
the five core value types, expires keys, supports transactions, blocking list
commands, pub/sub, sharded pub/sub, bitmaps and AOF persistence.

## Build

```console
$ cmake -S cpp -B cpp/build -G Ninja -DCMAKE_BUILD_TYPE=Release
$ cmake --build cpp/build -j
$ ./cpp/build/redlite --port 6379 --dir .
```

Tests:

```console
$ ./cpp/build/redlite_tests
# or
$ ctest --test-dir cpp/build --output-on-failure
```

## Implemented

- RESP2/RESP3 reader and writer, including maps, sets, doubles, booleans,
  big numbers, verbatim strings, push frames, nulls and inline commands.
- TCP server with one thread per connection, `--port`, `--bind`, `--dir`,
  `--appendonly`, `--appendfsync`, `--maxclients`.
- Keyspace: `DEL UNLINK EXISTS TYPE KEYS SCAN RANDOMKEY RENAME RENAMENX COPY
  EXPIRE PEXPIRE EXPIREAT PEXPIREAT TTL PTTL PERSIST EXPIRETIME PEXPIRETIME
  TOUCH DBSIZE FLUSHDB FLUSHALL`
- Strings: `GET SET SETNX SETEX PSETEX GETSET GETDEL GETEX MGET MSET MSETNX
  APPEND STRLEN GETRANGE SUBSTR SETRANGE INCR DECR INCRBY DECRBY INCRBYFLOAT`
- Hashes: `HSET HMSET HSETNX HGET HMGET HDEL HLEN HEXISTS HKEYS HVALS HGETALL
  HINCRBY HSTRLEN HSCAN HRANDFIELD`
- Lists: `LPUSH RPUSH LPUSHX RPUSHX LPOP RPOP LLEN LINDEX LSET LRANGE LTRIM
  LREM LINSERT LPOS LMOVE RPOPLPUSH LMPOP BLPOP BRPOP BLMOVE BRPOPLPUSH`
- Sets: `SADD SREM SCARD SISMEMBER SMISMEMBER SMEMBERS SINTER SUNION SDIFF
  SPOP SRANDMEMBER SSCAN SMOVE SINTERSTORE SUNIONSTORE SDIFFSTORE SINTERCARD`
- Sorted sets: `ZADD ZREM ZSCORE ZMSCORE ZINCRBY ZCARD ZCOUNT ZRANGE ZREVRANGE
  ZRANGEBYSCORE ZREVRANGEBYSCORE ZRANK ZREVRANK ZREMRANGEBYRANK
  ZREMRANGEBYSCORE ZPOPMIN ZPOPMAX ZRANDMEMBER ZSCAN`
- Bitmaps: `SETBIT GETBIT BITCOUNT`
- Transactions: `MULTI EXEC DISCARD WATCH UNWATCH`
- Pub/Sub: `SUBSCRIBE UNSUBSCRIBE PSUBSCRIBE PUNSUBSCRIBE PUBLISH PUBSUB
  SSUBSCRIBE SUNSUBSCRIBE SPUBLISH`
- Admin/introspection: `PING ECHO QUIT SELECT HELLO RESET CLIENT COMMAND CONFIG
  INFO TIME DBSIZE LASTSAVE BGREWRITEAOF SAVE BGSAVE DEBUG MODULE SLOWLOG
  LATENCY MONITOR ACL WAIT WAITAOF OBJECT MEMORY LOLWUT`
- AOF persistence (`appendonly yes`), startup replay and `BGREWRITEAOF`.
- RESP3 reply shapes for maps, sets, doubles and sorted-set pairs.

## Not implemented yet

- Streams and consumer groups
- Lua scripting and functions
- Multiple databases (`SELECT` only accepts 0)
- Cluster mode and the cluster bus
- Replication (`REPLICAOF`, `PSYNC`, `WAIT` peer ACKs)
- RDB save/load (AOF is the only persistence format)
- `DUMP`/`RESTORE`, `MIGRATE`
- Geo commands, HyperLogLog, `BITFIELD`
- `SORT`, `LCS`
- Key eviction/`maxmemory` policies, client-side caching
- Full ACL users/selectors (only the default user exists)
- Redis Modules, Redis Stack and Sentinel

The Go server in the parent directory remains the reference implementation and
has the larger command surface.

## Layout

| File | Purpose |
| --- | --- |
| `src/resp.{hpp,cpp}` | RESP2/RESP3 codec |
| `src/store.{hpp,cpp}` | typed values, expiry, zset, keyspace |
| `src/server.{hpp,cpp}` | TCP server, clients, command dispatch, pub/sub, AOF |
| `src/commands_core.cpp` | connection, keyspace, transactions, pub/sub, bitmaps |
| `src/commands_string.cpp` | string commands |
| `src/commands_hash.cpp` | hash commands |
| `src/commands_list.cpp` | list and blocking list commands |
| `src/commands_set.cpp` | set commands |
| `src/commands_zset.cpp` | sorted-set commands |
| `tests/tests.cpp` | codec, glob, parser and zset tests |
