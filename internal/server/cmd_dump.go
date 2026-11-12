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
	registerCommand("DUMP", 2, flagReadOnly, cmdDump)
	registerCommand("RESTORE", -4, flagWrite, cmdRestore)
}

func cmdDump(c *client, args [][]byte) (result, error) {
	payload, ok, err := c.srv.data.Dump(string(args[1]))
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: null}, nil
	}
	return result{reply: bulkReply(payload)}, nil
}

func cmdRestore(c *client, args [][]byte) (result, error) {
	ttl, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	if ttl < 0 {
		return result{}, fmt.Errorf("ERR Invalid TTL value, must be >= 0")
	}

	replace := false
	absTTL := false
	for i := 4; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "REPLACE":
			replace = true
		case "ABSTTL":
			absTTL = true
		case "IDLETIME":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			value, err := parseInt(args[i])
			if err != nil || value < 0 {
				return result{}, fmt.Errorf("ERR Invalid IDLETIME value, must be >= 0")
			}
		case "FREQ":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			value, err := parseInt(args[i])
			if err != nil || value < 0 || value > 255 {
				return result{}, fmt.Errorf("ERR Invalid FREQ value, must be >= 0 and <= 255")
			}
		default:
			return result{}, errSyntax
		}
	}

	now := c.srv.nowMs()
	expireAt := int64(0)
	if ttl > 0 {
		if absTTL {
			expireAt = ttl
		} else {
			if ttl > math.MaxInt64-now {
				return result{}, fmt.Errorf("ERR Invalid TTL value, must be >= 0")
			}
			expireAt = now + ttl
		}
	}

	err = c.srv.data.Restore(string(args[1]), args[3], expireAt, replace)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrBusyKey):
		return result{}, fmt.Errorf("BUSYKEY Target key name already exists.")
	case errors.Is(err, store.ErrDumpPayload):
		return result{}, fmt.Errorf("ERR DUMP payload version or checksum are wrong")
	default:
		return result{}, err
	}

	rewrite := [][]byte{
		[]byte("RESTORE"),
		args[1],
		[]byte(strconv.FormatInt(expireAt, 10)),
		args[3],
		[]byte("ABSTTL"),
		[]byte("REPLACE"),
	}
	return result{reply: simpleReply("OK"), mutated: true, rewrite: rewrite}, nil
}
