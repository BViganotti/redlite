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
	registerCommand("GET", 2, flagReadOnly|flagFast, cmdGet)
	registerCommand("SET", -3, flagWrite, cmdSet)
	registerCommand("SETNX", 3, flagWrite|flagFast, cmdSetNX)
	registerCommand("SETEX", 4, flagWrite, cmdSetEX)
	registerCommand("PSETEX", 4, flagWrite, cmdPSetEX)
	registerCommand("GETSET", 3, flagWrite|flagFast, cmdGetSet)
	registerCommand("GETDEL", 2, flagWrite|flagFast, cmdGetDel)
	registerCommand("GETEX", -2, flagWrite|flagFast, cmdGetEx)
	registerCommand("MGET", -2, flagReadOnly|flagFast, cmdMGet)
	registerCommand("MSET", -3, flagWrite, cmdMSet)
	registerCommand("MSETNX", -3, flagWrite, cmdMSetNX)
	registerCommand("APPEND", 3, flagWrite, cmdAppend)
	registerCommand("STRLEN", 2, flagReadOnly|flagFast, cmdStrLen)
	registerCommand("INCR", 2, flagWrite|flagFast, cmdIncr)
	registerCommand("DECR", 2, flagWrite|flagFast, func(c *client, args [][]byte) (result, error) {
		return incrBy(c, args[1], -1)
	})
	registerCommand("INCRBY", 3, flagWrite|flagFast, func(c *client, args [][]byte) (result, error) {
		delta, err := parseInt(args[2])
		if err != nil {
			return result{}, err
		}
		return incrBy(c, args[1], delta)
	})
	registerCommand("DECRBY", 3, flagWrite|flagFast, func(c *client, args [][]byte) (result, error) {
		delta, err := parseInt(args[2])
		if err != nil {
			return result{}, err
		}
		if delta == math.MinInt64 {
			return result{}, fmt.Errorf("ERR decrement would overflow")
		}
		return incrBy(c, args[1], -delta)
	})
}

func cmdGet(c *client, args [][]byte) (result, error) {
	value, ok, err := c.srv.data.Get(string(args[1]))
	if err != nil {
		return result{}, err
	}
	return result{reply: bulkOrNull(value, ok)}, nil
}

type setSpec struct {
	nx, xx, get, keepTTL bool
	hasExpire            bool
	expireAt             int64
}

func parseSetSpec(args [][]byte, now int64) (setSpec, error) {
	var spec setSpec
	for i := 0; i < len(args); i++ {
		opt := strings.ToUpper(string(args[i]))
		switch opt {
		case "NX":
			if spec.nx || spec.xx {
				return setSpec{}, errSyntax
			}
			spec.nx = true
		case "XX":
			if spec.nx || spec.xx {
				return setSpec{}, errSyntax
			}
			spec.xx = true
		case "GET":
			if spec.get {
				return setSpec{}, errSyntax
			}
			spec.get = true
		case "KEEPTTL":
			if spec.keepTTL || spec.hasExpire {
				return setSpec{}, errSyntax
			}
			spec.keepTTL = true
		case "EX", "PX", "EXAT", "PXAT":
			if spec.hasExpire || spec.keepTTL {
				return setSpec{}, errSyntax
			}
			if i+1 >= len(args) {
				return setSpec{}, errSyntax
			}
			i++
			n, err := parseInt(args[i])
			if err != nil {
				return setSpec{}, err
			}
			at, err := parseExpiryOption("set", opt, n, now)
			if err != nil {
				return setSpec{}, err
			}
			spec.hasExpire = true
			spec.expireAt = at
		default:
			return setSpec{}, errSyntax
		}
	}
	return spec, nil
}

