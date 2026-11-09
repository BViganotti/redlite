package server

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
	"github.com/BViganotti/redlite/internal/store"
	"github.com/BViganotti/redlite/internal/version"
)

// info renders the INFO command output. With no sections every section is
// returned; otherwise only the requested ones, in Redis' canonical order.
func (s *Server) info(requested ...string) string {
	sections := map[string]string{
		"server":      s.infoServer(),
		"clients":     s.infoClients(),
		"memory":      infoMemory(),
		"persistence": s.infoPersistence(),
		"stats":       s.infoStats(),
		"replication": s.infoReplication(),
		"keyspace":    s.infoKeyspace(),
	}
	order := []string{"server", "clients", "memory", "persistence", "stats", "replication", "keyspace"}

	if len(requested) == 0 || containsAny(requested, "all", "everything", "default") {
		var b strings.Builder
		for _, name := range order {
			b.WriteString(sections[name])
		}
		return b.String()
	}

	var b strings.Builder
	for _, name := range requested {
		if section, ok := sections[name]; ok {
			b.WriteString(section)
		}
	}
	return b.String()
}

func (s *Server) infoServer() string {
	var b strings.Builder
	b.WriteString("# Server\r\n")
	writeInfoField(&b, "redis_version", "7.0.0")
	writeInfoField(&b, "redlite_version", version.String())
	writeInfoField(&b, "go_version", runtime.Version())
	writeInfoField(&b, "os", runtime.GOOS)
	writeInfoField(&b, "arch", runtime.GOARCH)
	writeInfoField(&b, "process_id", strconv.Itoa(os.Getpid()))
	writeInfoField(&b, "tcp_port", strconv.Itoa(s.port()))
	writeInfoField(&b, "tls_port", s.cfg.TLSPortString())
	uptime := int64(time.Since(s.started).Seconds())
	writeInfoField(&b, "uptime_in_seconds", strconv.FormatInt(uptime, 10))
	writeInfoField(&b, "uptime_in_days", strconv.FormatInt(uptime/86400, 10))
	writeInfoField(&b, "hz", "10")
	writeInfoField(&b, "configured_hz", "10")
	return b.String()
}

func (s *Server) infoClients() string {
	var b strings.Builder
	b.WriteString("# Clients\r\n")
	writeInfoField(&b, "connected_clients", strconv.Itoa(s.clientCount()))
	writeInfoField(&b, "maxclients", strconv.FormatInt(s.maxClients.Load(), 10))
	writeInfoField(&b, "blocked_clients", "0")
	return b.String()
}

func infoMemory() string {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	var b strings.Builder
	b.WriteString("# Memory\r\n")
	writeInfoField(&b, "used_memory", strconv.FormatUint(stats.Alloc, 10))
	writeInfoField(&b, "used_memory_human", humanBytes(stats.Alloc))
	writeInfoField(&b, "used_memory_rss", strconv.FormatUint(stats.Sys, 10))
	writeInfoField(&b, "mem_allocator", "go")
	return b.String()
}

func (s *Server) infoPersistence() string {
	var b strings.Builder
	b.WriteString("# Persistence\r\n")
	writeInfoField(&b, "loading", "0")
	writeInfoField(&b, "rdb_changes_since_last_save", "0")
	writeInfoField(&b, "rdb_bgsave_in_progress", bool01(s.bgsaveActive.Load()))
	writeInfoField(&b, "rdb_last_save_time", strconv.FormatInt(s.lastSave.Load(), 10))
	status := "ok"
	if !s.rdbOK.Load() {
		status = "err"
	}
	writeInfoField(&b, "rdb_last_bgsave_status", status)
	if s.aof == nil {
		writeInfoField(&b, "aof_enabled", "0")
	} else {
		writeInfoField(&b, "aof_enabled", "1")
		writeInfoField(&b, "aof_current_size", strconv.FormatInt(s.aof.Size(), 10))
		writeInfoField(&b, "aof_base_size", "0")
		writeInfoField(&b, "aof_pending_rewrite", "0")
		writeInfoField(&b, "aof_rewrite_in_progress", bool01(s.aof.Rewriting()))
		writeInfoField(&b, "aof_last_bgrewrite_status", "ok")
		writeInfoField(&b, "aof_last_write_status", "ok")
		writeInfoField(&b, "aof_last_write_errno", "0")
	}
	return b.String()
}

