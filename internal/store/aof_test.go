package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BViganotti/redlite/internal/resp"
)

func TestAOFRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.aof")

	aof, err := OpenAOF(path, SyncAlways)
	if err != nil {
		t.Fatalf("OpenAOF: %v", err)
	}
	if err := aof.Append([][]byte{[]byte("SET"), []byte("a"), []byte("1")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := aof.Append([][]byte{[]byte("SET"), []byte("b"), []byte("2")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := aof.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenAOF(path, SyncEverySec)
	if err != nil {
		t.Fatalf("OpenAOF: %v", err)
	}
	defer reopened.Close()

	var commands [][][]byte
	truncated, err := reopened.Load(func(args [][]byte) error {
		commands = append(commands, args)
		return nil
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if truncated != 0 {
		t.Fatalf("truncated = %d, want 0", truncated)
	}
	if len(commands) != 2 {
		t.Fatalf("loaded %d commands, want 2", len(commands))
	}
	if string(commands[1][1]) != "b" || string(commands[1][2]) != "2" {
		t.Fatalf("unexpected second command: %q", commands[1])
	}
}

func TestAOFTruncatesPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.aof")
	complete := []byte("*3\r\n$3\r\nSET\r\n$1\r\na\r\n$1\r\n1\r\n")
	partial := []byte("*3\r\n$3\r\nSET\r\n$1\r\nb")

	if err := os.WriteFile(path, append(complete, partial...), 0o644); err != nil {
		t.Fatal(err)
	}

	aof, err := OpenAOF(path, SyncNo)
	if err != nil {
		t.Fatal(err)
	}
	defer aof.Close()

	var commands [][][]byte
	truncated, err := aof.Load(func(args [][]byte) error {
		commands = append(commands, args)
		return nil
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if truncated != int64(len(complete)) {
		t.Fatalf("truncated offset = %d, want %d", truncated, len(complete))
	}
	if len(commands) != 1 {
		t.Fatalf("loaded %d commands, want 1", len(commands))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(complete)) {
		t.Fatalf("file size = %d, want %d", info.Size(), len(complete))
	}

	// The log must still be appendable after truncation.
	if err := aof.Append([][]byte{[]byte("PING")}); err != nil {
		t.Fatalf("Append after truncate: %v", err)
	}
}

func TestAOFRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.aof")
	aof, err := OpenAOF(path, SyncAlways)
	if err != nil {
		t.Fatal(err)
	}
	defer aof.Close()

	if err := aof.Append([][]byte{[]byte("FLUSHALL")}); err != nil {
		t.Fatal(err)
	}
	if err := aof.Append([][]byte{[]byte("SET"), []byte("junk"), []byte("junk")}); err != nil {
		t.Fatal(err)
	}

	if err := aof.Rewrite(func(w *resp.Writer) error {
		if err := w.WriteArrayHeader(3); err != nil {
			return err
		}
		for _, part := range [][]byte{[]byte("SET"), []byte("kept"), []byte("value")} {
			if err := w.WriteBulk(part); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("Rewrite: %v", err)
	}

	var commands [][][]byte
	if _, err := aof.Load(func(args [][]byte) error {
		commands = append(commands, args)
		return nil
	}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("loaded %d commands after rewrite, want 1", len(commands))
	}
	if string(commands[0][1]) != "kept" {
		t.Fatalf("unexpected key after rewrite: %q", commands[0][1])
	}
}

func TestAOFRejectsUnknownSyncPolicy(t *testing.T) {
	if _, err := ParseSyncPolicy("sometimes"); err == nil {
		t.Fatal("want error for unknown policy")
	}
}
