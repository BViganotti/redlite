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
  redis-server --port "$REDIS_PORT" --save '' --appendonly no \
    --dir "$WORKDIR" --dbfilename redis-dump.rdb --daemonize yes >/dev/null
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

# check3 is the same comparison over RESP3.
check3() {
  local mode=$1
  shift
  local redis_reply redlite_reply

  if [ "$mode" = sort ]; then
    redis_reply=$(redis-cli -3 -p "$REDIS_PORT" --raw "$@" 2>&1 | sort)
    redlite_reply=$(redis-cli -3 -p "$REDLITE_PORT" --raw "$@" 2>&1 | sort)
  else
    redis_reply=$(redis-cli -3 -p "$REDIS_PORT" --raw "$@" 2>&1)
    redlite_reply=$(redis-cli -3 -p "$REDLITE_PORT" --raw "$@" 2>&1)
  fi

  if [ "$redis_reply" != "$redlite_reply" ]; then
    echo "MISMATCH (RESP3): $*" >&2
    echo "  redis:   $(printf '%s' "$redis_reply" | tr '\n' '|')" >&2
    echo "  redlite: $(printf '%s' "$redlite_reply" | tr '\n' '|')" >&2
    FAILED=1
  fi
}

# check_script pipes a multi-line script through one connection, for commands
# that need session state such as MULTI/EXEC.
check_script() {
  local name=$1
  local script=$2
  local redis_reply redlite_reply

  redis_reply=$(printf '%s\n' "$script" | redis-cli -p "$REDIS_PORT" --raw 2>&1)
  redlite_reply=$(printf '%s\n' "$script" | redis-cli -p "$REDLITE_PORT" --raw 2>&1)

  if [ "$redis_reply" != "$redlite_reply" ]; then
    echo "MISMATCH (script $name)" >&2
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

# Extended strings
check raw SET gs "This is a string"
check raw GETRANGE gs 0 3
check raw GETRANGE gs -3 -1
check raw SETRANGE rs 5 hello
check raw STRLEN rs
check raw SETRANGE rs 0 HELLO
check raw GETRANGE rs 0 4
check raw INCRBYFLOAT fl 10
check raw INCRBYFLOAT fl 5

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

# ZSets
check raw ZADD z 1 a 2 b 3 c
check raw ZCARD z
check raw ZSCORE z b
check raw ZSCORE z missing
check raw ZRANGE z 0 -1
check raw ZRANGE z 0 -1 WITHSCORES
check raw ZREVRANGE z 0 1
check raw ZRANGEBYSCORE z 2 3
check raw ZRANGEBYSCORE z "(2" "+inf" WITHSCORES
check raw ZRANGEBYSCORE z -inf +inf LIMIT 1 1
check raw ZREVRANGEBYSCORE z +inf -inf LIMIT 0 2
check raw ZRANK z b
check raw ZREVRANK z b
check raw ZCOUNT z 2 3
check raw ZINCRBY z 2 a
check raw ZADD z CH 5 b
check raw ZADD z NX 9 b
check raw ZMSCORE z a b missing
check raw ZREM z a
check raw ZREMRANGEBYRANK z 0 0
check raw ZCARD z

# RESP3 reply shapes
check raw HSET h3 a 1 b 2
check3 raw HGETALL h3
check raw ZADD z3 1 a 2 b
check3 raw ZRANGE z3 0 -1 WITHSCORES

# Transactions
check_script multi-exec $'MULTI\nSET tx 1\nINCR tx\nGET tx\nEXEC'
check_script multi-discard $'MULTI\nSET tx 2\nDISCARD'
check_script multi-error $'MULTI\nSET onlykey\nEXEC'
check_script exec-without-multi $'EXEC'
check raw PUBLISH nobody hello

# Blocking and moves
check raw RPUSH bl a
check raw BLPOP bl 1
check raw BLPOP bl 0.05
check raw RPUSH ml a b c
check raw LMOVE ml ml2 RIGHT LEFT
check raw RPOPLPUSH ml ml2
check sort LRANGE ml2 0 -1
check raw SAVE
check raw BGSAVE

# Bitmaps
check raw SETBIT bm 7 1
check raw SETBIT bm 0 1
check raw GETBIT bm 7
check raw GETBIT bm 6
check raw BITCOUNT bm
check raw BITCOUNT bm 0 0 BIT
check raw BITPOS bm 1
check raw BITOP NOT bmnot bm
check raw GET bmnot

# HyperLogLog
check raw PFADD hll a b c
check raw PFADD hll a
check raw PFCOUNT hll
check raw PFADD hll2 c d
check raw PFCOUNT hll hll2
check raw PFMERGE merged hll hll2
check raw PFCOUNT merged

# Sorted-set algebra
check raw ZADD za 1 a 2 b
check raw ZADD zb 10 b 20 c
check raw ZUNION 2 za zb
check raw ZUNION 2 za zb WITHSCORES
check raw ZUNION 2 za zb WEIGHTS 1 2 WITHSCORES
check raw ZINTER 2 za zb
check raw ZDIFF 2 za zb
check raw ZUNIONSTORE zout 2 za zb
check raw ZSCORE zout b
check raw ZPOPMIN zout

# Small commands
check raw HSET hs f1 v1
check raw HSTRLEN hs f1
check raw RPUSH li a b c
check raw LINSERT li BEFORE b x
check raw LRANGE li 0 -1
check raw LPOS li b
check raw LPOS li b COUNT 0
check raw COPY hs hscopy
check raw HGET hscopy f1
check raw COPY hs hscopy
check raw COPY hs hscopy REPLACE

# Errors
check raw HSET h odd
check raw LPUSH
check raw GET
check raw NOTACOMMAND somearg
check raw SET x v EX 0
check raw EXPIRE missing 10

if [ "$FAILED" -ne 0 ]; then
  echo "differential: mismatches found" >&2
  exit 1
fi

echo "differential: all checks passed"
