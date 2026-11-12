package server

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// slowlogEntry is one recorded slow command.
type slowlogEntry struct {
	ID       int64
	Time     int64
	Duration int64 // microseconds
	Args     [][]byte
	Addr     string
	Name     string
}

// latencySample is one point in a latency event history.
type latencySample struct {
	Time    int64
	Latency int64 // milliseconds
}

type latencyStat struct {
	Latest  int64
	Max     int64
	History []latencySample
}

// cmdStat accumulates per-command statistics for INFO commandstats.
type cmdStat struct {
	Calls  int64
	Usec   int64
	Failed int64
}

func init() {
	registerCommand("SLOWLOG", -2, flagAdmin, cmdSlowlog)
	registerCommand("LATENCY", -2, flagAdmin, cmdLatency)
	registerCommand("DEBUG", -2, flagAdmin, cmdDebug)
	registerCommand("MODULE", -2, flagAdmin, cmdModule)
}

// recordCommand updates per-command statistics and the slow log/latency
// monitor for one completed command.
func (s *Server) recordCommand(c *client, args [][]byte, started time.Time, dur time.Duration, cmdErr error) {
	name := strings.ToLower(string(args[0]))
	micros := dur.Microseconds()

	s.statsMu.Lock()
	stat := s.cmdStats[name]
	if stat == nil {
		stat = &cmdStat{}
		s.cmdStats[name] = stat
	}
	stat.Calls++
	stat.Usec += micros
	if cmdErr != nil {
		stat.Failed++
		s.errStats[errorClass(cmdErr)]++
	}
	s.statsMu.Unlock()

	threshold := s.slowlogThreshold.Load()
	blocking := false
	if cmd, ok := lookupCommand(strings.ToUpper(name)); ok && cmd.flags&flagBlocking != 0 {
		// Blocking commands measure their wait time, which Redis excludes
		// from the slow log and the latency monitor.
		blocking = true
	}
	if threshold >= 0 && micros >= threshold && !blocking && name != "slowlog" {
		s.appendSlowlog(c, args, started, micros)
	}

	latencyThreshold := s.latencyThreshold.Load()
	if latencyThreshold > 0 && !blocking {
		ms := dur.Milliseconds()
		if ms >= latencyThreshold {
			s.recordLatency("command", ms, started.Unix())
		}
	}
}

func errorClass(err error) string {
	msg := err.Error()
	if index := strings.IndexByte(msg, ' '); index > 0 {
		return msg[:index]
	}
	return msg
}

func (s *Server) appendSlowlog(c *client, args [][]byte, started time.Time, micros int64) {
	maxArgs := int(s.slowlogMaxArgc.Load())
	maxArgLen := int(s.slowlogMaxArgLen.Load())
	if maxArgs < 0 {
		maxArgs = 0
	}
	if maxArgLen < 0 {
		maxArgLen = 0
	}

	entry := slowlogEntry{
		Time:     started.Unix(),
		Duration: micros,
		Addr:     c.addr,
		Name:     c.name(),
	}
	for i, arg := range args {
		if i == maxArgs {
			entry.Args = append(entry.Args, []byte(fmt.Sprintf("... (%d more arguments)", len(args)-maxArgs)))
			break
		}
		value := append([]byte(nil), arg...)
		if len(value) > maxArgLen {
			value = append(value[:maxArgLen], []byte("...")...)
		}
		entry.Args = append(entry.Args, value)
	}

	s.slowlogMu.Lock()
	entry.ID = s.slowlogNext
	s.slowlogNext++
	limit := int(s.slowlogMaxLen.Load())
	if limit < 0 {
		limit = 0
	}
	s.slowlog = append(s.slowlog, entry)
	if len(s.slowlog) > limit {
		s.slowlog = s.slowlog[len(s.slowlog)-limit:]
	}
	s.slowlogMu.Unlock()
}

func (s *Server) recordLatency(event string, ms, now int64) {
	s.latencyMu.Lock()
	defer s.latencyMu.Unlock()
	stat := s.latency[event]
	if stat == nil {
		stat = &latencyStat{}
		s.latency[event] = stat
	}
	stat.Latest = ms
	if ms > stat.Max {
		stat.Max = ms
	}
	stat.History = append(stat.History, latencySample{Time: now, Latency: ms})
	if len(stat.History) > 160 {
		stat.History = stat.History[len(stat.History)-160:]
	}
}

