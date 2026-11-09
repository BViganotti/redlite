package server

import (
	"fmt"
	"strings"
)

// notifyFlags is the parsed form of notify-keyspace-events.
type notifyFlags struct {
	keyspace   bool
	keyevent   bool
	all        bool
	categories map[byte]bool
}

func parseNotifyFlags(value string) notifyFlags {
	flags := notifyFlags{categories: make(map[byte]bool)}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case 'K':
			flags.keyspace = true
		case 'E':
			flags.keyevent = true
		case 'A':
			flags.all = true
		default:
			flags.categories[value[i]] = true
		}
	}
	return flags
}

func validateNotifyFlags(value string) error {
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case 'K', 'E', 'A', 'g', '$', 'l', 's', 'h', 'z', 'x', 'e', 't', 'm':
		default:
			return fmt.Errorf("ERR Invalid argument '%s' for CONFIG SET 'notify-keyspace-events'", value)
		}
	}
	return nil
}

func (s *Server) expiryEventLoop() {
	for {
		select {
		case key := <-s.notifyCh:
			s.emitKeyspaceEvent([]byte(key), []byte("expired"), 'x')
		case <-s.notifyStop:
			return
		}
	}
}

// emitKeyspaceEvent publishes one event to the keyspace and/or keyevent
// channel, if the configured flags include its category.
func (s *Server) emitKeyspaceEvent(key, event []byte, category byte) {
	flags, _ := s.notifyConfig.Load().(notifyFlags)
	if !flags.all && !flags.categories[category] {
		return
	}
	if flags.keyspace {
		channel := append([]byte("__keyspace@0__:"), key...)
		s.pubsub.publish(string(channel), event)
	}
	if flags.keyevent {
		channel := append([]byte("__keyevent@0__:"), event...)
		s.pubsub.publish(string(channel), key)
	}
}

// notifyCommand emits the events associated with a mutated command.
func (s *Server) notifyCommand(args [][]byte, res result) {
	name := strings.ToUpper(string(args[0]))

	switch name {
	case "DEL", "UNLINK":
		for _, key := range args[1:] {
			s.emitKeyspaceEvent(key, []byte("del"), 'g')
		}
		return

	case "MSET", "MSETNX":
		for i := 1; i < len(args); i += 2 {
			s.emitKeyspaceEvent(args[i], []byte("set"), '$')
		}
		return

	case "RENAME", "RENAMENX":
		s.emitKeyspaceEvent(args[1], []byte("rename_from"), 'g')
		s.emitKeyspaceEvent(args[2], []byte("rename_to"), 'g')
		return

	case "LMOVE", "BLMOVE", "RPOPLPUSH", "BRPOPLPUSH":
		s.emitKeyspaceEvent(args[1], []byte("lpop"), 'l')
		s.emitKeyspaceEvent(args[2], []byte("rpush"), 'l')
		return

	case "BLPOP", "BRPOP":
		if len(res.rewrite) == 2 {
			event := "lpop"
			if name == "BRPOP" {
				event = "rpop"
			}
			s.emitKeyspaceEvent(res.rewrite[1], []byte(event), 'l')
		}
		return

	case "FLUSHALL", "FLUSHDB", "SAVE", "BGSAVE", "BGREWRITEAOF":
		return
	}

	event, category, ok := commandEvent(name)
	if !ok || len(args) < 2 {
		return
	}
	s.emitKeyspaceEvent(args[1], []byte(event), category)
}

func commandEvent(name string) (string, byte, bool) {
	type event struct {
		name     string
		category byte
	}
	events := map[string]event{
		"SET":              {"set", '$'},
		"SETNX":            {"set", '$'},
		"SETEX":            {"set", '$'},
		"PSETEX":           {"set", '$'},
		"GETSET":           {"set", '$'},
		"SETRANGE":         {"setrange", '$'},
		"APPEND":           {"append", '$'},
		"INCR":             {"incrby", '$'},
		"DECR":             {"decrby", '$'},
		"INCRBY":           {"incrby", '$'},
		"DECRBY":           {"decrby", '$'},
		"INCRBYFLOAT":      {"incrbyfloat", '$'},
		"GETDEL":           {"del", 'g'},
		"GETEX":            {"expire", 'g'},
		"EXPIRE":           {"expire", 'g'},
		"PEXPIRE":          {"expire", 'g'},
		"EXPIREAT":         {"expire", 'g'},
		"PEXPIREAT":        {"expire", 'g'},
		"PERSIST":          {"persist", 'g'},
		"HSET":             {"hset", 'h'},
		"HSETNX":           {"hset", 'h'},
		"HMSET":            {"hset", 'h'},
		"HDEL":             {"hdel", 'h'},
		"HINCRBY":          {"hincrby", 'h'},
		"LPUSH":            {"lpush", 'l'},
		"LPUSHX":           {"lpush", 'l'},
		"RPUSH":            {"rpush", 'l'},
		"RPUSHX":           {"rpush", 'l'},
		"LPOP":             {"lpop", 'l'},
		"RPOP":             {"rpop", 'l'},
		"LSET":             {"lset", 'l'},
		"LTRIM":            {"ltrim", 'l'},
		"LREM":             {"lrem", 'l'},
		"SADD":             {"sadd", 's'},
		"SREM":             {"srem", 's'},
		"SPOP":             {"spop", 's'},
		"ZADD":             {"zadd", 'z'},
		"ZREM":             {"zrem", 'z'},
		"ZINCRBY":          {"zincrby", 'z'},
		"ZREMRANGEBYRANK":  {"zremrangebyrank", 'z'},
		"ZREMRANGEBYSCORE": {"zremrangebyscore", 'z'},
	}
	entry, ok := events[name]
	return entry.name, entry.category, ok
}
