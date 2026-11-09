package server

import (
	"testing"
)

func TestListCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("RPUSH", "l", "a", "b", "c"), 3)
	expectInt(t, c.do("LPUSH", "l", "z"), 4)

	values := expectArray(t, c.do("LRANGE", "l", "0", "-1"))
	if len(values) != 4 {
		t.Fatalf("LRANGE returned %d values", len(values))
	}
	expectBulk(t, values[0], "z")
	expectBulk(t, values[1], "a")
	expectBulk(t, values[3], "c")

	expectInt(t, c.do("LLEN", "l"), 4)
	expectBulk(t, c.do("LINDEX", "l", "-1"), "c")
	expectNull(t, c.do("LINDEX", "l", "99"))

	rangePage := expectArray(t, c.do("LRANGE", "l", "-2", "-1"))
	if len(rangePage) != 2 {
		t.Fatalf("LRANGE negative returned %d values", len(rangePage))
	}
	expectBulk(t, rangePage[0], "b")

	expectSimple(t, c.do("LSET", "l", "0", "Z"), "OK")
	expectBulk(t, c.do("LINDEX", "l", "0"), "Z")
	expectError(t, c.do("LSET", "l", "99", "x"), "index out of range")
	expectError(t, c.do("LSET", "missing", "0", "x"), "no such key")
}

func TestListPopShapes(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("RPUSH", "l", "a", "b", "c"), 3)
	expectBulk(t, c.do("LPOP", "l"), "a")
	expectBulk(t, c.do("RPOP", "l"), "c")

	page := expectArray(t, c.do("LPOP", "l", "10"))
	if len(page) != 1 {
		t.Fatalf("LPOP with count returned %d elements", len(page))
	}
	expectBulk(t, page[0], "b")

	expectNull(t, c.do("LPOP", "l"))
	empty := expectArray(t, c.do("LPOP", "l", "2"))
	if len(empty) != 0 {
		t.Fatalf("LPOP with count on a missing key returned %d elements", len(empty))
	}
	expectError(t, c.do("LPOP", "l", "-1"), "must be positive")
}

func TestListTrimAndRem(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("RPUSH", "l", "a", "b", "c", "d"), 4)
	expectSimple(t, c.do("LTRIM", "l", "1", "2"), "OK")
	values := expectArray(t, c.do("LRANGE", "l", "0", "-1"))
	if len(values) != 2 {
		t.Fatalf("LTRIM left %d values", len(values))
	}
	expectBulk(t, values[0], "b")

	expectInt(t, c.do("RPUSH", "l", "x", "y", "x"), 5)
	expectInt(t, c.do("LREM", "l", "0", "x"), 2)
	values = expectArray(t, c.do("LRANGE", "l", "0", "-1"))
	if len(values) != 3 {
		t.Fatalf("LREM left %d values", len(values))
	}
}

func TestListWrongType(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "str", "v"), "OK")
	expectError(t, c.do("LPUSH", "str", "x"), "WRONGTYPE")
	expectError(t, c.do("LRANGE", "str", "0", "-1"), "WRONGTYPE")

	expectInt(t, c.do("RPUSH", "list", "a"), 1)
	expectSimple(t, c.do("TYPE", "list"), "list")
	expectError(t, c.do("GET", "list"), "WRONGTYPE")
}

func TestListPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := persistenceConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectInt(t, c.do("RPUSH", "l", "a", "b", "c"), 3)
	expectInt(t, c.do("EXPIRE", "l", "1000"), 1)
	shutdownServer(t, first)

	second := startServer(t, cfg)
	c2 := dial(t, second)
	values := expectArray(t, c2.do("LRANGE", "l", "0", "-1"))
	if len(values) != 3 {
		t.Fatalf("persisted list has %d values", len(values))
	}
	expectBulk(t, values[0], "a")
	expectBulk(t, values[2], "c")
	expectSimple(t, c2.do("TYPE", "l"), "list")
}
