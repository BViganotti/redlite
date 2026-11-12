package server

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/config"
)

func migrateEndpoint(t *testing.T, srv *Server) (string, string) {
	t.Helper()
	addr := srv.Addr()
	if addr == nil {
		t.Fatal("server has no address")
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		t.Fatalf("split address %q: %v", addr, err)
	}
	return host, port
}

func migrateDo(t *testing.T, c *testConn, target *Server, key, db, timeout string, extra ...string) any {
	t.Helper()
	host, port := migrateEndpoint(t, target)
	args := []string{"MIGRATE", host, port, key, db, timeout}
	args = append(args, extra...)
	return c.do(args...)
}

func TestMigrateSupportedTypes(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)
	dest := dial(t, dst)

	expectSimple(t, c.do("SET", "mig:string", "hello"), "OK")
	expectInt(t, c.do("RPUSH", "mig:list", "a", "b", "c"), 3)
	expectInt(t, c.do("HSET", "mig:hash", "f1", "v1", "f2", "v2"), 2)
	expectInt(t, c.do("SADD", "mig:set", "one", "two"), 2)
	expectInt(t, c.do("ZADD", "mig:zset", "1.5", "alpha", "2", "beta"), 2)

	for _, key := range []string{"mig:string", "mig:list", "mig:hash", "mig:set", "mig:zset"} {
		expectSimple(t, migrateDo(t, c, dst, key, "0", "5000"), "OK")
		expectInt(t, c.do("EXISTS", key), 0)
	}

	expectBulk(t, dest.do("GET", "mig:string"), "hello")

	list := expectArray(t, dest.do("LRANGE", "mig:list", "0", "-1"))
	if len(list) != 3 || string(list[0].([]byte)) != "a" || string(list[2].([]byte)) != "c" {
		t.Fatalf("migrated list = %v", list)
	}
	expectBulk(t, dest.do("HGET", "mig:hash", "f2"), "v2")
	expectInt(t, dest.do("SISMEMBER", "mig:set", "two"), 1)
	expectBulk(t, dest.do("ZSCORE", "mig:zset", "alpha"), "1.5")
	expectBulk(t, dest.do("ZSCORE", "mig:zset", "beta"), "2")
}

func TestMigratePreservesTTL(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)
	dest := dial(t, dst)

	expectSimple(t, c.do("SET", "ttl-key", "v", "PX", "100000"), "OK")
	expectSimple(t, migrateDo(t, c, dst, "ttl-key", "0", "5000"), "OK")
	expectInt(t, c.do("EXISTS", "ttl-key"), 0)

	ttl, ok := dest.do("PTTL", "ttl-key").(int64)
	if !ok {
		t.Fatal("PTTL did not return an integer")
	}
	if ttl <= 0 || ttl > 100000 {
		t.Fatalf("migrated PTTL = %d, want (0, 100000]", ttl)
	}
	expectBulk(t, dest.do("GET", "ttl-key"), "v")

	// A key without an expiry must arrive without one.
	expectSimple(t, c.do("SET", "no-ttl", "v"), "OK")
	expectSimple(t, migrateDo(t, c, dst, "no-ttl", "0", "5000"), "OK")
	expectInt(t, dest.do("TTL", "no-ttl"), -1)
}

func TestMigrateNOKEYAndValidation(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)

	expectSimple(t, migrateDo(t, c, dst, "missing", "0", "1000"), "NOKEY")

	host, port := migrateEndpoint(t, dst)
	expectError(t, c.do("MIGRATE", host, port, "", "0", "1000"), "KEYS option is required")
	expectError(t, c.do("MIGRATE", host, port, "", "0", "1000", "KEYS"), "at least one key")
	expectError(t, c.do("MIGRATE", host, port, "key", "0", "1000", "KEYS", "other"), "must be set to the empty string")
	expectError(t, c.do("MIGRATE", host, port, "key", "0", "-1"), "timeout is negative")
	expectError(t, c.do("MIGRATE", host, port, "key", "0", "1000", "BOGUS"), "syntax error")
	expectError(t, c.do("MIGRATE", host, port, "key", "0", "1000", "AUTH"), "syntax error")
	expectError(t, c.do("MIGRATE", host, port, "key", "0", "1000", "AUTH2", "user"), "syntax error")
}

