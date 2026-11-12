package server

import (
	"fmt"
	"strings"
	"time"
)

func init() {
	registerCommand("MONITOR", 1, flagAdmin, cmdMonitor)
}

func cmdMonitor(c *client, args [][]byte) (result, error) {
	c.srv.monitorMu.Lock()
	c.srv.monitors[c] = struct{}{}
	c.srv.monitorMu.Unlock()
	c.monitor = true
	return result{reply: simpleReply("OK")}, nil
}

func (s *Server) removeMonitor(c *client) {
	s.monitorMu.Lock()
	delete(s.monitors, c)
	s.monitorMu.Unlock()
}

// feedMonitors sends the Redis MONITOR line for one command to every monitor,
// except the connection that issued it.
func (s *Server) feedMonitors(origin *client, args [][]byte) {
	if origin.monitor || len(args) == 0 || strings.EqualFold(string(args[0]), "MONITOR") {
		return
	}
	s.monitorMu.Lock()
	if len(s.monitors) == 0 {
		s.monitorMu.Unlock()
		return
	}
	targets := make([]*client, 0, len(s.monitors))
	for monitor := range s.monitors {
		targets = append(targets, monitor)
	}
	s.monitorMu.Unlock()

	line := monitorLine(origin.addr, args)
	for _, monitor := range targets {
		_ = monitor.push(simpleReply(line))
	}
}

func monitorLine(addr string, args [][]byte) string {
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "%.6f [0 %s]", float64(now.UnixNano())/1e9, addr)
	for _, arg := range args {
		b.WriteByte(' ')
		b.WriteString(quoteMonitorArg(arg))
	}
	return b.String()
}

func quoteMonitorArg(arg []byte) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, ch := range arg {
		switch ch {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteByte(ch)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\a':
			b.WriteString(`\a`)
		default:
			if ch < 0x20 || ch > 0x7e {
				fmt.Fprintf(&b, `\x%02x`, ch)
			} else {
				b.WriteByte(ch)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
