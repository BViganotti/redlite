package server

import (
	"bytes"
	"strconv"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/resp"
)

func TestTransactionBasics(t *testing.T) {
	srv := newTestServer(t, nil)
	c1 := dial(t, srv)
	c2 := dial(t, srv)

	expectSimple(t, c1.do("MULTI"), "OK")
	expectSimple(t, c1.do("SET", "k", "v"), "QUEUED")
	expectSimple(t, c1.do("INCR", "n"), "QUEUED")
	expectNull(t, c2.do("GET", "k"))

	results := expectArray(t, c1.do("EXEC"))
	if len(results) != 2 {
		t.Fatalf("EXEC returned %d replies", len(results))
	}
	expectSimple(t, results[0], "OK")
	expectInt(t, results[1], 1)
	expectBulk(t, c2.do("GET", "k"), "v")
}

func TestTransactionErrors(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectError(t, c.do("EXEC"), "EXEC without MULTI")
	expectError(t, c.do("DISCARD"), "DISCARD without MULTI")

	expectSimple(t, c.do("MULTI"), "OK")
	expectError(t, c.do("MULTI"), "can not be nested")
	expectError(t, c.do("WATCH", "k"), "WATCH inside MULTI")
	expectSimple(t, c.do("SET", "k", "v"), "QUEUED")

	// A malformed command dirties the transaction.
	expectError(t, c.do("SET", "k"), "wrong number of arguments")
	expectError(t, c.do("EXEC"), "EXECABORT")
	expectInt(t, c.do("EXISTS", "k"), 0)

	// Runtime errors are reported per command and do not roll back the rest.
	expectSimple(t, c.do("MULTI"), "OK")
	expectSimple(t, c.do("SET", "text", "abc"), "QUEUED")
	expectSimple(t, c.do("INCR", "text"), "QUEUED")
	expectSimple(t, c.do("SET", "after", "yes"), "QUEUED")
	results := expectArray(t, c.do("EXEC"))
	if len(results) != 3 {
		t.Fatalf("EXEC returned %d replies", len(results))
	}
	expectSimple(t, results[0], "OK")
	expectError(t, results[1], "not an integer")
	expectSimple(t, results[2], "OK")
	expectBulk(t, c.do("GET", "after"), "yes")
}

func TestDiscard(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectSimple(t, c.do("MULTI"), "OK")
	expectSimple(t, c.do("SET", "k", "v"), "QUEUED")
	expectSimple(t, c.do("DISCARD"), "OK")
	expectInt(t, c.do("EXISTS", "k"), 0)
}

func TestWatchAborts(t *testing.T) {
	srv := newTestServer(t, nil)
	c1 := dial(t, srv)
	c2 := dial(t, srv)

	expectSimple(t, c2.do("SET", "k", "1"), "OK")
	expectSimple(t, c1.do("WATCH", "k"), "OK")
	expectSimple(t, c1.do("MULTI"), "OK")
	expectSimple(t, c1.do("SET", "k", "2"), "QUEUED")

	expectSimple(t, c2.do("SET", "k", "3"), "OK")

	expectNull(t, c1.do("EXEC"))
	expectBulk(t, c2.do("GET", "k"), "3")
}

func TestWatchIgnoresUnrelatedKeys(t *testing.T) {
	srv := newTestServer(t, nil)
	c1 := dial(t, srv)
	c2 := dial(t, srv)

	expectSimple(t, c2.do("SET", "k", "1"), "OK")
	expectSimple(t, c1.do("WATCH", "k"), "OK")
	expectSimple(t, c2.do("SET", "other", "x"), "OK")

	expectSimple(t, c1.do("MULTI"), "OK")
	expectSimple(t, c1.do("SET", "k", "2"), "QUEUED")
	results := expectArray(t, c1.do("EXEC"))
	expectSimple(t, results[0], "OK")
	expectBulk(t, c2.do("GET", "k"), "2")
}

