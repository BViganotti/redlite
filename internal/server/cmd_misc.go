package server

import (
	"fmt"
	"strings"

	"github.com/BViganotti/redlite/internal/version"
)

func init() {
	registerCommand("HSTRLEN", 3, flagReadOnly|flagFast, cmdHStrLen)
	registerCommand("HRANDFIELD", -2, flagReadOnly, cmdHRandField)
	registerCommand("LINSERT", 5, flagWrite, cmdLInsert)
	registerCommand("LPOS", -3, flagReadOnly, cmdLPos)
	registerCommand("COPY", -3, flagWrite, cmdCopy)
	registerCommand("OBJECT", -2, 0, cmdObject)
	registerCommand("RESET", 1, flagFast, cmdReset)
	registerCommand("LOLWUT", -1, flagReadOnly, cmdLolwut)
	registerCommand("WAIT", 3, 0, cmdWait)
	registerCommand("WAITAOF", 4, flagBlocking, cmdWaitAOF)
	registerCommand("MEMORY", -2, 0, cmdMemory)
}

func cmdHStrLen(c *client, args [][]byte) (result, error) {
	n, err := c.srv.data.HStrLen(string(args[1]), string(args[2]))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdHRandField(c *client, args [][]byte) (result, error) {
	key := string(args[1])
	if len(args) == 2 {
		fields, err := c.srv.data.HRandField(key, 1)
		if err != nil {
			return result{}, err
		}
		if len(fields) == 0 {
			return result{reply: null}, nil
		}
		return result{reply: bulkReply(fields[0].Field)}, nil
	}

	if len(args) < 3 || len(args) > 4 {
		return result{}, errSyntax
	}
	count, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	withValues := false
	if len(args) == 4 {
		if !strings.EqualFold(string(args[3]), "WITHVALUES") {
			return result{}, errSyntax
		}
		withValues = true
	}

	fields, err := c.srv.data.HRandField(key, int(count))
	if err != nil {
		return result{}, err
	}
	if !withValues {
		out := make(arrayReply, 0, len(fields))
		for _, field := range fields {
			out = append(out, bulkReply(field.Field))
		}
		return result{reply: out}, nil
	}
	out := make(arrayReply, 0, len(fields)*2)
	for _, field := range fields {
		out = append(out, bulkReply(field.Field), bulkReply(field.Value))
	}
	return result{reply: out}, nil
}

func cmdLInsert(c *client, args [][]byte) (result, error) {
	var before bool
	switch strings.ToUpper(string(args[2])) {
	case "BEFORE":
		before = true
	case "AFTER":
		before = false
	default:
		return result{}, errSyntax
	}
	n, err := c.srv.data.LInsert(string(args[1]), before, args[3], args[4])
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n), mutated: n > 0}, nil
}

func cmdLPos(c *client, args [][]byte) (result, error) {
	key := string(args[1])
	element := args[2]
	rank := 1
	count := -1 // not supplied
	maxlen := 0

	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "RANK":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			value, err := parseInt(args[i])
			if err != nil {
				return result{}, errSyntax
			}
			if value == 0 {
				return result{}, fmt.Errorf("ERR RANK can't be zero")
			}
			rank = int(value)
		case "COUNT":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			value, err := parseInt(args[i])
			if err != nil || value < 0 {
				return result{}, fmt.Errorf("ERR COUNT can't be negative")
			}
			count = int(value)
		case "MAXLEN":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			value, err := parseInt(args[i])
			if err != nil || value < 0 {
				return result{}, fmt.Errorf("ERR MAXLEN can't be negative")
			}
			maxlen = int(value)
		default:
			return result{}, errSyntax
		}
	}

	positions, err := c.srv.data.LPos(key, element, rank, count, maxlen)
	if err != nil {
		return result{}, err
	}

	if count == -1 {
		if len(positions) == 0 {
			return result{reply: null}, nil
		}
		return result{reply: intReply(positions[0])}, nil
	}
	out := make(arrayReply, 0, len(positions))
	for _, position := range positions {
		out = append(out, intReply(position))
	}
	return result{reply: out}, nil
}

