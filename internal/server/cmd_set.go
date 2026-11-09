package server

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func init() {
	registerCommand("SADD", -3, flagWrite, cmdSAdd)
	registerCommand("SREM", -3, flagWrite|flagFast, cmdSRem)
	registerCommand("SCARD", 2, flagReadOnly|flagFast, cmdSCard)
	registerCommand("SISMEMBER", 3, flagReadOnly|flagFast, cmdSIsMember)
	registerCommand("SMISMEMBER", -3, flagReadOnly|flagFast, cmdSMIsMember)
	registerCommand("SMEMBERS", 2, flagReadOnly, cmdSMembers)
	registerCommand("SINTER", -2, flagReadOnly, cmdSInter)
	registerCommand("SUNION", -2, flagReadOnly, cmdSUnion)
	registerCommand("SDIFF", -2, flagReadOnly, cmdSDiff)
	registerCommand("SPOP", -2, flagWrite|flagFast, cmdSPop)
	registerCommand("SRANDMEMBER", -2, flagReadOnly, cmdSRandMember)
	registerCommand("SSCAN", -3, flagReadOnly, cmdSScan)
}

func memberArgs(args [][]byte) [][]byte {
	return args[2:]
}

func cmdSAdd(c *client, args [][]byte) (result, error) {
	added, err := c.srv.data.SAdd(string(args[1]), memberArgs(args))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(added), mutated: added > 0}, nil
}

func cmdSRem(c *client, args [][]byte) (result, error) {
	removed, err := c.srv.data.SRem(string(args[1]), memberArgs(args))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(removed), mutated: removed > 0}, nil
}

func cmdSCard(c *client, args [][]byte) (result, error) {
	n, err := c.srv.data.SCard(string(args[1]))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n)}, nil
}

func cmdSIsMember(c *client, args [][]byte) (result, error) {
	ok, err := c.srv.data.SIsMember(string(args[1]), args[2])
	if err != nil {
		return result{}, err
	}
	if ok {
		return result{reply: intReply(1)}, nil
	}
	return result{reply: intReply(0)}, nil
}

func cmdSMIsMember(c *client, args [][]byte) (result, error) {
	out := make(arrayReply, 0, len(args)-2)
	for _, member := range args[2:] {
		ok, err := c.srv.data.SIsMember(string(args[1]), member)
		if err != nil {
			return result{}, err
		}
		if ok {
			out = append(out, intReply(1))
		} else {
			out = append(out, intReply(0))
		}
	}
	return result{reply: out}, nil
}

func cmdSMembers(c *client, args [][]byte) (result, error) {
	members, err := c.srv.data.SMembers(string(args[1]))
	if err != nil {
		return result{}, err
	}
	return result{reply: bulkArray(members)}, nil
}

func cmdSInter(c *client, args [][]byte) (result, error) {
	return setAlgebraReply(c, args, c.srv.data.SInter)
}

func cmdSUnion(c *client, args [][]byte) (result, error) {
	return setAlgebraReply(c, args, c.srv.data.SUnion)
}

func cmdSDiff(c *client, args [][]byte) (result, error) {
	return setAlgebraReply(c, args, c.srv.data.SDiff)
}

func setAlgebraReply(c *client, args [][]byte, op func([]string) ([][]byte, error)) (result, error) {
	keys := make([]string, 0, len(args)-1)
	for _, key := range args[1:] {
		keys = append(keys, string(key))
	}
	members, err := op(keys)
	if err != nil {
		return result{}, err
	}
	return result{reply: bulkArray(members)}, nil
}

func cmdSPop(c *client, args [][]byte) (result, error) {
	count, withCount, err := parsePopCount(args, "spop")
	if err != nil {
		return result{}, err
	}
	members, err := c.srv.data.SPop(string(args[1]), count)
	if err != nil {
		return result{}, err
	}

	out := result{mutated: len(members) > 0}
	if !withCount {
		if len(members) == 0 {
			out.reply = null
			return out, nil
		}
		out.reply = bulkReply(members[0])
		return out, nil
	}
	out.reply = bulkArray(members)
	return out, nil
}

func cmdSRandMember(c *client, args [][]byte) (result, error) {
	count, withCount, err := parseRandCount(args, "srandmember")
	if err != nil {
		return result{}, err
	}
	members, err := c.srv.data.SRandMember(string(args[1]), count)
	if err != nil {
		return result{}, err
	}

	if !withCount {
		if len(members) == 0 {
			return result{reply: null}, nil
		}
		return result{reply: bulkReply(members[0])}, nil
	}
	return result{reply: bulkArray(members)}, nil
}

func parseRandCount(args [][]byte, cmdName string) (int, bool, error) {
	if len(args) == 2 {
		return 1, false, nil
	}
	if len(args) > 3 {
		return 0, true, fmt.Errorf("ERR wrong number of arguments for '%s' command", cmdName)
	}
	n, err := parseInt(args[2])
	if err != nil {
		return 0, true, err
	}
	switch {
	case n > math.MaxInt32:
		n = math.MaxInt32
	case n < -math.MaxInt32:
		n = -math.MaxInt32
	}
	return int(n), true, nil
}

func cmdSScan(c *client, args [][]byte) (result, error) {
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

	next, members, err := c.srv.data.SScan(string(args[1]), cursor, pattern, count)
	if err != nil {
		return result{}, err
	}
	return result{reply: arrayReply{
		bulkReply(strconv.FormatUint(next, 10)),
		bulkArrayString(members),
	}}, nil
}

func bulkArray(values [][]byte) arrayReply {
	out := make(arrayReply, 0, len(values))
	for _, value := range values {
		out = append(out, bulkReply(value))
	}
	return out
}

func bulkArrayString(values []string) arrayReply {
	out := make(arrayReply, 0, len(values))
	for _, value := range values {
		out = append(out, bulkReply(value))
	}
	return out
}
