package store

import (
	"errors"
	"strconv"
	"testing"
)

func zsetMembers(entries []ScoreMember) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Member
	}
	return out
}

func TestZAddBasics(t *testing.T) {
	s, _ := newTestStore()

	res, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{
		{Score: 1, Member: "a"},
		{Score: 2, Member: "b"},
		{Score: 3, Member: "c"},
	}, false)
	if err != nil || res.Added != 3 || res.Changed != 3 {
		t.Fatalf("ZAdd = %+v, %v", res, err)
	}

	res, err = s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: 5, Member: "b"}}, false)
	if err != nil || res.Added != 0 || res.Changed != 1 {
		t.Fatalf("ZAdd update = %+v, %v", res, err)
	}

	res, err = s.ZAdd("z", ZAddOptions{CH: true}, []ScoreMember{{Score: 5, Member: "b"}}, false)
	if err != nil || res.Changed != 0 {
		t.Fatalf("ZAdd no-op = %+v, %v", res, err)
	}

	if n, err := s.ZCard("z"); err != nil || n != 3 {
		t.Fatalf("ZCard = %d, %v", n, err)
	}
	if score, ok, err := s.ZScore("z", "b"); err != nil || !ok || score != 5 {
		t.Fatalf("ZScore = %v, %v, %v", score, ok, err)
	}
	if _, ok, err := s.ZScore("z", "missing"); err != nil || ok {
		t.Fatalf("ZScore missing = %v, %v", ok, err)
	}
}

func TestZAddConditions(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: 10, Member: "a"}}, false); err != nil {
		t.Fatal(err)
	}

	// NX does not update an existing member and does add a new one.
	res, _ := s.ZAdd("z", ZAddOptions{NX: true}, []ScoreMember{
		{Score: 99, Member: "a"},
		{Score: 1, Member: "b"},
	}, false)
	if res.Added != 1 || res.Changed != 1 {
		t.Fatalf("NX = %+v", res)
	}
	if score, _, _ := s.ZScore("z", "a"); score != 10 {
		t.Fatalf("NX updated a: %v", score)
	}

	// XX only updates existing members.
	res, _ = s.ZAdd("z", ZAddOptions{XX: true}, []ScoreMember{
		{Score: 11, Member: "a"},
		{Score: 2, Member: "c"},
	}, false)
	if res.Added != 0 || res.Changed != 1 {
		t.Fatalf("XX = %+v", res)
	}
	if _, ok, _ := s.ZScore("z", "c"); ok {
		t.Fatal("XX added a new member")
	}

	// GT only raises.
	res, _ = s.ZAdd("z", ZAddOptions{GT: true}, []ScoreMember{
		{Score: 5, Member: "a"},
		{Score: 20, Member: "a"},
	}, false)
	if res.Changed != 1 {
		t.Fatalf("GT = %+v", res)
	}
	if score, _, _ := s.ZScore("z", "a"); score != 20 {
		t.Fatalf("GT final score = %v, want 20", score)
	}

	// LT only lowers.
	res, _ = s.ZAdd("z", ZAddOptions{LT: true}, []ScoreMember{
		{Score: 30, Member: "a"},
		{Score: 15, Member: "a"},
	}, false)
	if res.Changed != 1 {
		t.Fatalf("LT = %+v", res)
	}
	if score, _, _ := s.ZScore("z", "a"); score != 15 {
		t.Fatalf("LT final score = %v, want 15", score)
	}
}

