package server

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("PING", -1, flagFast, cmdPing)
	registerCommand("ECHO", 2, flagFast, cmdEcho)
	registerCommand("QUIT", -1, flagFast, cmdQuit)
	registerCommand("SELECT", 2, flagFast, cmdSelect)
	registerCommand("HELLO", -1, flagFast, cmdHello)
	registerCommand("CLIENT", -2, flagAdmin, cmdClient)
	registerCommand("COMMAND", -1, flagAdmin, cmdCommand)
	registerCommand("CONFIG", -2, flagAdmin, cmdConfig)
	registerCommand("INFO", -1, flagAdmin, cmdInfo)
	registerCommand("TIME", 1, flagFast, cmdTime)
	registerCommand("SHUTDOWN", -1, flagAdmin, cmdShutdown)
	registerCommand("BGREWRITEAOF", 1, flagAdmin, cmdBGRewriteAOF)
	registerCommand("LASTSAVE", 1, flagFast, cmdLastSave)
}

func cmdPing(c *client, args [][]byte) (result, error) {
	switch len(args) {
	case 1:
		if c.subscribed() {
			return result{reply: pushReply{bulkReply("pong"), bulkReply("")}}, nil
		}
		return result{reply: simpleReply("PONG")}, nil
	case 2:
		if c.subscribed() {
			return result{reply: pushReply{bulkReply("pong"), bulkReply(args[1])}}, nil
		}
		return result{reply: bulkReply(args[1])}, nil
	default:
		return result{}, fmt.Errorf("ERR wrong number of arguments for 'ping' command")
	}
}

func cmdEcho(c *client, args [][]byte) (result, error) {
	return result{reply: bulkReply(args[1])}, nil
}

func cmdQuit(c *client, args [][]byte) (result, error) {
	return result{reply: simpleReply("OK"), close: true}, nil
}

func cmdSelect(c *client, args [][]byte) (result, error) {
	index, err := parseInt(args[1])
	if err != nil {
		return result{}, err
	}
	if index != 0 {
		return result{}, fmt.Errorf("ERR DB index is out of range")
	}
	return result{reply: simpleReply("OK")}, nil
}

func cmdHello(c *client, args [][]byte) (result, error) {
	proto := c.protoNum()
	next := 1

	if next < len(args) {
		value, err := parseInt(args[next])
		if err != nil {
			return result{}, fmt.Errorf("ERR Protocol version is not an integer or out of range")
		}
		if value < 2 || value > 3 {
			return result{}, fmt.Errorf("NOPROTO unsupported protocol version")
		}
		proto = int(value)
		next++
	}

	for next < len(args) {
		option := strings.ToUpper(string(args[next]))
		switch option {
		case "AUTH":
			if next+2 >= len(args) {
				return result{}, errSyntax
			}
			name, err := c.srv.authenticate(string(args[next+1]), string(args[next+2]))
			if err != nil {
				return result{}, err
			}
			c.setUser(name)
			c.authed = true
			next += 3
		case "SETNAME":
			if next+1 >= len(args) {
				return result{}, errSyntax
			}
			if err := c.setName(string(args[next+1])); err != nil {
				return result{}, err
			}
			next += 2
		default:
			return result{}, errSyntax
		}
	}

	if c.srv.authRequired() && !c.authed {
		return result{}, errors.New("NOAUTH HELLO must be called with the client already authenticated, otherwise the HELLO <proto> AUTH <user> <pass> option can be used to authenticate the client and select the RESP protocol version at the same time")
	}

	if err := c.setProto(proto); err != nil {
		return result{}, err
	}

	return result{reply: mapReply{
		bulkReply("server"), bulkReply("redis"),
		bulkReply("version"), bulkReply("7.0.0"),
		bulkReply("proto"), intReply(proto),
		bulkReply("id"), intReply(c.id),
		bulkReply("mode"), bulkReply("standalone"),
		bulkReply("role"), bulkReply("master"),
		bulkReply("modules"), arrayReply{},
	}}, nil
}

