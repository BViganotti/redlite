package server

import (
	"strings"
	"testing"
	"time"
)

func TestSetStoreWireCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	expectInt(t, c.do("SADD", "a", "1", "2"), 2)
	expectInt(t, c.do("SADD", "b", "2", "3"), 2)

	expectInt(t, c.do("SINTERSTORE", "i", "a", "b"), 1)
	members := expectArray(t, c.do("SMEMBERS", "i"))
	if len(members) != 1 {
		t.Fatalf("SMEMBERS = %v", members)
	}
	expectInt(t, c.do("SUNIONSTORE", "u", "a", "b"), 3)
	expectInt(t, c.do("SDIFFSTORE", "d", "a", "b"), 1)
	expectInt(t, c.do("SINTERCARD", "2", "a", "b"), 1)
	expectInt(t, c.do("SINTERCARD", "2", "a", "b", "LIMIT", "1"), 1)
	expectInt(t, c.do("SMOVE", "a", "b", "1"), 1)
	expectInt(t, c.do("SMOVE", "a", "b", "missing"), 0)
	expectError(t, c.do("SINTERCARD", "0", "a"), "numkeys should be greater than 0")
	expectInt(t, c.do("SINTERSTORE", "x", "a", "nope"), 0)

	expectSimple(t, c.do("SET", "str", "v"), "OK")
	expectError(t, c.do("SINTERSTORE", "x", "str"), "WRONGTYPE")
	expectError(t, c.do("SMOVE", "a", "str", "2"), "WRONGTYPE")
}

func TestLMPopWireAndBlocking(t *testing.T) {
	srv := newTestServer(t, nil)
	c := dial(t, srv)

	expectInt(t, c.do("RPUSH", "l", "a", "b", "c"), 3)
	reply := expectArray(t, c.do("LMPOP", "1", "l", "LEFT", "COUNT", "2"))
	expectBulk(t, reply[0], "l")
	values := expectArray(t, reply[1])
	if len(values) != 2 {
		t.Fatalf("LMPOP values = %v", values)
	}
	expectBulk(t, values[0], "a")
	expectBulk(t, values[1], "b")
	expectNull(t, c.do("LMPOP", "2", "no", "none", "RIGHT"))
	expectError(t, c.do("LMPOP", "1", "l", "BAD"), "syntax error")
	expectError(t, c.do("LMPOP", "1", "l", "LEFT", "COUNT", "0"), "count should be greater than 0")

	blocked := dialAsync(t, srv)
	replies := make(chan any, 1)
	errs := make(chan error, 1)
	go func() {
		value, err := blocked.call("BLMPOP", "5", "1", "queue", "LEFT", "COUNT", "2")
		if err != nil {
			errs <- err
			return
		}
		replies <- value
	}()

	time.Sleep(30 * time.Millisecond)
	expectInt(t, c.do("RPUSH", "queue", "x", "y", "z"), 3)

	select {
	case err := <-errs:
		t.Fatalf("BLMPOP error: %v", err)
	case value := <-replies:
		reply := expectArray(t, value)
		expectBulk(t, reply[0], "queue")
		values := expectArray(t, reply[1])
		if len(values) != 2 {
			t.Fatalf("BLMPOP values = %v", values)
		}
		expectBulk(t, values[0], "x")
		expectBulk(t, values[1], "y")
	case <-time.After(3 * time.Second):
		t.Fatal("BLMPOP did not wake")
	}

	expectNull(t, c.do("BLMPOP", "0.01", "1", "empty", "LEFT"))
}