func TestMigrateCopyKeepsSource(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)
	dest := dial(t, dst)

	expectSimple(t, c.do("SET", "copied", "value"), "OK")
	expectSimple(t, migrateDo(t, c, dst, "copied", "0", "5000", "COPY"), "OK")
	expectBulk(t, c.do("GET", "copied"), "value")
	expectInt(t, c.do("EXISTS", "copied"), 1)
	expectBulk(t, dest.do("GET", "copied"), "value")

	// COPY into an occupied destination still needs REPLACE.
	expectSimple(t, dest.do("SET", "copied", "old"), "OK")
	expectError(t, migrateDo(t, c, dst, "copied", "0", "5000", "COPY"), "BUSYKEY")
	expectBulk(t, dest.do("GET", "copied"), "old")
	expectSimple(t, migrateDo(t, c, dst, "copied", "0", "5000", "COPY", "REPLACE"), "OK")
	expectBulk(t, dest.do("GET", "copied"), "value")
	expectBulk(t, c.do("GET", "copied"), "value")
}

func TestMigrateReplace(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)
	dest := dial(t, dst)

	expectSimple(t, c.do("SET", "replace-me", "new"), "OK")
	expectSimple(t, dest.do("SET", "replace-me", "old"), "OK")

	expectError(t, migrateDo(t, c, dst, "replace-me", "0", "5000"), "Target instance replied with error")
	expectError(t, migrateDo(t, c, dst, "replace-me", "0", "5000"), "BUSYKEY")
	expectBulk(t, dest.do("GET", "replace-me"), "old")
	expectInt(t, c.do("EXISTS", "replace-me"), 1)

	expectSimple(t, migrateDo(t, c, dst, "replace-me", "0", "5000", "REPLACE"), "OK")
	expectBulk(t, dest.do("GET", "replace-me"), "new")
	expectInt(t, c.do("EXISTS", "replace-me"), 0)
}

func TestMigrateKeysForm(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)
	dest := dial(t, dst)

	expectSimple(t, c.do("MSET", "k1", "one", "k2", "two", "k3", "three"), "OK")

	got := migrateDo(t, c, dst, "", "0", "5000", "KEYS", "k1", "k2", "missing")
	expectInt(t, got, 2)
	expectInt(t, c.do("EXISTS", "k1"), 0)
	expectInt(t, c.do("EXISTS", "k2"), 0)
	expectInt(t, c.do("EXISTS", "k3"), 1)
	expectBulk(t, dest.do("GET", "k1"), "one")
	expectBulk(t, dest.do("GET", "k2"), "two")

	// A KEYS form where nothing exists reports zero, not NOKEY.
	expectInt(t, migrateDo(t, c, dst, "", "0", "5000", "KEYS", "missing"), 0)

	// COPY reports the number of keys sent without deleting the source.
	expectInt(t, migrateDo(t, c, dst, "", "0", "5000", "COPY", "KEYS", "k3"), 1)
	expectBulk(t, c.do("GET", "k3"), "three")
	expectBulk(t, dest.do("GET", "k3"), "three")
}

func TestMigratePartialFailureDeletesOnlySucceededKeys(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)
	dest := dial(t, dst)

	expectSimple(t, c.do("MSET", "ok", "value", "busy", "value"), "OK")
	expectSimple(t, dest.do("SET", "busy", "already-here"), "OK")

	expectError(t, migrateDo(t, c, dst, "", "0", "5000", "KEYS", "ok", "busy"), "BUSYKEY")

	// The first RESTORE succeeded, so Redis (and redlite) deletes that key even
	// though a later key failed.
	expectInt(t, c.do("EXISTS", "ok"), 0)
	expectInt(t, c.do("EXISTS", "busy"), 1)
	expectBulk(t, dest.do("GET", "ok"), "value")
	expectBulk(t, dest.do("GET", "busy"), "already-here")
}