func (s *Server) infoStats() string {
	stats := s.data.Stats()
	var b strings.Builder
	b.WriteString("# Stats\r\n")
	writeInfoField(&b, "total_connections_received", strconv.FormatUint(s.totalConns.Load(), 10))
	writeInfoField(&b, "total_commands_processed", strconv.FormatUint(s.totalCmds.Load(), 10))
	writeInfoField(&b, "rejected_connections", strconv.FormatUint(s.rejected.Load(), 10))
	writeInfoField(&b, "expired_keys", strconv.FormatUint(stats.Expired, 10))
	writeInfoField(&b, "keyspace_hits", strconv.FormatUint(stats.Hits, 10))
	writeInfoField(&b, "keyspace_misses", strconv.FormatUint(stats.Misses, 10))
	writeInfoField(&b, "aof_write_errors", strconv.FormatUint(s.aofErrors.Load(), 10))
	return b.String()
}

func (s *Server) infoReplication() string {
	var b strings.Builder
	b.WriteString("# Replication\r\n")
	if host, port, ok := s.replicationTarget(); ok {
		writeInfoField(&b, "role", "slave")
		writeInfoField(&b, "master_host", host)
		writeInfoField(&b, "master_port", port)
		status := "down"
		if s.replUp.Load() {
			status = "up"
		}
		writeInfoField(&b, "master_link_status", status)
		writeInfoField(&b, "slave_read_only", "1")
		writeInfoField(&b, "master_repl_offset", strconv.FormatInt(s.masterOffset.Load(), 10))
	} else {
		writeInfoField(&b, "role", "master")
		writeInfoField(&b, "connected_slaves", strconv.Itoa(s.replicaCount()))
		writeInfoField(&b, "master_repl_offset", strconv.FormatInt(s.replOffset.Load(), 10))
		writeInfoField(&b, "sync_full", strconv.FormatUint(s.fullResyncs.Load(), 10))
	}
	writeInfoField(&b, "master_replid", s.replID)
	return b.String()
}

func (s *Server) infoKeyspace() string {
	keys, expiring := s.data.KeyCounts()
	var b strings.Builder
	b.WriteString("# Keyspace\r\n")
	if keys > 0 {
		fmt.Fprintf(&b, "db0:keys=%d,expires=%d,avg_ttl=0\r\n", keys, expiring)
	}
	return b.String()
}

func writeInfoField(b *strings.Builder, key, value string) {
	b.WriteString(key)
	b.WriteByte(':')
	b.WriteString(value)
	b.WriteString("\r\n")
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f%s", float64(n)/float64(div), string("KMGTPE"[exp]))
}

func containsAny(haystack []string, needles ...string) bool {
	for _, item := range haystack {
		for _, needle := range needles {
			if item == needle {
				return true
			}
		}
	}
	return false
}

func (s *Server) clientCount() int {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()
	return len(s.clients)
}

func (s *Server) clientList() string {
	s.clientsMu.RLock()
	clients := make([]*client, 0, len(s.clients))
	for _, c := range s.clients {
		clients = append(clients, c)
	}
	s.clientsMu.RUnlock()

	sort.Slice(clients, func(i, j int) bool { return clients[i].id < clients[j].id })

	var b strings.Builder
	for _, c := range clients {
		b.WriteString(c.infoLine())
		b.WriteByte('\n')
	}
	return b.String()
}

func (c *client) infoLine() string {
	channels, patterns := c.subCounts()
	return fmt.Sprintf(
		"id=%d addr=%s laddr=%s fd=-1 name=%s age=%d idle=0 flags=N db=0 sub=%d psub=%d ssub=0 multi=-1 qbuf=0 qbuf-free=0 argv-mem=0 obl=0 oll=0 omem=0 tot-mem=0 events=r cmd=%s user=default redir=-1 resp=%d",
		c.id,
		c.addr,
		c.localAddr(),
		c.name(),
		int64(time.Since(c.created).Seconds()),
		channels,
		patterns,
		c.lastCommand(),
		c.protoNum(),
	)
}

func (s *Server) port() int {
	if addr := s.Addr(); addr != nil {
		if tcp, ok := addr.(*net.TCPAddr); ok {
			return tcp.Port
		}
	}
	return s.cfg.Port
}

