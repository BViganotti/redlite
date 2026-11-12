package server

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("ZADD", -4, flagWrite, cmdZAdd)
	registerCommand("ZREM", -3, flagWrite|flagFast, cmdZRem)
	registerCommand("ZSCORE", 3, flagReadOnly|flagFast, cmdZScore)
	registerCommand("ZMSCORE", -3, flagReadOnly|flagFast, cmdZMScore)
	registerCommand("ZINCRBY", 4, flagWrite|flagFast, cmdZIncrBy)
	registerCommand("ZCARD", 2, flagReadOnly|flagFast, cmdZCard)
	registerCommand("ZCOUNT", 4, flagReadOnly, cmdZCount)
	registerCommand("ZRANGE", -4, flagReadOnly, cmdZRange)
	registerCommand("ZREVRANGE", -4, flagReadOnly, cmdZRange)
	registerCommand("ZRANGEBYSCORE", -4, flagReadOnly, cmdZRangeByScore)
	registerCommand("ZREVRANGEBYSCORE", -4, flagReadOnly, cmdZRangeByScore)
	registerCommand("ZRANK", -3, flagReadOnly|flagFast, cmdZRank)
	registerCommand("ZREVRANK", -3, flagReadOnly|flagFast, cmdZRank)
	registerCommand("ZREMRANGEBYRANK", 4, flagWrite, cmdZRemRangeByRank)
	registerCommand("ZREMRANGEBYSCORE", 4, flagWrite, cmdZRemRangeByScore)
	registerCommand("ZSCAN", -3, flagReadOnly, cmdZScan)
}

var errInvalidFloat = errors.New("ERR value is not a valid float")
var errInvalidScoreRange = errors.New("ERR min or max is not a float")

func cmdZAdd(c *client, args [][]byte) (result, error) {
	opts := store.ZAddOptions{}
	incr := false
	index := 2

parseOptions:
	for index < len(args) {
		switch strings.ToUpper(string(args[index])) {
		case "NX":
			opts.NX = true
		case "XX":
			opts.XX = true
		case "GT":
			opts.GT = true
		case "LT":
			opts.LT = true
		case "CH":
			opts.CH = true
		case "INCR":
			incr = true
		default:
			break parseOptions
		}
		index++
	}

	if opts.NX && opts.XX {
		return result{}, fmt.Errorf("ERR XX and NX options at the same time are not compatible")
	}
	if opts.GT && opts.LT {
		return result{}, fmt.Errorf("ERR GT and LT options at the same time are not compatible")
	}
	if (opts.GT || opts.LT) && opts.NX {
		return result{}, fmt.Errorf("ERR GT, LT, and/or NX options at the same time are not compatible")
	}

	rest := args[index:]
	if len(rest) == 0 || len(rest)%2 != 0 {
		return result{}, errSyntax
	}
	if incr && len(rest) != 2 {
		return result{}, fmt.Errorf("ERR INCR option supports a single increment-element pair")
	}

	pairs := make([]store.ScoreMember, 0, len(rest)/2)
	for i := 0; i < len(rest); i += 2 {
		score, err := store.ParseScore(rest[i])
		if err != nil {
			return result{}, errInvalidFloat
		}
		pairs = append(pairs, store.ScoreMember{Score: score, Member: string(rest[i+1])})
	}

	res, err := c.srv.data.ZAdd(string(args[1]), opts, pairs, incr)
	if err != nil {
		return result{}, err
	}

	if incr {
		if !res.HasScore {
			return result{reply: null}, nil
		}
		return result{reply: doubleReply(res.Score), mutated: true}, nil
	}

	count := res.Added
	if opts.CH {
		count = res.Changed
	}
	return result{reply: intReply(count), mutated: res.Changed > 0}, nil
}

func cmdZRem(c *client, args [][]byte) (result, error) {
	members := make([]string, 0, len(args)-2)
	for _, member := range args[2:] {
		members = append(members, string(member))
	}
	removed, err := c.srv.data.ZRem(string(args[1]), members)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(removed), mutated: removed > 0}, nil
}

func cmdZScore(c *client, args [][]byte) (result, error) {
	score, ok, err := c.srv.data.ZScore(string(args[1]), string(args[2]))
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: null}, nil
	}
	return result{reply: doubleReply(score)}, nil
}

func cmdZMScore(c *client, args [][]byte) (result, error) {
	out := make(arrayReply, 0, len(args)-2)
	for _, member := range args[2:] {
		score, ok, err := c.srv.data.ZScore(string(args[1]), string(member))
		if err != nil {
			return result{}, err
		}
		if !ok {
			out = append(out, null)
			continue
		}
		out = append(out, doubleReply(score))
	}
	return result{reply: out}, nil
}

