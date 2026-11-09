package server

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("DEL", -2, flagWrite, cmdDel)
	registerCommand("UNLINK", -2, flagWrite|flagFast, cmdDel)
	registerCommand("EXISTS", -2, flagReadOnly|flagFast, cmdExists)
	registerCommand("TYPE", 2, flagReadOnly|flagFast, cmdType)
	registerCommand("KEYS", 2, flagReadOnly, cmdKeys)
	registerCommand("SCAN", -2, flagReadOnly, cmdScan)
	registerCommand("RANDOMKEY", 1, flagReadOnly, cmdRandomKey)
	registerCommand("RENAME", 3, flagWrite, cmdRename)
	registerCommand("RENAMENX", 3, flagWrite, cmdRenameNX)
	registerCommand("COPY", -3, flagWrite, cmdCopy)
	registerCommand("EXPIRE", -3, flagWrite|flagFast, expireHandler("expire", 1000, false))
	registerCommand("PEXPIRE", -3, flagWrite|flagFast, expireHandler("pexpire", 1, false))
	registerCommand("EXPIREAT", -3, flagWrite|flagFast, expireHandler("expireat", 1000, true))
	registerCommand("PEXPIREAT", -3, flagWrite|flagFast, expireHandler("pexpireat", 1, true))
	registerCommand("TTL", 2, flagReadOnly|flagFast, cmdTTL)
	registerCommand("PTTL", 2, flagReadOnly|flagFast, cmdPTTL)
	registerCommand("PERSIST", 2, flagWrite|flagFast, cmdPersist)
	registerCommand("EXPIRETIME", 2, flagReadOnly|flagFast, cmdExpireTime)
	registerCommand("PEXPIRETIME", 2, flagReadOnly|flagFast, cmdPExpireTime)
	registerCommand("TOUCH", -2, flagReadOnly|flagFast, cmdTouch)
	registerCommand("DBSIZE", 1, flagReadOnly|flagFast, cmdDBSize)
	registerCommand("FLUSHDB", -1, flagWrite, cmdFlush)
	registerCommand("FLUSHALL", -1, flagWrite, cmdFlush)
}

func cmdDel(c *client, args [][]byte) (result, error) {
	keys := make([]string, 0, len(args)-1)
	for _, key := range args[1:] {
		keys = append(keys, string(key))
	}
	n := c.srv.data.Del(keys)
	return result{reply: intReply(n), mutated: n > 0}, nil
}

func cmdExists(c *client, args [][]byte) (result, error) {
	keys := make([]string, 0, len(args)-1)
	for _, key := range args[1:] {
		keys = append(keys, string(key))
	}
	return result{reply: intReply(c.srv.data.Exists(keys))}, nil
}

func cmdType(c *client, args [][]byte) (result, error) {
	return result{reply: simpleReply(c.srv.data.Type(string(args[1])))}, nil
}

func cmdKeys(c *client, args [][]byte) (result, error) {
	keys := c.srv.data.Keys(string(args[1]))
	out := make(arrayReply, 0, len(keys))
	for _, key := range keys {
		out = append(out, bulkReply(key))
	}
	return result{reply: out}, nil
}

func cmdScan(c *client, args [][]byte) (result, error) {
	cursor, err := strconv.ParseUint(string(args[1]), 10, 64)
	if err != nil {
		return result{}, fmt.Errorf("ERR invalid cursor")
	}

	pattern := ""
	count := 10
	typeName := ""
	for i := 2; i < len(args); i++ {
		opt := strings.ToUpper(string(args[i]))
		switch opt {
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
		case "TYPE":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			typeName = strings.ToLower(string(args[i]))
		default:
			return result{}, errSyntax
		}
	}

	next, keys := c.srv.data.Scan(cursor, pattern, count, typeName)
	items := make(arrayReply, 0, len(keys))
	for _, key := range keys {
		items = append(items, bulkReply(key))
	}
	return result{reply: arrayReply{bulkReply(strconv.FormatUint(next, 10)), items}}, nil
}

func cmdRandomKey(c *client, args [][]byte) (result, error) {
	key, ok := c.srv.data.RandomKey()
	return result{reply: bulkOrNull([]byte(key), ok)}, nil
}

func cmdRename(c *client, args [][]byte) (result, error) {
	if !c.srv.data.Rename(string(args[1]), string(args[2])) {
		return result{}, fmt.Errorf("ERR no such key")
	}
	return result{reply: simpleReply("OK"), mutated: true}, nil
}

func cmdRenameNX(c *client, args [][]byte) (result, error) {
	src := string(args[1])
	if c.srv.data.Exists([]string{src}) == 0 {
		return result{}, fmt.Errorf("ERR no such key")
	}
	if !c.srv.data.RenameNX(src, string(args[2])) {
		return result{reply: intReply(0)}, nil
	}
	return result{reply: intReply(1), mutated: true}, nil
}

