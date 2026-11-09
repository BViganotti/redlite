package server

import (
	"strconv"
	"testing"
)

func TestHashCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("HSET", "h", "a", "1", "b", "2"), 2)
	expectInt(t, c.do("HSET", "h", "a", "3"), 0)
	expectBulk(t, c.do("HGET", "h", "a"), "3")
	expectNull(t, c.do("HGET", "h", "missing"))
	expectInt(t, c.do("HLEN", "h"), 2)
	expectInt(t, c.do("HEXISTS", "h", "a"), 1)
	expectInt(t, c.do("HEXISTS", "h", "zzz"), 0)

	keys := expectArray(t, c.do("HKEYS", "h"))
	if len(keys) != 2 {
		t.Fatalf("HKEYS returned %d fields", len(keys))
	}
	expectBulk(t, keys[0], "a")
	expectBulk(t, keys[1], "b")

	values := expectArray(t, c.do("HVALS", "h"))
	if len(values) != 2 {
		t.Fatalf("HVALS returned %d values", len(values))
	}
	expectBulk(t, values[0], "3")

	all := expectArray(t, c.do("HGETALL", "h"))
	if len(all) != 4 {
		t.Fatalf("HGETALL returned %d elements", len(all))
	}
	expectBulk(t, all[0], "a")
	expectBulk(t, all[1], "3")
	expectBulk(t, all[2], "b")
	expectBulk(t, all[3], "2")

	multi := expectArray(t, c.do("HMGET", "h", "a", "zzz"))
	expectBulk(t, multi[0], "3")
	expectNull(t, multi[1])

	expectInt(t, c.do("HSETNX", "h", "a", "9"), 0)
	expectInt(t, c.do("HSETNX", "h", "c", "9"), 1)
	expectInt(t, c.do("HSET", "h", "text", "abc"), 1)

	expectInt(t, c.do("HINCRBY", "h", "n", "5"), 5)
	expectInt(t, c.do("HINCRBY", "h", "n", "-2"), 3)
	expectError(t, c.do("HINCRBY", "h", "text", "1"), "not an integer")

	expectInt(t, c.do("HDEL", "h", "c", "zzz"), 1)
	expectError(t, c.do("HSET", "h", "a", "1", "dangling"), "wrong number of arguments")
}

func TestHashScan(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	args := []string{"HSET", "h"}
	for i := 0; i < 25; i++ {
		args = append(args, "field:"+strconv.Itoa(i), "v")
	}
	c.do(args...)

	seen := map[string]bool{}
	cursor := "0"
	for i := 0; i < 100; i++ {
		reply := expectArray(t, c.do("HSCAN", "h", cursor, "MATCH", "field:*", "COUNT", "10"))
		cursor = string(reply[0].([]byte))
		pairs := expectArray(t, reply[1])
		if len(pairs)%2 != 0 {
			t.Fatalf("HSCAN returned an odd number of elements: %d", len(pairs))
		}
		for j := 0; j < len(pairs); j += 2 {
			seen[string(pairs[j].([]byte))] = true
		}
		if cursor == "0" {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatalf("HSCAN saw %d fields, want 25", len(seen))
	}
}

func TestHashWrongType(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "str", "value"), "OK")
	expectError(t, c.do("HGET", "str", "f"), "WRONGTYPE")
	expectError(t, c.do("HSET", "str", "f", "v"), "WRONGTYPE")
	expectError(t, c.do("HGETALL", "str"), "WRONGTYPE")

	expectInt(t, c.do("HSET", "hash", "f", "v"), 1)
	expectSimple(t, c.do("TYPE", "hash"), "hash")
	expectError(t, c.do("GET", "hash"), "WRONGTYPE")
	expectError(t, c.do("APPEND", "hash", "x"), "WRONGTYPE")
	expectError(t, c.do("INCR", "hash"), "WRONGTYPE")
	mget := expectArray(t, c.do("MGET", "hash"))
	expectNull(t, mget[0])
}

func TestHashRemovesEmptyKey(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("HSET", "h", "a", "1"), 1)
	expectInt(t, c.do("HDEL", "h", "a"), 1)
	expectInt(t, c.do("EXISTS", "h"), 0)
	expectSimple(t, c.do("TYPE", "h"), "none")
}

func TestHashPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := persistenceConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectInt(t, c.do("HSET", "h", "a", "1", "b", "2"), 2)
	expectInt(t, c.do("EXPIRE", "h", "1000"), 1)
	shutdownServer(t, first)

	second := startServer(t, cfg)
	c2 := dial(t, second)
	expectBulk(t, c2.do("HGET", "h", "a"), "1")
	expectBulk(t, c2.do("HGET", "h", "b"), "2")
	expectSimple(t, c2.do("TYPE", "h"), "hash")
	ttl := c2.do("TTL", "h").(int64)
	if ttl <= 0 || ttl > 1000 {
		t.Fatalf("TTL after reload = %d", ttl)
	}
}

func TestHGetAllRESP3IsMap(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	c.do("HELLO", "3")
	c.do("HSET", "h", "a", "1", "b", "2")

	reply := c.do("HGETALL", "h")
	value, ok := reply.(map[string]any)
	if !ok {
		t.Fatalf("RESP3 HGETALL returned %T, want map", reply)
	}
	if len(value) != 2 {
		t.Fatalf("HGETALL map has %d entries", len(value))
	}
	if got, _ := value["a"].([]byte); string(got) != "1" {
		t.Fatalf("HGETALL[a] = %q", got)
	}
}
