package server

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func serverPort(t *testing.T, srv *Server) int {
	t.Helper()
	addr, ok := srv.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected address %v", srv.Addr())
	}
	return addr.Port
}

func getString(c *testConn, key string) (string, bool) {
	value, ok := c.do("GET", key).([]byte)
	if !ok {
		return "", false
	}
	return string(value), true
}

func TestReplicationFullResync(t *testing.T) {
	master := newTestServer(t, nil)
	replica := newTestServer(t, nil)

	mc := dial(t, master)
	expectSimple(t, mc.do("SET", "pre", "1"), "OK")
	expectInt(t, mc.do("HSET", "hash", "f", "v"), 1)

	rc := dial(t, replica)
	expectSimple(t, rc.do("REPLICAOF", "127.0.0.1", strconv.Itoa(serverPort(t, master))), "OK")

	waitFor(t, 3*time.Second, "initial sync", func() bool {
		value, ok := getString(rc, "pre")
		return ok && value == "1"
	})
	expectBulk(t, rc.do("HGET", "hash", "f"), "v")

	expectSimple(t, mc.do("SET", "live", "2"), "OK")
	waitFor(t, 3*time.Second, "live propagation", func() bool {
		value, ok := getString(rc, "live")
		return ok && value == "2"
	})

	info := string(mc.do("INFO", "replication").([]byte))
	if !strings.Contains(info, "role:master") {
		t.Fatalf("master INFO missing role: %s", info)
	}
	if !strings.Contains(info, "connected_slaves:1") {
		t.Fatalf("master INFO missing connected_slaves:1: %s", info)
	}
	replicaInfo := string(rc.do("INFO", "replication").([]byte))
	if !strings.Contains(replicaInfo, "role:slave") {
		t.Fatalf("replica INFO missing role: %s", replicaInfo)
	}
	if !strings.Contains(replicaInfo, "master_link_status:up") {
		t.Fatalf("replica INFO missing link status: %s", replicaInfo)
	}
}

func TestReplicationPropagatesTransactions(t *testing.T) {
	master := newTestServer(t, nil)
	replica := newTestServer(t, nil)

	mc := dial(t, master)
	rc := dial(t, replica)
	expectSimple(t, rc.do("REPLICAOF", "127.0.0.1", strconv.Itoa(serverPort(t, master))), "OK")

	waitFor(t, 3*time.Second, "replica link", func() bool {
		info := string(rc.do("INFO", "replication").([]byte))
		return strings.Contains(info, "master_link_status:up")
	})

	expectSimple(t, mc.do("MULTI"), "OK")
	expectSimple(t, mc.do("SET", "tx", "1"), "QUEUED")
	expectSimple(t, mc.do("INCR", "counter"), "QUEUED")
	expectSimple(t, mc.do("HSET", "txhash", "a", "b"), "QUEUED")
	expectArray(t, mc.do("EXEC"))

	waitFor(t, 3*time.Second, "transaction propagation", func() bool {
		value, ok := getString(rc, "tx")
		return ok && value == "1"
	})
	waitFor(t, 3*time.Second, "counter propagation", func() bool {
		value, ok := getString(rc, "counter")
		return ok && value == "1"
	})
	expectBulk(t, rc.do("HGET", "txhash", "a"), "b")
}

func TestReplicaIsReadOnly(t *testing.T) {
	master := newTestServer(t, nil)
	replica := newTestServer(t, nil)

	rc := dial(t, replica)
	expectSimple(t, rc.do("REPLICAOF", "127.0.0.1", strconv.Itoa(serverPort(t, master))), "OK")

	waitFor(t, 3*time.Second, "replica link", func() bool {
		info := string(rc.do("INFO", "replication").([]byte))
		return strings.Contains(info, "master_link_status:up")
	})

	expectError(t, rc.do("SET", "nope", "1"), "READONLY")
	expectError(t, rc.do("LPUSH", "nope", "1"), "READONLY")

	// A rejected write inside MULTI does not dirty the transaction.
	expectSimple(t, rc.do("MULTI"), "OK")
	expectError(t, rc.do("SET", "nope", "1"), "READONLY")
	results := expectArray(t, rc.do("EXEC"))
	if len(results) != 0 {
		t.Fatalf("empty transaction returned %d replies", len(results))
	}

	expectSimple(t, rc.do("REPLICAOF", "NO", "ONE"), "OK")
	expectSimple(t, rc.do("SET", "local", "1"), "OK")
	expectBulk(t, rc.do("GET", "local"), "1")
}
