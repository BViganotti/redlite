package store

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// Errors specific to sorted sets.
var (
	ErrInvalidFloat      = errors.New("value is not a valid float")
	ErrInvalidScoreRange = errors.New("min or max is not a float")
)

// ScoreMember is one member of a sorted set.
type ScoreMember struct {
	Score  float64
	Member string
}

// ZAddOptions is the conditional behaviour of ZADD.
type ZAddOptions struct {
	NX bool
	XX bool
	GT bool
	LT bool
	CH bool
}

// ZAddResult reports what ZADD changed.
type ZAddResult struct {
	Added    int64
	Changed  int64
	Score    float64
	HasScore bool
}

// ScoreRange is an inclusive range with optional exclusive bounds.
type ScoreRange struct {
	Min          float64
	Max          float64
	MinExclusive bool
	MaxExclusive bool
}

type zsetValue struct {
	dict map[string]float64
	list *skiplist
}

func (*zsetValue) kind() ValueType { return TypeZSet }

func (v *zsetValue) clone() value {
	c := newZSetValue()
	for member, score := range v.dict {
		c.dict[member] = score
		c.list.insert(member, score)
	}
	return c
}

func newZSetValue() *zsetValue {
	return &zsetValue{
		dict: make(map[string]float64),
		list: newSkiplist(),
	}
}

func (s *Store) zsetLocked(sh *shard, key string, now int64, create bool) (*zsetValue, error) {
	e, ok := s.lookupWrite(sh, key, now)
	if !ok {
		if !create {
			return nil, nil
		}
		zv := newZSetValue()
		sh.data[key] = s.newEntry(zv, 0)
		s.sets.Add(1)
		return zv, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return nil, ErrWrongType
	}
	return zv, nil
}

// ZAdd adds or updates members. With incr, pairs must contain exactly one entry
// and the result carries the resulting score.
func (s *Store) ZAdd(key string, opts ZAddOptions, pairs []ScoreMember, incr bool) (ZAddResult, error) {
	now := s.now()
	sh := s.lockWrite(key)
	defer s.unlockWrite(sh)

	zv, err := s.zsetLocked(sh, key, now, false)
	if err != nil {
		return ZAddResult{}, err
	}
	if zv == nil && opts.XX {
		return ZAddResult{}, nil
	}

	created := false
	if zv == nil {
		zv = newZSetValue()
		sh.data[key] = s.newEntry(zv, 0)
		s.sets.Add(1)
		created = true
	}

	var res ZAddResult
	for _, pair := range pairs {
		current, exists := zv.dict[pair.Member]

		target := pair.Score
		if incr {
			if exists {
				if opts.NX {
					continue
				}
				target = current + pair.Score
			} else if opts.XX {
				continue
			}
			// +inf plus -inf produces NaN, which Redis rejects.
			if math.IsNaN(target) {
				return ZAddResult{}, ErrInvalidFloat
			}
		}

		if exists {
			if opts.NX {
				continue
			}
			if opts.GT && target <= current {
				continue
			}
			if opts.LT && target >= current {
				continue
			}
			if target == current {
				if incr {
					res.Score = target
					res.HasScore = true
				}
				continue
			}
			zv.list.delete(pair.Member, current)
			zv.list.insert(pair.Member, target)
			zv.dict[pair.Member] = target
			res.Changed++
		} else {
			if opts.XX {
				continue
			}
			zv.dict[pair.Member] = target
			zv.list.insert(pair.Member, target)
			res.Added++
			res.Changed++
		}

		if incr {
			res.Score = target
			res.HasScore = true
		}
	}

	if created && zv.list.length == 0 {
		delete(sh.data, key)
	} else if res.Changed > 0 {
		s.touch(sh.data[key])
	}
	return res, nil
}

// ZRem removes members and reports how many existed.
func (s *Store) ZRem(key string, members []string) (int64, error) {
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

	var removed int64
	for _, member := range members {
		score, ok := zv.dict[member]
		if !ok {
			continue
		}
		delete(zv.dict, member)
		zv.list.delete(member, score)
		removed++
	}
	if len(zv.dict) == 0 {
		delete(sh.data, key)
	} else if removed > 0 {
		s.touch(sh.data[key])
	}
	return removed, nil
}

// ZCard returns the cardinality.
func (s *Store) ZCard(key string) (int64, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return 0, ErrWrongType
	}
	return int64(len(zv.dict)), nil
}

// ZScore returns one member's score.
func (s *Store) ZScore(key, member string) (float64, bool, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, false, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return 0, false, ErrWrongType
	}
	score, ok := zv.dict[member]
	return score, ok, nil
}

// ZIncrBy increments a member's score, creating it when missing.
func (s *Store) ZIncrBy(key string, increment float64, member string) (float64, error) {
	res, err := s.ZAdd(key, ZAddOptions{}, []ScoreMember{{Score: increment, Member: member}}, true)
	if err != nil {
		return 0, err
	}
	return res.Score, nil
}

// ZCount counts members inside a score range.
func (s *Store) ZCount(key string, r ScoreRange) (int64, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return 0, ErrWrongType
	}

	var count int64
	for node := zv.list.firstInScoreRange(r.Min, r.MinExclusive); node != nil; node = node.levels[0].forward {
		if !inUpperBound(node.score, r) {
			break
		}
		count++
	}
	return count, nil
}

// ZRank returns a 0-based rank, or false when the member is missing.
func (s *Store) ZRank(key, member string, reverse bool) (int64, bool, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, false, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return 0, false, ErrWrongType
	}
	score, ok := zv.dict[member]
	if !ok {
		return 0, false, nil
	}
	rank := zv.list.rank(member, score)
	if rank < 0 {
		return 0, false, nil
	}
	if reverse {
		rank = zv.list.length - 1 - rank
	}
	return int64(rank), true, nil
}