func cmdCopy(c *client, args [][]byte) (result, error) {
	src, dst := string(args[1]), string(args[2])
	if src == dst {
		return result{}, fmt.Errorf("ERR source and destination objects are the same")
	}

	replace := false
	seenDB := false
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "REPLACE":
			replace = true
		case "DB":
			if seenDB || i+1 >= len(args) {
				return result{}, errSyntax
			}
			seenDB = true
			i++
			db, err := parseInt(args[i])
			if err != nil || db != 0 {
				return result{}, fmt.Errorf("ERR DB index is out of range")
			}
		default:
			return result{}, errSyntax
		}
	}

	if !c.srv.data.Copy(src, dst, replace) {
		return result{reply: intReply(0)}, nil
	}
	return result{reply: intReply(1), mutated: true}, nil
}

func expireHandler(cmdName string, unitMs int64, absolute bool) handlerFunc {
	return func(c *client, args [][]byte) (result, error) {
		key := string(args[1])
		n, err := parseInt(args[2])
		if err != nil {
			return result{}, err
		}

		mode := store.ExpireAlways
		seen := false
		for _, raw := range args[3:] {
			var next store.ExpireMode
			switch strings.ToUpper(string(raw)) {
			case "NX":
				next = store.ExpireNX
			case "XX":
				next = store.ExpireXX
			case "GT":
				next = store.ExpireGT
			case "LT":
				next = store.ExpireLT
			default:
				return result{}, errSyntax
			}
			if seen {
				return result{}, fmt.Errorf("ERR NX and XX, GT or LT options at the same time are not compatible")
			}
			seen = true
			mode = next
		}

		now := c.srv.nowMs()
		var at int64
		switch {
		case n < 0:
			// A negative TTL always deletes the key; clamping avoids
			// multiplication overflow without changing the outcome.
			at = 0
		case absolute:
			if n > math.MaxInt64/unitMs {
				return result{}, invalidExpireTime(cmdName)
			}
			at = n * unitMs
		default:
			if n > (math.MaxInt64-now)/unitMs {
				return result{}, invalidExpireTime(cmdName)
			}
			at = now + n*unitMs
		}

		if !c.srv.data.Expire(key, at, mode) {
			return result{reply: intReply(0)}, nil
		}
		return result{
			reply:   intReply(1),
			mutated: true,
			rewrite: [][]byte{[]byte("PEXPIREAT"), []byte(key), []byte(strconv.FormatInt(at, 10))},
		}, nil
	}
}

func cmdTTL(c *client, args [][]byte) (result, error) {
	ms := c.srv.data.TTLMillis(string(args[1]))
	if ms < 0 {
		return result{reply: intReply(ms)}, nil
	}
	return result{reply: intReply((ms + 500) / 1000)}, nil
}

func cmdPTTL(c *client, args [][]byte) (result, error) {
	return result{reply: intReply(c.srv.data.TTLMillis(string(args[1])))}, nil
}

func cmdPersist(c *client, args [][]byte) (result, error) {
	if !c.srv.data.Persist(string(args[1])) {
		return result{reply: intReply(0)}, nil
	}
	return result{reply: intReply(1), mutated: true}, nil
}

func cmdExpireTime(c *client, args [][]byte) (result, error) {
	at := c.srv.data.ExpireTimeMillis(string(args[1]))
	if at < 0 {
		return result{reply: intReply(at)}, nil
	}
	return result{reply: intReply(at / 1000)}, nil
}

func cmdPExpireTime(c *client, args [][]byte) (result, error) {
	return result{reply: intReply(c.srv.data.ExpireTimeMillis(string(args[1])))}, nil
}

func cmdTouch(c *client, args [][]byte) (result, error) {
	keys := make([]string, 0, len(args)-1)
	for _, key := range args[1:] {
		keys = append(keys, string(key))
	}
	return result{reply: intReply(c.srv.data.Exists(keys))}, nil
}

func cmdDBSize(c *client, args [][]byte) (result, error) {
	return result{reply: intReply(c.srv.data.DBSize())}, nil
}

func cmdFlush(c *client, args [][]byte) (result, error) {
	if len(args) > 2 {
		return result{}, errSyntax
	}
	if len(args) == 2 {
		switch strings.ToUpper(string(args[1])) {
		case "ASYNC", "SYNC":
		default:
			return result{}, errSyntax
		}
	}
	c.srv.data.Flush()
	return result{reply: simpleReply("OK"), mutated: true, rewrite: [][]byte{[]byte("FLUSHALL")}}, nil
}