func cmdZIncrBy(c *client, args [][]byte) (result, error) {
	increment, err := store.ParseScore(args[2])
	if err != nil {
		return result{}, errInvalidFloat
	}
	score, err := c.srv.data.ZIncrBy(string(args[1]), increment, string(args[3]))
	if err != nil {
		return result{}, err
	}
	return result{reply: doubleReply(score), mutated: true}, nil
}

func cmdZCard(c *client, args [][]byte) (result, error) {
	n, err := c.srv.data.ZCard(string(args[1]))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdZCount(c *client, args [][]byte) (result, error) {
	r, err := store.ParseScoreRange(args[2], args[3])
	if err != nil {
		return result{}, errInvalidScoreRange
	}
	n, err := c.srv.data.ZCount(string(args[1]), r)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdZRange(c *client, args [][]byte) (result, error) {
	legacyReverse := strings.EqualFold(string(args[0]), "ZREVRANGE")
	if legacyReverse && len(args) > 4 {
		for _, option := range args[4:] {
			if !strings.EqualFold(string(option), "WITHSCORES") {
				return result{}, errSyntax
			}
		}
	}
	spec, withScores, err := parseZRangeSpec(args, 2, 4, legacyReverse, true)
	if err != nil {
		return result{}, err
	}
	entries, err := c.srv.data.ZRangeGeneric(string(args[1]), spec)
	if err != nil {
		return result{}, err
	}
	return result{reply: scoreEntriesReply(entries, withScores)}, nil
}

// parseZRangeSpec parses the bounds and options shared by ZRANGE, ZRANGESTORE
// and ZREVRANGE. boundStart is the index of the first bound and optStart the
// index of the first option.
func parseZRangeSpec(args [][]byte, boundStart, optStart int, reverse bool, allowScores bool) (store.ZRangeSpec, bool, error) {
	spec := store.ZRangeSpec{Kind: store.ZRangeRank, Rev: reverse, Count: -1}
	withScores := false
	kindSeen := false
	for i := optStart; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "BYSCORE":
			if kindSeen {
				return spec, false, errSyntax
			}
			spec.Kind = store.ZRangeScore
			kindSeen = true
		case "BYLEX":
			if kindSeen {
				return spec, false, errSyntax
			}
			spec.Kind = store.ZRangeLex
			kindSeen = true
		case "REV":
			spec.Rev = !reverse
		case "WITHSCORES":
			if !allowScores {
				return spec, false, errSyntax
			}
			withScores = true
		case "LIMIT":
			if i+2 >= len(args) {
				return spec, false, errSyntax
			}
			offset, err := parseInt(args[i+1])
			if err != nil {
				return spec, false, errSyntax
			}
			count, err := parseInt(args[i+2])
			if err != nil {
				return spec, false, errSyntax
			}
			spec.HasLimit = true
			spec.Offset = int(offset)
			if count > math.MaxInt32 {
				count = math.MaxInt32
			}
			if count < -1 {
				count = -1
			}
			spec.Count = int(count)
			i += 2
		default:
			return spec, false, errSyntax
		}
	}

	if spec.HasLimit && spec.Kind == store.ZRangeRank {
		return spec, false, fmt.Errorf("ERR syntax error, LIMIT is only supported in combination with either BYSCORE or BYLEX")
	}
	if withScores && spec.Kind == store.ZRangeLex {
		return spec, false, fmt.Errorf("ERR syntax error, WITHSCORES not supported in combination with BYLEX")
	}
	if spec.Offset < 0 {
		// Redis yields an empty result for a negative LIMIT offset.
		spec.Count = 0
		spec.Offset = 0
	}

	lower, upper := args[boundStart], args[boundStart+1]
	switch spec.Kind {
	case store.ZRangeScore:
		minArg, maxArg := lower, upper
		if spec.Rev {
			minArg, maxArg = upper, lower
		}
		r, err := store.ParseScoreRange(minArg, maxArg)
		if err != nil {
			return spec, false, errInvalidScoreRange
		}
		spec.Score = r
	case store.ZRangeLex:
		minArg, maxArg := lower, upper
		if spec.Rev {
			minArg, maxArg = upper, lower
		}
		r, err := store.ParseLexRange(minArg, maxArg)
		if err != nil {
			return spec, false, fmt.Errorf("ERR min or max not valid string range item")
		}
		spec.Lex = r
	default:
		start, err := parseInt(lower)
		if err != nil {
			return spec, false, err
		}
		stop, err := parseInt(upper)
		if err != nil {
			return spec, false, err
		}
		spec.RankStart, spec.RankStop = start, stop
	}
	return spec, withScores, nil
}

