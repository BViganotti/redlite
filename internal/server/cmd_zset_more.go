package server

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("ZRANGEBYLEX", -4, flagReadOnly, cmdZRangeByLex)
	registerCommand("ZREVRANGEBYLEX", -4, flagReadOnly, cmdZRangeByLex)
	registerCommand("ZLEXCOUNT", 4, flagReadOnly|flagFast, cmdZLexCount)
	registerCommand("ZREMRANGEBYLEX", 4, flagWrite, cmdZRemRangeByLex)
	registerCommand("ZRANGESTORE", -5, flagWrite, cmdZRangeStore)
	registerCommand("ZINTERCARD", -3, flagReadOnly, cmdZInterCard)
	registerCommand("ZMPOP", -4, flagWrite, cmdZMPop)
	registerCommand("BZMPOP", -5, flagWrite|flagBlocking, cmdBZMPop)
}

func cmdZRangeByLex(c *client, args [][]byte) (result, error) {
	reverse := strings.EqualFold(string(args[0]), "ZREVRANGEBYLEX")
	minArg, maxArg := args[2], args[3]
	if reverse {
		minArg, maxArg = args[3], args[2]
	}
	r, err := storeParseLexRange(minArg, maxArg)
	if err != nil {
		return result{}, err
	}

	offset, count := int64(0), int64(-1)
	if len(args) > 4 {
		if len(args) != 7 || !strings.EqualFold(string(args[4]), "LIMIT") {
			return result{}, errSyntax
		}
		offset, err = parseInt(args[5])
		if err != nil {
			return result{}, errSyntax
		}
		count, err = parseInt(args[6])
		if err != nil {
			return result{}, errSyntax
		}
	}
	if offset < 0 {
		return result{reply: arrayReply{}}, nil
	}
	if count > math.MaxInt32 {
		count = math.MaxInt32
	}
	if count < -1 {
		count = -1
	}

	entries, err := c.srv.data.ZRangeByLex(string(args[1]), r, int(offset), int(count), reverse)
	if err != nil {
		return result{}, err
	}
	return result{reply: scoreEntriesReply(entries, false)}, nil
}

func cmdZLexCount(c *client, args [][]byte) (result, error) {
	r, err := storeParseLexRange(args[2], args[3])
	if err != nil {
		return result{}, err
	}
	n, err := c.srv.data.ZLexCount(string(args[1]), r)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdZRemRangeByLex(c *client, args [][]byte) (result, error) {
	r, err := storeParseLexRange(args[2], args[3])
	if err != nil {
		return result{}, err
	}
	n, err := c.srv.data.ZRemRangeByLex(string(args[1]), r)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n), mutated: n > 0}, nil
}

func cmdZRangeStore(c *client, args [][]byte) (result, error) {
	spec, _, err := parseZRangeSpec(args, 3, 5, false, false)
	if err != nil {
		return result{}, err
	}
	n, err := c.srv.data.ZRangeStore(string(args[1]), string(args[2]), spec)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n), mutated: true}, nil
}