func TestZIncrBy(t *testing.T) {
	s, _ := newTestStore()

	score, err := s.ZIncrBy("z", 5, "a")
	if err != nil || score != 5 {
		t.Fatalf("ZIncrBy = %v, %v", score, err)
	}
	score, err = s.ZIncrBy("z", -2, "a")
	if err != nil || score != 3 {
		t.Fatalf("ZIncrBy = %v, %v", score, err)
	}

	res, err := s.ZAdd("z", ZAddOptions{NX: true}, []ScoreMember{{Score: 10, Member: "a"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasScore {
		t.Fatalf("ZADD INCR NX on an existing member should return no score: %+v", res)
	}
}

func TestZRange(t *testing.T) {
	s, _ := newTestStore()
	for i, member := range []string{"a", "b", "c", "d"} {
		if _, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: float64(i), Member: member}}, false); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		start, stop int64
		reverse     bool
		want        []string
	}{
		{0, -1, false, []string{"a", "b", "c", "d"}},
		{1, 2, false, []string{"b", "c"}},
		{-2, -1, false, []string{"c", "d"}},
		{0, -1, true, []string{"d", "c", "b", "a"}},
		{0, 1, true, []string{"d", "c"}},
		{5, 10, false, nil},
	}
	for _, tc := range tests {
		got, err := s.ZRange("z", tc.start, tc.stop, tc.reverse)
		if err != nil {
			t.Fatalf("ZRange(%d, %d, %v): %v", tc.start, tc.stop, tc.reverse, err)
		}
		if members := zsetMembers(got); !equalStrings(members, tc.want) {
			t.Errorf("ZRange(%d, %d, %v) = %v, want %v", tc.start, tc.stop, tc.reverse, members, tc.want)
		}
	}
}

func TestZRangeByScore(t *testing.T) {
	s, _ := newTestStore()
	for i := 1; i <= 5; i++ {
		if _, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: float64(i), Member: "m" + strconv.Itoa(i)}}, false); err != nil {
			t.Fatal(err)
		}
	}

	r := ScoreRange{Min: 2, Max: 4}
	got, err := s.ZRangeByScore("z", r, 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	if members := zsetMembers(got); !equalStrings(members, []string{"m2", "m3", "m4"}) {
		t.Fatalf("range = %v", members)
	}

	exclusive := ScoreRange{Min: 2, Max: 4, MinExclusive: true, MaxExclusive: true}
	got, _ = s.ZRangeByScore("z", exclusive, 0, -1, false)
	if members := zsetMembers(got); !equalStrings(members, []string{"m3"}) {
		t.Fatalf("exclusive range = %v", members)
	}

	got, _ = s.ZRangeByScore("z", r, 1, 1, false)
	if members := zsetMembers(got); !equalStrings(members, []string{"m3"}) {
		t.Fatalf("limited range = %v", members)
	}

	got, _ = s.ZRangeByScore("z", r, 0, -1, true)
	if members := zsetMembers(got); !equalStrings(members, []string{"m4", "m3", "m2"}) {
		t.Fatalf("reverse range = %v", members)
	}

	unbounded := ScoreRange{Min: 0, Max: 10}
	if n, err := s.ZCount("z", unbounded); err != nil || n != 5 {
		t.Fatalf("ZCount = %d, %v", n, err)
	}
}

func TestZRank(t *testing.T) {
	s, _ := newTestStore()
	for i, member := range []string{"a", "b", "c"} {
		if _, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: float64(i), Member: member}}, false); err != nil {
			t.Fatal(err)
		}
	}

	if rank, ok, err := s.ZRank("z", "b", false); err != nil || !ok || rank != 1 {
		t.Fatalf("ZRank = %d, %v, %v", rank, ok, err)
	}
	if rank, ok, err := s.ZRank("z", "b", true); err != nil || !ok || rank != 1 {
		t.Fatalf("ZRevRank = %d, %v, %v", rank, ok, err)
	}
	if _, ok, err := s.ZRank("z", "missing", false); err != nil || ok {
		t.Fatalf("ZRank missing = %v, %v", ok, err)
	}
}

func TestZRem(t *testing.T) {
	s, _ := newTestStore()
	for i, member := range []string{"a", "b", "c"} {
		if _, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: float64(i), Member: member}}, false); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := s.ZRem("z", []string{"a", "missing"}); err != nil || n != 1 {
		t.Fatalf("ZRem = %d, %v", n, err)
	}
	if n, err := s.ZRem("z", []string{"b", "c"}); err != nil || n != 2 {
		t.Fatalf("ZRem = %d, %v", n, err)
	}
	if n := s.DBSize(); n != 0 {
		t.Fatalf("empty zset should be removed, DBSize = %d", n)
	}
}

