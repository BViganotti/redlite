package store

import (
	"errors"
	"sort"
	"strconv"
	"testing"
)

type testClock struct{ ms int64 }

func (c *testClock) Now() int64 { return c.ms }

func newTestStore() (*Store, *testClock) {
	clock := &testClock{ms: 1_700_000_000_000}
	return NewWithClock(clock.Now), clock
}

func TestSetGet(t *testing.T) {
	s, _ := newTestStore()

	res := s.Set("key", []byte("value"), SetOptions{})
	if !res.Written || res.HadOld {
		t.Fatalf("unexpected result: %+v", res)
	}

	got, ok := s.Get("key")
	if !ok || string(got) != "value" {
		t.Fatalf("Get = %q, %v", got, ok)
	}
}

func TestGetCopiesValue(t *testing.T) {
	s, _ := newTestStore()
	s.Set("key", []byte("value"), SetOptions{})

	first, _ := s.Get("key")
	first[0] = 'X'

	second, _ := s.Get("key")
	if string(second) != "value" {
		t.Fatalf("stored value was mutated through a returned slice: %q", second)
	}
}

func TestSetNXAndXX(t *testing.T) {
	s, _ := newTestStore()

	if res := s.Set("key", []byte("one"), SetOptions{NX: true}); !res.Written {
		t.Fatal("NX on missing key should write")
	}
	res := s.Set("key", []byte("two"), SetOptions{NX: true})
	if res.Written {
		t.Fatal("NX on existing key should not write")
	}
	if string(res.Old) != "one" || !res.HadOld {
		t.Fatalf("NX failure should report the old value, got %q", res.Old)
	}

	if res := s.Set("missing", []byte("x"), SetOptions{XX: true}); res.Written {
		t.Fatal("XX on missing key should not write")
	}
	if res := s.Set("key", []byte("three"), SetOptions{XX: true}); !res.Written {
		t.Fatal("XX on existing key should write")
	}
}

func TestExpiryIsLazy(t *testing.T) {
	s, clock := newTestStore()
	s.Set("key", []byte("v"), SetOptions{HasExpire: true, ExpireAt: clock.ms + 1000})

	if _, ok := s.Get("key"); !ok {
		t.Fatal("key should exist before expiry")
	}
	clock.ms += 1001
	if _, ok := s.Get("key"); ok {
		t.Fatal("key should be expired")
	}
	if got := s.TTLMillis("key"); got != ExpiryMissing {
		t.Fatalf("TTL of expired key = %d, want %d", got, ExpiryMissing)
	}
}

func TestSetWithPastExpiryDeletes(t *testing.T) {
	s, clock := newTestStore()
	s.Set("key", []byte("v"), SetOptions{})
	s.Set("key", []byte("v"), SetOptions{HasExpire: true, ExpireAt: clock.ms - 1})

	if _, ok := s.Get("key"); ok {
		t.Fatal("key should not exist after a past expiry")
	}
}

func TestKeepTTL(t *testing.T) {
	s, clock := newTestStore()
	expireAt := clock.ms + 5000
	s.Set("key", []byte("one"), SetOptions{HasExpire: true, ExpireAt: expireAt})
	s.Set("key", []byte("two"), SetOptions{KeepTTL: true})

	if got := s.ExpireTimeMillis("key"); got != expireAt {
		t.Fatalf("expiry = %d, want %d", got, expireAt)
	}
}

