package resp

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadMultibulk(t *testing.T) {
	input := "*3\r\n$3\r\nSET\r\n$3\r\nkey\r\n$5\r\nvalue\r\n"
	r := NewReader(strings.NewReader(input))

	args, err := r.ReadCommand()
	if err != nil {
		t.Fatalf("ReadCommand: %v", err)
	}
	want := []string{"SET", "key", "value"}
	if len(args) != len(want) {
		t.Fatalf("got %d args, want %d", len(args), len(want))
	}
	for i := range want {
		if string(args[i]) != want[i] {
			t.Errorf("arg %d = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestReadCommandStream(t *testing.T) {
	input := "*1\r\n$4\r\nPING\r\n*2\r\n$4\r\nECHO\r\n$2\r\nhi\r\n"
	r := NewReader(strings.NewReader(input))

	for i := 0; i < 2; i++ {
		if _, err := r.ReadCommand(); err != nil {
			t.Fatalf("command %d: %v", i, err)
		}
	}
	if _, err := r.ReadCommand(); !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF at end, got %v", err)
	}
}

func TestReadInline(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{"PING\r\n", []string{"PING"}},
		{"SET key value\r\n", []string{"SET", "key", "value"}},
		{"SET key \"hello world\"\r\n", []string{"SET", "key", "hello world"}},
		{"SET key 'a b'\r\n", []string{"SET", "key", "a b"}},
		{"ECHO \"a\\x41b\"\r\n", []string{"ECHO", "aAb"}},
		{"SET k \"\"\r\n", []string{"SET", "k", ""}},
	}
	for _, tc := range tests {
		r := NewReader(strings.NewReader(tc.line))
		args, err := r.ReadCommand()
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if len(args) != len(tc.want) {
			t.Fatalf("%q: got %d args, want %d", tc.line, len(args), len(tc.want))
		}
		for i := range tc.want {
			if string(args[i]) != tc.want[i] {
				t.Errorf("%q arg %d = %q, want %q", tc.line, i, args[i], tc.want[i])
			}
		}
	}
}

func TestReadInlineUnbalancedQuotes(t *testing.T) {
	r := NewReader(strings.NewReader("SET \"key value\r\n"))
	_, err := r.ReadCommand()
	var protoErr *ProtocolError
	if !errors.As(err, &protoErr) {
		t.Fatalf("want ProtocolError, got %v", err)
	}
}

func TestReadRejectsOversizedBulk(t *testing.T) {
	r := NewReader(strings.NewReader("*1\r\n$10\r\nx\r\n"))
	r.MaxBulk = 4
	_, err := r.ReadCommand()
	var protoErr *ProtocolError
	if !errors.As(err, &protoErr) {
		t.Fatalf("want ProtocolError, got %v", err)
	}
}

func TestReadRejectsOversizedArray(t *testing.T) {
	r := NewReader(strings.NewReader("*100\r\n"))
	r.MaxArray = 10
	_, err := r.ReadCommand()
	if err == nil {
		t.Fatal("want error for oversized array")
	}
}

func TestTruncatedCommandIsUnexpectedEOF(t *testing.T) {
	for _, input := range []string{
		"*2\r\n$3\r\nGET\r\n",
		"*2\r\n$3\r\nGET\r\n$5\r\nab",
	} {
		r := NewReader(strings.NewReader(input))
		_, err := r.ReadCommand()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%q: got %v, want io.ErrUnexpectedEOF", input, err)
		}
	}
}

func TestAppendCommandRoundTrip(t *testing.T) {
	want := [][]byte{[]byte("SET"), []byte("k"), {0x00, 0xff, '\r', '\n'}}
	encoded := AppendCommand(nil, want)

	r := NewReader(bytes.NewReader(encoded))
	got, err := r.ReadCommand()
	if err != nil {
		t.Fatalf("ReadCommand: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d args, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Errorf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriterNulls(t *testing.T) {
	tests := []struct {
		proto int
		want  string
	}{
		{2, "$-1\r\n"},
		{3, "_\r\n"},
	}
	for _, tc := range tests {
		var buf bytes.Buffer
		w := NewWriter(&buf)
		if err := w.SetProto(tc.proto); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteNull(); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if buf.String() != tc.want {
			t.Errorf("proto %d: got %q, want %q", tc.proto, buf.String(), tc.want)
		}
	}
}

func TestWritePushHeader(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WritePushHeader(2); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "*2\r\n"; got != want {
		t.Errorf("RESP2 push header = %q, want %q", got, want)
	}

	buf.Reset()
	if err := w.SetProto(3); err != nil {
		t.Fatal(err)
	}
	if err := w.WritePushHeader(2); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), ">2\r\n"; got != want {
		t.Errorf("RESP3 push header = %q, want %q", got, want)
	}
}

func TestWriterMapHeader(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteMapHeader(2); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "*4\r\n"; got != want {
		t.Errorf("RESP2 map header = %q, want %q", got, want)
	}

	buf.Reset()
	if err := w.SetProto(3); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteMapHeader(2); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "%2\r\n"; got != want {
		t.Errorf("RESP3 map header = %q, want %q", got, want)
	}
}

func TestWriterRejectsNewlineInSimpleString(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteSimple("OK\r\nINJECTED"); err == nil {
		t.Fatal("want error for newline in simple string")
	}
}

func TestWriterSanitizesError(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteError("ERR bad\nthing"); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "-ERR bad thing\r\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSetProtoRejectsUnknownVersion(t *testing.T) {
	w := NewWriter(io.Discard)
	if err := w.SetProto(4); err == nil {
		t.Fatal("want error for protocol version 4")
	}
}