func cmdClient(c *client, args [][]byte) (result, error) {
	sub := strings.ToUpper(string(args[1]))
	switch sub {
	case "ID":
		if len(args) != 2 {
			return result{}, errSyntax
		}
		return result{reply: intReply(c.id)}, nil
	case "GETNAME":
		if len(args) != 2 {
			return result{}, errSyntax
		}
		return result{reply: bulkReply(c.name())}, nil
	case "SETNAME":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		if err := c.setName(string(args[2])); err != nil {
			return result{}, err
		}
		return result{reply: simpleReply("OK")}, nil
	case "SETINFO":
		if len(args) != 4 {
			return result{}, errSyntax
		}
		return result{reply: simpleReply("OK")}, nil
	case "LIST":
		return result{reply: bulkReply(c.srv.clientList())}, nil
	case "INFO":
		return result{reply: bulkReply(c.infoLine())}, nil
	case "GETREDIR":
		return result{reply: intReply(-1)}, nil
	case "REPLY":
		if len(args) != 3 {
			return result{}, fmt.Errorf("ERR wrong number of arguments for 'client|reply' command")
		}
		switch strings.ToUpper(string(args[2])) {
		case "ON":
			c.replyMode = replyOn
			return result{reply: simpleReply("OK")}, nil
		case "OFF":
			c.replyMode = replyOff
			return result{}, nil
		case "SKIP":
			c.replyMode = replySkip
			return result{}, nil
		default:
			return result{}, errSyntax
		}
	case "KILL":
		return clientKill(c, args)
	case "UNBLOCK":
		if len(args) < 3 {
			return result{}, errSyntax
		}
		return result{reply: intReply(0)}, nil
	case "NO-EVICT", "NO-TOUCH":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		return result{reply: simpleReply("OK")}, nil
	case "MAINT_NOTIFICATIONS":
		// Redis 8 clients opt in to maintenance push messages. Push is not
		// implemented, so acknowledge without enabling anything.
		if len(args) < 3 {
			return result{}, errSyntax
		}
		return result{reply: simpleReply("OK")}, nil
	case "UNPAUSE", "PAUSE":
		return result{reply: simpleReply("OK")}, nil
	case "HELP":
		return result{reply: arrayReply{
			bulkReply("CLIENT <subcommand>"),
			bulkReply("ID, GETNAME, SETNAME, SETINFO, LIST, INFO, GETREDIR"),
		}}, nil
	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for CLIENT", printable(args[1]))
	}
}

type clientKillFilter struct {
	id     int64
	hasID  bool
	addr   string
	laddr  string
	kind   string
	user   string
	skipme bool
	maxAge int64
	hasAge bool
}

func clientKill(c *client, args [][]byte) (result, error) {
	// The legacy syntax is CLIENT KILL addr:port.
	legacy := len(args) == 3
	if legacy {
		switch strings.ToUpper(string(args[2])) {
		case "ID", "ADDR", "LADDR", "TYPE", "USER", "SKIPME", "MAXAGE":
			legacy = false
		}
	}
	if legacy {
		addr := string(args[2])
		target := c.srv.findClientByAddr(addr)
		if target == nil {
			return result{}, fmt.Errorf("ERR No such client")
		}
		target.close()
		return result{reply: simpleReply("OK")}, nil
	}

	filter := clientKillFilter{skipme: true}
	for i := 2; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return result{}, errSyntax
		}
		value := string(args[i+1])
		switch strings.ToUpper(string(args[i])) {
		case "ID":
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id <= 0 {
				return result{}, fmt.Errorf("ERR client-id should be greater than 0")
			}
			filter.id = id
			filter.hasID = true
		case "ADDR":
			filter.addr = value
		case "LADDR":
			filter.laddr = value
		case "TYPE":
			switch strings.ToLower(value) {
			case "normal", "master", "replica", "pubsub", "slave":
				filter.kind = strings.ToLower(value)
			default:
				return result{}, fmt.Errorf("ERR Unknown client type '%s'", value)
			}
		case "USER":
			filter.user = value
		case "SKIPME":
			switch strings.ToLower(value) {
			case "yes":
				filter.skipme = true
			case "no":
				filter.skipme = false
			default:
				return result{}, errSyntax
			}
		case "MAXAGE":
			age, err := strconv.ParseInt(value, 10, 64)
			if err != nil || age < 0 {
				return result{}, errSyntax
			}
			filter.maxAge = age
			filter.hasAge = true
		default:
			return result{}, errSyntax
		}
	}

	var victims []*client
	c.srv.clientsMu.RLock()
	for _, target := range c.srv.clients {
		if filter.skipme && target == c {
			continue
		}
		if filter.hasID && target.id != filter.id {
			continue
		}
		if filter.addr != "" && target.addr != filter.addr {
			continue
		}
		if filter.laddr != "" && target.localAddr() != filter.laddr {
			continue
		}
		if filter.user != "" && target.user() != filter.user {
			continue
		}
		if filter.hasAge && int64(time.Since(target.created).Seconds()) < filter.maxAge {
			continue
		}
		if filter.kind != "" && !clientMatchesType(target, filter.kind) {
			continue
		}
		victims = append(victims, target)
	}
	c.srv.clientsMu.RUnlock()

	for _, target := range victims {
		target.close()
	}
	return result{reply: intReply(int64(len(victims)))}, nil
}

