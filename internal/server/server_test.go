package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/config"
	"github.com/BViganotti/redlite/internal/resp"
)

func newTestServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0
	cfg.Dir = t.TempDir()
	cfg.LogLevel = "error"
	if mutate != nil {
		mutate(&cfg)
	}
	return startServer(t, cfg)
}

// persistenceConfig returns a config that writes an AOF into dir.
func persistenceConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0
	cfg.Dir = dir
	cfg.AppendOnly = true
	cfg.AppendFsync = "always"
	cfg.LogLevel = "error"
	return cfg
}

// shutdownServer stops srv and fails the test if the drain does not finish.
func shutdownServer(t *testing.T, srv *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func startServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	level := new(slog.LevelVar)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv, err := New(cfg, logger, level)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve() }()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv
}

type testConn struct {
	t    *testing.T
	conn net.Conn
	rd   *bufio.Reader
}

func dial(t *testing.T, srv *Server) *testConn {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	tc := &testConn{t: t, conn: conn, rd: bufio.NewReader(conn)}
	t.Cleanup(func() { _ = conn.Close() })
	return tc
}

func (c *testConn) send(args ...string) {
	c.t.Helper()
	raw := make([][]byte, len(args))
	for i, arg := range args {
		raw[i] = []byte(arg)
	}
	if _, err := c.conn.Write(resp.AppendCommand(nil, raw)); err != nil {
		c.t.Fatalf("write %v: %v", args, err)
	}
}

func (c *testConn) do(args ...string) any {
	c.t.Helper()
	c.send(args...)
	value, err := readValue(c.rd)
	if err != nil {
		c.t.Fatalf("read reply to %v: %v", args, err)
	}
	return value
}

type testNull struct{}
type testError string

func readValue(r *bufio.Reader) (any, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, fmt.Errorf("empty reply line")
	}

	switch line[0] {
	case '+':
		return string(line[1:]), nil
	case '-':
		return testError(string(line[1:])), nil
	case ':':
		return strconv.ParseInt(string(line[1:]), 10, 64)
	case '$':
		size, err := strconv.Atoi(string(line[1:]))
		if err != nil {
			return nil, err
		}
		if size < 0 {
			return testNull{}, nil
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return buf[:size], nil
	case '*':
		count, err := strconv.Atoi(string(line[1:]))
		if err != nil {
			return nil, err
		}
		if count < 0 {
			return testNull{}, nil
		}
		out := make([]any, count)
		for i := range out {
			if out[i], err = readValue(r); err != nil {
				return nil, err
			}
		}
		return out, nil
	case '%':
		pairs, err := strconv.Atoi(string(line[1:]))
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, pairs)
		for i := 0; i < pairs; i++ {
			key, err := readValue(r)
			if err != nil {
				return nil, err
			}
			value, err := readValue(r)
			if err != nil {
				return nil, err
			}
			out[valueString(key)] = value
		}
		return out, nil
	case '_':
		return testNull{}, nil
	case '#':
		return string(line[1:]) == "t", nil
	default:
		return nil, fmt.Errorf("unknown reply type %q", line[0])
	}
}

func valueString(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}

func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	line = line[:len(line)-1]
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line, nil
}

func expectSimple(t *testing.T, got any, want string) {
	t.Helper()
	s, ok := got.(string)
	if !ok || s != want {
		t.Fatalf("got %v (%T), want simple string %q", got, got, want)
	}
}

func expectBulk(t *testing.T, got any, want string) {
	t.Helper()
	b, ok := got.([]byte)
	if !ok {
		t.Fatalf("got %v (%T), want bulk string", got, got)
	}
	if string(b) != want {
		t.Fatalf("got %q, want %q", b, want)
	}
}

func expectInt(t *testing.T, got any, want int64) {
	t.Helper()
	n, ok := got.(int64)
	if !ok || n != want {
		t.Fatalf("got %v (%T), want integer %d", got, got, want)
	}
}

