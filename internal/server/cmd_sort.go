package server

import (
	"fmt"
	"strings"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("SORT", -2, flagWrite, cmdSort)
	registerCommand("SORT_RO", -2, flagReadOnly, func(c *client, args [][]byte) (result, error) {
		return sortCommand(c, args, true)
	})
}

func cmdSort(c *client, args [][]byte) (result, error) {
	return sortCommand(c, args, false)
}

func sortCommand(c *client, args [][]byte, readOnly bool) (result, error) {
	opts := store.SortOptions{Count: -1}
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "ASC":
			opts.Desc = false
		case "DESC":
			opts.Desc = true
		case "ALPHA":
			opts.Alpha = true
		case "LIMIT":
			if i+2 >= len(args) {
				return result{}, errSyntax
			}
			offset, err := parseInt(args[i+1])
			if err != nil {
				return result{}, err
			}
			count, err := parseInt(args[i+2])
			if err != nil {
				return result{}, err
			}
			opts.Limit = true
			opts.Offset = offset
			opts.Count = count
			i += 2
		case "BY":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			opts.ByPattern = string(args[i])
		case "GET":
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			opts.GetPatterns = append(opts.GetPatterns, string(args[i]))
		case "STORE":
			if readOnly {
				return result{}, errSyntax
			}
			if i+1 >= len(args) {
				return result{}, errSyntax
			}
			i++
			opts.Store = string(args[i])
		default:
			return result{}, errSyntax
		}
	}

	items, err := c.srv.data.Sort(string(args[1]), opts)
	if err != nil {
		if err == store.ErrSortNotDouble {
			return result{}, fmt.Errorf("ERR %s", err.Error())
		}
		return result{}, err
	}

	if opts.Store != "" {
		return result{reply: intReply(int64(len(items))), mutated: true}, nil
	}

	out := make(arrayReply, 0, len(items))
	for _, item := range items {
		if item.Missing {
			out = append(out, null)
			continue
		}
		out = append(out, bulkReply(item.Value))
	}
	return result{reply: out}, nil
}
