package server

import (
	"strings"
	"testing"
)

func TestBitmapCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("SETBIT", "b", "7", "1"), 0)
	expectInt(t, c.do("GETBIT", "b", "7"), 1)
	expectInt(t, c.do("GETBIT", "b", "6"), 0)
	expectBulk(t, c.do("GET", "b"), "\x01")
	expectInt(t, c.do("BITCOUNT", "b"), 1)

	expectInt(t, c.do("SETBIT", "b", "0", "1"), 0)
	expectInt(t, c.do("BITCOUNT", "b"), 2)
	expectInt(t, c.do("BITPOS", "b", "1"), 0)
	expectInt(t, c.do("BITCOUNT", "b", "0", "0", "BYTE"), 2)
	expectInt(t, c.do("BITCOUNT", "b", "0", "0", "BIT"), 1)

	expectInt(t, c.do("BITOP", "NOT", "n", "b"), 1)
	expectInt(t, c.do("GETBIT", "n", "1"), 1)
	expectInt(t, c.do("GETBIT", "n", "0"), 0)

	expectInt(t, c.do("SETBIT", "c", "1", "1"), 0)
	expectInt(t, c.do("BITOP", "AND", "and", "b", "c"), 1)
	expectInt(t, c.do("BITCOUNT", "and"), 0)
	expectInt(t, c.do("BITOP", "OR", "or", "b", "c"), 1)
	expectInt(t, c.do("BITCOUNT", "or"), 3)

	expectInt(t, c.do("HSET", "h", "f", "v"), 1)
	expectError(t, c.do("GETBIT", "h", "0"), "WRONGTYPE")
}

func TestHyperLogLogCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("PFADD", "hll", "a", "b", "c"), 1)
	expectInt(t, c.do("PFADD", "hll", "a"), 0)
	expectInt(t, c.do("PFCOUNT", "hll"), 3)
	expectInt(t, c.do("PFADD", "hll2", "c", "d"), 1)
	expectInt(t, c.do("PFCOUNT", "hll", "hll2"), 4)

	expectSimple(t, c.do("PFMERGE", "merged", "hll", "hll2"), "OK")
	expectInt(t, c.do("PFCOUNT", "merged"), 4)
	expectSimple(t, c.do("TYPE", "merged"), "string")

	expectSimple(t, c.do("SET", "plain", "not-an-hll"), "OK")
	expectError(t, c.do("PFCOUNT", "plain"), "not a valid HyperLogLog")
}

func TestZSetAlgebraCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("ZADD", "z1", "1", "a", "2", "b"), 2)
	expectInt(t, c.do("ZADD", "z2", "10", "b", "20", "c"), 2)

	union := expectArray(t, c.do("ZUNION", "2", "z1", "z2", "WITHSCORES"))
	if len(union) != 6 {
		t.Fatalf("ZUNION returned %d elements", len(union))
	}
	expectBulk(t, union[0], "a")
	expectBulk(t, union[1], "1")
	expectBulk(t, union[2], "b")
	expectBulk(t, union[3], "12")

	weighted := expectArray(t, c.do("ZUNION", "2", "z1", "z2", "WEIGHTS", "1", "2", "WITHSCORES"))
	expectBulk(t, weighted[3], "22")

	inter := expectArray(t, c.do("ZINTER", "2", "z1", "z2"))
	if len(inter) != 1 || string(inter[0].([]byte)) != "b" {
		t.Fatalf("ZINTER = %v", inter)
	}
	diff := expectArray(t, c.do("ZDIFF", "2", "z1", "z2"))
	if len(diff) != 1 || string(diff[0].([]byte)) != "a" {
		t.Fatalf("ZDIFF = %v", diff)
	}

	expectInt(t, c.do("ZUNIONSTORE", "out", "2", "z1", "z2"), 3)
	expectBulk(t, c.do("ZSCORE", "out", "b"), "12")
	expectInt(t, c.do("ZINTERSTORE", "inter", "2", "z1", "z2"), 1)
	expectInt(t, c.do("ZDIFFSTORE", "diff", "2", "z1", "z2"), 1)
}

func TestZPopAndRandCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("ZADD", "z", "1", "a", "2", "b", "3", "c"), 3)
	popped := expectArray(t, c.do("ZPOPMIN", "z"))
	expectBulk(t, popped[0], "a")
	expectBulk(t, popped[1], "1")

	popped = expectArray(t, c.do("ZPOPMAX", "z", "10"))
	if len(popped) != 4 {
		t.Fatalf("ZPOPMAX returned %d elements", len(popped))
	}
	expectBulk(t, popped[0], "c")
	expectInt(t, c.do("EXISTS", "z"), 0)

	expectInt(t, c.do("ZADD", "r", "1", "a", "2", "b"), 2)
	member := c.do("ZRANDMEMBER", "r")
	if _, ok := member.([]byte); !ok {
		t.Fatalf("ZRANDMEMBER without count returned %T", member)
	}
	unique := expectArray(t, c.do("ZRANDMEMBER", "r", "10"))
	if len(unique) != 2 {
		t.Fatalf("ZRANDMEMBER returned %d members", len(unique))
	}
	repeated := expectArray(t, c.do("ZRANDMEMBER", "r", "-5"))
	if len(repeated) != 5 {
		t.Fatalf("ZRANDMEMBER negative returned %d members", len(repeated))
	}
	pairs := expectArray(t, c.do("ZRANDMEMBER", "r", "2", "WITHSCORES"))
	if len(pairs) != 4 {
		t.Fatalf("ZRANDMEMBER WITHSCORES returned %d elements", len(pairs))
	}
}

func TestHashAndListExtras(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("HSET", "h", "f1", "v1", "f2", "v2"), 2)
	expectInt(t, c.do("HSTRLEN", "h", "f1"), 2)
	expectInt(t, c.do("HSTRLEN", "h", "missing"), 0)

	all := expectArray(t, c.do("HRANDFIELD", "h", "10"))
	if len(all) != 2 {
		t.Fatalf("HRANDFIELD returned %d fields", len(all))
	}
	withValues := expectArray(t, c.do("HRANDFIELD", "h", "2", "WITHVALUES"))
	if len(withValues) != 4 {
		t.Fatalf("HRANDFIELD WITHVALUES returned %d elements", len(withValues))
	}

	expectInt(t, c.do("RPUSH", "l", "a", "b", "c", "b"), 4)
	expectInt(t, c.do("LINSERT", "l", "BEFORE", "b", "x"), 5)
	expectInt(t, c.do("LINSERT", "l", "AFTER", "c", "y"), 6)
	expectInt(t, c.do("LINSERT", "l", "BEFORE", "nope", "z"), -1)
	expectInt(t, c.do("LINSERT", "missing", "BEFORE", "a", "b"), 0)

	values := expectArray(t, c.do("LRANGE", "l", "0", "-1"))
	if len(values) != 6 || string(values[1].([]byte)) != "x" {
		t.Fatalf("LRANGE = %v", values)
	}
	expectInt(t, c.do("LPOS", "l", "b"), 2)
	expectInt(t, c.do("LPOS", "l", "b", "RANK", "2"), 5)
	positions := expectArray(t, c.do("LPOS", "l", "b", "COUNT", "0"))
	if len(positions) != 2 {
		t.Fatalf("LPOS COUNT = %v", positions)
	}
	expectNull(t, c.do("LPOS", "l", "missing"))
}

func TestCopyAndObject(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "src", "value", "EX", "100"), "OK")
	expectInt(t, c.do("COPY", "src", "dst"), 1)
	expectBulk(t, c.do("GET", "dst"), "value")
	if ttl := c.do("TTL", "dst").(int64); ttl <= 0 {
		t.Fatalf("copied TTL = %d", ttl)
	}
	expectInt(t, c.do("COPY", "src", "dst"), 0)
	expectInt(t, c.do("COPY", "src", "dst", "REPLACE"), 1)
	expectInt(t, c.do("COPY", "missing", "dst2"), 0)

	expectSimple(t, c.do("SET", "num", "123"), "OK")
	expectBulk(t, c.do("OBJECT", "ENCODING", "num"), "int")
	expectSimple(t, c.do("SET", "text", "abc"), "OK")
	expectBulk(t, c.do("OBJECT", "ENCODING", "text"), "raw")
	expectInt(t, c.do("HSET", "h", "a", "1"), 1)
	expectBulk(t, c.do("OBJECT", "ENCODING", "h"), "listpack")
	expectInt(t, c.do("OBJECT", "REFCOUNT", "text"), 1)
	expectNull(t, c.do("OBJECT", "ENCODING", "missing"))

	usage := c.do("MEMORY", "USAGE", "text").(int64)
	if usage <= 0 {
		t.Fatalf("MEMORY USAGE = %d", usage)
	}
}

func TestResetCommand(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("MULTI"), "OK")
	expectSimple(t, c.do("SET", "k", "v"), "QUEUED")
	expectSimple(t, c.do("RESET"), "RESET")
	expectSimple(t, c.do("SET", "k", "v"), "OK")
	expectBulk(t, c.do("GET", "k"), "v")
}

func TestWaitAndLolwut(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	expectInt(t, c.do("WAIT", "0", "0"), 0)
	banner := string(c.do("LOLWUT").([]byte))
	if !strings.Contains(banner, "redlite") {
		t.Fatalf("LOLWUT = %q", banner)
	}
}
