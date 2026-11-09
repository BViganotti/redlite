package server

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("HSET", -4, flagWrite, cmdHSet)
	registerCommand("HSETNX", 4, flagWrite, cmdHSetNX)
	registerCommand("HMSET", -4, flagWrite, cmdHMSet)
	registerCommand("HGET", 3, flagReadOnly|flagFast, cmdHGet)
	registerCommand("HMGET", -3, flagReadOnly|flagFast, cmdHMGet)
	registerCommand("HDEL", -3, flagWrite|flagFast, cmdHDel)
	registerCommand("HLEN", 2, flagReadOnly|flagFast, cmdHLen)
	registerCommand("HEXISTS", 3, flagReadOnly|flagFast, cmdHExists)
	registerCommand("HKEYS", 2, flagReadOnly, cmdHKeys)
	registerCommand("HVALS", 2, flagReadOnly, cmdHVals)
	registerCommand("HGETALL", 2, flagReadOnly, cmdHGetAll)
	registerCommand("HINCRBY", 4, flagWrite|flagFast, cmdHIncrBy)
	registerCommand("HSCAN", -3, flagReadOnly, cmdHScan)
}

func hashPairsFromArgs(args [][]byte, command string) ([]store.KeyValue, error) {
	if (len(args)-2)%2 != 0 {
		return nil, fmt.Errorf("ERR wrong number of arguments for '%s' command", command)
	}
	pairs := make([]store.KeyValue, 0, (len(args)-2)/2)
	for i := 2; i+1 < len(args); i += 2 {
		pairs = append(pairs, store.KeyValue{Key: string(args[i]), Value: args[i+1]})
	}
	return pairs, nil
}

func cmdHSet(c *client, args [][]byte) (result, error) {
	pairs, err := hashPairsFromArgs(args, "hset")
	if err != nil {
		return result{}, err
	}
	added, err := c.srv.data.HSet(string(args[1]), pairs)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(added), mutated: true}, nil
}

func cmdHMSet(c *client, args [][]byte) (result, error) {
	pairs, err := hashPairsFromArgs(args, "hmset")
	if err != nil {
		return result{}, err
	}
	if _, err := c.srv.data.HSet(string(args[1]), pairs); err != nil {
		return result{}, err
	}
	return result{reply: simpleReply("OK"), mutated: true}, nil
}

func cmdHSetNX(c *client, args [][]byte) (result, error) {
	ok, err := c.srv.data.HSetNX(string(args[1]), string(args[2]), args[3])
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: intReply(0)}, nil
	}
	return result{reply: intReply(1), mutated: true}, nil
}

func cmdHGet(c *client, args [][]byte) (result, error) {
	value, ok, err := c.srv.data.HGet(string(args[1]), string(args[2]))
	if err != nil {
		return result{}, err
	}
	return result{reply: bulkOrNull(value, ok)}, nil
}

func cmdHMGet(c *client, args [][]byte) (result, error) {
	out := make(arrayReply, 0, len(args)-2)
	for _, field := range args[2:] {
		value, ok, err := c.srv.data.HGet(string(args[1]), string(field))
		if err != nil {
			return result{}, err
		}
		out = append(out, bulkOrNull(value, ok))
	}
	return result{reply: out}, nil
}

func cmdHDel(c *client, args [][]byte) (result, error) {
	fields := make([]string, 0, len(args)-2)
	for _, field := range args[2:] {
		fields = append(fields, string(field))
	}
	removed, err := c.srv.data.HDel(string(args[1]), fields)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(removed), mutated: removed > 0}, nil
}

func cmdHLen(c *client, args [][]byte) (result, error) {
	n, err := c.srv.data.HLen(string(args[1]))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdHExists(c *client, args [][]byte) (result, error) {
	ok, err := c.srv.data.HExists(string(args[1]), string(args[2]))
	if err != nil {
		return result{}, err
	}
	if ok {
		return result{reply: intReply(1)}, nil
	}
	return result{reply: intReply(0)}, nil
}

func cmdHKeys(c *client, args [][]byte) (result, error) {
	keys, err := c.srv.data.HKeys(string(args[1]))
	if err != nil {
		return result{}, err
	}
	out := make(arrayReply, 0, len(keys))
	for _, key := range keys {
		out = append(out, bulkReply(key))
	}
	return result{reply: out}, nil
}

func cmdHVals(c *client, args [][]byte) (result, error) {
	values, err := c.srv.data.HVals(string(args[1]))
	if err != nil {
		return result{}, err
	}
	out := make(arrayReply, 0, len(values))
	for _, value := range values {
		out = append(out, bulkReply(value))
	}
	return result{reply: out}, nil
}

func cmdHGetAll(c *client, args [][]byte) (result, error) {
	pairs, err := c.srv.data.HGetAll(string(args[1]))
	if err != nil {
		return result{}, err
	}
	out := make(arrayReply, 0, len(pairs)*2)
	for _, pair := range pairs {
		out = append(out, bulkReply(pair.Field), bulkReply(pair.Value))
	}
	return result{reply: out}, nil
}

func cmdHIncrBy(c *client, args [][]byte) (result, error) {
	delta, err := parseInt(args[3])
	if err != nil {
		return result{}, err
	}
	n, err := c.srv.data.HIncrBy(string(args[1]), string(args[2]), delta)
	if err != nil {
		switch {
		case isWrongType(err):
			return result{}, err
		case isNotInteger(err):
			return result{}, errInteger
		case isOverflow(err):
			return result{}, overflowError()
		}
		return result{}, err
	}
	return result{reply: intReply(n), mutated: true}, nil
}

func cmdHScan(c *client, args [][]byte) (result, error) {
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

	next, pairs, err := c.srv.data.HScan(string(args[1]), cursor, pattern, count)
	if err != nil {
		return result{}, err
	}
	items := make(arrayReply, 0, len(pairs)*2)
	for _, pair := range pairs {
		items = append(items, bulkReply(pair.Field), bulkReply(pair.Value))
	}
	return result{reply: arrayReply{bulkReply(strconv.FormatUint(next, 10)), items}}, nil
}
