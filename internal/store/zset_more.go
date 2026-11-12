package store

import "errors"

// ErrInvalidLexRange reports a malformed lexicographic range bound.
var ErrInvalidLexRange = errors.New("min or max not valid string range item")

// LexBound is one end of a lexicographic range. Infinite is -1 for "-", +1
// for "+" and 0 for a concrete value.
type LexBound struct {
	Value     string
	Infinite  int
	Exclusive bool
}

func (b LexBound) isMin() bool { return b.Infinite < 0 }
func (b LexBound) isMax() bool { return b.Infinite > 0 }

// LexRange is a lexicographic interval.
type LexRange struct {
	Min LexBound
	Max LexBound
}

// ParseLexBound parses "[value", "(value", "-" or "+".
func ParseLexBound(arg []byte) (LexBound, error) {
	if len(arg) == 0 {
		return LexBound{}, ErrInvalidLexRange
	}
	switch arg[0] {
	case '-':
		if len(arg) == 1 {
			return LexBound{Infinite: -1}, nil
		}
	case '+':
		if len(arg) == 1 {
			return LexBound{Infinite: 1}, nil
		}
	case '[':
		return LexBound{Value: string(arg[1:])}, nil
	case '(':
		return LexBound{Value: string(arg[1:]), Exclusive: true}, nil
	}
	return LexBound{}, ErrInvalidLexRange
}

// ParseLexRange parses a min/max pair.
func ParseLexRange(minArg, maxArg []byte) (LexRange, error) {
	min, err := ParseLexBound(minArg)
	if err != nil {
		return LexRange{}, err
	}
	max, err := ParseLexBound(maxArg)
	if err != nil {
		return LexRange{}, err
	}
	if min.isMax() || max.isMin() {
		return LexRange{}, ErrInvalidLexRange
	}
	return LexRange{Min: min, Max: max}, nil
}

func lexAtLeast(member string, bound LexBound) bool {
	if bound.isMin() {
		return true
	}
	if bound.isMax() {
		return false
	}
	if bound.Exclusive {
		return member > bound.Value
	}
	return member >= bound.Value
}

func lexAtMost(member string, bound LexBound) bool {
	if bound.isMax() {
		return true
	}
	if bound.isMin() {
		return false
	}
	if bound.Exclusive {
		return member < bound.Value
	}
	return member <= bound.Value
}

// lexEntries selects the nodes of a sorted set that fall inside a lexicographic
// range. The traversal mirrors Redis: it walks the (score, member) skiplist and
// takes the contiguous run that starts at the first member satisfying the lower
// bound (or the last satisfying the upper bound when reversed).
func lexEntries(zv *zsetValue, r LexRange, offset, count int, reverse bool) []ScoreMember {
	var out []ScoreMember
	skipped := 0
	appendNode := func(node *skipNode) bool {
		if skipped < offset {
			skipped++
			return true
		}
		if count == 0 {
			return false
		}
		out = append(out, ScoreMember{Score: node.score, Member: node.member})
		if count > 0 {
			count--
		}
		return count != 0
	}

	if reverse {
		node := zv.list.tail
		for node != nil && !lexAtMost(node.member, r.Max) {
			node = node.backward
		}
		for node != nil {
			if !lexAtLeast(node.member, r.Min) {
				break
			}
			if !appendNode(node) {
				break
			}
			node = node.backward
		}
		return out
	}

	node := zv.list.head.levels[0].forward
	for node != nil && !lexAtLeast(node.member, r.Min) {
		node = node.levels[0].forward
	}
	for node != nil {
		if !lexAtMost(node.member, r.Max) {
			break
		}
		if !appendNode(node) {
			break
		}
		node = node.levels[0].forward
	}
	return out
}

// ZRangeByLex returns members inside a lexicographic range.
func (s *Store) ZRangeByLex(key string, r LexRange, offset, count int, reverse bool) ([]ScoreMember, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return nil, ErrWrongType
	}
	return lexEntries(zv, r, offset, count, reverse), nil
}

// ZLexCount counts members inside a lexicographic range.
func (s *Store) ZLexCount(key string, r LexRange) (int64, error) {
	entries, err := s.ZRangeByLex(key, r, 0, -1, false)
	if err != nil {
		return 0, err
	}
	return int64(len(entries)), nil
}

// ZRemRangeByLex removes every member inside a lexicographic range.
func (s *Store) ZRemRangeByLex(key string, r LexRange) (int64, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	zv, err := s.zsetLocked(sh, key, now, false)
	if err != nil {
		return 0, err
	}
	if zv == nil {
		return 0, nil
	}
	entries := lexEntries(zv, r, 0, -1, false)
	for _, entry := range entries {
		delete(zv.dict, entry.Member)
		zv.list.delete(entry.Member, entry.Score)
	}
	if len(zv.dict) == 0 {
		delete(sh.data, key)
	} else if len(entries) > 0 {
		s.touch(sh.data[key])
	}
	return int64(len(entries)), nil
}

// ZRangeKind selects how ZRANGE interprets its bounds.
type ZRangeKind int

const (
	ZRangeRank ZRangeKind = iota
	ZRangeScore
	ZRangeLex
)