func expectNull(t *testing.T, got any) {
	t.Helper()
	if _, ok := got.(testNull); !ok {
		t.Fatalf("got %v (%T), want null", got, got)
	}
}

func expectError(t *testing.T, got any, contains string) {
	t.Helper()
	e, ok := got.(testError)
	if !ok {
		t.Fatalf("got %v (%T), want error containing %q", got, got, contains)
	}
	if !strings.Contains(string(e), contains) {
		t.Fatalf("error %q does not contain %q", e, contains)
	}
}

func expectArray(t *testing.T, got any) []any {
	t.Helper()
	items, ok := got.([]any)
	if !ok {
		t.Fatalf("got %v (%T), want array", got, got)
	}
	return items
}

func TestPingEcho(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("PING"), "PONG")
	expectBulk(t, c.do("PING", "hello"), "hello")
	expectBulk(t, c.do("ECHO", "hello"), "hello")
}

func TestSetGet(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectNull(t, c.do("GET", "missing"))
	expectSimple(t, c.do("SET", "key", "value"), "OK")
	expectBulk(t, c.do("GET", "key"), "value")
	expectInt(t, c.do("EXISTS", "key"), 1)
	expectInt(t, c.do("DEL", "key"), 1)
	expectInt(t, c.do("EXISTS", "key"), 0)
}

func TestSetConditions(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "key", "one"), "OK")
	expectNull(t, c.do("SET", "key", "two", "NX"))
	expectBulk(t, c.do("GET", "key"), "one")
	expectNull(t, c.do("SET", "absent", "x", "XX"))
	expectSimple(t, c.do("SET", "key", "three", "XX"), "OK")
	expectBulk(t, c.do("GET", "key"), "three")

	expectBulk(t, c.do("SET", "key", "four", "GET"), "three")
}

func TestSetExpiry(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "key", "v", "PX", "5000"), "OK")
	ttl := c.do("PTTL", "key").(int64)
	if ttl <= 0 || ttl > 5000 {
		t.Fatalf("PTTL = %d, want (0, 5000]", ttl)
	}
	expectInt(t, c.do("PERSIST", "key"), 1)
	expectInt(t, c.do("TTL", "key"), -1)
	expectInt(t, c.do("TTL", "missing"), -2)

	expectSimple(t, c.do("SET", "gone", "v", "PX", "1"), "OK")
	time.Sleep(5 * time.Millisecond)
	expectNull(t, c.do("GET", "gone"))

	expectError(t, c.do("SET", "k", "v", "EX", "0"), "invalid expire time")
	expectError(t, c.do("SET", "k", "v", "EX", "1", "PX", "1"), "syntax error")
	expectError(t, c.do("SET", "k", "v", "NX", "XX"), "syntax error")
}

func TestIncrDecr(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("INCR", "counter"), 1)
	expectInt(t, c.do("INCRBY", "counter", "41"), 42)
	expectInt(t, c.do("DECR", "counter"), 41)
	expectInt(t, c.do("DECRBY", "counter", "41"), 0)

	expectSimple(t, c.do("SET", "text", "abc"), "OK")
	expectError(t, c.do("INCR", "text"), "not an integer")
}

func TestMultiKey(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("MSET", "a", "1", "b", "2"), "OK")
	values := expectArray(t, c.do("MGET", "a", "b", "missing"))
	if len(values) != 3 {
		t.Fatalf("MGET returned %d values", len(values))
	}
	expectBulk(t, values[0], "1")
	expectBulk(t, values[1], "2")
	expectNull(t, values[2])

	expectInt(t, c.do("MSETNX", "c", "3", "d", "4"), 1)
	expectInt(t, c.do("MSETNX", "d", "x", "e", "5"), 0)
	expectInt(t, c.do("EXISTS", "e"), 0)
}

func TestKeyCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "user:1", "a"), "OK")
	expectSimple(t, c.do("SET", "user:2", "b"), "OK")
	expectSimple(t, c.do("SET", "other", "c"), "OK")

	expectSimple(t, c.do("TYPE", "user:1"), "string")
	expectSimple(t, c.do("TYPE", "missing"), "none")

	keys := expectArray(t, c.do("KEYS", "user:*"))
	if len(keys) != 2 {
		t.Fatalf("KEYS returned %d keys, want 2", len(keys))
	}

	scan := expectArray(t, c.do("SCAN", "0", "MATCH", "user:*", "COUNT", "10"))
	if len(scan) != 2 {
		t.Fatalf("SCAN returned %d elements", len(scan))
	}
	expectBulk(t, scan[0], "0")
	if len(expectArray(t, scan[1])) != 2 {
		t.Fatal("SCAN should return both matching keys")
	}

	expectSimple(t, c.do("RENAME", "user:1", "renamed"), "OK")
	expectBulk(t, c.do("GET", "renamed"), "a")
	expectError(t, c.do("RENAME", "missing", "x"), "no such key")
	expectInt(t, c.do("RENAMENX", "renamed", "other"), 0)
	expectInt(t, c.do("RENAMENX", "renamed", "fresh"), 1)
}

func TestStringCommands(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("APPEND", "k", "hello"), 5)
	expectInt(t, c.do("APPEND", "k", " world"), 11)
	expectInt(t, c.do("STRLEN", "k"), 11)
	expectBulk(t, c.do("GETDEL", "k"), "hello world")
	expectInt(t, c.do("EXISTS", "k"), 0)

	expectSimple(t, c.do("SET", "g", "v", "EX", "100"), "OK")
	expectBulk(t, c.do("GETEX", "g", "PERSIST"), "v")
	expectInt(t, c.do("TTL", "g"), -1)
	expectNull(t, c.do("GETEX", "missing"))
}

func TestExpireFamily(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("SET", "k", "v"), "OK")
	expectInt(t, c.do("EXPIRE", "k", "100"), 1)
	expectInt(t, c.do("EXPIRE", "k", "200", "NX"), 0)
	expectInt(t, c.do("EXPIRE", "k", "50", "GT"), 0)
	expectInt(t, c.do("EXPIRE", "k", "50", "LT"), 1)
	expectInt(t, c.do("PERSIST", "k"), 1)
	expectInt(t, c.do("EXPIRE", "k", "100", "XX"), 0)
	expectInt(t, c.do("EXPIRE", "missing", "100"), 0)
}

func TestErrors(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectError(t, c.do("NOSUCHCOMMAND"), "unknown command")
	expectError(t, c.do("GET"), "wrong number of arguments")
	expectError(t, c.do("SET", "k", "v", "BOGUS"), "syntax error")
	expectError(t, c.do("INCRBY", "k", "notanumber"), "not an integer")
	expectError(t, c.do("SELECT", "9"), "out of range")
}

func TestHelloRESP3(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	hello := c.do("HELLO", "3")
	info, ok := hello.(map[string]any)
	if !ok {
		t.Fatalf("HELLO returned %T, want map", hello)
	}
	if proto, _ := info["proto"].(int64); proto != 3 {
		t.Fatalf("proto = %v, want 3", info["proto"])
	}
	expectNull(t, c.do("GET", "missing"))
}

func TestUnknownCommandMessage(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"NOTACOMMAND"}, "ERR unknown command 'NOTACOMMAND', with args beginning with: "},
		{[]string{"NOTACOMMAND", "abc"}, "ERR unknown command 'NOTACOMMAND', with args beginning with: 'abc' "},
		{[]string{"NOTACOMMAND", "a", "b"}, "ERR unknown command 'NOTACOMMAND', with args beginning with: 'a' 'b' "},
	} {
		got := c.do(tc.args...)
		errValue, ok := got.(testError)
		if !ok {
			t.Fatalf("%v: got %T, want an error", tc.args, got)
		}
		if string(errValue) != tc.want {
			t.Errorf("%v: got %q, want %q", tc.args, errValue, tc.want)
		}
	}
}

