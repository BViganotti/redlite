package server

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("BITFIELD", -2, flagWrite, cmdBitField)
	registerCommand("BITFIELD_RO", -2, flagReadOnly, cmdBitFieldRO)
}

var errBitFieldType = fmt.Errorf("ERR Invalid bitfield type. Use something like i16 u8. Note that u64 is not supported but i64 is.")

func cmdBitFieldRO(c *client, args [][]byte) (result, error) {
	ops, err := parseBitFieldOps(args[2:])
	if err != nil {
		return result{}, err
	}
	for _, op := range ops {
		if op.Kind != 'G' {
			return result{}, fmt.Errorf("ERR BITFIELD_RO only supports the GET subcommand")
		}
	}
	return executeBitField(c, args[1], ops, true)
}

func cmdBitField(c *client, args [][]byte) (result, error) {
	ops, err := parseBitFieldOps(args[2:])
	if err != nil {
		return result{}, err
	}
	return executeBitField(c, args[1], ops, false)
}

func executeBitField(c *client, key []byte, ops []store.BitFieldOp, readOnly bool) (result, error) {
	results, err := c.srv.data.BitField(string(key), ops, readOnly)
	if err != nil {
		if err == store.ErrOffsetOutOfRange {
			return result{}, fmt.Errorf("ERR bit offset is not an integer or out of range")
		}
		return result{}, err
	}
	out := make(arrayReply, 0, len(results))
	mutated := false
	for i, res := range results {
		if !res.Valid {
			out = append(out, null)
		} else {
			out = append(out, intReply(res.Value))
		}
		if ops[i].Kind != 'G' {
			mutated = true
		}
	}
	return result{reply: out, mutated: mutated}, nil
}

func parseBitFieldOps(args [][]byte) ([]store.BitFieldOp, error) {
	ops := make([]store.BitFieldOp, 0, len(args)/3)
	overflow := byte('W')
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "OVERFLOW":
			if i+1 >= len(args) {
				return nil, errSyntax
			}
			i++
			switch strings.ToUpper(string(args[i])) {
			case "WRAP":
				overflow = 'W'
			case "SAT":
				overflow = 'S'
			case "FAIL":
				overflow = 'F'
			default:
				return nil, fmt.Errorf("ERR Invalid OVERFLOW type specified")
			}
		case "GET":
			if i+2 >= len(args) {
				return nil, errSyntax
			}
			op, err := parseBitFieldType(args[i+1])
			if err != nil {
				return nil, err
			}
			op.Kind = 'G'
			op.Offset, err = parseBitFieldOffset(args[i+2], op.Bits)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i += 2
		case "SET":
			if i+3 >= len(args) {
				return nil, errSyntax
			}
			op, err := parseBitFieldType(args[i+1])
			if err != nil {
				return nil, err
			}
			op.Kind = 'S'
			op.Overflow = overflow
			op.Offset, err = parseBitFieldOffset(args[i+2], op.Bits)
			if err != nil {
				return nil, err
			}
			op.Value, err = parseInt(args[i+3])
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i += 3
		case "INCRBY":
			if i+3 >= len(args) {
				return nil, errSyntax
			}
			op, err := parseBitFieldType(args[i+1])
			if err != nil {
				return nil, err
			}
			op.Kind = 'I'
			op.Overflow = overflow
			op.Offset, err = parseBitFieldOffset(args[i+2], op.Bits)
			if err != nil {
				return nil, err
			}
			op.Value, err = parseInt(args[i+3])
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i += 3
		default:
			return nil, errSyntax
		}
	}
	return ops, nil
}

func parseBitFieldType(arg []byte) (store.BitFieldOp, error) {
	text := string(arg)
	if len(text) < 2 {
		return store.BitFieldOp{}, errBitFieldType
	}
	var signed bool
	switch text[0] {
	case 'i', 'I':
		signed = true
	case 'u', 'U':
		signed = false
	default:
		return store.BitFieldOp{}, errBitFieldType
	}
	bits, err := strconv.Atoi(text[1:])
	if err != nil || bits < 1 || bits > 64 {
		return store.BitFieldOp{}, errBitFieldType
	}
	if !signed && bits == 64 {
		return store.BitFieldOp{}, errBitFieldType
	}
	return store.BitFieldOp{Signed: signed, Bits: bits}, nil
}

func parseBitFieldOffset(arg []byte, bits int) (int64, error) {
	text := string(arg)
	multiplier := int64(1)
	if strings.HasPrefix(text, "#") {
		text = text[1:]
		multiplier = int64(bits)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("ERR bit offset is not an integer or out of range")
	}
	if value > (1<<62)/multiplier {
		return 0, fmt.Errorf("ERR bit offset is not an integer or out of range")
	}
	return value * multiplier, nil
}