func cmdSet(c *client, args [][]byte) (result, error) {
	key := string(args[1])
	value := args[2]
	spec, err := parseSetSpec(args[3:], c.srv.nowMs())
	if err != nil {
		return result{}, err
	}

	res, err := c.srv.data.Set(key, value, store.SetOptions{
		NX:        spec.nx,
		XX:        spec.xx,
		KeepTTL:   spec.keepTTL,
		HasExpire: spec.hasExpire,
		ExpireAt:  spec.expireAt,
		Get:       spec.get,
	})
	if err != nil {
		return result{}, err
	}

	out := result{mutated: res.Written}
	if res.Written {
		out.rewrite = canonicalSet(key, value, spec)
	}
	if spec.get {
		out.reply = bulkOrNull(res.Old, res.HadOld)
		return out, nil
	}
	if !res.Written {
		out.reply = null
		return out, nil
	}
	out.reply = simpleReply("OK")
	return out, nil
}

func canonicalSet(key string, value []byte, spec setSpec) [][]byte {
	out := make([][]byte, 0, 6)
	out = append(out, []byte("SET"), []byte(key), value)
	switch {
	case spec.keepTTL:
		out = append(out, []byte("KEEPTTL"))
	case spec.hasExpire:
		out = append(out, []byte("PXAT"), []byte(strconv.FormatInt(spec.expireAt, 10)))
	}
	return out
}

func cmdSetNX(c *client, args [][]byte) (result, error) {
	key := string(args[1])
	res, err := c.srv.data.Set(key, args[2], store.SetOptions{NX: true})
	if err != nil {
		return result{}, err
	}
	if !res.Written {
		return result{reply: intReply(0)}, nil
	}
	return result{reply: intReply(1), mutated: true}, nil
}

func cmdSetEX(c *client, args [][]byte) (result, error) {
	return setWithTTL(c, args, 1000, "setex")
}

func cmdPSetEX(c *client, args [][]byte) (result, error) {
	return setWithTTL(c, args, 1, "psetex")
}

func setWithTTL(c *client, args [][]byte, unitMs int64, cmdName string) (result, error) {
	key := string(args[1])
	ttl, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	now := c.srv.nowMs()
	at, err := relativeExpiry(now, ttl, unitMs)
	if err != nil {
		return result{}, invalidExpireTime(cmdName)
	}
	if _, err := c.srv.data.Set(key, args[3], store.SetOptions{HasExpire: true, ExpireAt: at}); err != nil {
		return result{}, err
	}
	out := [][]byte{[]byte("SET"), []byte(key), args[3], []byte("PXAT"), []byte(strconv.FormatInt(at, 10))}
	return result{reply: simpleReply("OK"), mutated: true, rewrite: out}, nil
}

func cmdGetSet(c *client, args [][]byte) (result, error) {
	res, err := c.srv.data.Set(string(args[1]), args[2], store.SetOptions{Get: true})
	if err != nil {
		return result{}, err
	}
	return result{reply: bulkOrNull(res.Old, res.HadOld), mutated: true}, nil
}

func cmdGetDel(c *client, args [][]byte) (result, error) {
	key := string(args[1])
	value, ok, err := c.srv.data.Get(key)
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: null}, nil
	}
	c.srv.data.Del([]string{key})
	return result{reply: bulkReply(value), mutated: true, rewrite: [][]byte{[]byte("DEL"), []byte(key)}}, nil
}

func cmdGetEx(c *client, args [][]byte) (result, error) {
	key := string(args[1])
	now := c.srv.nowMs()

	var (
		hasExpire bool
		persist   bool
		expireAt  int64
	)
	for i := 2; i < len(args); i++ {
		opt := strings.ToUpper(string(args[i]))
		switch opt {
		case "EX", "PX", "EXAT", "PXAT":
			if hasExpire || persist {
				return result{}, errSyntax
			}
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			n, err := parseInt(args[i])
			if err != nil {
				return result{}, err
			}
			at, err := parseExpiryOption("getex", opt, n, now)
			if err != nil {
				return result{}, err
			}
			hasExpire = true
			expireAt = at
		case "PERSIST":
			if hasExpire || persist {
				return result{}, errSyntax
			}
			persist = true
		default:
			return result{}, errSyntax
		}
	}

	value, ok, err := c.srv.data.Get(key)
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: null}, nil
	}

	switch {
	case hasExpire:
		c.srv.data.Expire(key, expireAt, store.ExpireAlways)
		return result{
			reply:   bulkReply(value),
			mutated: true,
			rewrite: [][]byte{[]byte("PEXPIREAT"), []byte(key), []byte(strconv.FormatInt(expireAt, 10))},
		}, nil
	case persist:
		if c.srv.data.Persist(key) {
			return result{reply: bulkReply(value), mutated: true, rewrite: [][]byte{[]byte("PERSIST"), []byte(key)}}, nil
		}
	}
	return result{reply: bulkReply(value)}, nil
}

