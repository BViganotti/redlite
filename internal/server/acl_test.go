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

func TestACLSelectorsAndDryRun(t *testing.T) {
	srv := newTestServer(t, nil)
	admin := dial(t, srv)

	expectSimple(t, admin.do("SET", "a1", "value"), "OK")
	expectSimple(t, admin.do("ACL", "SETUSER", "sel", "on", ">pw",
		"~a*", "+@read", "(+set ~b* +get)"), "OK")

	expectSimple(t, admin.do("ACL", "DRYRUN", "sel", "GET", "a1"), "OK")
	expectSimple(t, admin.do("ACL", "DRYRUN", "sel", "SET", "b1", "v"), "OK")
	expectBulk(t, admin.do("ACL", "DRYRUN", "sel", "SET", "a1", "v"),
		"User sel has no permissions to access the 'a1' key")
	expectBulk(t, admin.do("ACL", "DRYRUN", "sel", "DEL", "a1"),
		"User sel has no permissions to run the 'del' command")
	expectError(t, admin.do("ACL", "DRYRUN", "missing", "GET", "x"), "not found")
	expectError(t, admin.do("ACL", "DRYRUN", "sel", "GET"), "wrong number of arguments")

	c := dial(t, srv)
	expectSimple(t, c.do("AUTH", "sel", "pw"), "OK")
	expectBulk(t, c.do("GET", "a1"), "value")
	expectSimple(t, c.do("SET", "b1", "v"), "OK")
	expectError(t, c.do("SET", "a1", "v"), "NOPERM")

	line := ""
	for _, raw := range expectArray(t, admin.do("ACL", "LIST")) {
		text := string(raw.([]byte))
		if strings.HasPrefix(text, "user sel ") {
			line = text
		}
	}
	if !strings.Contains(line, "(~b* resetchannels -@all +get +set)") {
		t.Fatalf("ACL LIST selector = %q", line)
	}

	getUser := expectArray(t, admin.do("ACL", "GETUSER", "sel"))
	if len(getUser) < 12 {
		t.Fatalf("ACL GETUSER = %v", getUser)
	}
	if string(getUser[10].([]byte)) != "selectors" {
		t.Fatalf("ACL GETUSER missing selectors: %v", getUser)
	}
}

func TestACLLogAndCat(t *testing.T) {
	srv := newTestServer(t, nil)
	admin := dial(t, srv)
	expectSimple(t, admin.do("ACL", "SETUSER", "logu", "on", ">pw", "~ok", "+get"), "OK")
	expectSimple(t, admin.do("ACL", "LOG", "RESET"), "OK")

	c := dial(t, srv)
	expectError(t, c.do("AUTH", "logu", "bad"), "WRONGPASS")
	expectSimple(t, c.do("AUTH", "logu", "pw"), "OK")
	expectError(t, c.do("GET", "denied"), "NOPERM")

	entries := expectArray(t, admin.do("ACL", "LOG", "10"))
	if len(entries) < 2 {
		t.Fatalf("ACL LOG returned %d entries", len(entries))
	}
	first := expectArray(t, entries[0])
	if len(first)%2 != 0 {
		t.Fatalf("ACL LOG entry = %v", first)
	}
	fields := map[string]any{}
	for i := 0; i < len(first); i += 2 {
		fields[string(first[i].([]byte))] = first[i+1]
	}
	if _, ok := fields["reason"]; !ok {
		t.Fatalf("ACL LOG entry missing reason: %v", fields)
	}
	expectSimple(t, admin.do("ACL", "LOG", "RESET"), "OK")
	if len(expectArray(t, admin.do("ACL", "LOG"))) != 0 {
		t.Fatal("ACL LOG RESET did not clear entries")
	}

	readCommands := expectArray(t, admin.do("ACL", "CAT", "read"))
	foundGet := false
	for _, raw := range readCommands {
		if string(raw.([]byte)) == "get" {
			foundGet = true
		}
	}
	if !foundGet {
		t.Fatalf("ACL CAT read is missing get: %v", readCommands)
	}
	expectError(t, admin.do("ACL", "CAT", "nope"), "Unknown category")

	pass := admin.do("ACL", "GENPASS", "32").([]byte)
	if len(pass) != 8 {
		t.Fatalf("ACL GENPASS length = %d", len(pass))
	}
}
