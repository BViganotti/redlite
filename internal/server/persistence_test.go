package server

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/config"
)

func snapshotConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0
	cfg.Dir = dir
	cfg.LogLevel = "error"
	return cfg
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	cfg := snapshotConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectSimple(t, c.do("SET", "s", "value"), "OK")
	expectInt(t, c.do("HSET", "h", "f", "v"), 1)
	expectInt(t, c.do("RPUSH", "l", "a", "b"), 2)
	expectInt(t, c.do("SADD", "st", "x", "y"), 2)
	expectInt(t, c.do("ZADD", "z", "1.5", "m"), 1)
	expectInt(t, c.do("EXPIRE", "s", "1000"), 1)
	expectSimple(t, c.do("SAVE"), "OK")

	info := string(c.do("INFO", "persistence").([]byte))
	if !strings.Contains(info, "rdb_last_bgsave_status:ok") {
		t.Fatalf("INFO persistence missing rdb status: %s", info)
	}
	shutdownServer(t, first)

	second := startServer(t, cfg)
	c2 := dial(t, second)
	expectBulk(t, c2.do("GET", "s"), "value")
	expectBulk(t, c2.do("HGET", "h", "f"), "v")
	expectBulk(t, c2.do("LINDEX", "l", "1"), "b")
	expectInt(t, c2.do("SCARD", "st"), 2)
	expectBulk(t, c2.do("ZSCORE", "z", "m"), "1.5")
	ttl := c2.do("TTL", "s").(int64)
	if ttl <= 0 || ttl > 1000 {
		t.Fatalf("TTL after reload = %d", ttl)
	}
}

func TestBGSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	cfg := snapshotConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectSimple(t, c.do("SET", "bg", "saved"), "OK")
	expectSimple(t, c.do("BGSAVE"), "Background saving started")

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(cfg.SnapshotPath()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot file was not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	shutdownServer(t, first)

	second := startServer(t, cfg)
	expectBulk(t, dial(t, second).do("GET", "bg"), "saved")
}

func TestShutdownSave(t *testing.T) {
	dir := t.TempDir()
	cfg := snapshotConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectSimple(t, c.do("SET", "shutdown", "saved"), "OK")
	c.send("SHUTDOWN", "SAVE")

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(cfg.SnapshotPath()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("SHUTDOWN SAVE did not write a snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}

	second := startServer(t, cfg)
	expectBulk(t, dial(t, second).do("GET", "shutdown"), "saved")
}

func TestCorruptSnapshotFailsStartup(t *testing.T) {
	dir := t.TempDir()
	cfg := snapshotConfig(t, dir)
	if err := os.WriteFile(cfg.SnapshotPath(), []byte("definitely not a snapshot"), 0o644); err != nil {
		t.Fatal(err)
	}

	level := new(slog.LevelVar)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(cfg, logger, level); err == nil {
		t.Fatal("expected startup to fail on a corrupt snapshot")
	}
}

func TestAOFWinsOverStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	cfg := snapshotConfig(t, dir)
	cfg.AppendOnly = true
	cfg.AppendFsync = "always"

	// Write a snapshot containing a stale value first.
	stale := startServer(t, cfg)
	c := dial(t, stale)
	expectSimple(t, c.do("SET", "k", "stale"), "OK")
	expectSimple(t, c.do("SAVE"), "OK")
	shutdownServer(t, stale)

	// With the AOF enabled, a newer value in the log must win.
	live := startServer(t, cfg)
	c = dial(t, live)
	expectSimple(t, c.do("SET", "k", "current"), "OK")
	shutdownServer(t, live)

	reloaded := startServer(t, cfg)
	expectBulk(t, dial(t, reloaded).do("GET", "k"), "current")
}