// ZRange returns an inclusive rank range. With reverse, rank 0 is the highest
// score.
func (s *Store) ZRange(key string, start, stop int64, reverse bool) ([]ScoreMember, error) {
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

	from, to, ok := normalizeRange(start, stop, zv.list.length)
	if !ok {
		return nil, nil
	}
	out := make([]ScoreMember, 0, to-from+1)
	for i := from; i <= to; i++ {
		rank := i
		if reverse {
			rank = zv.list.length - 1 - i
		}
		node := zv.list.nodeByRank(rank)
		if node == nil {
			continue
		}
		out = append(out, ScoreMember{Score: node.score, Member: node.member})
	}
	return out, nil
}

// ZRangeByScore returns members inside a score range, applying offset and count.
// A negative count means "no limit".
func (s *Store) ZRangeByScore(key string, r ScoreRange, offset, count int, reverse bool) ([]ScoreMember, error) {
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

	var out []ScoreMember
	appendNode := func(node *skipNode) bool {
		if !scoreAtLeast(node.score, r.Min, r.MinExclusive) || !inUpperBound(node.score, r) {
			return false
		}
		if offset > 0 {
			offset--
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
		for node != nil && !inUpperBound(node.score, r) {
			node = node.backward
		}
		for node != nil {
			if !appendNode(node) {
				break
			}
			node = node.backward
		}
	} else {
		node := zv.list.firstInScoreRange(r.Min, r.MinExclusive)
		for node != nil {
			if !appendNode(node) {
				break
			}
			node = node.levels[0].forward
		}
	}
	return out, nil
}

// ZRemRangeByRank removes an inclusive rank range.
func (s *Store) ZRemRangeByRank(key string, start, stop int64) (int64, error) {
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
	from, to, ok := normalizeRange(start, stop, zv.list.length)
	if !ok {
		return 0, nil
	}

	members := make([]ScoreMember, 0, to-from+1)
	for i := from; i <= to; i++ {
		node := zv.list.nodeByRank(i)
		if node != nil {
			members = append(members, ScoreMember{Score: node.score, Member: node.member})
		}
	}
	for _, member := range members {
		delete(zv.dict, member.Member)
		zv.list.delete(member.Member, member.Score)
	}
	if len(zv.dict) == 0 {
		delete(sh.data, key)
	} else if len(members) > 0 {
		s.touch(sh.data[key])
	}
	return int64(len(members)), nil
}

// ZRemRangeByScore removes every member inside a score range.
func (s *Store) ZRemRangeByScore(key string, r ScoreRange) (int64, error) {
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

	var members []ScoreMember
	for node := zv.list.firstInScoreRange(r.Min, r.MinExclusive); node != nil; node = node.levels[0].forward {
		if !inUpperBound(node.score, r) {
			break
		}
		members = append(members, ScoreMember{Score: node.score, Member: node.member})
	}
	for _, member := range members {
		delete(zv.dict, member.Member)
		zv.list.delete(member.Member, member.Score)
	}
	if len(zv.dict) == 0 {
		delete(sh.data, key)
	} else if len(members) > 0 {
		s.touch(sh.data[key])
	}
	return int64(len(members)), nil
}

// ZScan walks the sorted set from cursor, in rank order.
func (s *Store) ZScan(key string, cursor uint64, pattern string, count int) (uint64, []ScoreMember, error) {
	if count <= 0 {
		count = 10
	}
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return 0, nil, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return 0, nil, ErrWrongType
	}
	all := zv.list.all()
	if cursor >= uint64(len(all)) {
		return 0, nil, nil
	}

	var (
		out      []ScoreMember
		examined int
	)
	for examined < count && cursor < uint64(len(all)) {
		item := all[cursor]
		cursor++
		examined++
		if pattern != "" && pattern != "*" && !Match(pattern, item.Member) {
			continue
		}
		out = append(out, item)
	}
	if cursor >= uint64(len(all)) {
		cursor = 0
	}
	return cursor, out, nil
}

// ParseScore parses a score argument.
func ParseScore(arg []byte) (float64, error) {
	value, err := strconv.ParseFloat(string(arg), 64)
	if err != nil || math.IsNaN(value) {
		return 0, ErrInvalidFloat
	}
	return value, nil
}

// ParseScoreRange parses a min/max pair, including exclusive "(" bounds.
func ParseScoreRange(minArg, maxArg []byte) (ScoreRange, error) {
	min, minExclusive, err := parseScoreBound(minArg)
	if err != nil {
		return ScoreRange{}, err
	}
	max, maxExclusive, err := parseScoreBound(maxArg)
	if err != nil {
		return ScoreRange{}, err
	}
	return ScoreRange{
		Min:          min,
		Max:          max,
		MinExclusive: minExclusive,
		MaxExclusive: maxExclusive,
	}, nil
}

func parseScoreBound(arg []byte) (float64, bool, error) {
	raw := string(arg)
	exclusive := strings.HasPrefix(raw, "(")
	if exclusive {
		raw = raw[1:]
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) {
		return 0, false, ErrInvalidScoreRange
	}
	return value, exclusive, nil
}

// FormatScore renders a score the way Redis does for common values: integral
// scores have no decimal point and infinities are "inf"/"-inf".
func FormatScore(value float64) string {
	switch {
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	case value == 0:
		return "0"
	case value == math.Trunc(value) && math.Abs(value) < 1e17:
		return strconv.FormatInt(int64(value), 10)
	default:
		return strconv.FormatFloat(value, 'g', -1, 64)
	}
}

func inUpperBound(score float64, r ScoreRange) bool {
	if r.MaxExclusive {
		return score < r.Max
	}
	return score <= r.Max
}
