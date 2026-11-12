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
	registerCommand("LPUSH", -3, flagWrite, cmdLPush)
	registerCommand("RPUSH", -3, flagWrite, cmdRPush)
	registerCommand("LPUSHX", -3, flagWrite, cmdLPushX)
	registerCommand("RPUSHX", -3, flagWrite, cmdRPushX)
	registerCommand("LPOP", -2, flagWrite|flagFast, cmdLPop)
	registerCommand("RPOP", -2, flagWrite|flagFast, cmdRPop)
	registerCommand("LLEN", 2, flagReadOnly|flagFast, cmdLLen)
	registerCommand("LINDEX", 3, flagReadOnly|flagFast, cmdLIndex)
	registerCommand("LSET", 4, flagWrite, cmdLSet)
	registerCommand("LRANGE", 4, flagReadOnly, cmdLRange)
	registerCommand("LTRIM", 4, flagWrite, cmdLTrim)
	registerCommand("LREM", 4, flagWrite, cmdLRem)
	registerCommand("LMPOP", -4, flagWrite, cmdLMPop)
}

// parseMPop parses the common "numkeys key [key ...] <LEFT|RIGHT> [COUNT n]"
// tail that LMPOP and BLMPOP share. start is the index of numkeys.
func parseMPop(args [][]byte, start int) ([]string, bool, int, error) {
	if start >= len(args) {
		return nil, false, 0, errSyntax
	}
	numKeys, err := parseInt(args[start])
	if err != nil || numKeys <= 0 {
		return nil, false, 0, fmt.Errorf("ERR numkeys should be greater than 0")
	}
	if int(numKeys) > len(args)-start-1 {
		return nil, false, 0, fmt.Errorf("ERR Number of keys can't be greater than number of args")
	}
	keys := make([]string, 0, numKeys)
	for _, key := range args[start+1 : start+1+int(numKeys)] {
		keys = append(keys, string(key))
	}

	index := start + 1 + int(numKeys)
	if index >= len(args) {
		return nil, false, 0, errSyntax
	}
	var left bool
	switch strings.ToUpper(string(args[index])) {
	case "LEFT":
		left = true
	case "RIGHT":
		left = false
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
	return keys, left, count, nil
}

func cmdLMPop(c *client, args [][]byte) (result, error) {
	keys, left, count, err := parseMPop(args, 1)
	if err != nil {
		return result{}, err
	}
	key, values, err := c.srv.data.LMPop(keys, left, count)
	if err != nil {
		return result{}, err
	}
	if key == "" {
		return result{reply: nullArrayReply{}}, nil
	}
	return result{reply: mpOpReply(key, values), mutated: true, rewrite: lmpopRewrite(key, values, left)}, nil
}

func mpOpReply(key string, values [][]byte) arrayReply {
	out := make(arrayReply, 2)
	out[0] = bulkReply(key)
	items := make(arrayReply, 0, len(values))
	for _, value := range values {
		items = append(items, bulkReply(value))
	}
	out[1] = items
	return out
}

func lmpopRewrite(key string, values [][]byte, left bool) [][]byte {
	command := "LPOP"
	if !left {
		command = "RPOP"
	}
	return [][]byte{
		[]byte(command),
		[]byte(key),
		[]byte(strconv.Itoa(len(values))),
	}
}

func cmdLPush(c *client, args [][]byte) (result, error) {
	return push(c, args, true, true)
}

func cmdRPush(c *client, args [][]byte) (result, error) {
	return push(c, args, false, true)
}

func cmdLPushX(c *client, args [][]byte) (result, error) {
	return push(c, args, true, false)
}

func cmdRPushX(c *client, args [][]byte) (result, error) {
	return push(c, args, false, false)
}

func push(c *client, args [][]byte, front, create bool) (result, error) {
	values := args[2:]
	var (
		n   int64
		err error
	)
	key := string(args[1])
	switch {
	case front && create:
		n, err = c.srv.data.LPush(key, values)
	case front && !create:
		n, err = c.srv.data.LPushX(key, values)
	case !front && create:
		n, err = c.srv.data.RPush(key, values)
	default:
		n, err = c.srv.data.RPushX(key, values)
	}
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n), mutated: n > 0}, nil
}

