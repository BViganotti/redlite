package server

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
)

// asyncConn is a client safe to use from a goroutine: it returns errors instead
// of calling t.Fatalf.
type asyncConn struct {
	conn net.Conn
	rd   *bufio.Reader
}

func dialAsync(t *testing.T, srv *Server) *asyncConn {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &asyncConn{conn: conn, rd: bufio.NewReader(conn)}
}

func (c *asyncConn) call(args ...string) (any, error) {
	raw := make([][]byte, len(args))
	for i, arg := range args {
		raw[i] = []byte(arg)
	}
	if _, err := c.conn.Write(resp.AppendCommand(nil, raw)); err != nil {
		return nil, err
	}
	return readValue(c.rd)
}

func TestBlockingPopImmediate(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("RPUSH", "l", "a", "b"), 2)
	result := expectArray(t, c.do("BLPOP", "l", "1"))
	expectBulk(t, result[0], "l")
	expectBulk(t, result[1], "a")

	values := expectArray(t, c.do("LRANGE", "l", "0", "-1"))
	if len(values) != 1 {
		t.Fatalf("list has %d elements after BLPOP", len(values))
	}
	expectBulk(t, values[0], "b")

	expectInt(t, c.do("RPUSH", "r", "a", "b"), 2)
	tail := expectArray(t, c.do("BRPOP", "r", "1"))
	expectBulk(t, tail[1], "b")
}

func TestBlockingPopMultipleKeys(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("RPUSH", "second", "value"), 1)
	result := expectArray(t, c.do("BLPOP", "first", "second", "1"))
	expectBulk(t, result[0], "second")
	expectBulk(t, result[1], "value")
}

func TestBlockingPopWakesOnPush(t *testing.T) {
	srv := newTestServer(t, nil)
	blocked := dialAsync(t, srv)

	replies := make(chan any, 1)
	errs := make(chan error, 1)
	go func() {
		value, err := blocked.call("BLPOP", "queue", "5")
		if err != nil {
			errs <- err
			return
		}
		replies <- value
	}()

	time.Sleep(50 * time.Millisecond)
	pusher := dial(t, srv)
	expectInt(t, pusher.do("RPUSH", "queue", "job"), 1)

	select {
	case value := <-replies:
		result := expectArray(t, value)
		expectBulk(t, result[0], "queue")
		expectBulk(t, result[1], "job")
	case err := <-errs:
		t.Fatalf("BLPOP failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("BLPOP did not wake up")
	}
}

func TestBlockingPopTimeout(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	start := time.Now()
	expectNull(t, c.do("BLPOP", "empty", "0.05"))
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestBlockingPopInvalidTimeout(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectError(t, c.do("BLPOP", "k", "abc"), "not a float")
	expectError(t, c.do("BLPOP", "k", "-1"), "negative")
}

func TestBlockingCommandInsideMultiDoesNotBlock(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	start := time.Now()
	expectSimple(t, c.do("MULTI"), "OK")
	expectSimple(t, c.do("BLPOP", "empty", "5"), "QUEUED")
	results := expectArray(t, c.do("EXEC"))
	expectNull(t, results[0])
	if time.Since(start) > time.Second {
		t.Fatal("BLPOP blocked inside MULTI")
	}
}

func TestMoveCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("RPUSH", "src", "a", "b", "c"), 3)
	expectBulk(t, c.do("LMOVE", "src", "dst", "RIGHT", "LEFT"), "c")
	expectBulk(t, c.do("RPOPLPUSH", "src", "dst"), "b")

	dst := expectArray(t, c.do("LRANGE", "dst", "0", "-1"))
	expectBulk(t, dst[0], "b")
	expectBulk(t, dst[1], "c")

	expectNull(t, c.do("LMOVE", "missing", "dst", "LEFT", "LEFT"))
	expectError(t, c.do("LMOVE", "src", "dst", "UP", "LEFT"), "syntax error")
}

func TestBlockingMoveWakesOnPush(t *testing.T) {
	srv := newTestServer(t, nil)
	blocked := dialAsync(t, srv)

	replies := make(chan any, 1)
	go func() {
		value, err := blocked.call("BLMOVE", "src", "dst", "LEFT", "RIGHT", "5")
		if err != nil {
			replies <- err
			return
		}
		replies <- value
	}()

	time.Sleep(50 * time.Millisecond)
	pusher := dial(t, srv)
	expectInt(t, pusher.do("RPUSH", "src", "moved"), 1)

	select {
	case value := <-replies:
		expectBulk(t, value, "moved")
	case <-time.After(3 * time.Second):
		t.Fatal("BLMOVE did not wake up")
	}

	check := dial(t, srv)
	expectBulk(t, check.do("LINDEX", "dst", "0"), "moved")
}

func TestBRPopLPush(t *testing.T) {
	srv := newTestServer(t, nil)
	blocked := dialAsync(t, srv)

	replies := make(chan any, 1)
	go func() {
		value, err := blocked.call("BRPOPLPUSH", "src", "dst", "5")
		if err != nil {
			replies <- err
			return
		}
		replies <- value
	}()

	time.Sleep(50 * time.Millisecond)
	pusher := dial(t, srv)
	expectInt(t, pusher.do("RPUSH", "src", "a", "b"), 2)

	select {
	case value := <-replies:
		expectBulk(t, value, "b")
	case <-time.After(3 * time.Second):
		t.Fatal("BRPOPLPUSH did not wake up")
	}

	check := dial(t, srv)
	expectBulk(t, check.do("LINDEX", "dst", "0"), "b")
	remaining := expectArray(t, check.do("LRANGE", "src", "0", "-1"))
	if len(remaining) != 1 || string(remaining[0].([]byte)) != "a" {
		t.Fatalf("source after BRPOPLPUSH = %v", remaining)
	}
}

func TestBlockingPopFIFO(t *testing.T) {
	srv := newTestServer(t, nil)
	first := dialAsync(t, srv)
	second := dialAsync(t, srv)

	replies := make(chan string, 2)
	for _, client := range []*asyncConn{first, second} {
		conn := client
		go func() {
			value, err := conn.call("BLPOP", "q", "5")
			if err != nil {
				replies <- "error: " + err.Error()
				return
			}
			result, ok := value.([]any)
			if !ok || len(result) != 2 {
				replies <- "unexpected reply"
				return
			}
			replies <- string(result[1].([]byte))
		}()
	}

	time.Sleep(50 * time.Millisecond)
	pusher := dial(t, srv)
	expectInt(t, pusher.do("RPUSH", "q", "one"), 1)
	expectInt(t, pusher.do("RPUSH", "q", "two"), 1)

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case value := <-replies:
			seen[value] = true
		case <-time.After(3 * time.Second):
			t.Fatal("a blocked client never woke up")
		}
	}
	if !seen["one"] || !seen["two"] {
		t.Fatalf("blocked clients received %v", seen)
	}
}

func TestShutdownWakesBlockedClients(t *testing.T) {
	srv := newTestServer(t, nil)
	blocked := dialAsync(t, srv)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = blocked.call("BLPOP", "never", "0")
	}()

	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked client did not exit on shutdown")
	}
}