func TestZSetLexAndMPopWire(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	for _, member := range []string{"a", "b", "c", "d"} {
		expectInt(t, c.do("ZADD", "z", "0", member), 1)
	}

	items := expectArray(t, c.do("ZRANGE", "z", "[b", "[c", "BYLEX"))
	if len(items) != 2 {
		t.Fatalf("BYLEX = %v", items)
	}
	expectBulk(t, items[0], "b")
	expectBulk(t, items[1], "c")

	reverse := expectArray(t, c.do("ZREVRANGEBYLEX", "z", "+", "-"))
	expectBulk(t, reverse[0], "d")
	expectInt(t, c.do("ZLEXCOUNT", "z", "-", "+"), 4)
	expectInt(t, c.do("ZREMRANGEBYLEX", "z", "[a", "[b"), 2)
	expectInt(t, c.do("ZADD", "z", "1", "a", "2", "b", "3", "c"), 2)
	expectInt(t, c.do("DEL", "z", "dst"), 1)
	expectInt(t, c.do("ZADD", "z", "1", "a", "2", "b", "3", "c"), 3)

	expectInt(t, c.do("ZRANGESTORE", "dst", "z", "2", "3", "BYSCORE"), 2)
	expectInt(t, c.do("ZINTERCARD", "2", "z", "dst"), 2)
	expectError(t, c.do("ZRANGE", "z", "0", "-1", "LIMIT", "0", "1"), "LIMIT")
	expectError(t, c.do("ZRANGE", "z", "0", "-1", "BYLEX", "WITHSCORES"), "WITHSCORES")

	reply := expectArray(t, c.do("ZMPOP", "1", "z", "MIN", "COUNT", "2"))
	expectBulk(t, reply[0], "z")
	pairs := expectArray(t, reply[1])
	if len(pairs) != 2 {
		t.Fatalf("ZMPOP pairs = %v", pairs)
	}
	pair := expectArray(t, pairs[0])
	expectBulk(t, pair[0], "a")
	expectBulk(t, pair[1], "1")

	rest := expectArray(t, c.do("ZMPOP", "1", "z", "MIN", "COUNT", "100"))
	expectBulk(t, rest[0], "z")
	expectNull(t, c.do("ZMPOP", "1", "z", "MIN"))
	expectError(t, c.do("ZMPOP", "0", "z", "MIN"), "numkeys")
	expectError(t, c.do("ZMPOP", "1", "z", "BAD"), "syntax error")

	expectInt(t, c.do("ZADD", "q", "1", "only"), 1)
	blocked := expectArray(t, c.do("BZMPOP", "0.01", "1", "q", "MIN"))
	if len(blocked) != 2 {
		t.Fatalf("BZMPOP = %v", blocked)
	}
}

func TestBitFieldWire(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	reply := expectArray(t, c.do("BITFIELD", "bf", "SET", "u8", "0", "255", "GET", "u8", "0", "INCRBY", "u8", "0", "10"))
	want := []int64{0, 255, 9}
	for i, value := range want {
		expectInt(t, reply[i], value)
	}
	expectBulk(t, c.do("GET", "bf"), "\x09")

	expectArray(t, c.do("BITFIELD", "bf", "SET", "u8", "0", "250"))
	reply = expectArray(t, c.do("BITFIELD", "bf", "OVERFLOW", "SAT", "INCRBY", "u8", "0", "10"))
	expectInt(t, reply[0], 255)
	reply = expectArray(t, c.do("BITFIELD", "bf", "OVERFLOW", "FAIL", "INCRBY", "u8", "0", "10"))
	expectNull(t, reply[0])

	reply = expectArray(t, c.do("BITFIELD", "signed", "SET", "i8", "0", "-5", "GET", "i8", "0"))
	expectInt(t, reply[1], -5)
	reply = expectArray(t, c.do("BITFIELD", "wide", "SET", "i64", "0", "-5", "GET", "i64", "0"))
	expectInt(t, reply[1], -5)

	expectError(t, c.do("BITFIELD", "bf", "GET", "u64", "0"), "Invalid bitfield type")
	expectError(t, c.do("BITFIELD", "bf", "GET", "u8", "-1"), "bit offset")
	expectError(t, c.do("BITFIELD_RO", "bf", "SET", "u8", "0", "1"), "only supports the GET")
	reply = expectArray(t, c.do("BITFIELD_RO", "bf", "GET", "u8", "0"))
	expectInt(t, reply[0], 255)

	expectInt(t, c.do("RPUSH", "list", "x"), 1)
	expectError(t, c.do("BITFIELD", "list", "GET", "u8", "0"), "WRONGTYPE")
}

