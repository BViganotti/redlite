package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("ZUNION", -3, flagReadOnly, zsetOpHandler("UNION", false, true))
	registerCommand("ZINTER", -3, flagReadOnly, zsetOpHandler("INTER", false, true))
	registerCommand("ZDIFF", -3, flagReadOnly, zsetOpHandler("DIFF", false, false))
	registerCommand("ZUNIONSTORE", -4, flagWrite, zsetOpHandler("UNION", true, true))
	registerCommand("ZINTERSTORE", -4, flagWrite, zsetOpHandler("INTER", true, true))
	registerCommand("ZDIFFSTORE", -4, flagWrite, zsetOpHandler("DIFF", true, false))
	registerCommand("ZPOPMIN", -2, flagWrite|flagFast, func(c *client, args [][]byte) (result, error) {
		return zpop(c, args, true)
	})
	registerCommand("ZPOPMAX", -2, flagWrite|flagFast, func(c *client, args [][]byte) (result, error) {
		return zpop(c, args, false)
	})
	registerCommand("ZRANDMEMBER", -2, flagReadOnly, cmdZRandMember)
	registerCommand("BZPOPMIN", -3, flagWrite|flagBlocking, func(c *client, args [][]byte) (result, error) {
		return blockingZPop(c, args, true)
	})
	registerCommand("BZPOPMAX", -3, flagWrite|flagBlocking, func(c *client, args [][]byte) (result, error) {
		return blockingZPop(c, args, false)
	})
}

type zsetOpArgs struct {
	keys       []string
	weights    []float64
	aggregate  string
	withScores bool
}

func zsetOpHandler(op string, store bool, allowWeights bool) handlerFunc {
	return func(c *client, args [][]byte) (result, error) {
		parsed, err := parseZSetOpArgs(args, store, allowWeights, !store)
		if err != nil {
			return result{}, err
		}

		destination := ""
		if store {
			destination = string(args[1])
		}
		entries, err := c.srv.data.ZSetOp(op, parsed.keys, parsed.weights, parsed.aggregate, destination)
		if err != nil {
			return result{}, err
		}

		if store {
			return result{reply: intReply(int64(len(entries))), mutated: true}, nil
		}
		return result{reply: scoreEntriesReply(entries, parsed.withScores)}, nil
	}
}

func parseZSetOpArgs(args [][]byte, store bool, allowWeights bool, allowScores bool) (zsetOpArgs, error) {
	numKeysIndex := 1
	keyStart := 2
	if store {
		numKeysIndex = 2
		keyStart = 3
	}
	if numKeysIndex >= len(args) {
		return zsetOpArgs{}, errSyntax
	}

	numKeys, err := parseInt(args[numKeysIndex])
	if err != nil || numKeys <= 0 {
		return zsetOpArgs{}, fmt.Errorf("ERR at least 1 input key is needed for '%s' command", strings.ToLower(string(args[0])))
	}
	if keyStart+int(numKeys) > len(args) {
		return zsetOpArgs{}, errSyntax
	}

	parsed := zsetOpArgs{aggregate: "SUM"}
	for _, key := range args[keyStart : keyStart+int(numKeys)] {
		parsed.keys = append(parsed.keys, string(key))
	}

	for i := keyStart + int(numKeys); i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "WEIGHTS":
			if !allowWeights {
				return zsetOpArgs{}, errSyntax
			}
			if i+int(numKeys) >= len(args) {
				return zsetOpArgs{}, errSyntax
			}
			parsed.weights = make([]float64, 0, numKeys)
			for j := 0; j < int(numKeys); j++ {
				i++
				weight, parseErr := strconv.ParseFloat(string(args[i]), 64)
				if parseErr != nil {
					return zsetOpArgs{}, fmt.Errorf("ERR weight value is not a float")
				}
				parsed.weights = append(parsed.weights, weight)
			}
		case "AGGREGATE":
			if !allowWeights {
				return zsetOpArgs{}, errSyntax
			}
			if i+1 >= len(args) {
				return zsetOpArgs{}, errSyntax
			}
			i++
			parsed.aggregate = strings.ToUpper(string(args[i]))
			if parsed.aggregate != "SUM" && parsed.aggregate != "MIN" && parsed.aggregate != "MAX" {
				return zsetOpArgs{}, errSyntax
			}
		case "WITHSCORES":
			if !allowScores {
				return zsetOpArgs{}, errSyntax
			}
			parsed.withScores = true
		default:
			return zsetOpArgs{}, errSyntax
		}
	}
	return parsed, nil
}