func cmdCopy(c *client, args [][]byte) (result, error) {
	replace := false
	if len(args) == 4 {
		if !strings.EqualFold(string(args[3]), "REPLACE") {
			return result{}, errSyntax
		}
		replace = true
	} else if len(args) > 4 {
		return result{}, errSyntax
	}

	copied, err := c.srv.data.Copy(string(args[1]), string(args[2]), replace)
	if err != nil {
		return result{}, err
	}
	if copied {
		return result{reply: intReply(1), mutated: true}, nil
	}
	return result{reply: intReply(0)}, nil
}

func cmdObject(c *client, args [][]byte) (result, error) {
	sub := strings.ToUpper(string(args[1]))
	if sub == "HELP" {
		return result{reply: arrayReply{
			bulkReply("OBJECT <subcommand> [<key>]"),
			bulkReply("ENCODING, REFCOUNT, IDLETIME, FREQ"),
		}}, nil
	}
	if len(args) != 3 {
		return result{}, errSyntax
	}
	key := string(args[2])

	if c.srv.data.Type(key) == "none" {
		return result{reply: null}, nil
	}

	switch sub {
	case "ENCODING":
		encoding, err := c.srv.data.ObjectEncoding(key)
		if err != nil {
			return result{}, err
		}
		return result{reply: bulkReply(encoding)}, nil
	case "REFCOUNT":
		return result{reply: intReply(1)}, nil
	case "IDLETIME", "FREQ":
		return result{reply: intReply(0)}, nil
	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for OBJECT", printable(args[1]))
	}
}

func cmdReset(c *client, args [][]byte) (result, error) {
	c.resetMulti()
	c.replyMode = replyOn
	c.srv.removeMonitor(c)
	c.monitor = false
	c.srv.pubsub.removeAll(c)
	c.mu.Lock()
	c.subs = nil
	c.psubs = nil
	c.ssubs = nil
	c.cname = ""
	c.mu.Unlock()
	_ = c.setProto(2)
	if c.srv.authRequired() {
		c.authed = false
		c.setUser("default")
	}
	return result{reply: simpleReply("RESET")}, nil
}

func cmdLolwut(c *client, args [][]byte) (result, error) {
	return result{reply: bulkReply("redlite " + version.String() + "\nOne shard to rule them all.")}, nil
}

func cmdWait(c *client, args [][]byte) (result, error) {
	if _, err := parseInt(args[1]); err != nil {
		return result{}, err
	}
	if _, err := parseInt(args[2]); err != nil {
		return result{}, err
	}
	return result{reply: intReply(int64(c.srv.replicaCount()))}, nil
}

func cmdWaitAOF(c *client, args [][]byte) (result, error) {
	local, err := parseInt(args[1])
	if err != nil || local < 0 || local > 1 {
		return result{}, fmt.Errorf("ERR value is out of range, value must between 0 and 1")
	}
	replicas, err := parseInt(args[2])
	if err != nil || replicas < 0 || replicas > 1 {
		return result{}, fmt.Errorf("ERR value is out of range, value must between 0 and 1")
	}
	if _, err := parseInt(args[3]); err != nil {
		return result{}, err
	}
	if local > 0 && c.srv.aof == nil {
		return result{}, fmt.Errorf("ERR WAITAOF cannot be used when numlocal is set but appendonly is disabled.")
	}
	var acked int64
	if local > 0 && c.srv.aof != nil {
		acked = 1
	}
	_ = replicas
	return result{reply: arrayReply{intReply(acked), intReply(0)}}, nil
}

func cmdMemory(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "USAGE":
		if len(args) < 3 {
			return result{}, errSyntax
		}
		usage, ok, err := c.srv.data.MemoryUsage(string(args[2]))
		if err != nil {
			return result{}, err
		}
		if !ok {
			return result{reply: null}, nil
		}
		return result{reply: intReply(usage)}, nil
	case "DOCTOR":
		return result{reply: bulkReply("Sam Redlite looks fine.")}, nil
	case "STATS":
		return result{reply: arrayReply{}}, nil
	case "PURGE":
		return result{reply: simpleReply("OK")}, nil
	case "HELP":
		return result{reply: arrayReply{
			bulkReply("MEMORY <subcommand>"),
			bulkReply("USAGE <key> [SAMPLES <count>], STATS, DOCTOR, PURGE"),
		}}, nil
	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for MEMORY", printable(args[1]))
	}
}