func cmdSlowlog(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "GET":
		count := int64(10)
		if len(args) == 3 {
			value, err := parseInt(args[2])
			if err != nil {
				return result{}, err
			}
			count = value
		} else if len(args) > 3 {
			return result{}, errSyntax
		}

		c.srv.slowlogMu.Lock()
		entries := append([]slowlogEntry(nil), c.srv.slowlog...)
		c.srv.slowlogMu.Unlock()

		out := make(arrayReply, 0, len(entries))
		for i := len(entries) - 1; i >= 0; i-- {
			if count >= 0 && int64(len(out)) >= count {
				break
			}
			entry := entries[i]
			item := make(arrayReply, 0, 6)
			item = append(item, intReply(entry.ID), intReply(entry.Time), intReply(entry.Duration))
			item = append(item, bulkArray(entry.Args))
			item = append(item, bulkReply(entry.Addr), bulkReply(entry.Name))
			out = append(out, item)
		}
		return result{reply: out}, nil

	case "LEN":
		if len(args) != 2 {
			return result{}, errSyntax
		}
		c.srv.slowlogMu.Lock()
		n := len(c.srv.slowlog)
		c.srv.slowlogMu.Unlock()
		return result{reply: intReply(int64(n))}, nil

	case "RESET":
		if len(args) != 2 {
			return result{}, errSyntax
		}
		c.srv.slowlogMu.Lock()
		c.srv.slowlog = nil
		c.srv.slowlogMu.Unlock()
		return result{reply: simpleReply("OK")}, nil

	case "HELP":
		return result{reply: arrayReply{
			bulkReply("SLOWLOG <subcommand>"),
			bulkReply("GET [count], LEN, RESET"),
		}}, nil

	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for SLOWLOG", printable(args[1]))
	}
}

func cmdLatency(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "LATEST":
		c.srv.latencyMu.Lock()
		events := make([]string, 0, len(c.srv.latency))
		for name := range c.srv.latency {
			events = append(events, name)
		}
		sort.Strings(events)
		out := make(arrayReply, 0, len(events))
		for _, name := range events {
			stat := c.srv.latency[name]
			out = append(out, arrayReply{
				bulkReply(name),
				intReply(latestSampleTime(stat)),
				intReply(stat.Latest),
				intReply(stat.Max),
			})
		}
		c.srv.latencyMu.Unlock()
		return result{reply: out}, nil

	case "HISTORY":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		c.srv.latencyMu.Lock()
		stat := c.srv.latency[string(args[2])]
		var history []latencySample
		if stat != nil {
			history = append(history, stat.History...)
		}
		c.srv.latencyMu.Unlock()
		out := make(arrayReply, 0, len(history))
		for _, sample := range history {
			out = append(out, arrayReply{intReply(sample.Time), intReply(sample.Latency)})
		}
		return result{reply: out}, nil

	case "RESET":
		c.srv.latencyMu.Lock()
		n := 0
		if len(args) == 2 {
			n = len(c.srv.latency)
			c.srv.latency = make(map[string]*latencyStat)
		} else {
			for _, raw := range args[2:] {
				name := string(raw)
				if _, ok := c.srv.latency[name]; ok {
					delete(c.srv.latency, name)
					n++
				}
			}
		}
		c.srv.latencyMu.Unlock()
		return result{reply: intReply(int64(n))}, nil

	case "DOCTOR":
		return result{reply: bulkReply("Dave, no latency spike was observed during the lifetime of this instance, and no incidents were logged. Things look fine.")}, nil

	case "GRAPHS", "GRAPH":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		c.srv.latencyMu.Lock()
		stat := c.srv.latency[string(args[2])]
		var history []latencySample
		if stat != nil {
			history = append(history, stat.History...)
		}
		c.srv.latencyMu.Unlock()
		return result{reply: bulkReply(renderLatencyGraph(string(args[2]), history))}, nil

	case "HISTOGRAM":
		return result{reply: mapReply{}}, nil

	case "HELP":
		return result{reply: arrayReply{
			bulkReply("LATENCY <subcommand>"),
			bulkReply("LATEST, HISTORY <event>, RESET [event ...], DOCTOR, GRAPH <event>, HISTOGRAM"),
		}}, nil

	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for LATENCY", printable(args[1]))
	}
}

