package server

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
	"github.com/BViganotti/redlite/internal/store"
)

// client is one connected peer. Each client is served by exactly one
// goroutine, so its reader and writer need no locking; fields shared with
// CLIENT LIST are guarded by mu.
type client struct {
	srv  *Server
	conn net.Conn
	rd   *resp.Reader
	wr   *resp.Writer

	id      int64
	addr    string
	created time.Time
	cmds    atomic.Uint64
	proto   atomic.Int32

	mu      sync.Mutex
	cname   string
	lastCmd string

	multi   bool
	dirty   bool
	queued  [][][]byte
	watched map[string]store.WatchState

	writeMu sync.Mutex
	subs    map[string]struct{}
	psubs   map[string]struct{}
}

func newClient(srv *Server, conn net.Conn, id int64) *client {
	c := &client{
		srv:     srv,
		conn:    conn,
		rd:      resp.NewReader(conn),
		wr:      resp.NewWriter(conn),
		id:      id,
		addr:    conn.RemoteAddr().String(),
		created: time.Now(),
	}
	c.proto.Store(2)
	return c
}

// newReplayClient builds a detached client used while loading the append-only
// file. Its replies are discarded.
func newReplayClient(srv *Server) *client {
	c := &client{
		srv:     srv,
		wr:      resp.NewWriter(discardWriter{}),
		addr:    "replay",
		created: time.Now(),
	}
	c.proto.Store(2)
	return c
}

func (c *client) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

func (c *client) setName(name string) error {
	for i := 0; i < len(name); i++ {
		if name[i] < '!' || name[i] > '~' {
			return fmt.Errorf("ERR Client names cannot contain spaces, newlines or special characters")
		}
	}
	c.mu.Lock()
	c.cname = name
	c.mu.Unlock()
	return nil
}

func (c *client) name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cname
}

func (c *client) setLastCommand(cmd string) {
	c.mu.Lock()
	c.lastCmd = cmd
	c.mu.Unlock()
}

func (c *client) lastCommand() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastCmd == "" {
		return "NULL"
	}
	return c.lastCmd
}

func (c *client) localAddr() string {
	if c.conn == nil {
		return ""
	}
	return c.conn.LocalAddr().String()
}

// resetMulti clears transaction state. Watches are dropped as well, matching
// EXEC and DISCARD.
func (c *client) resetMulti() {
	c.multi = false
	c.dirty = false
	c.queued = nil
	c.watched = nil
}

// sendReply writes one reply under the connection's write lock. Pushes from
// PUBLISH arrive on other goroutines, so every write must take this lock.
func (c *client) sendReply(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.conn != nil {
		_ = c.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	}
	if err := writeReply(c.wr, value); err != nil {
		return err
	}
	return c.wr.Flush()
}

// push writes an out-of-band message to a subscriber.
func (c *client) push(value any) error {
	return c.sendReply(value)
}

func (c *client) subscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs)+len(c.psubs) > 0
}

func (c *client) subCounts() (channels, patterns int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs), len(c.psubs)
}

func (c *client) addSubscription(pattern bool, name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pattern {
		if c.psubs == nil {
			c.psubs = make(map[string]struct{})
		}
		c.psubs[name] = struct{}{}
	} else {
		if c.subs == nil {
			c.subs = make(map[string]struct{})
		}
		c.subs[name] = struct{}{}
	}
	return len(c.subs) + len(c.psubs)
}

func (c *client) removeSubscription(pattern bool, name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pattern {
		delete(c.psubs, name)
	} else {
		delete(c.subs, name)
	}
	return len(c.subs) + len(c.psubs)
}

func (c *client) subscriptionNames(pattern bool) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	source := c.subs
	if pattern {
		source = c.psubs
	}
	out := make([]string, 0, len(source))
	for name := range source {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (c *client) setProto(version int) error {
	if err := c.wr.SetProto(version); err != nil {
		return err
	}
	c.proto.Store(int32(version))
	return nil
}

func (c *client) protoNum() int {
	if v := c.proto.Load(); v != 0 {
		return int(v)
	}
	return 2
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