func TestIncrBy(t *testing.T) {
	s, _ := newTestStore()

	n, err := s.IncrBy("counter", 1)
	if err != nil || n != 1 {
		t.Fatalf("first increment = %d, %v", n, err)
	}
	n, err = s.IncrBy("counter", 41)
	if err != nil || n != 42 {
		t.Fatalf("second increment = %d, %v", n, err)
	}

	s.Set("text", []byte("nope"), SetOptions{})
	if _, err := s.IncrBy("text", 1); !errors.Is(err, ErrNotInteger) {
		t.Fatalf("want ErrNotInteger, got %v", err)
	}

	s.Set("max", []byte(strconv.FormatInt(1<<63-1, 10)), SetOptions{})
	if _, err := s.IncrBy("max", 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestExpireModes(t *testing.T) {
	s, clock := newTestStore()
	s.Set("key", []byte("v"), SetOptions{})

	if !s.Expire("key", clock.ms+1000, ExpireNX) {
		t.Fatal("NX should set expiry on a persistent key")
	}
	if s.Expire("key", clock.ms+2000, ExpireNX) {
		t.Fatal("NX should not replace an existing expiry")
	}
	if s.Expire("key", clock.ms+500, ExpireGT) {
		t.Fatal("GT should reject a lower expiry")
	}
	if !s.Expire("key", clock.ms+3000, ExpireGT) {
		t.Fatal("GT should accept a higher expiry")
	}
	if !s.Expire("key", clock.ms+2000, ExpireLT) {
		t.Fatal("LT should accept a lower expiry")
	}
	if s.Expire("key", clock.ms+4000, ExpireLT) {
		t.Fatal("LT should reject a higher expiry")
	}

	s.Set("plain", []byte("v"), SetOptions{})
	if s.Expire("plain", clock.ms+1000, ExpireXX) {
		t.Fatal("XX should not set expiry on a persistent key")
	}
	if s.Expire("plain", clock.ms+1000, ExpireGT) {
		t.Fatal("GT on a persistent key should fail")
	}
}

func TestTTL(t *testing.T) {
	s, clock := newTestStore()
	if got := s.TTLMillis("missing"); got != ExpiryMissing {
		t.Fatalf("missing TTL = %d", got)
	}
	s.Set("plain", []byte("v"), SetOptions{})
	if got := s.TTLMillis("plain"); got != ExpiryNoExpiry {
		t.Fatalf("persistent TTL = %d", got)
	}
	s.Set("timed", []byte("v"), SetOptions{HasExpire: true, ExpireAt: clock.ms + 1500})
	if got := s.TTLMillis("timed"); got != 1500 {
		t.Fatalf("timed TTL = %d, want 1500", got)
	}
	if !s.Persist("timed") {
		t.Fatal("Persist should return true")
	}
	if got := s.TTLMillis("timed"); got != ExpiryNoExpiry {
		t.Fatalf("after Persist TTL = %d", got)
	}
}

func TestAppendAndStrLen(t *testing.T) {
	s, _ := newTestStore()
	if n := s.Append("k", []byte("hello")); n != 5 {
		t.Fatalf("append length = %d, want 5", n)
	}
	if n := s.Append("k", []byte(" world")); n != 11 {
		t.Fatalf("append length = %d, want 11", n)
	}
	if n := s.StrLen("k"); n != 11 {
		t.Fatalf("strlen = %d, want 11", n)
	}
}

func TestKeysAndScan(t *testing.T) {
	s, _ := newTestStore()
	for i := 0; i < 25; i++ {
		s.Set("key:"+strconv.Itoa(i), []byte("v"), SetOptions{})
	}
	s.Set("other", []byte("v"), SetOptions{})

	keys := s.Keys("key:*")
	if len(keys) != 25 {
		t.Fatalf("KEYS returned %d keys, want 25", len(keys))
	}

	seen := map[string]bool{}
	cursor := uint64(0)
	for i := 0; i < 100; i++ {
		next, page := s.Scan(cursor, "key:*", 10, "")
		for _, key := range page {
			seen[key] = true
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatalf("SCAN saw %d unique keys, want 25", len(seen))
	}

	cursor = 0
	var unmatched []string
	for {
		next, page := s.Scan(cursor, "nomatch:*", 10, "")
		unmatched = append(unmatched, page...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(unmatched) != 0 {
		t.Fatalf("SCAN matched keys it should not have: %v", unmatched)
	}
}

func TestScanIsSorted(t *testing.T) {
	s, _ := newTestStore()
	for _, key := range []string{"c", "a", "b"} {
		s.Set(key, []byte("v"), SetOptions{})
	}
	_, page := s.Scan(0, "*", 10, "")
	if !sort.StringsAreSorted(page) {
		t.Fatalf("SCAN output is not sorted: %v", page)
	}
}

func TestRename(t *testing.T) {
	s, _ := newTestStore()
	if s.Rename("missing", "dst") {
		t.Fatal("renaming a missing key should fail")
	}

	s.Set("src", []byte("v"), SetOptions{})
	if !s.Rename("src", "dst") {
		t.Fatal("rename should succeed")
	}
	if _, ok := s.Get("src"); ok {
		t.Fatal("source should be gone")
	}
	if value, ok := s.Get("dst"); !ok || string(value) != "v" {
		t.Fatal("destination should hold the value")
	}

	s.Set("other", []byte("x"), SetOptions{})
	if s.RenameNX("dst", "other") {
		t.Fatal("RENAMENX should fail when the destination exists")
	}
}

func TestDelCountsOnlyLiveKeys(t *testing.T) {
	s, clock := newTestStore()
	s.Set("live", []byte("v"), SetOptions{})
	s.Set("dead", []byte("v"), SetOptions{HasExpire: true, ExpireAt: clock.ms - 1})

	if n := s.Del([]string{"live", "dead", "missing"}); n != 1 {
		t.Fatalf("DEL removed %d keys, want 1", n)
	}
}

func TestSetManyNX(t *testing.T) {
	s, _ := newTestStore()
	pairs := []KeyValue{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}}
	if !s.SetManyNX(pairs) {
		t.Fatal("first MSETNX should succeed")
	}
	if s.SetManyNX([]KeyValue{{Key: "b", Value: []byte("x")}, {Key: "c", Value: []byte("y")}}) {
		t.Fatal("second MSETNX should fail")
	}
	if _, ok := s.Get("c"); ok {
		t.Fatal("MSETNX must not partially apply")
	}
}

func TestKeyCounts(t *testing.T) {
	s, clock := newTestStore()
	s.Set("a", []byte("v"), SetOptions{})
	s.Set("b", []byte("v"), SetOptions{HasExpire: true, ExpireAt: clock.ms + 1000})
	s.Set("c", []byte("v"), SetOptions{HasExpire: true, ExpireAt: clock.ms - 1})

	keys, expiring := s.KeyCounts()
	if keys != 2 || expiring != 1 {
		t.Fatalf("KeyCounts = %d, %d; want 2, 1", keys, expiring)
	}
}

func TestSnapshotSkipsExpired(t *testing.T) {
	s, clock := newTestStore()
	s.Set("a", []byte("1"), SetOptions{})
	s.Set("b", []byte("2"), SetOptions{HasExpire: true, ExpireAt: clock.ms - 1})

	seen := map[string]bool{}
	s.Snapshot(func(key string, value []byte, expireAt int64) {
		seen[key] = true
	})
	if seen["b"] || !seen["a"] {
		t.Fatalf("snapshot included the wrong keys: %v", seen)
	}
}

func TestFlush(t *testing.T) {
	s, _ := newTestStore()
	s.Set("a", []byte("1"), SetOptions{})
	s.Flush()
	if n := s.DBSize(); n != 0 {
		t.Fatalf("DBSize after Flush = %d", n)
	}
}
