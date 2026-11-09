package server

import "strings"

// noKeyCommands never take a key argument, or take names that are not keys
// (channels, patterns, addresses).
var noKeyCommands = map[string]bool{
	"PING": true, "ECHO": true, "QUIT": true, "SELECT": true, "HELLO": true,
	"CLIENT": true, "COMMAND": true, "CONFIG": true, "INFO": true, "TIME": true,
	"SHUTDOWN": true, "BGREWRITEAOF": true, "SAVE": true, "BGSAVE": true,
	"LASTSAVE": true, "MULTI": true, "EXEC": true, "DISCARD": true,
	"UNWATCH": true, "PUBLISH": true, "SUBSCRIBE": true, "UNSUBSCRIBE": true,
	"PSUBSCRIBE": true, "PUNSUBSCRIBE": true, "PUBSUB": true, "REPLCONF": true,
	"PSYNC": true, "REPLICAOF": true, "SLAVEOF": true, "SCAN": true,
	"KEYS": true, "RANDOMKEY": true, "DBSIZE": true, "FLUSHDB": true,
	"FLUSHALL": true, "AUTH": true, "ACL": true, "RESET": true,
}

// commandKeys extracts key arguments using the same shapes the commands
// themselves use.
func commandKeys(name string, args [][]byte) [][]byte {
	name = strings.ToUpper(name)
	if noKeyCommands[name] {
		return nil
	}

	switch name {
	case "MSET", "MSETNX":
		out := make([][]byte, 0, (len(args)-1)/2)
		for i := 1; i < len(args); i += 2 {
			out = append(out, args[i])
		}
		return out

	case "MGET", "DEL", "UNLINK", "EXISTS", "TOUCH", "WATCH",
		"SINTER", "SUNION", "SDIFF":
		return args[1:]

	case "BLPOP", "BRPOP":
		return args[1 : len(args)-1]

	case "LMOVE", "BLMOVE", "RENAME", "RENAMENX", "RPOPLPUSH", "BRPOPLPUSH":
		return args[1:3]

	default:
		if len(args) < 2 {
			return nil
		}
		return args[1:2]
	}
}
