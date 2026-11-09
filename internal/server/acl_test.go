package server

import (
	"strings"
	"testing"
)

func TestACLPerUserPermissions(t *testing.T) {
	srv := newTestServer(t, nil)
	admin := dial(t, srv)

	expectSimple(t, admin.do("SET", "cache:k", "v"), "OK")
	expectSimple(t, admin.do("SET", "other", "v"), "OK")

	expectSimple(t, admin.do("ACL", "SETUSER", "alice", "on", ">pw", "~cache:*", "+@read", "+acl"), "OK")

	c := dial(t, srv)
	expectError(t, c.do("AUTH", "alice", "wrong"), "WRONGPASS")
	expectSimple(t, c.do("AUTH", "alice", "pw"), "OK")
	expectBulk(t, c.do("ACL", "WHOAMI"), "alice")

	expectBulk(t, c.do("GET", "cache:k"), "v")
	expectError(t, c.do("GET", "other"), "NOPERM")
	expectError(t, c.do("SET", "cache:k", "x"), "NOPERM")
	expectError(t, c.do("DEL", "cache:k"), "NOPERM")
}

func TestACLChannelPermissions(t *testing.T) {
	srv := newTestServer(t, nil)
	admin := dial(t, srv)

	expectSimple(t, admin.do("ACL", "SETUSER", "bob", "on", ">pw", "&news:*", "+subscribe", "+psubscribe"), "OK")

	c := dial(t, srv)
	expectSimple(t, c.do("AUTH", "bob", "pw"), "OK")
	expectError(t, c.do("SUBSCRIBE", "other"), "NOPERM")
	expectArray(t, c.do("SUBSCRIBE", "news:1"))
}

func TestACLManagement(t *testing.T) {
	srv := newTestServer(t, nil)
	admin := dial(t, srv)

	expectSimple(t, admin.do("ACL", "SETUSER", "carol", "on", ">pw", "~*", "+@read"), "OK")

	users := expectArray(t, admin.do("ACL", "USERS"))
	seen := map[string]bool{}
	for _, user := range users {
		seen[string(user.([]byte))] = true
	}
	if !seen["default"] || !seen["carol"] {
		t.Fatalf("ACL USERS = %v", seen)
	}

	list := expectArray(t, admin.do("ACL", "LIST"))
	found := false
	for _, line := range list {
		text := string(line.([]byte))
		if strings.HasPrefix(text, "user carol ") {
			found = true
			if !strings.Contains(text, "~*") || !strings.Contains(text, "+@read") {
				t.Fatalf("ACL LIST line = %q", text)
			}
		}
	}
	if !found {
		t.Fatalf("ACL LIST missing carol: %v", list)
	}

	getUser := expectArray(t, admin.do("ACL", "GETUSER", "carol"))
	if len(getUser) < 6 {
		t.Fatalf("ACL GETUSER returned %d fields", len(getUser))
	}
	expectBulk(t, getUser[0], "flags")

	expectError(t, admin.do("ACL", "SETUSER", "dave", "+notacommand"), "Unknown command")
	expectError(t, admin.do("ACL", "SETUSER", "dave", "bogusrule"), "Syntax error")

	expectInt(t, admin.do("ACL", "DELUSER", "carol"), 1)
	expectInt(t, admin.do("ACL", "DELUSER", "carol"), 0)
	expectInt(t, admin.do("ACL", "DELUSER", "default"), 0)
}

func TestACLDisabledUser(t *testing.T) {
	srv := newTestServer(t, nil)
	admin := dial(t, srv)
	expectSimple(t, admin.do("ACL", "SETUSER", "eve", "off", ">pw", "~*", "+@all"), "OK")

	c := dial(t, srv)
	expectError(t, c.do("AUTH", "eve", "pw"), "WRONGPASS")
}

func TestACLUserKeepsWorkingAfterRequirePassChange(t *testing.T) {
	srv := newTestServer(t, nil)
	admin := dial(t, srv)
	expectSimple(t, admin.do("ACL", "SETUSER", "frank", "on", ">pw", "~*", "+@read", "+ping"), "OK")
	expectSimple(t, admin.do("CONFIG", "SET", "requirepass", "secret"), "OK")

	next := dial(t, srv)
	expectSimple(t, next.do("AUTH", "frank", "pw"), "OK")
	expectSimple(t, next.do("PING"), "PONG")
}