func TestLCSWire(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	expectSimple(t, c.do("SET", "a", "ohmytext"), "OK")
	expectSimple(t, c.do("SET", "b", "mynewtext"), "OK")

	expectBulk(t, c.do("LCS", "a", "b"), "mytext")
	expectInt(t, c.do("LCS", "a", "b", "LEN"), 6)

	reply := expectArray(t, c.do("LCS", "a", "b", "IDX"))
	// RESP2 flattens the map: matches, [matches...], len, 6.
	if len(reply) < 4 {
		t.Fatalf("LCS IDX = %v", reply)
	}
	expectBulk(t, reply[0], "matches")
	matches := expectArray(t, reply[1])
	if len(matches) != 2 {
		t.Fatalf("matches = %v", matches)
	}
	first := expectArray(t, matches[0])
	aRange := expectArray(t, first[0])
	expectInt(t, aRange[0], 4)
	expectInt(t, aRange[1], 7)

	reply = expectArray(t, c.do("LCS", "a", "b", "IDX", "MINMATCHLEN", "4"))
	matches = expectArray(t, reply[1])
	if len(matches) != 1 {
		t.Fatalf("minmatchlen matches = %v", matches)
	}
	expectError(t, c.do("LCS", "a", "b", "LEN", "IDX"), "please just use IDX")
	expectBulk(t, c.do("SUBSTR", "a", "0", "1"), "oh")
}

func TestSetConditionWire(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	expectSimple(t, c.do("SET", "k", "v"), "OK")
	expectSimple(t, c.do("SET", "k", "x", "IFEQ", "v"), "OK")
	expectNull(t, c.do("SET", "k", "y", "IFEQ", "v"))
	expectBulk(t, c.do("GET", "k"), "x")
	expectNull(t, c.do("SET", "k", "y", "IFNE", "x"))
	expectSimple(t, c.do("SET", "k", "y", "IFNE", "nope"), "OK")
	expectNull(t, c.do("SET", "k", "z", "IFDEQ", "2225df3932692638"))
	expectError(t, c.do("SET", "k", "z", "IFDEQ", "abc"), "exactly 16 hexadecimal")

	expectSimple(t, c.do("SET", "miss", "x", "IFNE", "v"), "OK")
	expectNull(t, c.do("SET", "miss2", "x", "IFEQ", "v"))
	expectError(t, c.do("SET", "k", "z", "NX", "IFEQ", "x"), "syntax error")

	expectInt(t, c.do("RPUSH", "list", "a"), 1)
	expectError(t, c.do("SET", "list", "x", "IFEQ", "v"), "WRONGTYPE")
}

func TestSortWire(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	expectInt(t, c.do("RPUSH", "l", "3", "1", "2"), 3)
	values := expectArray(t, c.do("SORT", "l"))
	expectBulk(t, values[0], "1")
	expectBulk(t, values[2], "3")

	values = expectArray(t, c.do("SORT", "l", "DESC", "LIMIT", "0", "2"))
	expectBulk(t, values[0], "3")
	expectBulk(t, values[1], "2")

	expectInt(t, c.do("SORT", "l", "STORE", "dst"), 3)
	values = expectArray(t, c.do("LRANGE", "dst", "0", "-1"))
	expectBulk(t, values[0], "1")

	expectSimple(t, c.do("MSET", "w_1", "10", "w_2", "5", "w_3", "1"), "OK")
	expectSimple(t, c.do("MSET", "d_1", "a", "d_2", "b", "d_3", "c"), "OK")
	values = expectArray(t, c.do("SORT", "l", "BY", "w_*", "GET", "#", "GET", "d_*"))
	if len(values) != 6 {
		t.Fatalf("BY/GET = %v", values)
	}
	expectBulk(t, values[0], "3")
	expectBulk(t, values[1], "c")

	expectError(t, c.do("SORT", "l", "STORE", "dst", "ALPHA", "BAD"), "syntax error")
	expectError(t, c.do("SORT_RO", "l", "STORE", "dst"), "syntax error")
	expectSimple(t, c.do("SET", "str", "v"), "OK")
	expectError(t, c.do("SORT", "str"), "WRONGTYPE")
}

