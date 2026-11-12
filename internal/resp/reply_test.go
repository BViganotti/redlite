package resp

import (
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

func mustReadReply(t *testing.T, input string) *Reply {
	t.Helper()
	reply, err := NewReplyReader(strings.NewReader(input)).ReadReply()
	if err != nil {
		t.Fatalf("ReadReply(%q): %v", input, err)
	}
	return reply
}

func TestReplyReaderScalars(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(*testing.T, *Reply)
	}{
		{
			name:  "simple string",
			input: "+OK\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != SimpleStringReply || r.Text != "OK" {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "error",
			input: "-WRONGTYPE bad\r\n",
			check: func(t *testing.T, r *Reply) {
				if !r.IsError() || r.Text != "WRONGTYPE bad" {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "integer",
			input: ":-42\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != IntegerReply || r.Int != -42 {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "bulk string",
			input: "$5\r\nhello\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != BulkStringReply || r.Text != "hello" {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "null bulk",
			input: "$-1\r\n",
			check: func(t *testing.T, r *Reply) {
				if !r.IsNull() {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "null array",
			input: "*-1\r\n",
			check: func(t *testing.T, r *Reply) {
				if !r.IsNull() {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "null resp3",
			input: "_\r\n",
			check: func(t *testing.T, r *Reply) {
				if !r.IsNull() {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "boolean true",
			input: "#t\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != BooleanReply || !r.Bool {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "boolean false",
			input: "#f\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != BooleanReply || r.Bool {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "double",
			input: ",3.5\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != DoubleReply || r.Float != 3.5 {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "infinite double",
			input: ",-inf\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != DoubleReply || !math.IsInf(r.Float, -1) {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "big number",
			input: "(3492890328409238509324850943850943825024385\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != BigNumberReply || r.Text != "3492890328409238509324850943850943825024385" {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name:  "verbatim",
			input: "=9\r\ntxt:hello\r\n",
			check: func(t *testing.T, r *Reply) {
				if r.Kind != VerbatimReply || r.Text != "hello" {
					t.Fatalf("got %+v", r)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, mustReadReply(t, tc.input))
		})
	}
}

func TestReplyReaderContainers(t *testing.T) {
	array := mustReadReply(t, "*3\r\n:1\r\n$3\r\nabc\r\n+OK\r\n")
	if array.Kind != ArrayReply || len(array.Values) != 3 {
		t.Fatalf("array = %+v", array)
	}
	if array.Values[0].Int != 1 || array.Values[1].Text != "abc" || array.Values[2].Text != "OK" {
		t.Fatalf("array values = %+v", array.Values)
	}

	set := mustReadReply(t, "~2\r\n+one\r\n+two\r\n")
	if set.Kind != SetReply || len(set.Values) != 2 || set.Values[1].Text != "two" {
		t.Fatalf("set = %+v", set)
	}

	push := mustReadReply(t, ">2\r\n+message\r\n$2\r\nhi\r\n")
	if push.Kind != PushReply || len(push.Values) != 2 || push.Values[1].Text != "hi" {
		t.Fatalf("push = %+v", push)
	}

	mp := mustReadReply(t, "%2\r\n+one\r\n:1\r\n+two\r\n:2\r\n")
	if mp.Kind != MapReply || len(mp.Pairs) != 2 {
		t.Fatalf("map = %+v", mp)
	}
	if mp.Pairs[0].Key.Text != "one" || mp.Pairs[0].Value.Int != 1 {
		t.Fatalf("map first pair = %+v", mp.Pairs[0])
	}
	if mp.Pairs[1].Key.Text != "two" || mp.Pairs[1].Value.Int != 2 {
		t.Fatalf("map second pair = %+v", mp.Pairs[1])
	}

	nested := mustReadReply(t, "*1\r\n%1\r\n+k\r\n~1\r\n:7\r\n")
	if nested.Kind != ArrayReply || len(nested.Values) != 1 {
		t.Fatalf("nested = %+v", nested)
	}
	inner := nested.Values[0]
	if inner.Kind != MapReply || len(inner.Pairs) != 1 {
		t.Fatalf("inner map = %+v", inner)
	}
	if inner.Pairs[0].Value.Kind != SetReply || inner.Pairs[0].Value.Values[0].Int != 7 {
		t.Fatalf("inner set = %+v", inner.Pairs[0].Value)
	}
}

func TestReplyReaderAttributes(t *testing.T) {
	reply := mustReadReply(t, "|1\r\n+ttl\r\n:30\r\n+OK\r\n")
	if reply.Kind != SimpleStringReply || reply.Text != "OK" {
		t.Fatalf("reply = %+v", reply)
	}
	if len(reply.Attributes) != 1 {
		t.Fatalf("attributes = %+v", reply.Attributes)
	}
	if reply.Attributes[0].Key.Text != "ttl" || reply.Attributes[0].Value.Int != 30 {
		t.Fatalf("attribute = %+v", reply.Attributes[0])
	}
}

func TestReplyReaderStream(t *testing.T) {
	r := NewReplyReader(strings.NewReader("+OK\r\n:7\r\n$2\r\nhi\r\n"))
	for i, want := range []ReplyKind{SimpleStringReply, IntegerReply, BulkStringReply} {
		reply, err := r.ReadReply()
		if err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
		if reply.Kind != want {
			t.Fatalf("reply %d kind = %v, want %v", i, reply.Kind, want)
		}
	}
	if _, err := r.ReadReply(); !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF at end, got %v", err)
	}
}

func TestReplyReaderErrors(t *testing.T) {
	tests := []string{
		"$5\r\nhel",
		"?wat\r\n",
		"#x\r\n",
		"_,\r\n",
		",notanumber\r\n",
		"(12x\r\n",
		"=3\r\nabc\r\n",
		"*\r\n",
	}
	for _, input := range tests {
		if _, err := NewReplyReader(strings.NewReader(input)).ReadReply(); err == nil {
			t.Errorf("ReadReply(%q) succeeded, want error", input)
		}
	}
}

func TestReplyReaderLimits(t *testing.T) {
	r := NewReplyReader(strings.NewReader("$3\r\nabc\r\n"))
	r.MaxBulk = 2
	if _, err := r.ReadReply(); err == nil {
		t.Fatal("oversized bulk accepted")
	}

	r = NewReplyReader(strings.NewReader("*2\r\n:1\r\n:2\r\n"))
	r.MaxArray = 1
	if _, err := r.ReadReply(); err == nil {
		t.Fatal("oversized array accepted")
	}

	r = NewReplyReader(strings.NewReader("%2\r\n+k\r\n:1\r\n+k\r\n:2\r\n"))
	r.MaxArray = 3 // only three scalar slots available for a two-pair map
	if _, err := r.ReadReply(); err == nil {
		t.Fatal("oversized map accepted")
	}
}
