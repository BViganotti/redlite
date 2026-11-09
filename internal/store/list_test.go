package store

import (
	"errors"
	"strconv"
	"testing"
)

func listStrings(t *testing.T, values [][]byte) []string {
	t.Helper()
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLPushOrder(t *testing.T) {
	s, _ := newTestStore()

	n, err := s.LPush("l", [][]byte{[]byte("a"), []byte("b"), []byte("c")})
	if err != nil || n != 3 {
		t.Fatalf("LPush = %d, %v", n, err)
	}
	values, err := s.LRange("l", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if got := listStrings(t, values); !equalStrings(got, []string{"c", "b", "a"}) {
		t.Fatalf("LRange = %v, want [c b a]", got)
	}

	if _, err := s.RPush("r", [][]byte{[]byte("a"), []byte("b")}); err != nil {
		t.Fatal(err)
	}
	values, _ = s.LRange("r", 0, -1)
	if got := listStrings(t, values); !equalStrings(got, []string{"a", "b"}) {
		t.Fatalf("RPush order = %v", got)
	}
}

func TestPushXDoesNotCreate(t *testing.T) {
	s, _ := newTestStore()

	n, err := s.LPushX("missing", [][]byte{[]byte("a")})
	if err != nil || n != 0 {
		t.Fatalf("LPushX = %d, %v", n, err)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("LPUSHX created a key, DBSize = %d", n)
	}

	if _, err := s.RPush("l", [][]byte{[]byte("a")}); err != nil {
		t.Fatal(err)
	}
	n, err = s.RPushX("l", [][]byte{[]byte("b")})
	if err != nil || n != 2 {
		t.Fatalf("RPushX = %d, %v", n, err)
	}
}

func TestPopBothEnds(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.RPush("l", [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}

	head, err := s.LPop("l", 1)
	if err != nil || len(head) != 1 || string(head[0]) != "a" {
		t.Fatalf("LPop = %q, %v", head, err)
	}
	tail, err := s.RPop("l", 1)
	if err != nil || len(tail) != 1 || string(tail[0]) != "c" {
		t.Fatalf("RPop = %q, %v", tail, err)
	}

	rest, err := s.LPop("l", 10)
	if err != nil || len(rest) != 1 || string(rest[0]) != "b" {
		t.Fatalf("LPop rest = %q, %v", rest, err)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("empty list should be removed, DBSize = %d", n)
	}

	empty, err := s.LPop("missing", 1)
	if err != nil || len(empty) != 0 {
		t.Fatalf("LPop on missing = %q, %v", empty, err)
	}
}

func TestListIndexAndSet(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.RPush("l", [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}

	value, ok, err := s.LIndex("l", 0)
	if err != nil || !ok || string(value) != "a" {
		t.Fatalf("LIndex(0) = %q, %v, %v", value, ok, err)
	}
	value, ok, err = s.LIndex("l", -1)
	if err != nil || !ok || string(value) != "c" {
		t.Fatalf("LIndex(-1) = %q, %v, %v", value, ok, err)
	}
	if _, ok, err := s.LIndex("l", 5); err != nil || ok {
		t.Fatalf("LIndex(5) = %v, %v", ok, err)
	}
	if _, ok, err := s.LIndex("missing", 0); err != nil || ok {
		t.Fatalf("LIndex on missing = %v, %v", ok, err)
	}

	if err := s.LSet("l", 1, []byte("B")); err != nil {
		t.Fatal(err)
	}
	if value, _, _ := s.LIndex("l", 1); string(value) != "B" {
		t.Fatalf("LSet did not apply: %q", value)
	}
	if err := s.LSet("l", 9, []byte("x")); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("want ErrIndexOutOfRange, got %v", err)
	}
	if err := s.LSet("missing", 0, []byte("x")); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("want ErrNoSuchKey, got %v", err)
	}
}

func TestLRange(t *testing.T) {
	s, _ := newTestStore()
	items := make([][]byte, 5)
	for i := range items {
		items[i] = []byte(strconv.Itoa(i))
	}
	if _, err := s.RPush("l", items); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		start, stop int64
		want        []string
	}{
		{0, -1, []string{"0", "1", "2", "3", "4"}},
		{1, 3, []string{"1", "2", "3"}},
		{-2, -1, []string{"3", "4"}},
		{-100, 100, []string{"0", "1", "2", "3", "4"}},
		{3, 1, nil},
		{10, 20, nil},
	}
	for _, tc := range tests {
		values, err := s.LRange("l", tc.start, tc.stop)
		if err != nil {
			t.Fatalf("LRange(%d, %d): %v", tc.start, tc.stop, err)
		}
		if got := listStrings(t, values); !equalStrings(got, tc.want) {
			t.Errorf("LRange(%d, %d) = %v, want %v", tc.start, tc.stop, got, tc.want)
		}
	}
}

func TestLTrim(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.RPush("l", [][]byte{[]byte("a"), []byte("b"), []byte("c"), []byte("d")}); err != nil {
		t.Fatal(err)
	}
	if err := s.LTrim("l", 1, 2); err != nil {
		t.Fatal(err)
	}
	values, _ := s.LRange("l", 0, -1)
	if got := listStrings(t, values); !equalStrings(got, []string{"b", "c"}) {
		t.Fatalf("after LTrim = %v", got)
	}

	if err := s.LTrim("l", 5, 10); err != nil {
		t.Fatal(err)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("trimming everything should delete the key, DBSize = %d", n)
	}
	if err := s.LTrim("missing", 0, -1); err != nil {
		t.Fatalf("LTrim on missing key: %v", err)
	}
}

func TestLRem(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.RPush("l", [][]byte{
		[]byte("x"), []byte("a"), []byte("x"), []byte("b"), []byte("x"),
	}); err != nil {
		t.Fatal(err)
	}

	if n, err := s.LRem("l", 2, []byte("x")); err != nil || n != 2 {
		t.Fatalf("LRem(2) = %d, %v", n, err)
	}
	values, _ := s.LRange("l", 0, -1)
	if got := listStrings(t, values); !equalStrings(got, []string{"a", "b", "x"}) {
		t.Fatalf("after LRem(2) = %v", got)
	}

	if n, err := s.LRem("l", -1, []byte("x")); err != nil || n != 1 {
		t.Fatalf("LRem(-1) = %d, %v", n, err)
	}

	if _, err := s.RPush("m", [][]byte{[]byte("x"), []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.LRem("m", 0, []byte("x")); err != nil || n != 2 {
		t.Fatalf("LRem(0) = %d, %v", n, err)
	}
	if n := s.DBSize(); n != 1 {
		t.Fatalf("DBSize = %d, want the empty list removed", n)
	}
}

func TestListRingBuffer(t *testing.T) {
	s, _ := newTestStore()
	for i := 0; i < 20; i++ {
		if _, err := s.RPush("l", [][]byte{[]byte(strconv.Itoa(i))}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 15; i++ {
		if _, err := s.LPop("l", 1); err != nil {
			t.Fatal(err)
		}
	}
	for i := 20; i < 40; i++ {
		if _, err := s.LPush("l", [][]byte{[]byte(strconv.Itoa(i))}); err != nil {
			t.Fatal(err)
		}
	}

	values, err := s.LRange("l", 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 25 {
		t.Fatalf("len = %d, want 25", len(values))
	}
	if string(values[0]) != "39" || string(values[len(values)-1]) != "19" {
		t.Fatalf("ring buffer order is wrong: first %q last %q", values[0], values[len(values)-1])
	}
}

func TestListWrongType(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.Set("str", []byte("v"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LPush("str", [][]byte{[]byte("a")}); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}
	if _, err := s.LRange("str", 0, -1); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}

	if _, err := s.LPush("list", [][]byte{[]byte("a")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("list"); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}
}

func TestEmitSnapshotRebuildsList(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.RPush("l", [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}

	var commands [][]string
	s.EmitSnapshot(func(args [][]byte) {
		cmd := make([]string, len(args))
		for i, arg := range args {
			cmd[i] = string(arg)
		}
		commands = append(commands, cmd)
	})
	if len(commands) != 1 {
		t.Fatalf("snapshot emitted %d commands, want 1", len(commands))
	}
	if commands[0][0] != "RPUSH" || commands[0][1] != "l" || commands[0][2] != "a" || commands[0][4] != "c" {
		t.Fatalf("unexpected snapshot command: %v", commands[0])
	}
}
