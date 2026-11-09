package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/config"
)

func authConfig(t *testing.T, password string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "error"
	cfg.RequirePass = password
	return cfg
}

func TestAuthRequired(t *testing.T) {
	srv := startServer(t, authConfig(t, "secret"))
	c := dial(t, srv)

	expectError(t, c.do("PING"), "NOAUTH")
	expectError(t, c.do("GET", "k"), "NOAUTH")
	expectError(t, c.do("AUTH", "wrong"), "WRONGPASS")
	expectSimple(t, c.do("AUTH", "secret"), "OK")
	expectSimple(t, c.do("PING"), "PONG")
	expectSimple(t, c.do("SET", "k", "v"), "OK")
	expectBulk(t, c.do("GET", "k"), "v")

	// AUTH is idempotent once authenticated.
	expectSimple(t, c.do("AUTH", "default", "secret"), "OK")
}

func TestAuthWithoutPasswordSet(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectError(t, c.do("AUTH", "anything"), "no password is set")
	expectSimple(t, c.do("PING"), "PONG")
}

func TestHelloAuth(t *testing.T) {
	srv := startServer(t, authConfig(t, "secret"))

	good := dial(t, srv)
	hello := good.do("HELLO", "3", "AUTH", "default", "secret")
	info, ok := hello.(map[string]any)
	if !ok {
		t.Fatalf("HELLO returned %T", hello)
	}
	if proto, _ := info["proto"].(int64); proto != 3 {
		t.Fatalf("proto = %v", info["proto"])
	}
	expectSimple(t, good.do("PING"), "PONG")

	bad := dial(t, srv)
	expectError(t, bad.do("HELLO", "3", "AUTH", "default", "nope"), "WRONGPASS")

	anonymous := dial(t, srv)
	expectError(t, anonymous.do("HELLO", "3"), "NOAUTH")
}

func TestConfigSetRequirePass(t *testing.T) {
	srv := newTestServer(t, nil)
	c := dial(t, srv)

	expectSimple(t, c.do("CONFIG", "SET", "requirepass", "newpass"), "OK")
	// The connection that set the password stays authenticated.
	expectSimple(t, c.do("PING"), "PONG")

	next := dial(t, srv)
	expectError(t, next.do("PING"), "NOAUTH")
	expectSimple(t, next.do("AUTH", "newpass"), "OK")
	expectSimple(t, next.do("PING"), "PONG")

	expectSimple(t, c.do("CONFIG", "SET", "requirepass", ""), "OK")
	fresh := dial(t, srv)
	expectSimple(t, fresh.do("PING"), "PONG")
}

func TestAclIntrospection(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	expectBulk(t, c.do("ACL", "WHOAMI"), "default")

	list := expectArray(t, c.do("ACL", "LIST"))
	if len(list) != 1 {
		t.Fatalf("ACL LIST returned %d entries", len(list))
	}
	if line := string(list[0].([]byte)); !strings.Contains(line, "nopass") {
		t.Fatalf("ACL LIST without a password = %q", line)
	}

	protected := dial(t, startServer(t, authConfig(t, "secret")))
	expectSimple(t, protected.do("AUTH", "secret"), "OK")
	list = expectArray(t, protected.do("ACL", "LIST"))
	if line := string(list[0].([]byte)); strings.Contains(line, "secret") || !strings.Contains(line, "#") {
		t.Fatalf("ACL LIST should hash the password: %q", line)
	}
}

func TestReplicationWithAuth(t *testing.T) {
	masterCfg := authConfig(t, "secret")
	master := startServer(t, masterCfg)
	mc := dial(t, master)
	expectSimple(t, mc.do("AUTH", "secret"), "OK")
	expectSimple(t, mc.do("SET", "pre", "1"), "OK")

	replicaCfg := authConfig(t, "")
	replicaCfg.MasterAuth = "secret"
	replica := startServer(t, replicaCfg)
	rc := dial(t, replica)
	expectSimple(t, rc.do("REPLICAOF", "127.0.0.1", strconv.Itoa(serverPort(t, master))), "OK")

	waitFor(t, 3*time.Second, "authenticated sync", func() bool {
		value, ok := getString(rc, "pre")
		return ok && value == "1"
	})
}
