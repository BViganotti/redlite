package redlite

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
)

func TestStartAndServe(t *testing.T) {
	srv, err := Start(Config{Dir: t.TempDir(), Port: 0})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("dial %s: %v", srv.Addr(), err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)
	send := func(args ...string) string {
		t.Helper()
		raw := make([][]byte, len(args))
		for i, arg := range args {
			raw[i] = []byte(arg)
		}
		if _, err := conn.Write(resp.AppendCommand(nil, raw)); err != nil {
			t.Fatalf("write: %v", err)
		}
		reply, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return reply
	}

	if got := send("PING"); got != "+PONG\r\n" {
		t.Fatalf("PING = %q", got)
	}
	if got := send("SET", "k", "v"); got != "+OK\r\n" {
		t.Fatalf("SET = %q", got)
	}
	if got := send("GET", "k"); got != "$1\r\n" {
		t.Fatalf("GET header = %q", got)
	}
	payload, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if payload != "v\r\n" {
		t.Fatalf("GET payload = %q", payload)
	}
}

func TestStartPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	first, err := Start(Config{Dir: dir, Port: 0, AppendOnly: true, AppendFsync: "always"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", first.Addr())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write(resp.AppendCommand(nil, [][]byte{[]byte("SET"), []byte("persisted"), []byte("yes")}))
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := first.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	cancel()

	second, err := Start(Config{Dir: dir, Port: 0, AppendOnly: true, AppendFsync: "always"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = second.Close(closeCtx)
	}()

	conn, err = net.Dial("tcp", second.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_, _ = conn.Write(resp.AppendCommand(nil, [][]byte{[]byte("GET"), []byte("persisted")}))
	reader = bufio.NewReader(conn)
	header, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if header != "$3\r\n" {
		t.Fatalf("GET header after restart = %q", header)
	}
	payload, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if payload != "yes\r\n" {
		t.Fatalf("GET payload after restart = %q", payload)
	}
}

func TestCloseTwice(t *testing.T) {
	srv, err := Start(Config{Dir: t.TempDir(), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if srv.Addr() == "" {
		t.Fatal("Addr should remain readable after Close")
	}
}