func clientMatchesType(target *client, kind string) bool {
	replica := target.srv.isReplicaClient(target)
	subscribed := target.subscribed()
	switch kind {
	case "normal":
		return !replica && !subscribed
	case "replica", "slave":
		return replica
	case "pubsub":
		return subscribed
	case "master":
		return false
	}
	return false
}

func (s *Server) findClientByAddr(addr string) *client {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	for _, c := range s.clients {
		if c.addr == addr {
			return c
		}
	}
	return nil
}

func (s *Server) isReplicaClient(c *client) bool {
	s.replicasMu.RLock()
	defer s.replicasMu.RUnlock()
	_, ok := s.replicas[c]
	return ok
}

func cmdCommand(c *client, args [][]byte) (result, error) {
	if len(args) == 1 {
		return result{reply: commandListReply()}, nil
	}

	switch strings.ToUpper(string(args[1])) {
	case "COUNT":
		return result{reply: intReply(int64(len(commandTable)))}, nil
	case "INFO":
		if len(args) == 2 {
			return result{reply: commandListReply()}, nil
		}
		out := make(arrayReply, 0, len(args)-2)
		for _, name := range args[2:] {
			cmd, ok := lookupCommand(strings.ToUpper(string(name)))
			if !ok {
				out = append(out, null)
				continue
			}
			out = append(out, commandInfoReply(cmd))
		}
		return result{reply: out}, nil
	case "DOCS":
		names := args[2:]
		if len(names) == 0 {
			names = make([][]byte, 0, len(commandTable))
			for _, name := range commandNames() {
				names = append(names, []byte(name))
			}
		}
		return result{reply: commandDocsReply(names)}, nil
	case "LIST":
		return commandListFiltered(c, args[2:])
	case "GETKEYS":
		if len(args) < 3 {
			return result{}, errSyntax
		}
		target := strings.ToUpper(string(args[2]))
		cmd, ok := lookupCommand(target)
		if !ok {
			return result{}, fmt.Errorf("ERR Invalid command specified")
		}
		if !arityOK(cmd.arity, len(args)-2) {
			return result{}, fmt.Errorf("ERR Invalid number of arguments specified for command")
		}
		keys := commandKeys(target, args[2:])
		out := make(arrayReply, 0, len(keys))
		for _, key := range keys {
			out = append(out, bulkReply(key))
		}
		return result{reply: out}, nil
	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for COMMAND", printable(args[1]))
	}
}

func commandDocsReply(names [][]byte) mapReply {
	out := make(mapReply, 0, len(names)*2)
	for _, raw := range names {
		name := strings.ToUpper(string(raw))
		cmd, ok := lookupCommand(name)
		if !ok {
			continue
		}
		group := commandGroup(cmd.name)
		out = append(out, bulkReply(strings.ToLower(cmd.name)), mapReply{
			bulkReply("summary"), bulkReply(strings.ToLower(cmd.name) + " command"),
			bulkReply("since"), bulkReply("1.0.0"),
			bulkReply("group"), bulkReply(group),
			bulkReply("complexity"), bulkReply("O(1)"),
		})
	}
	return out
}