func TestDumpRestoreWire(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	expectSimple(t, c.do("SET", "src", "hello"), "OK")
	expectInt(t, c.do("RPUSH", "list", "a", "b"), 2)

	payload := c.do("DUMP", "src")
	data, ok := payload.([]byte)
	if !ok || len(data) == 0 {
		t.Fatalf("DUMP = %v", payload)
	}
	expectSimple(t, c.do("RESTORE", "dst", "0", string(data)), "OK")
	expectBulk(t, c.do("GET", "dst"), "hello")
	expectError(t, c.do("RESTORE", "dst", "0", string(data)), "BUSYKEY")
	expectSimple(t, c.do("RESTORE", "dst", "0", string(data), "REPLACE"), "OK")
	expectError(t, c.do("RESTORE", "bad", "0", "garbage"), "DUMP payload")

	listPayload := c.do("DUMP", "list").([]byte)
	expectSimple(t, c.do("RESTORE", "list2", "0", string(listPayload)), "OK")
	values := expectArray(t, c.do("LRANGE", "list2", "0", "-1"))
	expectBulk(t, values[0], "a")
	expectBulk(t, values[1], "b")

	expectNull(t, c.do("DUMP", "missing"))
	expectError(t, c.do("RESTORE", "x", "-1", string(data)), "Invalid TTL")
	expectError(t, c.do("RESTORE", "x", "0", string(data), "BOGUS"), "syntax error")
}

func TestShardedPubSub(t *testing.T) {
	srv := newTestServer(t, nil)
	sub := dial(t, srv)
	pub := dial(t, srv)

	ack := expectArray(t, sub.do("SSUBSCRIBE", "shard"))
	expectBulk(t, ack[0], "ssubscribe")
	expectBulk(t, ack[1], "shard")
	expectInt(t, ack[2], 1)

	expectInt(t, pub.do("SPUBLISH", "shard", "hello"), 1)
	message := expectArray(t, sub.readFrame())
	expectBulk(t, message[0], "smessage")
	expectBulk(t, message[1], "shard")
	expectBulk(t, message[2], "hello")

	channels := expectArray(t, pub.do("PUBSUB", "SHARDCHANNELS"))
	if len(channels) != 1 {
		t.Fatalf("SHARDCHANNELS = %v", channels)
	}
	nums := expectArray(t, pub.do("PUBSUB", "SHARDNUMSUB", "shard", "other"))
	expectBulk(t, nums[0], "shard")
	expectInt(t, nums[1], 1)
	expectInt(t, nums[3], 0)

	// Pattern subscriptions do not receive shard messages.
	expectArray(t, pub.do("PSUBSCRIBE", "sha*"))
	expectInt(t, pub.do("SPUBLISH", "shard", "again"), 1)
	expectArray(t, sub.readFrame())

	ack = expectArray(t, sub.do("SUNSUBSCRIBE", "shard"))
	expectBulk(t, ack[0], "sunsubscribe")
	expectInt(t, ack[2], 0)
}

func TestSlowlogLatencyAndDebug(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("CONFIG", "SET", "slowlog-log-slower-than", "0"), "OK")
	expectSimple(t, c.do("SET", "k", "v"), "OK")
	expectBulk(t, c.do("GET", "k"), "v")

	entries := expectArray(t, c.do("SLOWLOG", "GET", "10"))
	if len(entries) == 0 {
		t.Fatal("slow log is empty")
	}
	entry := expectArray(t, entries[0])
	if len(entry) < 6 {
		t.Fatalf("slowlog entry = %v", entry)
	}
	callArgs := expectArray(t, entry[3])
	expectBulk(t, callArgs[0], "GET")
	expectSimple(t, c.do("SLOWLOG", "RESET"), "OK")
	expectInt(t, c.do("SLOWLOG", "LEN"), 0)

	expectSimple(t, c.do("CONFIG", "SET", "latency-monitor-threshold", "1"), "OK")
	expectSimple(t, c.do("DEBUG", "SLEEP", "0.01"), "OK")
	latest := expectArray(t, c.do("LATENCY", "LATEST"))
	if len(latest) == 0 {
		t.Fatal("latency event was not recorded")
	}
	event := expectArray(t, latest[0])
	expectBulk(t, event[0], "command")

	expectSimple(t, c.do("DEBUG", "SET-ACTIVE-EXPIRE", "0"), "OK")
	expectSimple(t, c.do("DEBUG", "SET-ACTIVE-EXPIRE", "1"), "OK")
	expectSimple(t, c.do("DEBUG", "JMAP"), "OK")
	expectSimple(t, c.do("DEBUG", "CHANGE-REPL-ID"), "OK")
	expectSimple(t, c.do("SET", "obj", "value"), "OK")
	info := c.do("DEBUG", "OBJECT", "obj")
	if _, ok := info.([]byte); !ok {
		t.Fatalf("DEBUG OBJECT = %v", info)
	}
	expectError(t, c.do("DEBUG", "OBJECT", "missing"), "no such key")

	modules := expectArray(t, c.do("MODULE", "LIST"))
	if len(modules) != 0 {
		t.Fatalf("MODULE LIST = %v", modules)
	}
	names := expectArray(t, c.do("COMMAND", "LIST", "FILTERBY", "PATTERN", "z*"))
	if len(names) == 0 {
		t.Fatal("COMMAND LIST FILTERBY PATTERN returned nothing")
	}
	docs := expectArray(t, c.do("COMMAND", "DOCS", "GET"))
	if len(docs) == 0 {
		t.Fatal("COMMAND DOCS returned nothing")
	}
}

