package store

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
)

func TestRDBRoundTrip(t *testing.T) {
	clock := &testClock{ms: 1_700_000_000_000}
	source := NewWithClock(clock.Now)

	if _, err := source.Set("string", []byte("hello"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.HSet("hash", []KeyValue{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RPush("list", [][]byte{[]byte("x"), []byte("y"), []byte("z")}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SAdd("set", [][]byte{[]byte("m1"), []byte("m2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ZAdd("zset", ZAddOptions{}, []ScoreMember{
		{Score: 1.5, Member: "a"},
		{Score: -2, Member: "b"},
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Set("expiring", []byte("v"), SetOptions{
		HasExpire: true,
		ExpireAt:  clock.ms + 60_000,
	}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := source.SaveRDB(&buf); err != nil {
		t.Fatalf("SaveRDB: %v", err)
	}
	if got := buf.String()[:9]; got != "REDIS0010" {
		t.Fatalf("magic = %q", got)
	}

	target := NewWithClock(clock.Now)
	if err := target.LoadRDB(bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("LoadRDB: %v", err)
	}

	if value, ok, err := target.Get("string"); err != nil || !ok || string(value) != "hello" {
		t.Fatalf("string = %q, %v, %v", value, ok, err)
	}
	if value, ok, err := target.HGet("hash", "b"); err != nil || !ok || string(value) != "2" {
		t.Fatalf("hash = %q, %v, %v", value, ok, err)
	}
	list, err := target.LRange("list", 0, -1)
	if err != nil || len(list) != 3 || string(list[2]) != "z" {
		t.Fatalf("list = %q, %v", list, err)
	}
	members, err := target.SMembers("set")
	if err != nil || len(members) != 2 {
		t.Fatalf("set = %q, %v", members, err)
	}
	if score, ok, err := target.ZScore("zset", "b"); err != nil || !ok || score != -2 {
		t.Fatalf("zset = %v, %v, %v", score, ok, err)
	}
	if got := target.TTLMillis("expiring"); got != 60_000 {
		t.Fatalf("TTL = %d", got)
	}
}

func TestRDBRejectsBadChecksum(t *testing.T) {
	source := New()
	if _, err := source.Set("key", []byte("value"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := source.SaveRDB(&buf); err != nil {
		t.Fatal(err)
	}

	data := buf.Bytes()
	data[len(data)/2] ^= 0xff

	if err := New().LoadRDB(bytes.NewReader(data)); !errors.Is(err, ErrRDBFormat) {
		t.Fatalf("want ErrRDBFormat, got %v", err)
	}
}

// --- integration with a real Redis binary, skipped when it is not installed ---

func freeTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func redisPipe(t *testing.T, port int, commands [][]string) {
	t.Helper()
	var buf bytes.Buffer
	for _, command := range commands {
		raw := make([][]byte, len(command))
		for i, arg := range command {
			raw[i] = []byte(arg)
		}
		buf.Write(resp.AppendCommand(nil, raw))
	}
	c := exec.Command("redis-cli", "-p", strconv.Itoa(port), "--pipe")
	c.Stdin = &buf
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("redis-cli --pipe: %v\n%s", err, out)
	}
}

func redisCLI(t *testing.T, port int, args ...string) string {
	t.Helper()
	full := append([]string{"-p", strconv.Itoa(port)}, args...)
	out, err := exec.Command("redis-cli", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("redis-cli %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func startRedis(t *testing.T, dir string, dbfilename string) int {
	t.Helper()
	port := freeTestPort(t)
	command := exec.Command("redis-server",
		"--port", strconv.Itoa(port),
		"--dir", dir,
		"--dbfilename", dbfilename,
		"--save", "",
		"--appendonly", "no",
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start redis-server: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("redis-cli", "-p", strconv.Itoa(port), "PING").Output()
		if err == nil && strings.TrimSpace(string(out)) == "PONG" {
			return port
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("redis-server did not start")
	return 0
}

func TestRDBRedisReadsOurs(t *testing.T) {
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server is not installed")
	}

	dir := t.TempDir()
	source := New()
	if _, err := source.Set("compat:string", []byte("hello"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.HSet("compat:hash", []KeyValue{{Key: "a", Value: []byte("1")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RPush("compat:list", [][]byte{[]byte("x"), []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SAdd("compat:set", [][]byte{[]byte("m1"), []byte("m2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ZAdd("compat:zset", ZAddOptions{}, []ScoreMember{
		{Score: 1, Member: "one"}, {Score: 2, Member: "two"},
	}, false); err != nil {
		t.Fatal(err)
	}
	expiring := time.Now().Add(time.Hour).UnixMilli()
	if _, err := source.Set("compat:ttl", []byte("v"), SetOptions{HasExpire: true, ExpireAt: expiring}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "dump.rdb")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SaveRDB(file); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if checker, err := exec.LookPath("redis-check-rdb"); err == nil {
		out, err := exec.Command(checker, path).CombinedOutput()
		if err != nil {
			t.Fatalf("redis-check-rdb rejected the file: %v\n%s", err, out)
		}
	}

	port := startRedis(t, dir, "dump.rdb")
	if got := redisCLI(t, port, "GET", "compat:string"); got != "hello" {
		t.Fatalf("Redis GET = %q", got)
	}
	if got := redisCLI(t, port, "HGET", "compat:hash", "a"); got != "1" {
		t.Fatalf("Redis HGET = %q", got)
	}
	if got := redisCLI(t, port, "LRANGE", "compat:list", "0", "-1"); got != "x\ny" {
		t.Fatalf("Redis LRANGE = %q", got)
	}
	if got := redisCLI(t, port, "SCARD", "compat:set"); got != "2" {
		t.Fatalf("Redis SCARD = %q", got)
	}
	if got := redisCLI(t, port, "ZSCORE", "compat:zset", "two"); got != "2" {
		t.Fatalf("Redis ZSCORE = %q", got)
	}
	ttl, err := strconv.Atoi(redisCLI(t, port, "TTL", "compat:ttl"))
	if err != nil || ttl <= 0 || ttl > 3600 {
		t.Fatalf("Redis TTL = %d, %v", ttl, err)
	}
}

func TestRDBWeReadRedis(t *testing.T) {
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server is not installed")
	}

	dir := t.TempDir()
	port := startRedis(t, dir, "dump.rdb")

	// Small collections use listpack/intset encodings; large ones use the
	// plain encodings, so both reader paths are exercised.
	commands := [][]string{
		{"SET", "src:string", "hello"},
		{"HSET", "src:hash-small", "a", "1", "b", "2"},
		{"RPUSH", "src:list-small", "x", "y"},
		{"SADD", "src:set-ints", "1", "2", "3"},
		{"ZADD", "src:zset-small", "1", "one", "2", "two"},
		{"SET", "src:ttl", "v", "EX", "3600"},
	}
	for i := 0; i < 600; i++ {
		commands = append(commands, []string{"HSET", "src:hash-large", "f" + strconv.Itoa(i), "v"})
	}
	for i := 0; i < 500; i++ {
		commands = append(commands, []string{"RPUSH", "src:list-large", "i" + strconv.Itoa(i)})
	}
	for i := 0; i < 600; i++ {
		commands = append(commands, []string{"SADD", "src:set-large", "m" + strconv.Itoa(i)})
	}
	for i := 0; i < 200; i++ {
		commands = append(commands, []string{"ZADD", "src:zset-large", strconv.Itoa(i), "m" + strconv.Itoa(i)})
	}
	redisPipe(t, port, commands)
	redisCLI(t, port, "SAVE")
	redisCLI(t, port, "SHUTDOWN", "NOSAVE")

	file, err := os.Open(filepath.Join(dir, "dump.rdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	target := New()
	if err := target.LoadRDB(file); err != nil {
		t.Fatalf("LoadRDB: %v", err)
	}

	if value, ok, _ := target.Get("src:string"); !ok || string(value) != "hello" {
		t.Fatalf("string = %q", value)
	}
	if value, ok, _ := target.HGet("src:hash-small", "b"); !ok || string(value) != "2" {
		t.Fatalf("small hash = %q", value)
	}
	if n, _ := target.HLen("src:hash-large"); n != 600 {
		t.Fatalf("large hash length = %d", n)
	}
	if n, _ := target.LLen("src:list-small"); n != 2 {
		t.Fatalf("small list length = %d", n)
	}
	if n, _ := target.LLen("src:list-large"); n != 500 {
		t.Fatalf("large list length = %d", n)
	}
	if ok, _ := target.SIsMember("src:set-ints", []byte("2")); !ok {
		t.Fatal("intset member missing")
	}
	if n, _ := target.SCard("src:set-large"); n != 600 {
		t.Fatalf("large set size = %d", n)
	}
	if score, ok, _ := target.ZScore("src:zset-small", "two"); !ok || score != 2 {
		t.Fatalf("small zset score = %v", score)
	}
	if n, _ := target.ZCard("src:zset-large"); n != 200 {
		t.Fatalf("large zset size = %d", n)
	}
	ttl := target.TTLMillis("src:ttl")
	if ttl <= 0 || ttl > 3600*1000 {
		t.Fatalf("TTL = %d", ttl)
	}
}

func TestRDBIntset(t *testing.T) {
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server is not installed")
	}
	dir := t.TempDir()
	port := startRedis(t, dir, "dump.rdb")
	redisCLI(t, port, "SADD", "s", "1", "2", "3")
	redisCLI(t, port, "SAVE")
	redisCLI(t, port, "SHUTDOWN", "NOSAVE")

	file, err := os.Open(filepath.Join(dir, "dump.rdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	target := New()
	if err := target.LoadRDB(file); err != nil {
		t.Fatalf("LoadRDB: %v", err)
	}
	if n, _ := target.SCard("s"); n != 3 {
		t.Fatalf("SCard = %d", n)
	}
}
