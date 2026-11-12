package server

import (
	"strconv"
	"strings"
)

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
	"SSUBSCRIBE": true, "SUNSUBSCRIBE": true, "SPUBLISH": true,
	"MONITOR": true, "SLOWLOG": true, "LATENCY": true, "DEBUG": true,
	"MODULE": true, "WAIT": true, "WAITAOF": true,
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
		"SINTER", "SUNION", "SDIFF", "SINTERSTORE", "SUNIONSTORE", "SDIFFSTORE":
		return args[1:]

	case "BLPOP", "BRPOP":
		return args[1 : len(args)-1]

	case "LMOVE", "BLMOVE", "RENAME", "RENAMENX", "RPOPLPUSH", "BRPOPLPUSH",
		"SMOVE", "COPY", "LCS", "ZRANGESTORE":
		if len(args) < 3 {
			return args[1:]
		}
		return args[1:3]

	case "LMPOP", "ZMPOP", "SINTERCARD", "ZINTERCARD":
		if n, ok := numberedKeys(args, 1); ok {
			return n
		}
		return nil

	case "BLMPOP", "BZMPOP":
		if n, ok := numberedKeys(args, 2); ok {
			return n
		}
		return nil

	case "SORT", "SORT_RO":
		keys := make([][]byte, 0, 2)
		keys = append(keys, args[1])
		for i := 2; i+1 < len(args); i++ {
			if strings.EqualFold(string(args[i]), "STORE") {
				keys = append(keys, args[i+1])
			}
		}
		return keys

	default:
		if len(args) < 2 {
			return nil
		}
		return args[1:2]
	}
}

// numberedKeys returns the numkeys-delimited key list starting at start.
func numberedKeys(args [][]byte, start int) ([][]byte, bool) {
	if start >= len(args) {
		return nil, false
	}
	n, err := strconv.Atoi(string(args[start]))
	if err != nil || n <= 0 || start+1+n > len(args) {
		return nil, false
	}
	return args[start+1 : start+1+n], true
}