func (s *Server) timeReply() arrayReply {
	now := time.Now()
	return arrayReply{
		bulkReply(strconv.FormatInt(now.Unix(), 10)),
		bulkReply(strconv.Itoa(now.Nanosecond() / 1000)),
	}
}

func (s *Server) configSnapshot() map[string]string {
	snapshot := map[string]string{
		"bind":        s.cfg.Bind,
		"port":        strconv.Itoa(s.port()),
		"dir":         s.cfg.Dir,
		"dbfilename":  s.cfg.DBFilename,
		"maxclients":  strconv.FormatInt(s.maxClients.Load(), 10),
		"appendonly":  boolToYesNo(s.aof != nil),
		"appendfsync": s.cfg.AppendFsync,
		"loglevel":    strings.ToLower(s.level.Level().String()),
		"timeout":     strconv.Itoa(int(s.cfg.Timeout.Seconds())),
		"databases":   "1",
		"requirepass": s.acl.defaultPassword(),
		"notify-keyspace-events": func() string {
			value, _ := s.notifyRaw.Load().(string)
			return value
		}(),
	}
	if s.aof != nil {
		snapshot["appendfsync"] = string(s.aof.Policy())
	}
	return snapshot
}

func (s *Server) setConfig(key, value string) error {
	switch strings.ToLower(key) {
	case "maxclients":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 1 {
			return fmt.Errorf("ERR Invalid argument '%s' for CONFIG SET 'maxclients'", value)
		}
		s.maxClients.Store(n)
		return nil
	case "appendfsync":
		if s.aof == nil {
			return fmt.Errorf("ERR appendfsync cannot be changed when appendonly is off")
		}
		policy, err := store.ParseSyncPolicy(value)
		if err != nil {
			return fmt.Errorf("ERR Invalid argument '%s' for CONFIG SET 'appendfsync'", value)
		}
		if err := s.aof.SetPolicy(policy); err != nil {
			return fmt.Errorf("ERR %v", err)
		}
		return nil
	case "loglevel":
		switch strings.ToLower(value) {
		case "debug":
			s.level.Set(slog.LevelDebug)
		case "info":
			s.level.Set(slog.LevelInfo)
		case "warn", "warning":
			s.level.Set(slog.LevelWarn)
		case "error":
			s.level.Set(slog.LevelError)
		default:
			return fmt.Errorf("ERR Invalid argument '%s' for CONFIG SET 'loglevel'", value)
		}
		return nil
	case "requirepass":
		s.acl.setPassword("default", value)
		return nil
	case "notify-keyspace-events":
		if err := validateNotifyFlags(value); err != nil {
			return err
		}
		s.notifyRaw.Store(value)
		s.notifyConfig.Store(parseNotifyFlags(value))
		return nil
	case "appendonly", "dir", "bind", "port":
		return fmt.Errorf("ERR CONFIG SET '%s' cannot be changed at runtime", key)
	default:
		return fmt.Errorf("ERR Unsupported CONFIG parameter: %s", key)
	}
}

func boolToYesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func bool01(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func (s *Server) resetStats() {
	s.data.ResetStats()
	s.totalCmds.Store(0)
	s.totalConns.Store(0)
	s.rejected.Store(0)
	s.aofErrors.Store(0)
}

func (s *Server) rewriteAOF() error {
	if s.aof == nil {
		return fmt.Errorf("append only file is disabled")
	}
	if !s.startBGRewriteAOF() {
		return fmt.Errorf("background append only file rewriting already in progress")
	}
	return nil
}

// startBGRewriteAOF rewrites the log in the background. Appends during the
// rewrite are buffered by the AOF and folded into the new file.
func (s *Server) startBGRewriteAOF() bool {
	if s.aof == nil {
		return false
	}
	if !s.bgrewriteActive.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer s.bgrewriteActive.Store(false)
		err := s.aof.RewriteAsync(func(w *resp.Writer) error {
			var writeErr error
			s.data.EmitSnapshot(func(args [][]byte) {
				if writeErr != nil {
					return
				}
				writeErr = writeCommand(w, args)
			})
			return writeErr
		})
		if err != nil {
			s.logger.Error("append-only file rewrite failed", "err", err)
			return
		}
		s.lastSave.Store(time.Now().Unix())
	}()
	return true
}

func writeCommand(w *resp.Writer, args [][]byte) error {
	if err := w.WriteArrayHeader(len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if err := w.WriteBulk(arg); err != nil {
			return err
		}
	}
	return nil
}
