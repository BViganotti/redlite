# Architecture

Redlite is four packages and a command table.

```
cmd/redlite        flag parsing, signal handling, lifecycle
internal/config    settings and validation
internal/resp      RESP2/RESP3 parser and writer
internal/store     keyspace, expiration, append-only file
internal/server    listeners, connections, commands, dispatch
```

## Request lifecycle

1. `server.serveClient` sets a read deadline (if configured) and asks
   `resp.Reader` for one command.
2. `server.execute` normalises the command name, looks it up in the table,
   checks arity, and calls the handler.
3. Handlers return one of `simpleReply`, `bulkReply`, `intReply`, `arrayReply`,
   `mapReply`, `nullReply` — or an `error`, which becomes a `-ERR` reply.
4. If the handler reports a mutation, `execute` writes the canonical command to
   the append-only file **before** the caller writes the reply. A failed AOF
   write is reported to the client as `MISCONF` instead of being silently lost.
5. `serveClient` encodes the reply and flushes. `QUIT` and `SHUTDOWN` set a flag
   that closes the connection after the reply is flushed.

Replies are values rather than direct writes for a reason: it makes the
mutate → persist → reply ordering explicit and keeps protocol encoding out of
command logic.

## Concurrency

- One goroutine per connection; commands from one client are strictly ordered.
- The keyspace is guarded by a single `sync.RWMutex`. Reads take `RLock` and
  copy values out; writes take `Lock`. This is the simplest correct choice.
  Sharding is a performance task, tracked in `ROADMAP.md`.
- Counters are `atomic.Uint64`. Server-wide mutable settings (`maxclients`,
  log level, fsync policy) are atomics or owned by the AOF's mutex.
- `Shutdown` flips an atomic flag, closes the listener, closes every client
  connection, waits for connection goroutines, then closes the store and the
  log. It is idempotent.

## Expiration

Reads treat an expired entry as missing without mutating the map under a read
lock. A background sampler wakes every 100 ms, inspects up to 20 arbitrary keys,
and deletes the expired ones. That mirrors Redis' probabilistic approach and
keeps reaping O(1) per tick instead of O(keys).

`TTL` rounds to the nearest second (`(ms + 500) / 1000`) like Redis; `PTTL`
returns exact milliseconds. `-2` means missing, `-1` means no expiry.

## Persistence

The append-only file stores RESP arrays — the same encoding clients send. A
write command is appended only when the handler reports that state changed, so a
failed `SET NX` is not replayed as a success.

Relative expiries never reach the file. `SET k v EX 60` is stored as
`SET k v PXAT <absolute-ms>` and `EXPIRE k 60` as `PEXPIREAT k <absolute-ms>`,
so replaying a week-old log does not restart the clock.

`Load` tracks the offset of the last complete command. If the process died
mid-write, everything after that offset is truncated and the server starts with
a warning. This is why `resp.Reader` distinguishes `io.EOF` (clean boundary)
from `io.ErrUnexpectedEOF` (mid-command).

`BGREWRITEAOF` writes a temp file, fsyncs it, renames it over the log, and
reopens the handle. It runs synchronously today; the name is kept for client
compatibility and `ROADMAP.md` tracks the background version.

## Limits

| Limit | Default | Where |
| --- | --- | --- |
| Bulk string | 512 MiB | `resp.DefaultMaxBulk` |
| Array elements | 1M | `resp.DefaultMaxArray` |
| Inline command | 64 KiB | `resp.DefaultMaxInline` |
| Clients | 10,000 | `--maxclients` |
| Idle timeout | none | `--timeout` |

Protocol violations produce `-ERR Protocol error: ...` and close the connection,
which prevents an invalid stream from desynchronising replies.

## What is deliberately absent

No auth, no multiple databases, no cluster, no Lua, no RDB yet. The server binds
to loopback by default because an unauthenticated Redis-compatible server on a
public interface is a bad default. See `docs/compatibility.md`.
