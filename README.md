# redlite

[![ci](https://github.com/BViganotti/redlite/actions/workflows/ci.yml/badge.svg)](https://github.com/BViganotti/redlite/actions/workflows/ci.yml)

A Redis-compatible in-memory data store, written from scratch in Go.

Redlite speaks RESP2 and RESP3 over TCP, so `redis-cli` and ordinary Redis client
libraries work against it unchanged. It is not trying to replace Redis in
production. It is trying to be the Redis you can read end to end: one Go binary,
no dependencies, and a protocol small enough to understand in an afternoon.

```console
$ go run ./cmd/redlite
$ redis-cli SET greeting "hello"
OK
$ redis-cli GET greeting
"hello"
```

## Why

Running Redis in tests usually means pulling an image, waiting for a container,
and hoping CI uses the same version you do. Redlite is an attempt to make that
boring part small: a single static binary that speaks the wire protocol well
enough that client code cannot tell the difference.

Redis is also one of the best-documented protocols in the world, which makes it
a rare thing for a systems project — an objective oracle. Every command here can
be compared against real Redis, byte for byte, which is exactly how the test
suite is headed ([ROADMAP.md](ROADMAP.md)).

## Quick start

Build and run:

```console
make build
./bin/redlite --appendonly --dir ./data
```

Or run it straight from source:

```console
go run ./cmd/redlite
```

Talk to it with any Redis client:

```console
$ redis-cli PING
PONG
$ redis-cli SET user:1 alice EX 60
OK
$ redis-cli TTL user:1
60
$ redis-cli SCAN 0 MATCH 'user:*' COUNT 10
```

Docker:

```console
docker build -t redlite .
docker run --rm -p 6379:6379 -v redlite-data:/data redlite
```

Or with Compose:

```console
docker compose up --build
```

## What works today

**Connection**: `PING` `ECHO` `QUIT` `SELECT` `HELLO`

**Security**: `AUTH` `ACL`

**Transactions**: `MULTI` `EXEC` `DISCARD` `WATCH` `UNWATCH`

**Pub/Sub**: `SUBSCRIBE` `UNSUBSCRIBE` `PSUBSCRIBE` `PUNSUBSCRIBE` `PUBLISH` `PUBSUB`

**Introspection**: `CLIENT` `COMMAND` `CONFIG` `INFO` `TIME` `LASTSAVE` `SHUTDOWN` `BGREWRITEAOF` `SAVE` `BGSAVE`

**Strings**: `GET` `SET` `SETNX` `SETEX` `PSETEX` `GETSET` `GETDEL` `GETEX`
`MGET` `MSET` `MSETNX` `APPEND` `STRLEN` `GETRANGE` `SETRANGE` `INCR` `DECR`
`INCRBY` `DECRBY` `INCRBYFLOAT`

**Hashes**: `HSET` `HSETNX` `HMSET` `HGET` `HMGET` `HDEL` `HLEN` `HEXISTS`
`HKEYS` `HVALS` `HGETALL` `HINCRBY` `HSCAN`

**Lists**: `LPUSH` `RPUSH` `LPUSHX` `RPUSHX` `LPOP` `RPOP` `LLEN` `LINDEX`
`LSET` `LRANGE` `LTRIM` `LREM` `LMOVE` `RPOPLPUSH`

**Blocking**: `BLPOP` `BRPOP` `BLMOVE` `BRPOPLPUSH`

**Replication**: `REPLICAOF` `SLAVEOF` `PSYNC` `REPLCONF`

**Sets**: `SADD` `SREM` `SCARD` `SISMEMBER` `SMISMEMBER` `SMEMBERS` `SINTER`
`SUNION` `SDIFF` `SPOP` `SRANDMEMBER` `SSCAN`

**Sorted sets**: `ZADD` `ZREM` `ZSCORE` `ZMSCORE` `ZINCRBY` `ZCARD` `ZCOUNT`
`ZRANGE` `ZREVRANGE` `ZRANGEBYSCORE` `ZREVRANGEBYSCORE` `ZRANK` `ZREVRANK`
`ZREMRANGEBYRANK` `ZREMRANGEBYSCORE` `ZSCAN`

**Keys**: `DEL` `UNLINK` `EXISTS` `TYPE` `KEYS` `SCAN` `RANDOMKEY` `RENAME`
`RENAMENX` `EXPIRE` `PEXPIRE` `EXPIREAT` `PEXPIREAT` `TTL` `PTTL` `PERSIST`
`EXPIRETIME` `PEXPIRETIME` `TOUCH` `DBSIZE` `FLUSHDB` `FLUSHALL`

That is 133 commands. Every value carries an explicit type, so touching a hash
with `GET` or a string with `HGET` returns `WRONGTYPE` instead of guessing.
See [docs/compatibility.md](docs/compatibility.md) for the differences you will
hit and [ROADMAP.md](ROADMAP.md) for what comes next.

## Design

- **Five value types** — strings, hashes, lists, sets and sorted sets — behind
  one sealed interface. Every command checks the type and returns `WRONGTYPE`
  on a mismatch; `SET` replaces a value of any type, as Redis does.
- **RESP2 + RESP3** with hard limits on every length prefix (`proto-max-bulk-len`
  defaults to 512 MiB, arrays to 1M elements, inline commands to 64 KiB).
- **Append-only file** in RESP format. `appendfsync` supports `always`,
  `everysec` and `no`. A partial command at the tail of the file — the normal
  result of a crash — is truncated on load instead of failing startup.
- **Snapshots**: `SAVE` and `BGSAVE` write a versioned, checksummed file with a
  temporary file and an atomic rename. A corrupt snapshot refuses to load
  instead of starting empty.
- **Lazy and active expiration**: reads treat an expired key as missing; a
  background sampler reaps expired keys the way Redis does.
- **Deterministic recovery**: relative expiries are rewritten to absolute
  deadlines (`PXAT`, `PEXPIREAT`) before they reach the log, so replaying an old
  log does not extend TTLs. Snapshots rebuild every type with its native
  commands (`HSET`, `RPUSH`, `SADD`).
- **One goroutine per connection**, a small command table, and replies that are
  values rather than writes, which keeps persistence ordered before replies.
- **Transactions are atomic**: `EXEC` takes a server-wide write gate so a queued
  batch cannot interleave with another client. `WATCH` compares per-key versions
  captured when the watch was armed.
- **Pub/Sub** delivers RESP3 push frames, or plain arrays to RESP2 subscribers,
  with per-connection write serialisation.
- **Lists are ring buffers**, so pushes and pops are O(1) at both ends.
- **Blocking commands** register a waiter per key and are woken by pushes; they
  release the transaction gate while waiting, so a blocked client never holds up
  another client.
- **Replication** streams a snapshot under the transaction gate and then the
  command stream, so no write can slip between the two. Replicas apply
  transactions as a unit and reject client writes.
- **Authentication** uses `requirepass` with a constant-time comparison; every
  command except `AUTH`, `HELLO` and `QUIT` is guarded by `NOAUTH`.
- **Background AOF rewrite** buffers appends during the rewrite and folds them
  into the new file before the atomic swap.
- **Sorted sets use a skiplist** with spans, like Redis. Inserts, deletes and
  rank queries are O(log n), and the structure is checked against a naive model
  over thousands of randomised operations.

More detail in [docs/architecture.md](docs/architecture.md).

## Embedding

Redlite can run inside a Go test process, which means no container and no
background service:

```go
srv, err := redlite.Start(redlite.Config{Port: 0, Dir: t.TempDir()})
if err != nil {
    t.Fatal(err)
}
defer srv.Close(context.Background())

client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
```

`Port: 0` picks a free port and `Addr()` reports it. Set `AppendOnly` and `Dir`
if you want the data to survive a restart. The package logs nothing unless you
pass a `Logger`.

## Configuration

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bind` | `127.0.0.1` | Address to bind. Loopback by default because there is no auth yet. |
| `--port` | `6379` | TCP port. `0` picks an ephemeral port. |
| `--dir` | `.` | Directory holding the append-only file and snapshots. |
| `--dbfilename` | `dump.rdb` | Snapshot file name inside `--dir`. |
| `--appendonly` | `false` | Enable persistence. |
| `--appendfsync` | `everysec` | `always`, `everysec` or `no`. |
| `--loglevel` | `info` | `debug`, `info`, `warn`, `error`. |
| `--maxclients` | `10000` | Concurrent connection limit. |
| `--timeout` | `0` | Idle client timeout; `0` disables. |
| `--requirepass` | none | Password clients must send with `AUTH`. |
| `--masterauth` | none | Password a replica sends to its primary. |
| `--notify-keyspace-events` | none | Keyspace event flags, for example `KEA`. |
| `--version` | | Print the version and exit. |

At runtime:

```console
$ redis-cli CONFIG GET maxclients
$ redis-cli CONFIG SET maxclients 2048
$ redis-cli CONFIG SET appendfsync always
```

## Keyspace notifications

Enable events and subscribe to the Redis channel names:

```console
$ redis-cli CONFIG SET notify-keyspace-events KEA
OK
$ redis-cli PSUBSCRIBE '__keyevent@0__:*'
$ redis-cli SET greeting hello
```

Flags follow Redis: `K` keyspace channel, `E` keyevent channel, `A` all
categories, and `g`, `$`, `l`, `s`, `h`, `z`, `x` for generic, string, list,
set, hash, sorted-set and expired events.

## Authentication

Set a password and every connection must authenticate before it can run a
command:

```console
$ ./bin/redlite --requirepass secret
$ redis-cli -a secret PING
PONG
```

`CONFIG SET requirepass <value>` changes it at runtime; an empty value disables
authentication. A replica connecting to a protected primary needs
`--masterauth <password>`. There is one user, `default`; `ACL WHOAMI`,
`ACL LIST` and `ACL CAT` are implemented, per-user ACLs are not.

## Replication

Point a second instance at a primary:

```console
$ redis-cli -p 6380 REPLICAOF 127.0.0.1 6379
OK
$ redis-cli -p 6380 INFO replication
role:slave
master_host:127.0.0.1
master_port:6379
master_link_status:up
```

The replica performs a full resync — snapshot plus command stream — then
applies writes as they happen. It is read-only until `REPLICAOF NO ONE`. Partial
resync and a replication backlog are not implemented yet, so a dropped link
triggers a new full resync.

## Development

```console
make test         # go test ./...
make race         # go test -race ./...
make cover        # coverage report
make vet          # go vet ./...
make fmt          # gofmt
make docker       # build the image
make differential # compare replies against a real Redis
```

The integration tests start a real server on an ephemeral port and speak the
wire protocol to it, so they exercise framing, dispatch, persistence and
shutdown together.

`make differential` needs `redis-server` and `redis-cli` on `PATH`. It starts an
isolated Redis on port 16400 with persistence disabled, runs the same command
script against both servers, and fails on the first mismatch in either direction.

The parser and the glob matcher have fuzz targets:

```console
go test ./internal/resp -run '^$' -fuzz=FuzzReadCommand
go test ./internal/store -run '^$' -fuzz=FuzzMatch
```

Store benchmarks:

```console
go test ./internal/store -run '^$' -bench . -benchmem
```

## License

MIT. See [LICENSE](LICENSE).
