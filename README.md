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

**Connection**: `PING` `ECHO` `QUIT` `SELECT` `HELLO` `CLIENT` `COMMAND`
`CONFIG` `INFO` `TIME` `SHUTDOWN` `BGREWRITEAOF` `LASTSAVE`

**Strings**: `GET` `SET` `SETNX` `SETEX` `PSETEX` `GETSET` `GETDEL` `GETEX`
`MGET` `MSET` `MSETNX` `APPEND` `STRLEN` `INCR` `DECR` `INCRBY` `DECRBY`

**Keys**: `DEL` `UNLINK` `EXISTS` `TYPE` `KEYS` `SCAN` `RANDOMKEY` `RENAME`
`RENAMENX` `EXPIRE` `PEXPIRE` `EXPIREAT` `PEXPIREAT` `TTL` `PTTL` `PERSIST`
`EXPIRETIME` `PEXPIRETIME` `TOUCH` `DBSIZE` `FLUSHDB` `FLUSHALL`

That is 52 commands. The full set is deliberately not implemented yet; see
[docs/compatibility.md](docs/compatibility.md) for the differences you will hit
and [ROADMAP.md](ROADMAP.md) for what comes next.

## Design

- **RESP2 + RESP3** with hard limits on every length prefix (`proto-max-bulk-len`
  defaults to 512 MiB, arrays to 1M elements, inline commands to 64 KiB).
- **Append-only file** in RESP format. `appendfsync` supports `always`,
  `everysec` and `no`. A partial command at the tail of the file — the normal
  result of a crash — is truncated on load instead of failing startup.
- **Lazy and active expiration**: reads treat an expired key as missing; a
  background sampler reaps expired keys the way Redis does.
- **Deterministic recovery**: relative expiries are rewritten to absolute
  deadlines (`PXAT`, `PEXPIREAT`) before they reach the log, so replaying an old
  log does not extend TTLs.
- **One goroutine per connection**, a small command table, and replies that are
  values rather than writes, which keeps persistence ordered before replies.

More detail in [docs/architecture.md](docs/architecture.md).

## Configuration

| Flag | Default | Meaning |
| --- | --- | --- |
| `--bind` | `127.0.0.1` | Address to bind. Loopback by default because there is no auth yet. |
| `--port` | `6379` | TCP port. `0` picks an ephemeral port. |
| `--dir` | `.` | Directory holding the append-only file. |
| `--appendonly` | `false` | Enable persistence. |
| `--appendfsync` | `everysec` | `always`, `everysec` or `no`. |
| `--loglevel` | `info` | `debug`, `info`, `warn`, `error`. |
| `--maxclients` | `10000` | Concurrent connection limit. |
| `--timeout` | `0` | Idle client timeout; `0` disables. |
| `--version` | | Print the version and exit. |

At runtime:

```console
$ redis-cli CONFIG GET maxclients
$ redis-cli CONFIG SET maxclients 2048
$ redis-cli CONFIG SET appendfsync always
```

## Development

```console
make test      # go test ./...
make race      # go test -race ./...
make cover     # coverage report
make vet       # go vet ./...
make fmt       # gofmt
make docker    # build the image
```

The integration tests start a real server on an ephemeral port and speak the
wire protocol to it, so they exercise framing, dispatch, persistence and
shutdown together.

## License

MIT. See [LICENSE](LICENSE).
