#!/usr/bin/env bash
#
# Differential check: run the same deterministic commands against a real Redis
# and against redlite, then compare the replies byte for byte.
#
# Requires redis-server and redis-cli on PATH. Redis is started on an isolated
# port with persistence disabled, so it cannot touch anything you care about.
#
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BIN=${BIN:-"$ROOT/bin/redlite"}
REDIS_PORT=${REDIS_PORT:-16400}
REDLITE_PORT=${REDLITE_PORT:-16401}
WORKDIR=$(mktemp -d)

for tool in redis-server redis-cli; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "differential: $tool is required" >&2
    exit 2
  fi
done

if [ ! -x "$BIN" ]; then
  echo "differential: $BIN not found, run 'make build' first" >&2
  exit 2
fi

cleanup() {
  if [ -n "${REDLITE_PID:-}" ]; then
    kill "$REDLITE_PID" >/dev/null 2>&1 || true
    wait "$REDLITE_PID" >/dev/null 2>&1 || true
  fi
  redis-cli -p "$REDIS_PORT" SHUTDOWN NOSAVE >/dev/null 2>&1 || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT INT TERM

start() {
  redis-server --port "$REDIS_PORT" --save '' --appendonly no --daemonize yes >/dev/null
  "$BIN" --port "$REDLITE_PORT" --dir "$WORKDIR" --loglevel error &
  REDLITE_PID=$!

  for _ in $(seq 1 50); do
    if redis-cli -p "$REDIS_PORT" PING >/dev/null 2>&1 &&
      redis-cli -p "$REDLITE_PORT" PING >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done

  echo "differential: servers did not become ready" >&2
  exit 1
}

FAILED=0

# check <sort|raw> <command...>
# "sort" is for commands whose reply order Redis does not guarantee.
check() {
  local mode=$1
  shift
  local redis_reply redlite_reply

  if [ "$mode" = sort ]; then
    redis_reply=$(redis-cli -p "$REDIS_PORT" --raw "$@" 2>&1 | sort)
    redlite_reply=$(redis-cli -p "$REDLITE_PORT" --raw "$@" 2>&1 | sort)
  else
    redis_reply=$(redis-cli -p "$REDIS_PORT" --raw "$@" 2>&1)
    redlite_reply=$(redis-cli -p "$REDLITE_PORT" --raw "$@" 2>&1)
  fi

  if [ "$redis_reply" != "$redlite_reply" ]; then
    echo "MISMATCH: $*" >&2
    echo "  redis:   $(printf '%s' "$redis_reply" | tr '\n' '|')" >&2
    echo "  redlite: $(printf '%s' "$redlite_reply" | tr '\n' '|')" >&2
    FAILED=1
  fi
}

start

# Strings
check raw SET s hello
check raw GET s
check raw APPEND s " world"
check raw STRLEN s
check raw GET missing
check raw SET n 10
check raw INCR n
check raw INCRBY n 5
check raw DECR n
check raw GET n
check raw SETNX s x
check raw SET k v XX
check raw SET k2 v NX
check raw MSET a 1 b 2
check raw MGET a b c
check raw TYPE s
check raw EXISTS s
check raw DEL s
check raw EXISTS s
check raw SET bad notanumber
check raw INCR bad

# Hashes
check raw HSET h f1 v1 f2 v2
check raw HGET h f1
check raw HGET h nobody
check raw HLEN h
check raw HEXISTS h f1
check raw HINCRBY h counter 5
check raw HINCRBY h counter -2
check sort HGETALL h
check sort HKEYS h
check sort HVALS h
check raw HDEL h f1 nobody
check raw HLEN h

# Lists
check raw RPUSH l a b c
check raw LRANGE l 0 -1
check raw LPUSH l z
check raw LRANGE l 0 -1
check raw LINDEX l -1
check raw LINDEX l 99
check raw LSET l 0 A
check raw LRANGE l 0 -1
check raw LTRIM l 1 2
check raw LRANGE l 0 -1
check raw LPOP l
check raw LRANGE l 0 -1
check raw LPOP missing
check raw LLEN missing
check raw LSET missing 0 x

# Sets
check raw SADD st a b c
check raw SCARD st
check raw SISMEMBER st a
check raw SISMEMBER st zz
check sort SMEMBERS st
check raw SADD st2 b c d
check sort SINTER st st2
check sort SUNION st st2
check sort SDIFF st st2
check raw SREM st a zz
check raw SCARD st

# Errors
check raw HSET h odd
check raw LPUSH
check raw GET
check raw NOTACOMMAND
check raw SET x v EX 0
check raw EXPIRE missing 10

if [ "$FAILED" -ne 0 ]; then
  echo "differential: mismatches found" >&2
  exit 1
fi

echo "differential: all checks passed"