func TestInlineCommand(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	if _, err := c.conn.Write([]byte("PING\r\n")); err != nil {
		t.Fatal(err)
	}
	value, err := readValue(c.rd)
	if err != nil {
		t.Fatal(err)
	}
	expectSimple(t, value, "PONG")
}

func TestConfigAndInfo(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	pairs := expectArray(t, c.do("CONFIG", "GET", "maxclients"))
	if len(pairs) != 2 {
		t.Fatalf("CONFIG GET returned %d elements", len(pairs))
	}
	expectBulk(t, pairs[0], "maxclients")

	expectSimple(t, c.do("CONFIG", "SET", "maxclients", "64"), "OK")
	expectError(t, c.do("CONFIG", "SET", "maxclients", "0"), "Invalid argument")

	info := c.do("INFO")
	bulk, ok := info.([]byte)
	if !ok {
		t.Fatalf("INFO returned %T", info)
	}
	if !strings.Contains(string(bulk), "redis_version:7.0.0") {
		t.Fatal("INFO is missing redis_version")
	}
	if !strings.Contains(string(bulk), "connected_clients:") {
		t.Fatal("INFO is missing connected_clients")
	}
}

func TestCommandIntrospection(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("COMMAND", "COUNT"), int64(len(commandTable)))

	info := expectArray(t, c.do("COMMAND", "INFO", "get"))
	if len(info) != 1 {
		t.Fatalf("COMMAND INFO returned %d entries", len(info))
	}
	entry := expectArray(t, info[0])
	expectBulk(t, entry[0], "get")
}

func TestAOFPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0
	cfg.Dir = dir
	cfg.AppendOnly = true
	cfg.AppendFsync = "always"
	cfg.LogLevel = "error"

	first := startServer(t, cfg)
	c := dial(t, first)
	expectSimple(t, c.do("SET", "a", "1"), "OK")
	expectSimple(t, c.do("MSET", "b", "2", "c", "3"), "OK")
	expectInt(t, c.do("EXPIRE", "a", "1000"), 1)
	_ = c.conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := first.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	cancel()

	second := startServer(t, cfg)
	c2 := dial(t, second)
	expectBulk(t, c2.do("GET", "a"), "1")
	expectBulk(t, c2.do("GET", "b"), "2")
	expectBulk(t, c2.do("GET", "c"), "3")
	ttl := c2.do("TTL", "a").(int64)
	if ttl <= 0 || ttl > 1000 {
		t.Fatalf("persisted TTL = %d, want (0, 1000]", ttl)
	}
}

func TestBGRewriteAOF(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0
	cfg.Dir = dir
	cfg.AppendOnly = true
	cfg.AppendFsync = "always"
	cfg.LogLevel = "error"

	first := startServer(t, cfg)
	c := dial(t, first)
	expectSimple(t, c.do("SET", "kept", "value"), "OK")
	expectInt(t, c.do("DEL", "kept"), 1)
	expectSimple(t, c.do("SET", "kept", "final"), "OK")
	expectSimple(t, c.do("BGREWRITEAOF"), "Background append only file rewriting started")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := first.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	cancel()

	second := startServer(t, cfg)
	c2 := dial(t, second)
	expectBulk(t, c2.do("GET", "kept"), "final")
}

func TestConcurrentIncrements(t *testing.T) {
	srv := newTestServer(t, nil)

	const clients = 8
	const perClient = 50

	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("tcp", srv.Addr().String())
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()

			reader := bufio.NewReader(conn)
			for j := 0; j < perClient; j++ {
				cmd := resp.AppendCommand(nil, [][]byte{[]byte("INCR"), []byte("counter")})
				if _, err := conn.Write(cmd); err != nil {
					errs <- err
					return
				}
				if _, err := readValue(reader); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	c := dial(t, srv)
	expectBulk(t, c.do("GET", "counter"), strconv.Itoa(clients*perClient))
}