func cmdMGet(c *client, args [][]byte) (result, error) {
	out := make(arrayReply, 0, len(args)-1)
	for _, key := range args[1:] {
		value, ok, err := c.srv.data.Get(string(key))
		if errors.Is(err, store.ErrWrongType) {
			// MGET reports a non-string value as nil instead of failing.
			out = append(out, null)
			continue
		}
		if err != nil {
			return result{}, err
		}
		out = append(out, bulkOrNull(value, ok))
	}
	return result{reply: out}, nil
}

func cmdMSet(c *client, args [][]byte) (result, error) {
	if (len(args)-1)%2 != 0 {
		return result{}, errSyntax
	}
	pairs := make([]store.KeyValue, 0, (len(args)-1)/2)
	for i := 1; i < len(args); i += 2 {
		pairs = append(pairs, store.KeyValue{Key: string(args[i]), Value: args[i+1]})
	}
	c.srv.data.SetMany(pairs)
	return result{reply: simpleReply("OK"), mutated: true}, nil
}

func cmdMSetNX(c *client, args [][]byte) (result, error) {
	if (len(args)-1)%2 != 0 {
		return result{}, errSyntax
	}
	pairs := make([]store.KeyValue, 0, (len(args)-1)/2)
	for i := 1; i < len(args); i += 2 {
		pairs = append(pairs, store.KeyValue{Key: string(args[i]), Value: args[i+1]})
	}
	if !c.srv.data.SetManyNX(pairs) {
		return result{reply: intReply(0)}, nil
	}
	return result{reply: intReply(1), mutated: true}, nil
}

func cmdAppend(c *client, args [][]byte) (result, error) {
	n, err := c.srv.data.Append(string(args[1]), args[2])
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(int64(n)), mutated: true}, nil
}

func cmdStrLen(c *client, args [][]byte) (result, error) {
	n, err := c.srv.data.StrLen(string(args[1]))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdIncr(c *client, args [][]byte) (result, error) {
	return incrBy(c, args[1], 1)
}

func incrBy(c *client, key []byte, delta int64) (result, error) {
	n, err := c.srv.data.IncrBy(string(key), delta)
	if err != nil {
		switch {
		case isNotInteger(err):
			return result{}, errInteger
		case isOverflow(err):
			return result{}, overflowError()
		}
		return result{}, err
	}
	return result{reply: intReply(n), mutated: true}, nil
}

// parseExpiryOption converts an expiry option and its argument into an
// absolute unix-millisecond deadline.
func parseExpiryOption(cmdName, option string, n, now int64) (int64, error) {
	switch option {
	case "EX":
		return relativeExpiryChecked(cmdName, now, n, 1000)
	case "PX":
		return relativeExpiryChecked(cmdName, now, n, 1)
	case "EXAT":
		if n <= 0 || n > math.MaxInt64/1000 {
			return 0, invalidExpireTime(cmdName)
		}
		return n * 1000, nil
	case "PXAT":
		if n <= 0 {
			return 0, invalidExpireTime(cmdName)
		}
		return n, nil
	}
	return 0, errSyntax
}

func relativeExpiry(now, n, unitMs int64) (int64, error) {
	if n <= 0 {
		return 0, errSyntax
	}
	if n > (math.MaxInt64-now)/unitMs {
		return 0, errSyntax
	}
	return now + n*unitMs, nil
}

func relativeExpiryChecked(cmdName string, now, n, unitMs int64) (int64, error) {
	at, err := relativeExpiry(now, n, unitMs)
	if err != nil {
		return 0, invalidExpireTime(cmdName)
	}
	return at, nil
}

func invalidExpireTime(cmdName string) error {
	return fmt.Errorf("ERR invalid expire time in '%s' command", cmdName)
}
