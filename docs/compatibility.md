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
| Keyspace | `DEL` `UNLINK` `EXISTS` `TYPE` `KEYS` `SCAN` `RANDOMKEY` `RENAME` `RENAMENX` `TOUCH` `DBSIZE` `FLUSHDB` `FLUSHALL` |
| Expiry | `EXPIRE` `PEXPIRE` `EXPIREAT` `PEXPIREAT` `TTL` `PTTL` `PERSIST` `EXPIRETIME` `PEXPIRETIME` |

## Known differences

### Data model

- Only string values exist. `TYPE` returns `string` or `none`. Hash, list, set,
  sorted-set and stream commands are not implemented yet (`ROADMAP.md`, v0.2).
- One database. `SELECT 0` succeeds; any other index returns
  `ERR DB index is out of range`.

### Scanning

`SCAN` cursors are indexes into a sorted snapshot of the keyspace, not hash
buckets. Consequences:

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
- `DEL` of a key that has expired but has not been reaped yet returns `0`, and
  `EXISTS`/`TYPE` behave as if it were gone.
- `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT` accept `NX`, `XX`, `GT`, `LT`.
- Expired keys are reaped by a sampler every 100 ms, in addition to lazy checks
  on read.

### Authentication and security

- No password or ACL support. `HELLO ... AUTH` returns `WRONGPASS`.
- The server binds to `127.0.0.1` by default. Do not expose it publicly.

### Transactions and pub/sub

- `MULTI`, `EXEC`, `WATCH`, `SUBSCRIBE`, `PUBLISH` and blocking commands are not
  implemented. They are tracked for v0.3.

### Persistence

- The append-only file is a sequence of RESP commands, which is the same shape
  Redis uses, but redlite does not read Redis' RDB files.
- `BGREWRITEAOF` is synchronous today; the reply string is the Redis-compatible
  one so clients do not break.
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
