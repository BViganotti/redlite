package store

import (
	"errors"
	"strconv"
	"testing"
)

func TestHashBasics(t *testing.T) {
	s, _ := newTestStore()

	added, err := s.HSet("h", []KeyValue{
		{Key: "a", Value: []byte("1")},
		{Key: "b", Value: []byte("2")},
	})
	if err != nil || added != 2 {
		t.Fatalf("HSet = %d, %v; want 2, nil", added, err)
	}

	added, err = s.HSet("h", []KeyValue{{Key: "a", Value: []byte("3")}})
	if err != nil || added != 0 {
		t.Fatalf("HSet update = %d, %v; want 0, nil", added, err)
	}

	value, ok, err := s.HGet("h", "a")
	if err != nil || !ok || string(value) != "3" {
		t.Fatalf("HGet = %q, %v, %v", value, ok, err)
	}

	if n, err := s.HLen("h"); err != nil || n != 2 {
		t.Fatalf("HLen = %d, %v", n, err)
	}
	if ok, err := s.HExists("h", "a"); err != nil || !ok {
		t.Fatalf("HExists(a) = %v, %v", ok, err)
	}
	if ok, err := s.HExists("h", "zzz"); err != nil || ok {
		t.Fatalf("HExists(zzz) = %v, %v", ok, err)
	}

	keys, err := s.HKeys("h")
	if err != nil || len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("HKeys = %v, %v", keys, err)
	}

	values, err := s.HVals("h")
	if err != nil || len(values) != 2 || string(values[0]) != "3" {
		t.Fatalf("HVals = %q, %v", values, err)
	}

	pairs, err := s.HGetAll("h")
	if err != nil || len(pairs) != 2 || pairs[0].Field != "a" || string(pairs[0].Value) != "3" {
		t.Fatalf("HGetAll = %+v, %v", pairs, err)
	}

	removed, err := s.HDel("h", []string{"a", "missing"})
	if err != nil || removed != 1 {
		t.Fatalf("HDel = %d, %v", removed, err)
	}
	if n, _ := s.HLen("h"); n != 1 {
		t.Fatalf("HLen after delete = %d", n)
	}
	if _, err := s.HDel("h", []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("empty hash should be removed, DBSize = %d", n)
	}
}

func TestHashMissingKey(t *testing.T) {
	s, _ := newTestStore()

	if value, ok, err := s.HGet("missing", "f"); err != nil || ok || value != nil {
		t.Fatalf("HGet on missing key = %q, %v, %v", value, ok, err)
	}
	if n, err := s.HLen("missing"); err != nil || n != 0 {
		t.Fatalf("HLen on missing key = %d, %v", n, err)
	}
	if ok, err := s.HExists("missing", "f"); err != nil || ok {
		t.Fatalf("HExists on missing key = %v, %v", ok, err)
	}
	if pairs, err := s.HGetAll("missing"); err != nil || len(pairs) != 0 {
		t.Fatalf("HGetAll on missing key = %v, %v", pairs, err)
	}
	if n, err := s.HDel("missing", []string{"f"}); err != nil || n != 0 {
		t.Fatalf("HDel on missing key = %d, %v", n, err)
	}
}

func TestHSetNX(t *testing.T) {
	s, _ := newTestStore()

	ok, err := s.HSetNX("h", "f", []byte("1"))
	if err != nil || !ok {
		t.Fatalf("first HSetNX = %v, %v", ok, err)
	}
	ok, err = s.HSetNX("h", "f", []byte("2"))
	if err != nil || ok {
		t.Fatalf("second HSetNX = %v, %v", ok, err)
	}
	value, _, _ := s.HGet("h", "f")
	if string(value) != "1" {
		t.Fatalf("HSetNX overwrote the field: %q", value)
	}
}

