package server

import "testing"

func TestExtendedStringCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "k", "This is a string"), "OK")
	expectBulk(t, c.do("GETRANGE", "k", "0", "3"), "This")
	expectBulk(t, c.do("GETRANGE", "k", "-3", "-1"), "ing")
	expectBulk(t, c.do("GETRANGE", "k", "5", "3"), "")
	expectBulk(t, c.do("GETRANGE", "missing", "0", "-1"), "")

	expectInt(t, c.do("SETRANGE", "r", "5", "hello"), 10)
	expectBulk(t, c.do("GET", "r"), "\x00\x00\x00\x00\x00hello")
	expectInt(t, c.do("SETRANGE", "r", "0", "HELLO"), 10)
	expectBulk(t, c.do("GET", "r"), "HELLOhello")
	expectError(t, c.do("SETRANGE", "r", "-1", "x"), "offset is out of range")

	expectBulk(t, c.do("INCRBYFLOAT", "f", "10.5"), "10.5")
	expectBulk(t, c.do("INCRBYFLOAT", "f", "0.1"), "10.6")
	expectBulk(t, c.do("INCRBYFLOAT", "small", "0.1"), "0.1")
	expectBulk(t, c.do("INCRBYFLOAT", "small", "0.2"), "0.3")

	expectSimple(t, c.do("SET", "text", "abc"), "OK")
	expectError(t, c.do("INCRBYFLOAT", "text", "1"), "not a valid float")
	expectError(t, c.do("INCRBYFLOAT", "f", "nope"), "not a valid float")
}

func TestSetRangeWrongType(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("HSET", "h", "a", "1"), 1)
	expectError(t, c.do("GETRANGE", "h", "0", "-1"), "WRONGTYPE")
	expectError(t, c.do("SETRANGE", "h", "0", "x"), "WRONGTYPE")
	expectError(t, c.do("INCRBYFLOAT", "h", "1"), "WRONGTYPE")
}

func TestCommandGetKeys(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"SET", "k", "v"}, []string{"k"}},
		{[]string{"MSET", "a", "1", "b", "2"}, []string{"a", "b"}},
		{[]string{"DEL", "a", "b", "c"}, []string{"a", "b", "c"}},
		{[]string{"MGET", "a", "b"}, []string{"a", "b"}},
		{[]string{"ZADD", "z", "1", "a"}, []string{"z"}},
		{[]string{"BLPOP", "a", "b", "1"}, []string{"a", "b"}},
		{[]string{"LMOVE", "src", "dst", "LEFT", "RIGHT"}, []string{"src", "dst"}},
		{[]string{"PING"}, nil},
		{[]string{"PUBLISH", "ch", "msg"}, nil},
		{[]string{"SCAN", "0"}, nil},
	}
	for _, tc := range tests {
		reply := expectArray(t, c.do(append([]string{"COMMAND", "GETKEYS"}, tc.args...)...))
		if len(reply) != len(tc.want) {
			t.Fatalf("%v: got %d keys, want %d", tc.args, len(reply), len(tc.want))
		}
		for i, key := range tc.want {
			expectBulk(t, reply[i], key)
		}
	}

	expectError(t, c.do("COMMAND", "GETKEYS", "NOTACOMMAND"), "Invalid command")
	expectError(t, c.do("COMMAND", "GETKEYS", "SET", "k"), "Invalid number of arguments")
}
