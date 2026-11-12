package store

import (
	"bytes"
	"errors"
	"testing"
)

func TestSetStores(t *testing.T) {
	s, _ := newTestStore()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.SAdd("a", [][]byte{[]byte("1"), []byte("2")})
	must(err)
	_, err = s.SAdd("b", [][]byte{[]byte("2"), []byte("3")})
	must(err)

	n, err := s.SStore("INTER", "i", []string{"a", "b"})
	must(err)
	if n != 1 {
		t.Fatalf("SINTERSTORE cardinality = %d", n)
	}
	members, _ := s.SMembers("i")
	if len(members) != 1 || string(members[0]) != "2" {
		t.Fatalf("intersection = %v", membersOf(t, members))
	}

	n, err = s.SStore("UNION", "u", []string{"a", "b"})
	must(err)
	if n != 3 {
		t.Fatalf("SUNIONSTORE cardinality = %d", n)
	}
	n, err = s.SStore("DIFF", "d", []string{"a", "b"})
	must(err)
	if n != 1 {
		t.Fatalf("SDIFFSTORE cardinality = %d", n)
	}

	// Storing an empty result deletes the destination.
	_, err = s.SAdd("empty", [][]byte{})
	must(err)
	n, err = s.SStore("INTER", "d", []string{"a", "empty"})
	must(err)
	if n != 0 || s.Exists([]string{"d"}) != 0 {
		t.Fatalf("empty store left destination behind")
	}
}

func TestSetStoreWrongTypeIsAtomic(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.SAdd("src", [][]byte{[]byte("a")})
	_, _ = s.Set("dst", []byte("survivor"), SetOptions{})
	if _, err := s.SStore("UNION", "dst", []string{"src", "missing"}); err != nil {
		t.Fatalf("store over missing key: %v", err)
	}
	// The destination is overwritten, not left as a string.
	if got, _, _ := s.Get("dst"); got != nil {
		t.Fatalf("destination still a string: %q", got)
	}

	_, _ = s.Set("str", []byte("v"), SetOptions{})
	if _, err := s.SStore("UNION", "dst", []string{"src", "str"}); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want WRONGTYPE, got %v", err)
	}
}

func TestSMove(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.SAdd("src", [][]byte{[]byte("a"), []byte("b")})
	_, _ = s.SAdd("dst", [][]byte{[]byte("c")})

	moved, err := s.SMove("src", "dst", []byte("a"))
	if err != nil || !moved {
		t.Fatalf("SMove = %v, %v", moved, err)
	}
	if ok, _ := s.SIsMember("src", []byte("a")); ok {
		t.Fatal("member still in source")
	}
	if ok, _ := s.SIsMember("dst", []byte("a")); !ok {
		t.Fatal("member not in destination")
	}
	if moved, err := s.SMove("src", "dst", []byte("nope")); err != nil || moved {
		t.Fatalf("moving missing member = %v, %v", moved, err)
	}
	if moved, err := s.SMove("missing", "dst", []byte("x")); err != nil || moved {
		t.Fatalf("moving from missing key = %v, %v", moved, err)
	}

	// Destination type is checked before the source is mutated.
	_, _ = s.Set("str", []byte("v"), SetOptions{})
	if _, err := s.SMove("src", "str", []byte("b")); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want WRONGTYPE, got %v", err)
	}
	if ok, _ := s.SIsMember("src", []byte("b")); !ok {
		t.Fatal("source was mutated despite destination error")
	}
}

func TestSInterCard(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.SAdd("a", [][]byte{[]byte("1"), []byte("2"), []byte("3")})
	_, _ = s.SAdd("b", [][]byte{[]byte("2"), []byte("3"), []byte("4")})

	n, err := s.SInterCard([]string{"a", "b"}, 0)
	if err != nil || n != 2 {
		t.Fatalf("SInterCard = %d, %v", n, err)
	}
	n, err = s.SInterCard([]string{"a", "b"}, 1)
	if err != nil || n != 1 {
		t.Fatalf("limited SInterCard = %d, %v", n, err)
	}
	n, err = s.SInterCard([]string{"a", "missing"}, 0)
	if err != nil || n != 0 {
		t.Fatalf("missing SInterCard = %d, %v", n, err)
	}

	_, _ = s.Set("str", []byte("v"), SetOptions{})
	if _, err := s.SInterCard([]string{"missing", "str"}, 0); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want WRONGTYPE even after a miss, got %v", err)
	}
}