func TestClientKillAndWaitAOF(t *testing.T) {
	srv := newTestServer(t, nil)
	victim := dial(t, srv)
	admin := dial(t, srv)

	id := expectIntValue(t, victim.do("CLIENT", "ID"))
	expectInt(t, admin.do("CLIENT", "KILL", "ID", "999999"), 0)
	expectError(t, admin.do("CLIENT", "KILL", "BOGUS", "x"), "syntax error")
	expectInt(t, admin.do("CLIENT", "KILL", "ID", itoa(id)), 1)

	wait := expectArray(t, admin.do("WAITAOF", "0", "0", "0"))
	expectInt(t, wait[0], 0)
	expectInt(t, wait[1], 0)
	expectError(t, admin.do("WAITAOF", "1", "0", "0"), "appendonly")
	expectError(t, admin.do("WAITAOF", "2", "0", "0"), "out of range")
}

func TestNewCommandsAOFReplay(t *testing.T) {
	dir := t.TempDir()
	cfg := persistenceConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectInt(t, c.do("SADD", "s1", "a", "b"), 2)
	expectInt(t, c.do("SADD", "s2", "c"), 1)
	expectInt(t, c.do("SMOVE", "s1", "s2", "a"), 1)
	expectInt(t, c.do("SINTERSTORE", "si", "s1", "s2"), 0)
	expectInt(t, c.do("RPUSH", "l", "a", "b", "c"), 3)
	expectArray(t, c.do("LMPOP", "1", "l", "LEFT", "COUNT", "2"))
	expectInt(t, c.do("ZADD", "z", "1", "a", "2", "b", "3", "c"), 3)
	expectInt(t, c.do("ZRANGESTORE", "zs", "z", "2", "3", "BYSCORE"), 2)
	expectArray(t, c.do("ZMPOP", "1", "z", "MAX", "COUNT", "2"))
	expectArray(t, c.do("BITFIELD", "bf", "SET", "u8", "0", "255", "INCRBY", "u8", "0", "10"))
	expectSimple(t, c.do("SET", "cond", "v"), "OK")
	expectSimple(t, c.do("SET", "cond", "w", "IFEQ", "v"), "OK")
	expectInt(t, c.do("SORT", "l", "ALPHA", "STORE", "sorted"), 1)
	expectInt(t, c.do("SADD", "spop", "a", "b", "c"), 3)
	expectArray(t, c.do("SPOP", "spop", "2"))

	payload := c.do("DUMP", "zs").([]byte)
	expectSimple(t, c.do("RESTORE", "zscopy", "0", string(payload)), "OK")
	shutdownServer(t, first)

	second := startServer(t, cfg)
	c2 := dial(t, second)
	expectInt(t, c2.do("SCARD", "s2"), 2)
	expectInt(t, c2.do("SCARD", "si"), 0)
	values := expectArray(t, c2.do("LRANGE", "l", "0", "-1"))
	expectBulk(t, values[0], "c")
	expectBulk(t, c2.do("LINDEX", "l", "0"), "c")
	expectInt(t, c2.do("ZCARD", "z"), 1)
	expectInt(t, c2.do("ZCARD", "zs"), 2)
	expectInt(t, c2.do("ZCARD", "zscopy"), 2)
	expectBulk(t, c2.do("GET", "cond"), "w")
	expectInt(t, c2.do("STRLEN", "bf"), 1)
	expectInt(t, c2.do("LLEN", "sorted"), 1)
	expectInt(t, c2.do("SCARD", "spop"), 1)
}

