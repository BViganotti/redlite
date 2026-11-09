package server

import (
	"strconv"
	"testing"
)

func TestSetCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("SADD", "s", "a", "b", "a"), 2)
	expectInt(t, c.do("SCARD", "s"), 2)
	expectInt(t, c.do("SISMEMBER", "s", "a"), 1)
	expectInt(t, c.do("SISMEMBER", "s", "z"), 0)

	multi := expectArray(t, c.do("SMISMEMBER", "s", "a", "z"))
	expectInt(t, multi[0], 1)
	expectInt(t, multi[1], 0)

	members := expectArray(t, c.do("SMEMBERS", "s"))
	if len(members) != 2 {
		t.Fatalf("SMEMBERS returned %d members", len(members))
	}
	expectBulk(t, members[0], "a")
	expectBulk(t, members[1], "b")

	expectInt(t, c.do("SREM", "s", "a", "zzz"), 1)
	expectInt(t, c.do("SCARD", "s"), 1)
	expectInt(t, c.do("SREM", "s", "b"), 1)
	expectInt(t, c.do("EXISTS", "s"), 0)
}

func TestSetAlgebraCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("SADD", "a", "1", "2", "3"), 3)
	expectInt(t, c.do("SADD", "b", "2", "3", "4"), 3)

	inter := expectArray(t, c.do("SINTER", "a", "b"))
	if len(inter) != 2 {
		t.Fatalf("SINTER returned %d members", len(inter))
	}
	expectBulk(t, inter[0], "2")

	union := expectArray(t, c.do("SUNION", "a", "b"))
	if len(union) != 4 {
		t.Fatalf("SUNION returned %d members", len(union))
	}

	diff := expectArray(t, c.do("SDIFF", "a", "b"))
	if len(diff) != 1 {
		t.Fatalf("SDIFF returned %d members", len(diff))
	}
	expectBulk(t, diff[0], "1")

	if got := expectArray(t, c.do("SINTER", "a", "missing")); len(got) != 0 {
		t.Fatalf("SINTER with a missing key returned %d members", len(got))
	}
}

func TestSetPopAndRandom(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("SADD", "s", "a", "b", "c"), 3)
	one := c.do("SPOP", "s")
	if _, ok := one.([]byte); !ok {
		t.Fatalf("SPOP without count returned %T", one)
	}
	expectInt(t, c.do("SCARD", "s"), 2)

	page := expectArray(t, c.do("SPOP", "s", "10"))
	if len(page) != 2 {
		t.Fatalf("SPOP with count returned %d members", len(page))
	}
	expectNull(t, c.do("SPOP", "s"))
	empty := expectArray(t, c.do("SPOP", "s", "2"))
	if len(empty) != 0 {
		t.Fatalf("SPOP with count on a missing key returned %d members", len(empty))
	}

	expectInt(t, c.do("SADD", "r", "a", "b", "c"), 3)
	random := c.do("SRANDMEMBER", "r")
	if _, ok := random.([]byte); !ok {
		t.Fatalf("SRANDMEMBER without count returned %T", random)
	}
	unique := expectArray(t, c.do("SRANDMEMBER", "r", "10"))
	if len(unique) != 3 {
		t.Fatalf("SRANDMEMBER(10) returned %d members", len(unique))
	}
	repeated := expectArray(t, c.do("SRANDMEMBER", "r", "-10"))
	if len(repeated) != 10 {
		t.Fatalf("SRANDMEMBER(-10) returned %d members", len(repeated))
	}
	expectInt(t, c.do("SCARD", "r"), 3)
}

func TestSetScan(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	args := []string{"SADD", "s"}
	for i := 0; i < 25; i++ {
		args = append(args, "m:"+strconv.Itoa(i))
	}
	c.do(args...)

	seen := map[string]bool{}
	cursor := "0"
	for i := 0; i < 100; i++ {
		reply := expectArray(t, c.do("SSCAN", "s", cursor, "MATCH", "m:*", "COUNT", "10"))
		cursor = string(reply[0].([]byte))
		for _, member := range expectArray(t, reply[1]) {
			seen[string(member.([]byte))] = true
		}
		if cursor == "0" {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatalf("SSCAN saw %d members, want 25", len(seen))
	}
}

func TestSetWrongType(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "str", "v"), "OK")
	expectError(t, c.do("SADD", "str", "a"), "WRONGTYPE")
	expectError(t, c.do("SMEMBERS", "str"), "WRONGTYPE")
	expectError(t, c.do("SINTER", "str"), "WRONGTYPE")

	expectInt(t, c.do("SADD", "set", "a"), 1)
	expectSimple(t, c.do("TYPE", "set"), "set")
	expectError(t, c.do("GET", "set"), "WRONGTYPE")
}

func TestSetPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := persistenceConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectInt(t, c.do("SADD", "s", "a", "b", "c"), 3)
	expectInt(t, c.do("EXPIRE", "s", "1000"), 1)
	shutdownServer(t, first)

	second := startServer(t, cfg)
	c2 := dial(t, second)
	members := expectArray(t, c2.do("SMEMBERS", "s"))
	if len(members) != 3 {
		t.Fatalf("persisted set has %d members", len(members))
	}
	expectBulk(t, members[0], "a")
	expectSimple(t, c2.do("TYPE", "s"), "set")
}
