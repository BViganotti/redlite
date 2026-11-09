package server

import (
	"strconv"
	"testing"
)

func TestZSetCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("ZADD", "z", "1", "a", "2", "b", "3", "c"), 3)
	expectInt(t, c.do("ZCARD", "z"), 3)
	expectBulk(t, c.do("ZSCORE", "z", "b"), "2")
	expectNull(t, c.do("ZSCORE", "z", "missing"))

	scores := expectArray(t, c.do("ZMSCORE", "z", "a", "missing"))
	expectBulk(t, scores[0], "1")
	expectNull(t, scores[1])

	expectInt(t, c.do("ZADD", "z", "CH", "5", "b"), 1)
	expectBulk(t, c.do("ZSCORE", "z", "b"), "5")
	expectInt(t, c.do("ZADD", "z", "NX", "9", "b"), 0)
	expectBulk(t, c.do("ZSCORE", "z", "b"), "5")
	expectInt(t, c.do("ZADD", "z", "XX", "4", "d"), 0)
	expectInt(t, c.do("EXISTS", "d"), 0)

	expectBulk(t, c.do("ZADD", "z", "INCR", "2", "a"), "3")
	expectBulk(t, c.do("ZINCRBY", "z", "-1", "a"), "2")

	expectInt(t, c.do("ZCOUNT", "z", "2", "5"), 3)
	expectInt(t, c.do("ZCOUNT", "z", "(2", "5"), 2)
	expectInt(t, c.do("ZCOUNT", "z", "-inf", "+inf"), 3)

	expectInt(t, c.do("ZREM", "z", "a", "missing"), 1)
	expectInt(t, c.do("ZCARD", "z"), 2)
}

func TestZRangeCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	c.do("ZADD", "z", "1", "a", "2", "b", "3", "c")

	all := expectArray(t, c.do("ZRANGE", "z", "0", "-1"))
	if len(all) != 3 {
		t.Fatalf("ZRANGE returned %d members", len(all))
	}
	expectBulk(t, all[0], "a")
	expectBulk(t, all[2], "c")

	withScores := expectArray(t, c.do("ZRANGE", "z", "0", "-1", "WITHSCORES"))
	if len(withScores) != 6 {
		t.Fatalf("ZRANGE WITHSCORES returned %d elements", len(withScores))
	}
	expectBulk(t, withScores[0], "a")
	expectBulk(t, withScores[1], "1")

	reverse := expectArray(t, c.do("ZREVRANGE", "z", "0", "1"))
	expectBulk(t, reverse[0], "c")
	expectBulk(t, reverse[1], "b")

	byScore := expectArray(t, c.do("ZRANGEBYSCORE", "z", "2", "3"))
	if len(byScore) != 2 {
		t.Fatalf("ZRANGEBYSCORE returned %d members", len(byScore))
	}
	expectBulk(t, byScore[0], "b")

	limited := expectArray(t, c.do("ZRANGEBYSCORE", "z", "-inf", "+inf", "LIMIT", "1", "1"))
	if len(limited) != 1 {
		t.Fatalf("LIMIT returned %d members", len(limited))
	}
	expectBulk(t, limited[0], "b")

	revScore := expectArray(t, c.do("ZREVRANGEBYSCORE", "z", "+inf", "-inf", "LIMIT", "0", "2"))
	expectBulk(t, revScore[0], "c")

	expectInt(t, c.do("ZRANK", "z", "b"), 1)
	expectInt(t, c.do("ZREVRANK", "z", "b"), 1)
	expectNull(t, c.do("ZRANK", "z", "missing"))
}

func TestZRemRangeCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	c.do("ZADD", "z", "1", "a", "2", "b", "3", "c", "4", "d")

	expectInt(t, c.do("ZREMRANGEBYRANK", "z", "0", "0"), 1)
	expectInt(t, c.do("ZREMRANGEBYSCORE", "z", "3", "4"), 2)
	expectInt(t, c.do("ZCARD", "z"), 1)

	remaining := expectArray(t, c.do("ZRANGE", "z", "0", "-1", "WITHSCORES"))
	if len(remaining) != 2 {
		t.Fatalf("remaining range has %d elements", len(remaining))
	}
	expectBulk(t, remaining[0], "b")
	expectBulk(t, remaining[1], "2")

	expectInt(t, c.do("ZREM", "z", "b"), 1)
	expectInt(t, c.do("EXISTS", "z"), 0)
}

func TestZScanCommand(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	args := []string{"ZADD", "z"}
	for i := 0; i < 25; i++ {
		args = append(args, strconv.Itoa(i), "m:"+strconv.Itoa(i))
	}
	c.do(args...)

	seen := map[string]bool{}
	cursor := "0"
	for i := 0; i < 100; i++ {
		reply := expectArray(t, c.do("ZSCAN", "z", cursor, "MATCH", "m:*", "COUNT", "10"))
		cursor = string(reply[0].([]byte))
		pairs := expectArray(t, reply[1])
		for j := 0; j < len(pairs); j += 2 {
			seen[string(pairs[j].([]byte))] = true
		}
		if cursor == "0" {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatalf("ZSCAN saw %d members, want 25", len(seen))
	}
}

func TestZSetWrongType(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "str", "v"), "OK")
	expectError(t, c.do("ZADD", "str", "1", "a"), "WRONGTYPE")
	expectError(t, c.do("ZRANGE", "str", "0", "-1"), "WRONGTYPE")

	expectInt(t, c.do("ZADD", "z", "1", "a"), 1)
	expectSimple(t, c.do("TYPE", "z"), "zset")
	expectError(t, c.do("GET", "z"), "WRONGTYPE")
}

func TestZSetErrors(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectError(t, c.do("ZADD", "z", "notafloat", "a"), "not a valid float")
	expectError(t, c.do("ZADD", "z", "1", "a", "2"), "syntax error")
	expectError(t, c.do("ZADD", "z", "NX", "XX", "1", "a"), "not compatible")
	expectError(t, c.do("ZADD", "z", "NX", "GT", "1", "a"), "not compatible")
	expectError(t, c.do("ZADD", "z", "GT", "LT", "1", "a"), "not compatible")
	expectError(t, c.do("ZADD", "z", "INCR", "1", "a", "2", "b"), "single increment-element pair")
	expectError(t, c.do("ZRANGEBYSCORE", "z", "bad", "2"), "not a float")
	expectError(t, c.do("ZRANGEBYSCORE", "z", "1", "2", "LIMIT", "-1", "1"), "offset is out of range")
}

func TestZSetPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := persistenceConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectInt(t, c.do("ZADD", "z", "1", "a", "2.5", "b"), 2)
	expectInt(t, c.do("EXPIRE", "z", "1000"), 1)
	shutdownServer(t, first)

	second := startServer(t, cfg)
	c2 := dial(t, second)
	expectBulk(t, c2.do("ZSCORE", "z", "b"), "2.5")
	expectSimple(t, c2.do("TYPE", "z"), "zset")
	order := expectArray(t, c2.do("ZRANGE", "z", "0", "-1"))
	expectBulk(t, order[0], "a")
	expectBulk(t, order[1], "b")
}

func TestZSetRESP3WithScores(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	c.do("HELLO", "3")
	c.do("ZADD", "z", "1", "a", "2", "b")

	reply := expectArray(t, c.do("ZRANGE", "z", "0", "-1", "WITHSCORES"))
	if len(reply) != 2 {
		t.Fatalf("RESP3 WITHSCORES returned %d pairs", len(reply))
	}
	pair := expectArray(t, reply[1])
	if len(pair) != 2 {
		t.Fatalf("RESP3 pair has %d elements", len(pair))
	}
	expectBulk(t, pair[0], "b")
	expectBulk(t, pair[1], "2")
}