func TestInfoSectionsAndCommandList(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	def := c.do("INFO")
	text, ok := def.([]byte)
	if !ok {
		t.Fatalf("INFO = %v", def)
	}
	if !strings.Contains(string(text), "# Server") {
		t.Fatalf("INFO is missing the server section")
	}
	if strings.Contains(string(text), "cmdstat_") {
		t.Fatalf("default INFO should not include commandstats")
	}

	all := c.do("INFO", "all").([]byte)
	if !strings.Contains(string(all), "cmdstat_") {
		t.Fatalf("INFO all is missing commandstats")
	}

	names := expectArray(t, c.do("COMMAND", "LIST"))
	foundGet := false
	for _, raw := range names {
		name := string(raw.([]byte))
		if name != strings.ToLower(name) {
			t.Fatalf("COMMAND LIST returned a non-lowercase name %q", name)
		}
		if name == "get" {
			foundGet = true
		}
	}
	if !foundGet {
		t.Fatal("COMMAND LIST is missing get")
	}

	stringCommands := expectArray(t, c.do("COMMAND", "LIST", "FILTERBY", "ACLCAT", "string"))
	expectBulk(t, stringCommands[0], "append")
	empty := expectArray(t, c.do("COMMAND", "LIST", "FILTERBY", "MODULE", "nothing"))
	if len(empty) != 0 {
		t.Fatalf("module filter = %v", empty)
	}
}

func TestClientReplyModes(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("CLIENT", "REPLY", "ON"), "OK")

	c.send("CLIENT", "REPLY", "OFF")
	c.send("PING")
	_ = c.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := readValue(c.rd); err == nil {
		t.Fatal("CLIENT REPLY OFF did not suppress the reply")
	}
	_ = c.conn.SetReadDeadline(time.Time{})

	c.send("CLIENT", "REPLY", "ON")
	expectSimple(t, mustRead(t, c), "OK")
	c.send("PING")
	expectSimple(t, mustRead(t, c), "PONG")

	c.send("CLIENT", "REPLY", "SKIP")
	c.send("PING")
	c.send("PING")
	expectSimple(t, mustRead(t, c), "PONG")
	_ = c.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := readValue(c.rd); err == nil {
		t.Fatal("CLIENT REPLY SKIP suppressed only one reply")
	}
	_ = c.conn.SetReadDeadline(time.Time{})
}

func mustRead(t *testing.T, c *testConn) any {
	t.Helper()
	value, err := readValue(c.rd)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return value
}

func TestMonitorFeed(t *testing.T) {
	srv := newTestServer(t, nil)
	monitor := dial(t, srv)
	other := dial(t, srv)

	expectSimple(t, monitor.do("MONITOR"), "OK")
	expectSimple(t, other.do("SET", "mon", "value"), "OK")

	line, ok := mustRead(t, monitor).(string)
	if !ok || !strings.Contains(line, `"SET" "mon" "value"`) {
		t.Fatalf("monitor line = %v", line)
	}
	if !strings.Contains(line, "[0 ") {
		t.Fatalf("monitor line missing db/addr: %q", line)
	}

	// A monitor's own commands are not echoed back to it.
	expectSimple(t, monitor.do("PING"), "PONG")
	_ = monitor.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := readValue(monitor.rd); err == nil {
		t.Fatal("monitor received its own command")
	}
	_ = monitor.conn.SetReadDeadline(time.Time{})
}

func expectIntValue(t *testing.T, value any) int64 {
	t.Helper()
	n, ok := value.(int64)
	if !ok {
		t.Fatalf("got %v (%T), want integer", value, value)
	}
	return n
}

func itoa(n int64) string {
	return strconvFormatInt(n)
}

func strconvFormatInt(n int64) string {
	// Kept separate so the test does not import strconv just for one call.
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
