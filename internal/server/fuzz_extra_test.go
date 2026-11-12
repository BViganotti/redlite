package server

import (
	"strings"
	"testing"
)

func fields(line string) [][]byte {
	parts := strings.Fields(line)
	out := make([][]byte, len(parts))
	for i, part := range parts {
		out[i] = []byte(part)
	}
	return out
}

func FuzzParseBitFieldOps(f *testing.F) {
	f.Add("GET u8 0")
	f.Add("SET u8 0 255 OVERFLOW SAT INCRBY i8 0 10")
	f.Add("OVERFLOW FAIL INCRBY u63 315 -111")
	f.Add("SET bad")
	f.Fuzz(func(t *testing.T, line string) {
		_, _ = parseBitFieldOps(fields(line))
	})
}

func FuzzParseZRangeSpec(f *testing.F) {
	f.Add("ZRANGE k 0 -1")
	f.Add("ZRANGE k [a + BYLEX REV LIMIT 0 10")
	f.Add("ZRANGE k -inf +inf BYSCORE WITHSCORES")
	f.Add("ZRANGESTORE d k 1 2 BYSCORE")
	f.Fuzz(func(t *testing.T, line string) {
		args := fields(line)
		if len(args) < 4 {
			return
		}
		_, _, _ = parseZRangeSpec(args, 2, 4, false, true)
	})
}

func FuzzParseMultiPop(f *testing.F) {
	f.Add("LMPOP 1 k LEFT")
	f.Add("LMPOP 2 a b RIGHT COUNT 10")
	f.Add("ZMPOP 1 z MIN COUNT 2")
	f.Add("BZMPOP 1 2 a b MAX")
	f.Fuzz(func(t *testing.T, line string) {
		args := fields(line)
		if len(args) < 4 {
			return
		}
		_, _, _, _ = parseMPop(args, 1)
		_, _, _, _ = parseZMPop(args, 1)
	})
}