func TestMigrateAuthentication(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, func(cfg *config.Config) {
		cfg.RequirePass = "secret"
	})
	c := dial(t, src)

	expectSimple(t, c.do("SET", "auth-key", "value"), "OK")

	expectError(t, migrateDo(t, c, dst, "auth-key", "0", "5000"), "NOAUTH")
	expectInt(t, c.do("EXISTS", "auth-key"), 1)

	expectError(t, migrateDo(t, c, dst, "auth-key", "0", "5000", "AUTH", "wrong"), "WRONGPASS")
	expectInt(t, c.do("EXISTS", "auth-key"), 1)

	expectSimple(t, migrateDo(t, c, dst, "auth-key", "0", "5000", "AUTH", "secret"), "OK")
	expectInt(t, c.do("EXISTS", "auth-key"), 0)

	dest := dial(t, dst)
	expectSimple(t, dest.do("AUTH", "secret"), "OK")
	expectBulk(t, dest.do("GET", "auth-key"), "value")

	// AUTH2 uses the username form.
	expectSimple(t, c.do("SET", "auth2-key", "value"), "OK")
	expectSimple(t, migrateDo(t, c, dst, "auth2-key", "0", "5000", "AUTH2", "default", "secret"), "OK")
	expectBulk(t, dest.do("GET", "auth2-key"), "value")
}

func TestMigrateClosedPort(t *testing.T) {
	src := newTestServer(t, nil)
	c := dial(t, src)

	expectSimple(t, c.do("SET", "io-key", "value"), "OK")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	host, _ := migrateEndpoint(t, src)
	expectError(t, c.do("MIGRATE", host, port, "io-key", "0", "500"), "IOERR")
	expectInt(t, c.do("EXISTS", "io-key"), 1)
}

func TestMigrateDestinationDBRejectsNonZero(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)

	expectSimple(t, c.do("SET", "db-key", "value"), "OK")
	expectError(t, migrateDo(t, c, dst, "db-key", "1", "5000"), "DB index is out of range")
	expectInt(t, c.do("EXISTS", "db-key"), 1)
}

func TestMigrateAOFLogsDelete(t *testing.T) {
	dir := t.TempDir()
	src := startServer(t, persistenceConfig(t, dir))
	dst := newTestServer(t, nil)
	c := dial(t, src)

	expectSimple(t, c.do("SET", "aof-key", "value"), "OK")
	expectSimple(t, migrateDo(t, c, dst, "aof-key", "0", "5000"), "OK")

	data, err := os.ReadFile(filepath.Join(dir, "appendonly.aof"))
	if err != nil {
		t.Fatalf("read AOF: %v", err)
	}
	if !bytes.Contains(data, []byte("$3\r\nDEL\r\n")) {
		t.Fatalf("AOF does not contain the DEL rewrite:\n%q", data)
	}
	if !bytes.Contains(data, []byte("$7\r\naof-key\r\n")) {
		t.Fatalf("AOF does not contain the migrated key:\n%q", data)
	}
	if bytes.Contains(data, []byte("MIGRATE")) {
		t.Fatalf("AOF still contains the MIGRATE command:\n%q", data)
	}

	// Replaying the AOF must not resurrect the key on the source.
	shutdownServer(t, src)
	restarted := startServer(t, persistenceConfig(t, dir))
	c2 := dial(t, restarted)
	expectInt(t, c2.do("EXISTS", "aof-key"), 0)
}

func TestMigrateTimeoutValue(t *testing.T) {
	src := newTestServer(t, nil)
	dst := newTestServer(t, nil)
	c := dial(t, src)

	expectSimple(t, c.do("SET", "zero-timeout", "value"), "OK")
	// A zero timeout means the Redis default rather than an immediate error.
	start := time.Now()
	expectSimple(t, migrateDo(t, c, dst, "zero-timeout", "0", "0"), "OK")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("migration with zero timeout took %v", elapsed)
	}
}
