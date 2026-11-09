package server

import (
	"errors"
	"fmt"
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
	registerCommand("ZRANK", 3, flagReadOnly|flagFast, cmdZRank)
	registerCommand("ZREVRANK", 3, flagReadOnly|flagFast, cmdZRank)
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
		return result{reply: bulkReply(store.FormatScore(res.Score)), mutated: true}, nil
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
	return result{reply: bulkReply(store.FormatScore(score))}, nil
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
		out = append(out, bulkReply(store.FormatScore(score)))
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
	return result{reply: bulkReply(store.FormatScore(score)), mutated: true}, nil
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
	reverse := strings.EqualFold(string(args[0]), "ZREVRANGE")
	start, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	stop, err := parseInt(args[3])
	if err != nil {
		return result{}, err
	}
	withScores := false
	for _, option := range args[4:] {
		switch strings.ToUpper(string(option)) {
		case "WITHSCORES":
			withScores = true
		default:
			return result{}, errSyntax
		}
	}

	entries, err := c.srv.data.ZRange(string(args[1]), start, stop, reverse)
	if err != nil {
		return result{}, err
	}
	return result{reply: scoreEntriesReply(entries, withScores)}, nil
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
	reverse := strings.EqualFold(string(args[0]), "ZREVRANK")
	rank, ok, err := c.srv.data.ZRank(string(args[1]), string(args[2]), reverse)
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: null}, nil
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
		out = append(out, bulkReply(entry.Member), bulkReply(store.FormatScore(entry.Score)))
	}
	return out
}