func TestLMPop(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.RPush("l1", [][]byte{[]byte("a"), []byte("b")})
	_, _ = s.RPush("l2", [][]byte{[]byte("c")})

	key, values, err := s.LMPop([]string{"missing", "l1", "l2"}, true, 1)
	if err != nil || key != "l1" || len(values) != 1 || string(values[0]) != "a" {
		t.Fatalf("LMPop = %q %v %v", key, values, err)
	}
	key, values, err = s.LMPop([]string{"missing", "l1"}, false, 5)
	if err != nil || key != "l1" || len(values) != 1 || string(values[0]) != "b" {
		t.Fatalf("LMPop rest = %q %v %v", key, values, err)
	}
	if s.Exists([]string{"l1"}) != 0 {
		t.Fatal("empty list was not deleted")
	}
	key, values, err = s.LMPop([]string{"l1", "l2"}, true, 1)
	if err != nil || key != "l2" || len(values) != 1 {
		t.Fatalf("LMPop second key = %q %v %v", key, values, err)
	}
	key, values, err = s.LMPop([]string{"l1", "l2"}, true, 1)
	if err != nil || key != "" || values != nil {
		t.Fatalf("LMPop empty = %q %v %v", key, values, err)
	}

	_, _ = s.Set("str", []byte("v"), SetOptions{})
	if _, _, err := s.LMPop([]string{"str"}, true, 1); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want WRONGTYPE, got %v", err)
	}
}