func cmdZRangeByScore(c *client, args [][]byte) (result, error) {
	reverse := strings.EqualFold(string(args[0]), "ZREVRANGEBYSCORE")

	// Forward: min max. Reverse: max min.
	minArg, maxArg := args[2], args[3]
	if reverse {
		minArg, maxArg = args[3], args[2]
	}
	r, err := store.ParseScoreRange(minArg, maxArg)
	if err != nil {
		return result{}, errInvalidScoreRange
	}

	withScores := false
	var offset, count int64
	count = -1
	for i := 4; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "WITHSCORES":
			withScores = true
		case "LIMIT":
			if i+2 >= len(args) {
				return result{}, errSyntax
			}
			offset, err = parseInt(args[i+1])
			if err != nil || offset < 0 {
				return result{}, fmt.Errorf("ERR offset is out of range")
			}
			count, err = parseInt(args[i+2])
			if err != nil {
				return result{}, err
			}
			i += 2
		default:
			return result{}, errSyntax
		}
	}

	const maxLimit = int64(1<<31 - 1)
	if offset > maxLimit {
		offset = maxLimit
	}
	if count > maxLimit {
		count = maxLimit
	}
	entries, err := c.srv.data.ZRangeByScore(string(args[1]), r, int(offset), int(count), reverse)
	if err != nil {
		return result{}, err
	}
	return result{reply: scoreEntriesReply(entries, withScores)}, nil
}

func cmdZRank(c *client, args [][]byte) (result, error) {
	withScore := false
	if len(args) == 4 {
		if !strings.EqualFold(string(args[3]), "WITHSCORE") {
			return result{}, errSyntax
		}
		withScore = true
	} else if len(args) > 4 {
		return result{}, errSyntax
	}

	reverse := strings.EqualFold(string(args[0]), "ZREVRANK")
	key := string(args[1])
	member := string(args[2])
	rank, ok, err := c.srv.data.ZRank(key, member, reverse)
	if err != nil {
		return result{}, err
	}
	if !ok {
		if withScore {
			return result{reply: nullArrayReply{}}, nil
		}
		return result{reply: null}, nil
	}
	if withScore {
		score, _, err := c.srv.data.ZScore(key, member)
		if err != nil {
			return result{}, err
		}
		return result{reply: arrayReply{intReply(rank), bulkReply(store.FormatScore(score))}}, nil
	}
	return result{reply: intReply(rank)}, nil
}

func cmdZRemRangeByRank(c *client, args [][]byte) (result, error) {
	start, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	stop, err := parseInt(args[3])
	if err != nil {
		return result{}, err
	}
	removed, err := c.srv.data.ZRemRangeByRank(string(args[1]), start, stop)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(removed), mutated: removed > 0}, nil
}

func cmdZRemRangeByScore(c *client, args [][]byte) (result, error) {
	r, err := store.ParseScoreRange(args[2], args[3])
	if err != nil {
		return result{}, errInvalidScoreRange
	}
	removed, err := c.srv.data.ZRemRangeByScore(string(args[1]), r)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(removed), mutated: removed > 0}, nil
}

func cmdZScan(c *client, args [][]byte) (result, error) {
	cursor, err := strconv.ParseUint(string(args[2]), 10, 64)
	if err != nil {
		return result{}, fmt.Errorf("ERR invalid cursor")
	}

	pattern := ""
	count := 10
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "MATCH":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			pattern = string(args[i])
		case "COUNT":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			n, err := parseInt(args[i])
			if err != nil || n < 1 {
				return result{}, errSyntax
			}
			count = int(n)
		default:
			return result{}, errSyntax
		}
	}

	next, entries, err := c.srv.data.ZScan(string(args[1]), cursor, pattern, count)
	if err != nil {
		return result{}, err
	}
	items := make(arrayReply, 0, len(entries)*2)
	for _, entry := range entries {
		items = append(items, bulkReply(entry.Member), bulkReply(store.FormatScore(entry.Score)))
	}
	return result{reply: arrayReply{bulkReply(strconv.FormatUint(next, 10)), items}}, nil
}

func scoreEntriesReply(entries []store.ScoreMember, withScores bool) any {
	if !withScores {
		out := make(arrayReply, 0, len(entries))
		for _, entry := range entries {
			out = append(out, bulkReply(entry.Member))
		}
		return out
	}
	out := make(pairsReply, 0, len(entries)*2)
	for _, entry := range entries {
		out = append(out, bulkReply(entry.Member), doubleReply(entry.Score))
	}
	return out
}
