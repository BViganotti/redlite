package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BViganotti/redlite/internal/store"
)

func cmdConfig(c *client, args [][]byte) (result, error) {
	sub := strings.ToUpper(string(args[1]))
	switch sub {
	case "GET":
		if len(args) < 3 {
			return result{}, errSyntax
		}
		snapshot := c.srv.configSnapshot()
		names := make([]string, 0, len(snapshot))
		for name := range snapshot {
			names = append(names, name)
		}
		sort.Strings(names)

		out := make(mapReply, 0, len(names)*2)
		for _, name := range names {
			for _, pattern := range args[2:] {
				if store.Match(string(pattern), name) {
					out = append(out, bulkReply(name), bulkReply(snapshot[name]))
					break
				}
			}
		}
		return result{reply: out}, nil

	case "SET":
		if len(args) != 4 {
			return result{}, errSyntax
		}
		if err := c.srv.setConfig(string(args[2]), string(args[3])); err != nil {
			return result{}, err
		}
		return result{reply: simpleReply("OK")}, nil

	case "RESETSTAT":
		c.srv.resetStats()
		return result{reply: simpleReply("OK")}, nil

	case "REWRITE":
		return result{}, fmt.Errorf("ERR The server is running without a config file")

	case "HELP":
		return result{reply: arrayReply{
			bulkReply("CONFIG <subcommand>"),
			bulkReply("GET <pattern>, SET <parameter> <value>, RESETSTAT"),
		}}, nil

	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for CONFIG", printable(args[1]))
	}
}