func TestZRemRange(t *testing.T) {
	s, _ := newTestStore()
	for i := 1; i <= 5; i++ {
		if _, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: float64(i), Member: "m" + strconv.Itoa(i)}}, false); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.ZRemRangeByScore("z", ScoreRange{Min: 2, Max: 3})
	if err != nil || n != 2 {
		t.Fatalf("ZRemRangeByScore = %d, %v", n, err)
	}
	if n, _ := s.ZCard("z"); n != 3 {
		t.Fatalf("ZCard after score removal = %d", n)
	}

	n, err = s.ZRemRangeByRank("z", 0, 0)
	if err != nil || n != 1 {
		t.Fatalf("ZRemRangeByRank = %d, %v", n, err)
	}
	left, _ := s.ZRange("z", 0, -1, false)
	if members := zsetMembers(left); !equalStrings(members, []string{"m4", "m5"}) {
		t.Fatalf("remaining = %v", members)
	}
}

func TestZScan(t *testing.T) {
	s, _ := newTestStore()
	pairs := make([]ScoreMember, 0, 25)
	for i := 0; i < 25; i++ {
		pairs = append(pairs, ScoreMember{Score: float64(i), Member: "m:" + strconv.Itoa(i)})
	}
	if _, err := s.ZAdd("z", ZAddOptions{}, pairs, false); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	cursor := uint64(0)
	for i := 0; i < 100; i++ {
		next, page, err := s.ZScan("z", cursor, "m:*", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page {
			seen[item.Member] = true
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(seen) != 25 {
		t.Fatalf("ZScan saw %d members, want 25", len(seen))
	}
}

func TestZSetWrongType(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.Set("str", []byte("v"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ZAdd("str", ZAddOptions{}, []ScoreMember{{Score: 1, Member: "a"}}, false); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}
	if _, err := s.ZRange("str", 0, -1, false); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}

	if _, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: 1, Member: "a"}}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("z"); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want ErrWrongType, got %v", err)
	}
}

func TestParseScoreRange(t *testing.T) {
	r, err := ParseScoreRange([]byte("-inf"), []byte("+inf"))
	if err != nil || !isNegInf(r.Min) || !isPosInf(r.Max) {
		t.Fatalf("ParseScoreRange = %+v, %v", r, err)
	}
	r, err = ParseScoreRange([]byte("(1.5"), []byte("2"))
	if err != nil || r.Min != 1.5 || !r.MinExclusive || r.MaxExclusive {
		t.Fatalf("ParseScoreRange = %+v, %v", r, err)
	}
	if _, err := ParseScoreRange([]byte("nope"), []byte("1")); !errors.Is(err, ErrInvalidScoreRange) {
		t.Fatalf("want ErrInvalidScoreRange, got %v", err)
	}
}

func TestFormatScore(t *testing.T) {
	tests := map[float64]string{
		1:    "1",
		1.5:  "1.5",
		-2:   "-2",
		0:    "0",
		1e6:  "1000000",
		1e20: "1e+20",
	}
	for value, want := range tests {
		if got := FormatScore(value); got != want {
			t.Errorf("FormatScore(%v) = %q, want %q", value, got, want)
		}
	}
}

func TestEmitSnapshotRebuildsZSet(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.ZAdd("z", ZAddOptions{}, []ScoreMember{
		{Score: 2, Member: "b"},
		{Score: 1, Member: "a"},
	}, false); err != nil {
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
	if len(commands) != 1 || commands[0][0] != "ZADD" || commands[0][1] != "z" {
		t.Fatalf("unexpected snapshot commands: %v", commands)
	}
	if commands[0][2] != "1" || commands[0][3] != "a" || commands[0][4] != "2" || commands[0][5] != "b" {
		t.Fatalf("unexpected ZADD order: %v", commands[0])
	}
}

func isNegInf(v float64) bool { return v < -1e308 }
func isPosInf(v float64) bool { return v > 1e308 }