func commandGroup(name string) string {
	categories := commandACLCategories(name)
	for _, group := range []string{"string", "list", "set", "sortedset", "hash", "bitmap",
		"hyperloglog", "geo", "stream", "pubsub", "scripting", "transaction", "connection", "keyspace"} {
		if categories[group] {
			return group
		}
	}
	return "server"
}

func commandListFiltered(c *client, args [][]byte) (result, error) {
	names := commandNames()
	if len(args) == 0 {
		out := make([]string, 0, len(names))
		for _, name := range names {
			out = append(out, strings.ToLower(name))
		}
		return result{reply: bulkArrayString(out)}, nil
	}
	if len(args) != 3 || !strings.EqualFold(string(args[0]), "FILTERBY") {
		return result{}, errSyntax
	}
	filter := strings.ToUpper(string(args[1]))
	value := string(args[2])
	var out []string
	switch filter {
	case "MODULE":
		// No modules can be loaded, so this filter always matches nothing.
		return result{reply: arrayReply{}}, nil
	case "PATTERN":
		for _, name := range names {
			if store.Match(value, strings.ToLower(name)) {
				out = append(out, strings.ToLower(name))
			}
		}
	case "ACLCAT":
		category := strings.ToLower(value)
		for _, name := range names {
			if commandACLCategories(name)[category] {
				out = append(out, strings.ToLower(name))
			}
		}
	default:
		return result{}, errSyntax
	}
	sort.Strings(out)
	return result{reply: bulkArrayString(out)}, nil
}

func commandACLCategories(name string) map[string]bool {
	cmd, ok := lookupCommand(name)
	if !ok {
		return nil
	}
	categories := make(map[string]bool)
	if cmd.flags&flagReadOnly != 0 {
		categories["read"] = true
	}
	if cmd.flags&flagWrite != 0 {
		categories["write"] = true
	}
	if cmd.flags&flagAdmin != 0 {
		categories["admin"] = true
	}
	if cmd.flags&flagFast != 0 {
		categories["fast"] = true
	} else {
		categories["slow"] = true
	}
	if cmd.flags&flagBlocking != 0 {
		categories["blocking"] = true
	}

	switch name {
	case "DEL", "UNLINK", "EXISTS", "TYPE", "KEYS", "SCAN", "RANDOMKEY", "RENAME",
		"RENAMENX", "EXPIRE", "PEXPIRE", "EXPIREAT", "PEXPIREAT", "TTL", "PTTL",
		"PERSIST", "EXPIRETIME", "PEXPIRETIME", "TOUCH", "DBSIZE", "FLUSHDB",
		"FLUSHALL", "COPY", "OBJECT", "MEMORY", "SORT", "SORT_RO", "DUMP", "RESTORE",
		"MOVE", "SWAPDB", "MIGRATE", "WAIT":
		categories["keyspace"] = true
	}
	switch {
	case hasAny(name, "AUTH", "ECHO", "PING", "QUIT", "SELECT", "HELLO", "CLIENT", "RESET"):
		categories["connection"] = true
	case hasAny(name, "MULTI", "EXEC", "DISCARD", "WATCH", "UNWATCH"):
		categories["transaction"] = true
	case hasAny(name, "SUBSCRIBE", "UNSUBSCRIBE", "PSUBSCRIBE", "PUNSUBSCRIBE",
		"SSUBSCRIBE", "SUNSUBSCRIBE", "PUBLISH", "SPUBLISH", "PUBSUB"):
		categories["pubsub"] = true
	case hasAny(name, "EVAL", "EVALSHA", "EVAL_RO", "EVALSHA_RO", "SCRIPT", "FUNCTION", "FCALL", "FCALL_RO"):
		categories["scripting"] = true
	case hasAny(name, "GET", "SET", "SETNX", "SETEX", "PSETEX", "GETSET", "GETDEL",
		"GETEX", "MGET", "MSET", "MSETNX", "APPEND", "STRLEN", "GETRANGE", "SUBSTR",
		"SETRANGE", "INCR", "DECR", "INCRBY", "DECRBY", "INCRBYFLOAT", "LCS"):
		categories["string"] = true
	case hasAny(name, "LPUSH", "RPUSH", "LPUSHX", "RPUSHX", "LPOP", "RPOP", "LLEN",
		"LINDEX", "LSET", "LRANGE", "LTRIM", "LREM", "LINSERT", "LPOS", "LMOVE",
		"RPOPLPUSH", "BLPOP", "BRPOP", "BLMOVE", "BRPOPLPUSH", "LMPOP", "BLMPOP"):
		categories["list"] = true
	case hasAny(name, "SADD", "SREM", "SCARD", "SISMEMBER", "SMISMEMBER", "SMEMBERS",
		"SINTER", "SUNION", "SDIFF", "SPOP", "SRANDMEMBER", "SSCAN", "SMOVE",
		"SINTERSTORE", "SUNIONSTORE", "SDIFFSTORE", "SINTERCARD"):
		categories["set"] = true
	case strings.HasPrefix(name, "Z"):
		categories["sortedset"] = true
	case strings.HasPrefix(name, "H"):
		categories["hash"] = true
	case hasAny(name, "SETBIT", "GETBIT", "BITCOUNT", "BITPOS", "BITOP", "BITFIELD", "BITFIELD_RO"):
		categories["bitmap"] = true
	case strings.HasPrefix(name, "PF"):
		categories["hyperloglog"] = true
	case strings.HasPrefix(name, "GEO"):
		categories["geo"] = true
	case strings.HasPrefix(name, "X"):
		categories["stream"] = true
	}
	return categories
}