func cmdZInterCard(c *client, args [][]byte) (result, error) {
	numKeys, err := parseInt(args[1])
	if err != nil {
		return result{}, err
	}
	if numKeys <= 0 {
		return result{}, fmt.Errorf("ERR at least 1 input key is needed for 'zintercard' command")
	}
	if int(numKeys) > len(args)-2 {
		return result{}, errSyntax
	}
	keys := make([]string, 0, numKeys)
	for _, key := range args[2 : 2+int(numKeys)] {
		keys = append(keys, string(key))
	}

	limit := int64(0)
	rest := args[2+int(numKeys):]
	if len(rest) > 0 {
		if len(rest) != 2 || !strings.EqualFold(string(rest[0]), "LIMIT") {
			return result{}, errSyntax
		}
		limit, err = parseInt(rest[1])
		if err != nil {
			return result{}, errSyntax
		}
		if limit < 0 {
			return result{}, fmt.Errorf("ERR LIMIT can't be negative")
		}
	}

	n, err := c.srv.data.ZInterCard(keys, limit)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func parseZMPop(args [][]byte, start int) ([]string, bool, int, error) {
	numKeys, err := parseInt(args[start])
	if err != nil || numKeys <= 0 {
		return nil, false, 0, fmt.Errorf("ERR numkeys should be greater than 0")
	}
	if int(numKeys) > len(args)-start-1 {
		return nil, false, 0, errSyntax
	}
	keys := make([]string, 0, numKeys)
	for _, key := range args[start+1 : start+1+int(numKeys)] {
		keys = append(keys, string(key))
	}

	index := start + 1 + int(numKeys)
	if index >= len(args) {
		return nil, false, 0, errSyntax
	}
	var min bool
	switch strings.ToUpper(string(args[index])) {
	case "MIN":
		min = true
	case "MAX":
		min = false
	default:
		return nil, false, 0, errSyntax
	}
	index++

	count := 1
	if index < len(args) {
		if !strings.EqualFold(string(args[index]), "COUNT") || index+1 >= len(args) {
			return nil, false, 0, errSyntax
		}
		index++
		value, err := parseInt(args[index])
		if err != nil || value <= 0 {
			return nil, false, 0, fmt.Errorf("ERR count should be greater than 0")
		}
		if value > math.MaxInt32 {
			value = math.MaxInt32
		}
		count = int(value)
		index++
		if index != len(args) {
			return nil, false, 0, errSyntax
		}
	}
	return keys, min, count, nil
}

func cmdZMPop(c *client, args [][]byte) (result, error) {
	keys, min, count, err := parseZMPop(args, 1)
	if err != nil {
		return result{}, err
	}
	key, entries, err := c.srv.data.ZMPop(keys, min, count)
	if err != nil {
		return result{}, err
	}
	if key == "" {
		return result{reply: nullArrayReply{}}, nil
	}
	return result{reply: zmpopReply(key, entries), mutated: true, rewrite: zmpopRewrite(key, entries)}, nil
}

func cmdBZMPop(c *client, args [][]byte) (result, error) {
	timeout, err := parseBlockTimeout(args[1])
	if err != nil {
		return result{}, err
	}
	keys, min, count, err := parseZMPop(args, 2)
	if err != nil {
		return result{}, err
	}

	try := func() (string, []store.ScoreMember, error) {
		release := c.enterReadGate()
		defer release()
		return c.srv.data.ZMPop(keys, min, count)
	}

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	for {
		key, entries, err := try()
		if err != nil {
			return result{}, err
		}
		if key != "" {
			return result{reply: zmpopReply(key, entries), mutated: true, rewrite: zmpopRewrite(key, entries)}, nil
		}
		if c.executing || c.srv.shuttingDown.Load() {
			return result{reply: nullArrayReply{}}, nil
		}

		waiter := c.srv.data.Wait(keys)
		key, entries, err = try()
		if err != nil {
			c.srv.data.CancelWait(waiter)
			return result{}, err
		}
		if key != "" {
			c.srv.data.CancelWait(waiter)
			return result{reply: zmpopReply(key, entries), mutated: true, rewrite: zmpopRewrite(key, entries)}, nil
		}

		if waitBlocked(c, waiter.C, timeoutCh) {
			c.srv.data.CancelWait(waiter)
		} else {
			c.srv.data.CancelWait(waiter)
			return result{reply: nullArrayReply{}}, nil
		}
	}
}

func zmpopReply(key string, entries []store.ScoreMember) arrayReply {
	out := make(arrayReply, 2)
	out[0] = bulkReply(key)
	out[1] = zmpopPairsReply(entries)
	return out
}

func zmpopPairsReply(entries []store.ScoreMember) arrayReply {
	out := make(arrayReply, 0, len(entries))
	for _, entry := range entries {
		out = append(out, arrayReply{bulkReply(entry.Member), doubleReply(entry.Score)})
	}
	return out
}

func zmpopRewrite(key string, entries []store.ScoreMember) [][]byte {
	args := make([][]byte, 0, len(entries)+2)
	args = append(args, []byte("ZREM"), []byte(key))
	for _, entry := range entries {
		args = append(args, []byte(entry.Member))
	}
	return args
}

func storeParseLexRange(minArg, maxArg []byte) (store.LexRange, error) {
	r, err := store.ParseLexRange(minArg, maxArg)
	if err != nil {
		return store.LexRange{}, fmt.Errorf("ERR min or max not valid string range item")
	}
	return r, nil
}
