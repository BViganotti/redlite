package store

import (
	"encoding/hex"
	"testing"
)

// sparseFixture is the exact 27-byte value Redis 8.8 produces for
// PFADD key a b c. It exercises the ZERO/VAL/XZERO sparse decoder.
const sparseFixture = "48594c4c01000000000000000000008060f38050b1844bfb80425a"

func TestHLLReadsRedisSparse(t *testing.T) {
	raw, err := hex.DecodeString(sparseFixture)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newTestStore()
	if _, err := s.Set("h", raw, SetOptions{}); err != nil {
		t.Fatal(err)
	}
	n, err := s.PFCount([]string{"h"})
	if err != nil || n != 3 {
		t.Fatalf("PFCount sparse = %d, %v", n, err)
	}
	// Adding an element rewrites the value as dense and remains readable.
	if changed, err := s.PFAdd("h", [][]byte{[]byte("d")}); err != nil || changed != 1 {
		t.Fatalf("PFAdd = %d, %v", changed, err)
	}
	n, err = s.PFCount([]string{"h"})
	if err != nil || n != 4 {
		t.Fatalf("PFCount after add = %d, %v", n, err)
	}
}

func TestHLLDenseFormatAndEstimate(t *testing.T) {
	s, _ := newTestStore()
	if changed, err := s.PFAdd("h", [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil || changed != 1 {
		t.Fatalf("PFAdd = %d, %v", changed, err)
	}
	value, ok, err := s.Get("h")
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if len(value) != 12304 {
		t.Fatalf("dense HLL length = %d", len(value))
	}
	if string(value[:4]) != "HYLL" || value[4] != 0 {
		t.Fatalf("unexpected HLL header: %x", value[:5])
	}
	n, err := s.PFCount([]string{"h"})
	if err != nil || n != 3 {
		t.Fatalf("PFCount = %d, %v", n, err)
	}
	// The cached cardinality survives without changing the value.
	n, err = s.PFCount([]string{"h"})
	if err != nil || n != 3 {
		t.Fatalf("cached PFCount = %d, %v", n, err)
	}
}

func TestHLLUnionAndMerge(t *testing.T) {
	s, _ := newTestStore()
	_, _ = s.PFAdd("a", [][]byte{[]byte("1"), []byte("2")})
	_, _ = s.PFAdd("b", [][]byte{[]byte("2"), []byte("3")})
	n, err := s.PFCount([]string{"a", "b"})
	if err != nil || n != 3 {
		t.Fatalf("union PFCount = %d, %v", n, err)
	}
	if err := s.PFMerge("dst", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	n, err = s.PFCount([]string{"dst"})
	if err != nil || n != 3 {
		t.Fatalf("merged PFCount = %d, %v", n, err)
	}

	_, _ = s.Set("plain", []byte("not hll"), SetOptions{})
	if _, err := s.PFCount([]string{"plain"}); err != ErrNotHLL {
		t.Fatalf("want ErrNotHLL, got %v", err)
	}
	_, _ = s.RPush("list", [][]byte{[]byte("x")})
	if _, err := s.PFCount([]string{"list"}); err != ErrWrongType {
		t.Fatalf("want ErrWrongType, got %v", err)
	}
}