func hasAny(name string, values ...string) bool {
	for _, value := range values {
		if name == value {
			return true
		}
	}
	return false
}

func commandListReply() arrayReply {
	names := commandNames()
	out := make(arrayReply, 0, len(names))
	for _, name := range names {
		out = append(out, commandInfoReply(commandTable[name]))
	}
	return out
}

func commandInfoReply(cmd *command) arrayReply {
	firstKey, lastKey, step := int64(0), int64(0), int64(0)
	if cmd.flags&(flagWrite|flagReadOnly) != 0 && cmd.arity >= 2 {
		firstKey, lastKey, step = 1, 1, 1
	}
	return arrayReply{
		bulkReply(strings.ToLower(cmd.name)),
		intReply(int64(cmd.arity)),
		arrayReply(cmd.flags.names()),
		intReply(firstKey),
		intReply(lastKey),
		intReply(step),
		arrayReply{},
		arrayReply{},
		arrayReply{},
		arrayReply{},
	}
}

func cmdInfo(c *client, args [][]byte) (result, error) {
	sections := make([]string, 0, len(args)-1)
	for _, raw := range args[1:] {
		sections = append(sections, strings.ToLower(string(raw)))
	}
	return result{reply: bulkReply(c.srv.info(sections...))}, nil
}

func cmdTime(c *client, args [][]byte) (result, error) {
	return result{reply: c.srv.timeReply()}, nil
}

func cmdShutdown(c *client, args [][]byte) (result, error) {
	if len(args) > 2 {
		return result{}, errSyntax
	}
	save := false
	if len(args) == 2 {
		switch strings.ToUpper(string(args[1])) {
		case "SAVE":
			save = true
		case "NOSAVE":
		default:
			return result{}, errSyntax
		}
	}
	if save {
		if err := c.srv.saveSnapshot(); err != nil {
			return result{}, fmt.Errorf("ERR %v", err)
		}
	}
	c.srv.requestShutdown()
	return result{close: true}, nil
}

func cmdBGRewriteAOF(c *client, args [][]byte) (result, error) {
	if err := c.srv.rewriteAOF(); err != nil {
		return result{}, fmt.Errorf("ERR %v", err)
	}
	return result{reply: simpleReply("Background append only file rewriting started")}, nil
}

func cmdLastSave(c *client, args [][]byte) (result, error) {
	return result{reply: intReply(c.srv.lastSave.Load())}, nil
}