func cmdLPop(c *client, args [][]byte) (result, error) {
	return pop(c, args, true, "lpop")
}

func cmdRPop(c *client, args [][]byte) (result, error) {
	return pop(c, args, false, "rpop")
}

func pop(c *client, args [][]byte, front bool, cmdName string) (result, error) {
	count, withCount, err := parsePopCount(args, cmdName)
	if err != nil {
		return result{}, err
	}

	key := string(args[1])
	var values [][]byte
	if front {
		values, err = c.srv.data.LPop(key, count)
	} else {
		values, err = c.srv.data.RPop(key, count)
	}
	if err != nil {
		return result{}, err
	}

	out := result{mutated: len(values) > 0}
	if !withCount {
		if len(values) == 0 {
			out.reply = null
			return out, nil
		}
		out.reply = bulkReply(values[0])
		return out, nil
	}
	reply := make(arrayReply, 0, len(values))
	for _, value := range values {
		reply = append(reply, bulkReply(value))
	}
	out.reply = reply
	return out, nil
}

func parsePopCount(args [][]byte, cmdName string) (int, bool, error) {
	if len(args) == 2 {
		return 1, false, nil
	}
	if len(args) > 3 {
		return 0, true, fmt.Errorf("ERR wrong number of arguments for '%s' command", cmdName)
	}
	count, err := parseInt(args[2])
	if err != nil {
		return 0, true, err
	}
	if count < 0 {
		return 0, true, fmt.Errorf("ERR value is out of range, must be positive")
	}
	if count > math.MaxInt32 {
		count = math.MaxInt32
	}
	return int(count), true, nil
}

func cmdLLen(c *client, args [][]byte) (result, error) {
	n, err := c.srv.data.LLen(string(args[1]))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdLIndex(c *client, args [][]byte) (result, error) {
	index, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	value, ok, err := c.srv.data.LIndex(string(args[1]), index)
	if err != nil {
		return result{}, err
	}
	return result{reply: bulkOrNull(value, ok)}, nil
}

func cmdLSet(c *client, args [][]byte) (result, error) {
	index, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	err = c.srv.data.LSet(string(args[1]), index, args[3])
	switch {
	case err == nil:
		return result{reply: simpleReply("OK"), mutated: true}, nil
	case errors.Is(err, store.ErrNoSuchKey):
		return result{}, fmt.Errorf("ERR no such key")
	case errors.Is(err, store.ErrIndexOutOfRange):
		return result{}, fmt.Errorf("ERR index out of range")
	}
	return result{}, err
}

func cmdLRange(c *client, args [][]byte) (result, error) {
	start, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	stop, err := parseInt(args[3])
	if err != nil {
		return result{}, err
	}
	values, err := c.srv.data.LRange(string(args[1]), start, stop)
	if err != nil {
		return result{}, err
	}
	out := make(arrayReply, 0, len(values))
	for _, value := range values {
		out = append(out, bulkReply(value))
	}
	return result{reply: out}, nil
}

func cmdLTrim(c *client, args [][]byte) (result, error) {
	start, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	stop, err := parseInt(args[3])
	if err != nil {
		return result{}, err
	}
	if err := c.srv.data.LTrim(string(args[1]), start, stop); err != nil {
		return result{}, err
	}
	return result{reply: simpleReply("OK"), mutated: true}, nil
}

func cmdLRem(c *client, args [][]byte) (result, error) {
	count, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	removed, err := c.srv.data.LRem(string(args[1]), count, args[3])
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(removed), mutated: removed > 0}, nil
}