func TestWatchSeesMissingKey(t *testing.T) {
	srv := newTestServer(t, nil)
	c1 := dial(t, srv)
	c2 := dial(t, srv)

	expectSimple(t, c1.do("WATCH", "missing"), "OK")
	expectSimple(t, c2.do("SET", "missing", "created"), "OK")

	expectSimple(t, c1.do("MULTI"), "OK")
	expectSimple(t, c1.do("SET", "missing", "mine"), "QUEUED")
	expectNull(t, c1.do("EXEC"))
	expectBulk(t, c2.do("GET", "missing"), "created")
}

func TestUnwatch(t *testing.T) {
	srv := newTestServer(t, nil)
	c1 := dial(t, srv)
	c2 := dial(t, srv)

	expectSimple(t, c2.do("SET", "k", "1"), "OK")
	expectSimple(t, c1.do("WATCH", "k"), "OK")
	expectSimple(t, c1.do("UNWATCH"), "OK")
	expectSimple(t, c2.do("SET", "k", "2"), "OK")

	expectSimple(t, c1.do("MULTI"), "OK")
	expectSimple(t, c1.do("SET", "k", "3"), "QUEUED")
	results := expectArray(t, c1.do("EXEC"))
	expectSimple(t, results[0], "OK")
	expectBulk(t, c2.do("GET", "k"), "3")
}

func TestWatchSeesExpiry(t *testing.T) {
	srv := newTestServer(t, nil)
	c := dial(t, srv)

	expectSimple(t, c.do("SET", "ephemeral", "v", "PX", "40"), "OK")
	expectSimple(t, c.do("WATCH", "ephemeral"), "OK")
	time.Sleep(60 * time.Millisecond)

	expectSimple(t, c.do("MULTI"), "OK")
	expectSimple(t, c.do("SET", "ephemeral", "mine"), "QUEUED")
	expectNull(t, c.do("EXEC"))
}

func TestExecIsAtomic(t *testing.T) {
	srv := newTestServer(t, nil)
	writer := dial(t, srv)
	reader := dial(t, srv)

	const iterations = 300
	done := make(chan struct{})
	failures := make(chan string, 1)

	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			command := resp.AppendCommand(nil, [][]byte{[]byte("MGET"), []byte("a"), []byte("b")})
			if _, err := reader.conn.Write(command); err != nil {
				failures <- "write: " + err.Error()
				return
			}
			value, err := readValue(reader.rd)
			if err != nil {
				failures <- "read: " + err.Error()
				return
			}
			pairs, ok := value.([]any)
			if !ok || len(pairs) != 2 {
				failures <- "unexpected MGET reply"
				return
			}
			if !replyEqual(pairs[0], pairs[1]) {
				failures <- "observed a partial transaction"
				return
			}
		}
	}()

	for i := 1; i <= iterations; i++ {
		writer.do("MULTI")
		writer.do("SET", "a", strconv.Itoa(i))
		writer.do("SET", "b", strconv.Itoa(i))
		writer.do("EXEC")
	}
	<-done

	select {
	case failure := <-failures:
		t.Fatal(failure)
	default:
	}
}

func TestTransactionPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := persistenceConfig(t, dir)

	first := startServer(t, cfg)
	c := dial(t, first)
	expectSimple(t, c.do("MULTI"), "OK")
	expectSimple(t, c.do("SET", "a", "1"), "QUEUED")
	expectSimple(t, c.do("INCR", "counter"), "QUEUED")
	expectSimple(t, c.do("HSET", "h", "f", "v"), "QUEUED")
	results := expectArray(t, c.do("EXEC"))
	if len(results) != 3 {
		t.Fatalf("EXEC returned %d replies", len(results))
	}
	shutdownServer(t, first)

	second := startServer(t, cfg)
	c2 := dial(t, second)
	expectBulk(t, c2.do("GET", "a"), "1")
	expectBulk(t, c2.do("GET", "counter"), "1")
	expectBulk(t, c2.do("HGET", "h", "f"), "v")
}

// replyEqual compares two MGET elements, covering the null case.
func replyEqual(a, b any) bool {
	ab, aok := a.([]byte)
	bb, bok := b.([]byte)
	if aok && bok {
		return bytes.Equal(ab, bb)
	}
	_, an := a.(testNull)
	_, bn := b.(testNull)
	return an && bn
}
