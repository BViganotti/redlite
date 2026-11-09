package store

import (
	"bytes"
	"errors"
	"testing"
)

func TestSnapshotRoundTrip(t *testing.T) {
	clock := &testClock{ms: 1_700_000_000_000}
	source := NewWithClock(clock.Now)

	if _, err := source.Set("string", []byte("hello"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.HSet("hash", []KeyValue{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RPush("list", [][]byte{[]byte("x"), []byte("y"), []byte("z")}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SAdd("set", [][]byte{[]byte("m1"), []byte("m2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ZAdd("zset", ZAddOptions{}, []ScoreMember{
		{Score: 1.5, Member: "a"},
		{Score: -2, Member: "b"},
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Set("expiring", []byte("v"), SetOptions{
		HasExpire: true,
		ExpireAt:  clock.ms + 60_000,
	}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := source.Save(&buf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	target := NewWithClock(clock.Now)
	if err := target.Load(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if value, ok, err := target.Get("string"); err != nil || !ok || string(value) != "hello" {
		t.Fatalf("string = %q, %v, %v", value, ok, err)
	}
	if value, ok, err := target.HGet("hash", "b"); err != nil || !ok || string(value) != "2" {
		t.Fatalf("hash = %q, %v, %v", value, ok, err)
	}
	list, err := target.LRange("list", 0, -1)
	if err != nil || len(list) != 3 || string(list[0]) != "x" || string(list[2]) != "z" {
		t.Fatalf("list = %q, %v", list, err)
	}
	members, err := target.SMembers("set")
	if err != nil || len(members) != 2 {
		t.Fatalf("set = %q, %v", members, err)
	}
	if score, ok, err := target.ZScore("zset", "b"); err != nil || !ok || score != -2 {
		t.Fatalf("zset = %v, %v, %v", score, ok, err)
	}
	if got := target.TTLMillis("expiring"); got != 60_000 {
		t.Fatalf("TTL after load = %d, want 60000", got)
	}
	if got := target.Type("hash"); got != "hash" {
		t.Fatalf("Type = %q", got)
	}
}

func TestSnapshotSkipsExpiredEntries(t *testing.T) {
	clock := &testClock{ms: 1_700_000_000_000}
	source := NewWithClock(clock.Now)
	if _, err := source.Set("dead", []byte("v"), SetOptions{HasExpire: true, ExpireAt: clock.ms - 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Set("live", []byte("v"), SetOptions{}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := source.Save(&buf); err != nil {
		t.Fatal(err)
	}
	target := NewWithClock(clock.Now)
	if err := target.Load(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if target.DBSize() != 1 {
		t.Fatalf("loaded %d keys, want 1", target.DBSize())
	}
	if _, ok, _ := target.Get("dead"); ok {
		t.Fatal("expired key was restored")
	}
}

func TestSnapshotEmptyStore(t *testing.T) {
	source := New()
	var buf bytes.Buffer
	if err := source.Save(&buf); err != nil {
		t.Fatal(err)
	}
	target := New()
	if err := target.Load(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatal(err)
	}
	if target.DBSize() != 0 {
		t.Fatalf("DBSize = %d", target.DBSize())
	}
}

func TestSnapshotRejectsCorruption(t *testing.T) {
	source := New()
	if _, err := source.Set("key", []byte("value"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := source.Save(&buf); err != nil {
		t.Fatal(err)
	}

	data := buf.Bytes()
	data[len(data)/2] ^= 0xff

	target := New()
	if _, err := target.Set("existing", []byte("kept"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := target.Load(bytes.NewReader(data)); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("want ErrSnapshotCorrupt, got %v", err)
	}
	if value, ok, _ := target.Get("existing"); !ok || string(value) != "kept" {
		t.Fatal("failed load must not touch existing data")
	}
}

func TestSnapshotRejectsTruncation(t *testing.T) {
	source := New()
	if _, err := source.Set("key", []byte("value"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := source.Save(&buf); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	if err := New().Load(bytes.NewReader(data[:len(data)-2])); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("want ErrSnapshotCorrupt, got %v", err)
	}
}

func TestSnapshotRejectsWrongMagic(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("NOTREDIS")
	buf.Write(make([]byte, 20))
	if err := New().Load(bytes.NewReader(buf.Bytes())); !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatalf("want ErrSnapshotCorrupt, got %v", err)
	}
}