func TestHIncrBy(t *testing.T) {
	s, _ := newTestStore()

	n, err := s.HIncrBy("h", "count", 5)
	if err != nil || n != 5 {
		t.Fatalf("HIncrBy = %d, %v", n, err)
	}
	n, err = s.HIncrBy("h", "count", -2)
	if err != nil || n != 3 {
		t.Fatalf("HIncrBy = %d, %v", n, err)
	}

	if _, err := s.HSet("h", []KeyValue{{Key: "text", Value: []byte("nope")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HIncrBy("h", "text", 1); !errors.Is(err, ErrNotInteger) {
		t.Fatalf("want ErrNotInteger, got %v", err)
	}

	if _, err := s.HSet("h", []KeyValue{{Key: "max", Value: []byte(strconv.FormatInt(1<<63-1, 10))}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.HIncrBy("h", "max", 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestHScan(t *testing.T) {
	s, _ := newTestStore()
	pairs := make([]KeyValue, 0, 25)
	for i := 0; i < 25; i++ {
		pairs = append(pairs, KeyValue{Key: "field:" + strconv.Itoa(i), Value: []byte("v")})
	}
	if _, err := s.HSet("h", pairs); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	cursor := uint64(0)
	for i := 0; i < 100; i++ {
		next, page, err := s.HScan("h", cursor, "field:*", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, pair := range page {
			seen[pair.Field] = true
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatalf("HScan saw %d fields, want 25", len(seen))
	}

	if _, page, err := s.HScan("h", 0, "nomatch:*", 10); err != nil || len(page) != 0 {
		t.Fatalf("HScan with no match = %v, %v", page, err)
	}
}

func TestWrongType(t *testing.T) {
	s, _ := newTestStore()

	if _, err := s.Set("str", []byte("v"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.HGet("str", "f"); !errors.Is(err, ErrWrongType) {
		t.Fatalf("HGet on string: want ErrWrongType, got %v", err)
	}
	if _, err := s.HSet("str", []KeyValue{{Key: "f", Value: []byte("v")}}); !errors.Is(err, ErrWrongType) {
		t.Fatalf("HSet on string: want ErrWrongType, got %v", err)
	}

	if _, err := s.HSet("hash", []KeyValue{{Key: "f", Value: []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("hash"); !errors.Is(err, ErrWrongType) {
		t.Fatalf("Get on hash: want ErrWrongType, got %v", err)
	}
	if got := s.Type("hash"); got != "hash" {
		t.Fatalf("Type = %q, want hash", got)
	}
}

func TestSetReplacesHash(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.HSet("k", []KeyValue{{Key: "f", Value: []byte("v")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("k", []byte("string"), SetOptions{}); err != nil {
		t.Fatalf("SET should replace any type: %v", err)
	}
	if got := s.Type("k"); got != "string" {
		t.Fatalf("Type = %q, want string", got)
	}
}

func TestEmitSnapshotRebuildsHash(t *testing.T) {
	s, clock := newTestStore()
	if _, err := s.HSet("h", []KeyValue{
		{Key: "a", Value: []byte("1")},
		{Key: "b", Value: []byte("2")},
	}); err != nil {
		t.Fatal(err)
	}
	if !s.Expire("h", clock.ms+60_000, ExpireAlways) {
		t.Fatal("Expire failed")
	}

	var commands [][][]byte
	s.EmitSnapshot(func(args [][]byte) {
		copied := make([][]byte, len(args))
		for i, arg := range args {
			copied[i] = append([]byte(nil), arg...)
		}
		commands = append(commands, copied)
	})

	var sawHash, sawExpiry bool
	for _, cmd := range commands {
		switch string(cmd[0]) {
		case "HSET":
			sawHash = true
			if len(cmd) != 6 {
				t.Fatalf("HSET snapshot has %d args, want 6: %q", len(cmd), cmd)
			}
		case "PEXPIREAT":
			sawExpiry = true
		}
	}
	if !sawHash || !sawExpiry {
		t.Fatalf("snapshot missing commands: hash=%v expiry=%v (%q)", sawHash, sawExpiry, commands)
	}
}