func zpop(c *client, args [][]byte, min bool) (result, error) {
	count := 1
	if len(args) == 3 {
		parsed, err := parseInt(args[2])
		if err != nil || parsed < 0 {
			return result{}, fmt.Errorf("ERR value is out of range, must be positive")
		}
		count = int(parsed)
	} else if len(args) > 3 {
		return result{}, errSyntax
	}

	entries, err := c.srv.data.ZPop(string(args[1]), count, min)
	if err != nil {
		return result{}, err
	}
	return result{reply: scoreEntriesReply(entries, true), mutated: len(entries) > 0}, nil
}

func cmdZRandMember(c *client, args [][]byte) (result, error) {
	key := string(args[1])
	if len(args) == 2 {
		entries, err := c.srv.data.ZRandMember(key, 1)
		if err != nil {
			return result{}, err
		}
		if len(entries) == 0 {
			return result{reply: null}, nil
		}
		return result{reply: bulkReply(entries[0].Member)}, nil
	}

	if len(args) < 3 || len(args) > 4 {
		return result{}, errSyntax
	}
	count, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	withScores := false
	if len(args) == 4 {
		if !strings.EqualFold(string(args[3]), "WITHSCORES") {
			return result{}, errSyntax
		}
		withScores = true
	}

	entries, err := c.srv.data.ZRandMember(key, int(count))
	if err != nil {
		return result{}, err
	}
	return result{reply: scoreEntriesReply(entries, withScores)}, nil
}

func blockingZPop(c *client, args [][]byte, min bool) (result, error) {
	timeout, err := parseBlockTimeout(args[len(args)-1])
	if err != nil {
		return result{}, err
	}
	keys := make([]string, 0, len(args)-2)
	for _, key := range args[1 : len(args)-1] {
		keys = append(keys, string(key))
	}

	popCommand := "ZPOPMAX"
	if min {
		popCommand = "ZPOPMIN"
	}

	try := func() (string, store.ScoreMember, bool, error) {
		release := c.enterReadGate()
		defer release()
		for _, key := range keys {
			entries, err := c.srv.data.ZPop(key, 1, min)
			if err != nil {
				return "", store.ScoreMember{}, false, err
			}
			if len(entries) > 0 {
				return key, entries[0], true, nil
			}
		}
		return "", store.ScoreMember{}, false, nil
	}

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	for {
		key, entry, ok, err := try()
		if err != nil {
			return result{}, err
		}
		if ok {
			return zpopReply(key, entry, popCommand), nil
		}
		if c.executing || c.srv.shuttingDown.Load() {
			return result{reply: nullArrayReply{}}, nil
		}

		waiter := c.srv.data.Wait(keys)
		key, entry, ok, err = try()
		if err != nil {
			c.srv.data.CancelWait(waiter)
			return result{}, err
		}
		if ok {
			c.srv.data.CancelWait(waiter)
			return zpopReply(key, entry, popCommand), nil
		}

		select {
		case <-waiter.C:
			c.srv.data.CancelWait(waiter)
		case <-timeoutCh:
			c.srv.data.CancelWait(waiter)
			return result{reply: nullArrayReply{}}, nil
		}
	}
}

func zpopReply(key string, entry store.ScoreMember, popCommand string) result {
	return result{
		reply: arrayReply{
			bulkReply(key),
			bulkReply(entry.Member),
			bulkReply(store.FormatScore(entry.Score)),
		},
		mutated: true,
		rewrite: [][]byte{[]byte(popCommand), []byte(key)},
	}
}
