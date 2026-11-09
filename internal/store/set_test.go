package store

import (
	"errors"
	"strconv"
	"testing"
)

func membersOf(t *testing.T, values [][]byte) []string {
	t.Helper()
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func TestSetBasics(t *testing.T) {
	s, _ := newTestStore()

	added, err := s.SAdd("s", [][]byte{[]byte("a"), []byte("b"), []byte("a")})
	if err != nil || added != 2 {
		t.Fatalf("SAdd = %d, %v; want 2", added, err)
	}
	if n, err := s.SCard("s"); err != nil || n != 2 {
		t.Fatalf("SCard = %d, %v", n, err)
	}
	if ok, err := s.SIsMember("s", []byte("a")); err != nil || !ok {
		t.Fatalf("SIsMember(a) = %v, %v", ok, err)
	}
	if ok, err := s.SIsMember("s", []byte("z")); err != nil || ok {
		t.Fatalf("SIsMember(z) = %v, %v", ok, err)
	}

	members, err := s.SMembers("s")
	if err != nil {
		t.Fatal(err)
	}
	if got := membersOf(t, members); !equalStrings(got, []string{"a", "b"}) {
		t.Fatalf("SMembers = %v", got)
	}

	removed, err := s.SRem("s", [][]byte{[]byte("a"), []byte("zzz")})
	if err != nil || removed != 1 {
		t.Fatalf("SRem = %d, %v", removed, err)
	}
	if _, err := s.SRem("s", [][]byte{[]byte("b")}); err != nil {
		t.Fatal(err)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("empty set should be removed, DBSize = %d", n)
	}
}

func TestSetAlgebra(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.SAdd("a", [][]byte{[]byte("1"), []byte("2"), []byte("3")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SAdd("b", [][]byte{[]byte("2"), []byte("3"), []byte("4")}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		op   func([]string) ([][]byte, error)
		keys []string
		want []string
	}{
		{"inter", s.SInter, []string{"a", "b"}, []string{"2", "3"}},
		{"union", s.SUnion, []string{"a", "b"}, []string{"1", "2", "3", "4"}},
		{"diff", s.SDiff, []string{"a", "b"}, []string{"1"}},
		{"inter-missing", s.SInter, []string{"a", "missing"}, nil},
		{"union-missing", s.SUnion, []string{"a", "missing"}, []string{"1", "2", "3"}},
		{"diff-missing", s.SDiff, []string{"a", "missing"}, []string{"1", "2", "3"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.op(tc.keys)
			if err != nil {
				t.Fatal(err)
			}
			if names := membersOf(t, got); !equalStrings(names, tc.want) {
				t.Fatalf("%s = %v, want %v", tc.name, names, tc.want)
			}
		})
	}
}

func TestSPop(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.SAdd("s", [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}

	popped, err := s.SPop("s", 2)
	if err != nil || len(popped) != 2 {
		t.Fatalf("SPop = %d members, %v", len(popped), err)
	}
	for _, member := range popped {
		ok, _ := s.SIsMember("s", member)
		if ok {
			t.Fatalf("popped member %q is still in the set", member)
		}
	}
	if n, _ := s.SCard("s"); n != 1 {
		t.Fatalf("SCard after pop = %d", n)
	}

	if _, err := s.SPop("s", 10); err != nil {
		t.Fatal(err)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("empty set should be removed, DBSize = %d", n)
	}
	if members, err := s.SPop("missing", 1); err != nil || len(members) != 0 {
		t.Fatalf("SPop on missing = %v, %v", members, err)
	}
}

func TestSRandMember(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.SAdd("s", [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
		t.Fatal(err)
	}

	unique, err := s.SRandMember("s", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := membersOf(t, unique); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Fatalf("SRandMember(10) = %v", got)
	}

	one, err := s.SRandMember("s", 1)
	if err != nil || len(one) != 1 {
		t.Fatalf("SRandMember(1) = %v, %v", one, err)
	}

	repeated, err := s.SRandMember("s", -10)
	if err != nil || len(repeated) != 10 {
		t.Fatalf("SRandMember(-10) length = %d, %v", len(repeated), err)
	}
	for _, member := range repeated {
		if string(member) != "a" && string(member) != "b" && string(member) != "c" {
			t.Fatalf("unexpected member %q", member)
		}
	}

	if n, _ := s.SCard("s"); n != 3 {
		t.Fatalf("SRandMember mutated the set, SCard = %d", n)
	}
}

func TestSScan(t *testing.T) {
	s, _ := newTestStore()
	members := make([][]byte, 0, 25)
	for i := 0; i < 25; i++ {
		members = append(members, []byte("m:"+strconv.Itoa(i)))
	}
	if _, err := s.SAdd("s", members); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	cursor := uint64(0)
	for i := 0; i < 100; i++ {
		next, page, err := s.SScan("s", cursor, "m:*", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, member := range page {
			seen[member] = true
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatalf("SScan saw %d members, want 25", len(seen))
	}
}

func TestSetWrongType(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.Set("str", []byte("v"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SAdd("str", [][]byte{[]byte("a")}); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}
	if _, err := s.SInter([]string{"str"}); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}

	if _, err := s.SAdd("set", [][]byte{[]byte("a")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("set"); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}
}

func TestEmitSnapshotRebuildsSet(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.SAdd("s", [][]byte{[]byte("a"), []byte("b")}); err != nil {
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
	if len(commands) != 1 || commands[0][0] != "SADD" || commands[0][1] != "s" {
		t.Fatalf("unexpected snapshot commands: %v", commands)
	}
	if len(commands[0]) != 4 {
		t.Fatalf("SADD snapshot has %d args, want 4", len(commands[0]))
	}
}
