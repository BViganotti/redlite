package server

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBlockingZPopCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("ZADD", "bz", "1", "a", "2", "b"), 2)
	popped := expectArray(t, c.do("BZPOPMIN", "bz", "1"))
	expectBulk(t, popped[0], "bz")
	expectBulk(t, popped[1], "a")
	expectBulk(t, popped[2], "1")

	popped = expectArray(t, c.do("BZPOPMAX", "bz", "1"))
	expectBulk(t, popped[1], "b")

	expectNull(t, c.do("BZPOPMIN", "empty", "0.05"))
}

func TestBlockingZPopWakesOnAdd(t *testing.T) {
	srv := newTestServer(t, nil)
	blocked := dialAsync(t, srv)

	replies := make(chan any, 1)
	go func() {
		value, err := blocked.call("BZPOPMIN", "queue", "5")
		if err != nil {
			replies <- err
			return
		}
		replies <- value
	}()

	time.Sleep(50 * time.Millisecond)
	pusher := dial(t, srv)
	expectInt(t, pusher.do("ZADD", "queue", "7", "job"), 1)

	select {
	case value := <-replies:
		message := expectArray(t, value)
		expectBulk(t, message[0], "queue")
		expectBulk(t, message[1], "job")
		expectBulk(t, message[2], "7")
	case <-time.After(3 * time.Second):
		t.Fatal("BZPOPMIN did not wake up")
	}
}

func TestRoleCommand(t *testing.T) {
	master := newTestServer(t, nil)
	mc := dial(t, master)
	expectSimple(t, mc.do("SET", "k", "v"), "OK")

	role := expectArray(t, mc.do("ROLE"))
	expectBulk(t, role[0], "master")
	if len(role) != 3 {
		t.Fatalf("master ROLE has %d elements", len(role))
	}

	replica := newTestServer(t, nil)
	rc := dial(t, replica)
	expectSimple(t, rc.do("REPLICAOF", "127.0.0.1", strconv.Itoa(serverPort(t, master))), "OK")
	waitFor(t, 3*time.Second, "replica link", func() bool {
		info := string(rc.do("INFO", "replication").([]byte))
		return strings.Contains(info, "master_link_status:up")
	})

	role = expectArray(t, rc.do("ROLE"))
	expectBulk(t, role[0], "slave")
	expectBulk(t, role[1], "127.0.0.1")
	expectInt(t, role[2], int64(serverPort(t, master)))
	expectBulk(t, role[3], "connected")
}

func TestClusterAndHashExtras(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectError(t, c.do("READONLY"), "cluster support disabled")
	expectError(t, c.do("READWRITE"), "cluster support disabled")
	expectError(t, c.do("ASKING"), "cluster support disabled")
	expectSimple(t, c.do("PFSELFTEST"), "OK")

	expectInt(t, c.do("HSET", "h", "f", "10.5"), 1)
	expectBulk(t, c.do("HINCRBYFLOAT", "h", "f", "0.1"), "10.6")
	expectError(t, c.do("HINCRBYFLOAT", "h", "f", "nan"), "not a valid float")

	expectSimple(t, c.do("SET", "text", "abc"), "OK")
	expectError(t, c.do("HINCRBYFLOAT", "text", "f", "1"), "WRONGTYPE")
}
