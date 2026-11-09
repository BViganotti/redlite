package server

import (
	"fmt"
	"strings"
)

func init() {
	registerCommand("SETBIT", 4, flagWrite, cmdSetBit)
	registerCommand("GETBIT", 3, flagReadOnly|flagFast, cmdGetBit)
	registerCommand("BITCOUNT", -2, flagReadOnly, cmdBitCount)
	registerCommand("BITPOS", -3, flagReadOnly, cmdBitPos)
	registerCommand("BITOP", -4, flagWrite, cmdBitOp)
}

func cmdSetBit(c *client, args [][]byte) (result, error) {
	offset, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	bit, err := parseInt(args[3])
	if err != nil {
		return result{}, err
	}
	if bit != 0 && bit != 1 {
		return result{}, fmt.Errorf("ERR bit is not an integer or out of range")
	}
	old, err := c.srv.data.SetBit(string(args[1]), offset, int(bit))
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(old), mutated: true}, nil
}

func cmdGetBit(c *client, args [][]byte) (result, error) {
	offset, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	bit, err := c.srv.data.GetBit(string(args[1]), offset)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(bit)}, nil
}

func cmdBitCount(c *client, args [][]byte) (result, error) {
	key := string(args[1])
	if len(args) == 2 {
		n, err := c.srv.data.BitCount(key, 0, -1, false)
		if err != nil {
			return result{}, err
		}
		return result{reply: intReply(n)}, nil
	}
	if len(args) != 4 && len(args) != 5 {
		return result{}, errSyntax
	}

	start, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	end, err := parseInt(args[3])
	if err != nil {
		return result{}, err
	}
	unit := "BYTE"
	if len(args) == 5 {
		unit = strings.ToUpper(string(args[4]))
	}

	switch unit {
	case "BYTE":
		n, err := c.srv.data.BitCount(key, start, end, true)
		if err != nil {
			return result{}, err
		}
		return result{reply: intReply(n)}, nil
	case "BIT":
		n, err := c.srv.data.BitCountBits(key, start, end)
		if err != nil {
			return result{}, err
		}
		return result{reply: intReply(n)}, nil
	default:
		return result{}, errSyntax
	}
}

func cmdBitPos(c *client, args [][]byte) (result, error) {
	bit, err := parseInt(args[2])
	if err != nil {
		return result{}, err
	}
	if bit != 0 && bit != 1 {
		return result{}, fmt.Errorf("ERR The bit argument must be 1 or 0.")
	}

	var (
		start, end int64
		hasRange   bool
	)
	if len(args) >= 4 {
		hasRange = true
		start, err = parseInt(args[3])
		if err != nil {
			return result{}, err
		}
		end = -1
	}
	if len(args) >= 5 {
		end, err = parseInt(args[4])
		if err != nil {
			return result{}, err
		}
	}
	if len(args) >= 6 {
		if !strings.EqualFold(string(args[5]), "BYTE") {
			return result{}, errSyntax
		}
	}
	if len(args) > 6 {
		return result{}, errSyntax
	}

	position, err := c.srv.data.BitPos(string(args[1]), int(bit), start, end, hasRange)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(position)}, nil
}

func cmdBitOp(c *client, args [][]byte) (result, error) {
	op := strings.ToUpper(string(args[1]))
	sources := make([]string, 0, len(args)-3)
	for _, key := range args[3:] {
		sources = append(sources, string(key))
	}
	n, err := c.srv.data.BitOp(op, string(args[2]), sources)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n), mutated: true}, nil
}
