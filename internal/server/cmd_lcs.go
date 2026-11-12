package server

import (
	"fmt"
	"strings"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("LCS", -3, flagReadOnly, cmdLCS)
	registerCommand("SUBSTR", 4, flagReadOnly, cmdGetRange)
}

func cmdLCS(c *client, args [][]byte) (result, error) {
	getLen := false
	getIdx := false
	withMatchLen := false
	minMatchLen := int64(0)

	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "IDX":
			getIdx = true
		case "LEN":
			getLen = true
		case "WITHMATCHLEN":
			withMatchLen = true
		case "MINMATCHLEN":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			value, err := parseInt(args[i])
			if err != nil {
				return result{}, errInteger
			}
			if value < 0 {
				value = 0
			}
			minMatchLen = value
		default:
			return result{}, errSyntax
		}
	}
	if getLen && getIdx {
		return result{}, fmt.Errorf("ERR If you want both the length and indexes, please just use IDX.")
	}

	res, err := c.srv.data.LCS(string(args[1]), string(args[2]), getLen, getIdx, minMatchLen)
	if err != nil {
		if err == store.ErrLCSWrongType {
			return result{}, fmt.Errorf("ERR %s", err.Error())
		}
		return result{}, fmt.Errorf("ERR %s", err.Error())
	}

	if getIdx {
		matchesReply := make(arrayReply, 0, len(res.Matches))
		for _, match := range res.Matches {
			item := make(arrayReply, 0, 3)
			item = append(item,
				arrayReply{intReply(match.AStart), intReply(match.AEnd)},
				arrayReply{intReply(match.BStart), intReply(match.BEnd)})
			if withMatchLen {
				item = append(item, intReply(match.Length))
			}
			matchesReply = append(matchesReply, item)
		}
		return result{reply: mapReply{
			bulkReply("matches"), matchesReply,
			bulkReply("len"), intReply(res.Length),
		}}, nil
	}
	if getLen {
		return result{reply: intReply(res.Length)}, nil
	}
	return result{reply: bulkReply(res.Sequence)}, nil
}
