package server

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
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
