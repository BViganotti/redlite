package server

import (
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/config"
)

func notifyConfig(t *testing.T, flags string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "error"
	cfg.NotifyKeyspaceEvents = flags
	return cfg
}

func TestKeyspaceNotifications(t *testing.T) {
	srv := startServer(t, notifyConfig(t, "KEA"))

	keyspace := dial(t, srv)
	expectArray(t, keyspace.do("SUBSCRIBE", "__keyspace@0__:k"))

	keyevent := dial(t, srv)
	expectArray(t, keyevent.do("SUBSCRIBE", "__keyevent@0__:set"))

	writer := dial(t, srv)
	expectSimple(t, writer.do("SET", "k", "v"), "OK")

	ksMessage := expectArray(t, keyspace.readFrame())
	expectBulk(t, ksMessage[1], "__keyspace@0__:k")
	expectBulk(t, ksMessage[2], "set")

	keMessage := expectArray(t, keyevent.readFrame())
	expectBulk(t, keMessage[1], "__keyevent@0__:set")
	expectBulk(t, keMessage[2], "k")
}

func TestKeyspaceNotificationsCategoryFilter(t *testing.T) {
	srv := startServer(t, notifyConfig(t, "E$"))

	stringsOnly := dial(t, srv)
	expectArray(t, stringsOnly.do("SUBSCRIBE", "__keyevent@0__:lpush"))

	writer := dial(t, srv)
	expectInt(t, writer.do("RPUSH", "list", "a"), 1)
	expectSimple(t, writer.do("SET", "k", "v"), "OK")

	// The list event has category 'l', which is not enabled.
	_ = stringsOnly.conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if value, err := readValue(stringsOnly.rd); err == nil {
		t.Fatalf("unexpected notification for a filtered category: %v", value)
	}
	_ = stringsOnly.conn.SetReadDeadline(time.Time{})
}

func TestKeyspaceExpiredNotification(t *testing.T) {
	srv := startServer(t, notifyConfig(t, "Ex"))

	sub := dial(t, srv)
	expectArray(t, sub.do("SUBSCRIBE", "__keyevent@0__:expired"))

	writer := dial(t, srv)
	expectSimple(t, writer.do("SET", "ephemeral", "v", "PX", "20"), "OK")

	_ = sub.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	message, err := readValue(sub.rd)
	if err != nil {
		t.Fatalf("timed out waiting for the expired event: %v", err)
	}
	fields := expectArray(t, message)
	expectBulk(t, fields[1], "__keyevent@0__:expired")
	expectBulk(t, fields[2], "ephemeral")
	_ = sub.conn.SetReadDeadline(time.Time{})
}

func TestNotifyConfigValidation(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectError(t, c.do("CONFIG", "SET", "notify-keyspace-events", "KQ"), "Invalid argument")
	expectSimple(t, c.do("CONFIG", "SET", "notify-keyspace-events", "KEA"), "OK")

	pairs := expectArray(t, c.do("CONFIG", "GET", "notify-keyspace-events"))
	expectBulk(t, pairs[0], "notify-keyspace-events")
	expectBulk(t, pairs[1], "KEA")
}