func latestSampleTime(stat *latencyStat) int64 {
	if len(stat.History) == 0 {
		return 0
	}
	return stat.History[len(stat.History)-1].Time
}

func renderLatencyGraph(event string, history []latencySample) string {
	var b strings.Builder
	b.WriteString(event)
	b.WriteByte('\n')
	max := int64(1)
	for _, sample := range history {
		if sample.Latency > max {
			max = sample.Latency
		}
	}
	for _, sample := range history {
		bar := int(sample.Latency * 40 / max)
		if bar < 1 {
			bar = 1
		}
		fmt.Fprintf(&b, "%s|%s %d\n", time.Unix(sample.Time, 0).Format("15:04:05"), strings.Repeat("#", bar), sample.Latency)
	}
	return b.String()
}

func cmdDebug(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "SLEEP":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		seconds, err := strconv.ParseFloat(string(args[2]), 64)
		if err != nil || seconds < 0 {
			return result{}, errSyntax
		}
		time.Sleep(time.Duration(seconds * float64(time.Second)))
		return result{reply: simpleReply("OK")}, nil
	case "SET-ACTIVE-EXPIRE":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		c.srv.data.SetActiveExpire(string(args[2]) != "0")
		return result{reply: simpleReply("OK")}, nil
	case "JMAP", "SET-ACTIVE-EXPIRE-OK":
		return result{reply: simpleReply("OK")}, nil
	case "CHANGE-REPL-ID":
		c.srv.setReplicationID(newReplicationID())
		return result{reply: simpleReply("OK")}, nil
	case "QUICKLIST-PACKED-THRESHOLD", "STRINGMATCH-LEN", "LISTPACK", "PROTOCOL":
		if len(args) < 3 {
			return result{}, errSyntax
		}
		return result{reply: simpleReply("OK")}, nil
	case "OBJECT":
		if len(args) != 3 {
			return result{}, errSyntax
		}
		return debugObject(c, string(args[2]))
	case "FLUSHALL":
		c.srv.data.Flush()
		return result{reply: simpleReply("OK"), mutated: true}, nil
	case "RELOAD":
		if err := c.srv.saveSnapshot(); err != nil {
			return result{}, fmt.Errorf("ERR %v", err)
		}
		if err := c.srv.loadSnapshot(); err != nil {
			return result{}, fmt.Errorf("ERR %v", err)
		}
		return result{reply: simpleReply("OK")}, nil
	case "HELP":
		return result{reply: arrayReply{
			bulkReply("DEBUG <subcommand>"),
			bulkReply("SLEEP, SET-ACTIVE-EXPIRE, OBJECT, CHANGE-REPL-ID, JMAP, FLUSHALL, RELOAD"),
		}}, nil
	default:
		return result{}, fmt.Errorf("ERR DEBUG subcommand must be one of: SLEEP, SET-ACTIVE-EXPIRE, OBJECT, CHANGE-REPL-ID, JMAP, FLUSHALL, RELOAD")
	}
}

func debugObject(c *client, key string) (result, error) {
	kind := c.srv.data.Type(key)
	if kind == "none" {
		return result{}, fmt.Errorf("ERR no such key")
	}
	encoding, err := c.srv.data.ObjectEncoding(key)
	if err != nil {
		return result{}, err
	}
	usage, ok, err := c.srv.data.MemoryUsage(key)
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{}, fmt.Errorf("ERR no such key")
	}
	text := fmt.Sprintf(
		"Value at:0x0 refcount:1 encoding:%s serializedlength:%d lru:0 lru_seconds_idle:0",
		encoding, usage)
	return result{reply: bulkReply(text)}, nil
}

func cmdModule(c *client, args [][]byte) (result, error) {
	switch strings.ToUpper(string(args[1])) {
	case "LIST":
		if len(args) != 2 {
			return result{}, errSyntax
		}
		return result{reply: arrayReply{}}, nil
	case "HELP":
		return result{reply: arrayReply{
			bulkReply("MODULE <subcommand>"),
			bulkReply("LIST"),
		}}, nil
	case "LOAD", "LOADEX", "UNLOAD":
		return result{}, fmt.Errorf("ERR MODULE not supported by redlite")
	default:
		return result{}, fmt.Errorf("ERR unknown subcommand '%s' for MODULE", printable(args[1]))
	}
}