// ZRangeSpec is a parsed ZRANGE request.
type ZRangeSpec struct {
	Kind      ZRangeKind
	Rev       bool
	RankStart int64
	RankStop  int64
	Score     ScoreRange
	Lex       LexRange
	Offset    int
	Count     int // negative means unlimited
	HasLimit  bool
}

// Apply returns the requested members of a sorted set value. The caller must
// hold at least the shard read lock.
func applyZRange(zv *zsetValue, spec ZRangeSpec) []ScoreMember {
	switch spec.Kind {
	case ZRangeScore:
		return zrangeByScoreLocked(zv, spec.Score, spec.Offset, spec.Count, spec.Rev)
	case ZRangeLex:
		return lexEntries(zv, spec.Lex, spec.Offset, spec.Count, spec.Rev)
	default:
		from, to, ok := normalizeRange(spec.RankStart, spec.RankStop, zv.list.length)
		if !ok {
			return nil
		}
		out := make([]ScoreMember, 0, to-from+1)
		for i := from; i <= to; i++ {
			rank := i
			if spec.Rev {
				rank = zv.list.length - 1 - i
			}
			node := zv.list.nodeByRank(rank)
			if node == nil {
				continue
			}
			out = append(out, ScoreMember{Score: node.score, Member: node.member})
		}
		return out
	}
}

// ZRangeGeneric evaluates a unified ZRANGE request.
func (s *Store) ZRangeGeneric(key string, spec ZRangeSpec) ([]ScoreMember, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return nil, ErrWrongType
	}
	return applyZRange(zv, spec), nil
}

// ZRangeStore evaluates a range on src and stores the result at dst.
func (s *Store) ZRangeStore(dst, src string, spec ZRangeSpec) (int64, error) {
	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)

	now := s.now()
	srcShard := s.shardFor(src)
	srcEntry, ok := s.lookupRead(srcShard, src, now)
	var entries []ScoreMember
	if ok {
		zv, ok := srcEntry.value.(*zsetValue)
		if !ok {
			return 0, ErrWrongType
		}
		entries = applyZRange(zv, spec)
	}

	dstShard := s.shardFor(dst)
	if len(entries) == 0 {
		delete(dstShard.data, dst)
		return 0, nil
	}
	zv := newZSetValue()
	for _, entry := range entries {
		zv.dict[entry.Member] = entry.Score
		zv.list.insert(entry.Member, entry.Score)
	}
	dstShard.data[dst] = s.newEntry(zv, 0)
	s.sets.Add(1)
	return int64(len(entries)), nil
}

// ZInterCard counts members present in every sorted set, stopping at limit.
func (s *Store) ZInterCard(keys []string, limit int64) (int64, error) {
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	sets := make([]map[string]float64, 0, len(keys))
	for _, key := range keys {
		e, ok := s.lookupRead(s.shardFor(key), key, now)
		if !ok {
			sets = append(sets, nil)
			continue
		}
		zv, ok := e.value.(*zsetValue)
		if !ok {
			return 0, ErrWrongType
		}
		sets = append(sets, zv.dict)
	}
	if len(sets) == 0 {
		return 0, nil
	}
	for _, set := range sets {
		if set == nil {
			return 0, nil
		}
	}
	smallest := 0
	for i := 1; i < len(sets); i++ {
		if len(sets[i]) < len(sets[smallest]) {
			smallest = i
		}
	}
	var count int64
	for member := range sets[smallest] {
		present := true
		for i, set := range sets {
			if i == smallest {
				continue
			}
			if _, ok := set[member]; !ok {
				present = false
				break
			}
		}
		if present {
			count++
			if limit > 0 && count >= limit {
				return count, nil
			}
		}
	}
	return count, nil
}

// ZMPop pops up to count members from the first non-empty sorted set among
// keys, taking the lowest or highest scores.
func (s *Store) ZMPop(keys []string, min bool, count int) (string, []ScoreMember, error) {
	if count <= 0 {
		return "", nil, nil
	}
	locked := s.lockKeys(keys)
	defer s.unlockKeys(locked)

	now := s.now()
	for _, key := range keys {
		sh := s.shardFor(key)
		e, ok := s.lookupWrite(sh, key, now)
		if !ok {
			continue
		}
		zv, ok := e.value.(*zsetValue)
		if !ok {
			return "", nil, ErrWrongType
		}
		if len(zv.dict) == 0 {
			delete(sh.data, key)
			continue
		}

		limit := count
		if limit > len(zv.dict) {
			limit = len(zv.dict)
		}
		out := make([]ScoreMember, 0, limit)
		for i := 0; i < limit; i++ {
			var node *skipNode
			if min {
				node = zv.list.nodeByRank(0)
			} else {
				node = zv.list.nodeByRank(zv.list.length - 1)
			}
			if node == nil {
				break
			}
			out = append(out, ScoreMember{Score: node.score, Member: node.member})
			delete(zv.dict, node.member)
			zv.list.delete(node.member, node.score)
		}
		if len(zv.dict) == 0 {
			delete(sh.data, key)
		} else {
			s.touch(sh.data[key])
		}
		return key, out, nil
	}
	return "", nil, nil
}