func TestLexRanges(t *testing.T) {
	s, _ := newTestStore()
	for _, member := range []string{"a", "b", "c", "d"} {
		_, _ = s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: 0, Member: member}}, false)
	}

	r, err := ParseLexRange([]byte("[b"), []byte("[c"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.ZRangeByLex("z", r, 0, -1, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := memberNames(entries); !equalStrings(got, []string{"b", "c"}) {
		t.Fatalf("lex range = %v", got)
	}

	r, _ = ParseLexRange([]byte("-"), []byte("+"))
	entries, _ = s.ZRangeByLex("z", r, 0, -1, true)
	if got := memberNames(entries); !equalStrings(got, []string{"d", "c", "b", "a"}) {
		t.Fatalf("reverse lex = %v", got)
	}

	entries, _ = s.ZRangeByLex("z", r, 1, 2, false)
	if got := memberNames(entries); !equalStrings(got, []string{"b", "c"}) {
		t.Fatalf("limited lex = %v", got)
	}

	n, err := s.ZLexCount("z", r)
	if err != nil || n != 4 {
		t.Fatalf("ZLexCount = %d, %v", n, err)
	}
	n, err = s.ZRemRangeByLex("z", mustLex(t, "[a", "[b"))
	if err != nil || n != 2 {
		t.Fatalf("ZRemRangeByLex = %d, %v", n, err)
	}
	if card, _ := s.ZCard("z"); card != 2 {
		t.Fatalf("after lex removal card = %d", card)
	}

	if _, err := ParseLexRange([]byte("bad"), []byte("+")); !errors.Is(err, ErrInvalidLexRange) {
		t.Fatalf("want ErrInvalidLexRange, got %v", err)
	}
}

func mustLex(t *testing.T, min, max string) LexRange {
	t.Helper()
	r, err := ParseLexRange([]byte(min), []byte(max))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func memberNames(entries []ScoreMember) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Member
	}
	return out
}

func TestZRangeGenericAndStore(t *testing.T) {
	s, _ := newTestStore()
	for i, member := range []string{"a", "b", "c"} {
		_, _ = s.ZAdd("z", ZAddOptions{}, []ScoreMember{{Score: float64(i + 1), Member: member}}, false)
	}

	spec := ZRangeSpec{Kind: ZRangeScore, Score: ScoreRange{Min: 2, Max: 3}, Count: -1}
	entries, err := s.ZRangeGeneric("z", spec)
	if err != nil {
		t.Fatal(err)
	}
	if got := memberNames(entries); !equalStrings(got, []string{"b", "c"}) {
		t.Fatalf("score range = %v", got)
	}

	n, err := s.ZRangeStore("dst", "z", spec)
	if err != nil || n != 2 {
		t.Fatalf("ZRangeStore = %d, %v", n, err)
	}
	if card, _ := s.ZCard("dst"); card != 2 {
		t.Fatalf("stored card = %d", card)
	}

	spec = ZRangeSpec{Kind: ZRangeRank, RankStart: 0, RankStop: -1, Rev: true, Count: -1}
	entries, _ = s.ZRangeGeneric("z", spec)
	if got := memberNames(entries); !equalStrings(got, []string{"c", "b", "a"}) {
		t.Fatalf("reverse rank = %v", got)
	}
}

func TestZInterCardAndMPop(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.ZAdd("a", ZAddOptions{}, []ScoreMember{{1, "x"}, {2, "y"}}, false)
	_, _ = s.ZAdd("b", ZAddOptions{}, []ScoreMember{{1, "y"}, {2, "z"}}, false)

	n, err := s.ZInterCard([]string{"a", "b"}, 0)
	if err != nil || n != 1 {
		t.Fatalf("ZInterCard = %d, %v", n, err)
	}
	n, err = s.ZInterCard([]string{"a", "b"}, 1)
	if err != nil || n != 1 {
		t.Fatalf("limited ZInterCard = %d, %v", n, err)
	}

	key, entries, err := s.ZMPop([]string{"missing", "a"}, true, 1)
	if err != nil || key != "a" || len(entries) != 1 || entries[0].Member != "x" {
		t.Fatalf("ZMPop = %q %v %v", key, entries, err)
	}
	key, entries, err = s.ZMPop([]string{"a"}, false, 5)
	if err != nil || key != "a" || len(entries) != 1 || entries[0].Member != "y" {
		t.Fatalf("ZMPop max = %q %v %v", key, entries, err)
	}
	if s.Exists([]string{"a"}) != 0 {
		t.Fatal("empty zset was not deleted")
	}
}

func TestBitFieldStore(t *testing.T) {
	s, _ := newTestStore()

	res, err := s.BitField("bits", []BitFieldOp{
		{Kind: 'S', Signed: false, Bits: 8, Offset: 0, Value: 255, Overflow: 'W'},
		{Kind: 'G', Signed: false, Bits: 8, Offset: 0},
		{Kind: 'I', Signed: false, Bits: 8, Offset: 0, Value: 10, Overflow: 'W'},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0, 255, 9}
	for i, value := range want {
		if !res[i].Valid || res[i].Value != value {
			t.Fatalf("op %d = %+v, want %d", i, res[i], value)
		}
	}

	// Saturating and failing increments.
	res, _ = s.BitField("bits", []BitFieldOp{
		{Kind: 'S', Signed: false, Bits: 8, Offset: 0, Value: 250, Overflow: 'W'},
		{Kind: 'I', Signed: false, Bits: 8, Offset: 0, Value: 10, Overflow: 'S'},
	}, false)
	if res[1].Value != 255 {
		t.Fatalf("saturate = %+v", res[1])
	}
	res, _ = s.BitField("bits", []BitFieldOp{
		{Kind: 'I', Signed: false, Bits: 8, Offset: 0, Value: 10, Overflow: 'F'},
	}, false)
	if res[0].Valid {
		t.Fatalf("fail overflow should be invalid: %+v", res[0])
	}

	// Signed negative values and i64.
	res, _ = s.BitField("signed", []BitFieldOp{
		{Kind: 'S', Signed: true, Bits: 8, Offset: 0, Value: -5, Overflow: 'W'},
		{Kind: 'G', Signed: true, Bits: 8, Offset: 0},
	}, false)
	if res[1].Value != -5 {
		t.Fatalf("signed read = %+v", res[1])
	}
	res, _ = s.BitField("i64", []BitFieldOp{
		{Kind: 'S', Signed: true, Bits: 64, Offset: 0, Value: -5, Overflow: 'W'},
		{Kind: 'G', Signed: true, Bits: 64, Offset: 0},
	}, false)
	if res[1].Value != -5 {
		t.Fatalf("i64 read = %+v", res[1])
	}

	// GET on a missing key does not create it.
	res, _ = s.BitField("absent", []BitFieldOp{{Kind: 'G', Signed: false, Bits: 8, Offset: 0}}, true)
	if res[0].Value != 0 {
		t.Fatalf("missing GET = %+v", res[0])
	}
	if s.Exists([]string{"absent"}) != 0 {
		t.Fatal("GET created the key")
	}

	_, _ = s.RPush("list", [][]byte{[]byte("v")})
	if _, err := s.BitField("list", []BitFieldOp{{Kind: 'G', Bits: 8}}, true); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want WRONGTYPE, got %v", err)
	}
}

func TestSortStore(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.RPush("l", [][]byte{[]byte("3"), []byte("1"), []byte("2")})

	items, err := s.Sort("l", SortOptions{Count: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || string(items[0].Value) != "1" {
		t.Fatalf("numeric sort = %v", items)
	}

	items, err = s.Sort("l", SortOptions{Count: -1, Alpha: true, Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(items[0].Value) != "3" || string(items[2].Value) != "1" {
		t.Fatalf("alpha desc = %v", items)
	}

	_, _ = s.Set("w_1", []byte("10"), SetOptions{})
	_, _ = s.Set("w_2", []byte("5"), SetOptions{})
	_, _ = s.Set("w_3", []byte("1"), SetOptions{})
	_, _ = s.Set("d_1", []byte("a"), SetOptions{})
	_, _ = s.Set("d_3", []byte("c"), SetOptions{})
	items, err = s.Sort("l", SortOptions{ByPattern: "w_*", GetPatterns: []string{"#", "d_*"}, Count: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 || string(items[0].Value) != "3" || string(items[1].Value) != "c" {
		t.Fatalf("BY/GET sort = %v", items)
	}

	// Missing BY values sort as zero rather than failing.
	items, err = s.Sort("l", SortOptions{ByPattern: "missing_*", Count: -1})
	if err != nil {
		t.Fatalf("missing BY: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("missing BY count = %d", len(items))
	}

	items, err = s.Sort("l", SortOptions{Count: -1, Store: "dst"})
	if err != nil || len(items) != 3 {
		t.Fatalf("SORT STORE = %v, %v", items, err)
	}
	length, _ := s.LLen("dst")
	if length != 3 {
		t.Fatalf("stored list length = %d", length)
	}

	_, _ = s.Set("str", []byte("v"), SetOptions{})
	if _, err := s.Sort("str", SortOptions{Count: -1}); !errors.Is(err, ErrWrongType) {
		t.Fatalf("want WRONGTYPE, got %v", err)
	}
}

func TestDumpRestoreRoundTrip(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.Set("str", []byte("hello"), SetOptions{})
	_, _ = s.HSet("hash", []KeyValue{{Key: "a", Value: []byte("1")}})
	_, _ = s.RPush("list", [][]byte{[]byte("x"), []byte("y")})
	_, _ = s.SAdd("set", [][]byte{[]byte("m")})
	_, _ = s.ZAdd("zset", ZAddOptions{}, []ScoreMember{{1.5, "m"}}, false)

	for _, key := range []string{"str", "hash", "list", "set", "zset"} {
		payload, ok, err := s.Dump(key)
		if err != nil || !ok {
			t.Fatalf("Dump(%s) = %v, %v", key, ok, err)
		}
		if err := s.Restore("copy_"+key, payload, 0, false); err != nil {
			t.Fatalf("Restore(%s): %v", key, err)
		}
		if s.Type("copy_"+key) != s.Type(key) {
			t.Fatalf("%s type mismatch: %s vs %s", key, s.Type("copy_"+key), s.Type(key))
		}
	}
	if value, _, _ := s.Get("copy_str"); !bytes.Equal(value, []byte("hello")) {
		t.Fatalf("restored string = %q", value)
	}
	payload, _, _ := s.Dump("str")
	if err := s.Restore("copy_str", payload, 0, false); !errors.Is(err, ErrBusyKey) {
		t.Fatalf("want ErrBusyKey, got %v", err)
	}
	if err := s.Restore("copy_str", []byte("x"), 0, true); !errors.Is(err, ErrDumpPayload) {
		t.Fatalf("want ErrDumpPayload, got %v", err)
	}
	if _, ok, _ := s.Dump("missing"); ok {
		t.Fatal("Dump reported a missing key")
	}
}

func TestDumpMatchesRedisStringPayload(t *testing.T) {
	// Payload captured from Redis 8.8 for SET key hello: type 0, bulk
	// "hello", RDB version 14, CRC64.
	fixture := []byte{
		0x00, 0x05, 'h', 'e', 'l', 'l', 'o',
		0x0e, 0x00,
		0x11, 0x29, 0x07, 0x09, 0x36, 0x07, 0x34, 0x7b,
	}
	s, _ := newTestStore()
	if err := s.Restore("k", fixture, 0, false); err != nil {
		t.Fatalf("Restore of Redis payload: %v", err)
	}
	if value, _, _ := s.Get("k"); string(value) != "hello" {
		t.Fatalf("restored value = %q", value)
	}

	payload, _, err := s.Dump("k")
	if err != nil {
		t.Fatal(err)
	}
	// Our writer uses object version 9, which every supported Redis accepts,
	// but the body and checksum must still be well formed.
	if payload[0] != 0x00 || string(payload[1:7]) != "\x05hello" {
		t.Fatalf("unexpected dump body: %x", payload[:7])
	}
	if len(payload) < 17 {
		t.Fatalf("dump too short: %d", len(payload))
	}
}

func TestLCSMatchesRedis(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.Set("a", []byte("ohmytext"), SetOptions{})
	_, _ = s.Set("b", []byte("mynewtext"), SetOptions{})

	res, err := s.LCS("a", "b", false, false, 0)
	if err != nil || string(res.Sequence) != "mytext" || res.Length != 6 {
		t.Fatalf("LCS = %q len=%d err=%v", res.Sequence, res.Length, err)
	}
	res, err = s.LCS("a", "b", false, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 2 {
		t.Fatalf("matches = %+v", res.Matches)
	}
	// The first match in Redis is [4,7] and [5,8].
	first := res.Matches[0]
	if first.AStart != 4 || first.AEnd != 7 || first.BStart != 5 || first.BEnd != 8 {
		t.Fatalf("first match = %+v", first)
	}
	res, _ = s.LCS("a", "b", false, true, 4)
	if len(res.Matches) != 1 {
		t.Fatalf("minmatchlen matches = %+v", res.Matches)
	}

	_, _ = s.RPush("list", [][]byte{[]byte("x")})
	if _, err := s.LCS("a", "list", false, false, 0); !errors.Is(err, ErrLCSWrongType) {
		t.Fatalf("want ErrLCSWrongType, got %v", err)
	}
}

func TestDigestHex(t *testing.T) {
	if got := DigestHex([]byte("v2")); got != "2225df3932692638" {
		t.Fatalf("XXH3 digest = %s", got)
	}
	if got := DigestHex([]byte("hello")); got != "9555e8555c62dcfd" {
		t.Fatalf("XXH3 digest = %s", got)
	}
}
