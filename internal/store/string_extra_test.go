package store

import (
	"errors"
	"testing"
)

func TestGetRange(t *testing.T) {
	s, _ := newTestStore()
	if _, err := s.Set("k", []byte("This is a string"), SetOptions{}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		start, end int64
		want       string
	}{
		{0, 3, "This"},
		{-3, -1, "ing"},
		{10, 100, "string"},
		{0, -1, "This is a string"},
		{-100, 100, "This is a string"},
		{5, 3, ""},
		{100, 200, ""},
	}
	for _, tc := range tests {
		got, err := s.GetRange("k", tc.start, tc.end)
		if err != nil {
			t.Fatalf("GetRange(%d,%d): %v", tc.start, tc.end, err)
		}
		if string(got) != tc.want {
			t.Errorf("GetRange(%d,%d) = %q, want %q", tc.start, tc.end, got, tc.want)
		}
	}

	if value, err := s.GetRange("missing", 0, -1); err != nil || len(value) != 0 {
		t.Fatalf("GetRange on missing = %q, %v", value, err)
	}
}

func TestSetRange(t *testing.T) {
	s, _ := newTestStore()

	n, err := s.SetRange("k", 5, []byte("hello"))
	if err != nil || n != 10 {
		t.Fatalf("SetRange = %d, %v", n, err)
	}
	value, _, _ := s.Get("k")
	if string(value) != "\x00\x00\x00\x00\x00hello" {
		t.Fatalf("padded value = %q", value)
	}

	n, err = s.SetRange("k", 0, []byte("HELLO"))
	if err != nil || n != 10 {
		t.Fatalf("SetRange overwrite = %d, %v", n, err)
	}
	value, _, _ = s.Get("k")
	if string(value) != "HELLOhello" {
		t.Fatalf("value = %q", value)
	}

	if _, err := s.SetRange("k", -1, []byte("x")); !errors.Is(err, ErrOffsetOutOfRange) {
		t.Fatalf("want ErrOffsetOutOfRange, got %v", err)
	}

	if n, err := s.SetRange("empty", 0, nil); err != nil || n != 0 {
		t.Fatalf("empty SetRange = %d, %v", n, err)
	}
	if _, ok, _ := s.Get("empty"); ok {
		t.Fatal("empty SETRANGE must not create the key")
	}
}

func TestIncrByFloat(t *testing.T) {
	s, _ := newTestStore()

	value, err := s.IncrByFloat("k", "10.5")
	if err != nil || value != "10.5" {
		t.Fatalf("IncrByFloat = %q, %v", value, err)
	}
	value, err = s.IncrByFloat("k", "0.1")
	if err != nil || value != "10.6" {
		t.Fatalf("IncrByFloat = %q, %v", value, err)
	}

	// 0.1 + 0.2 must print as 0.3, the way Redis' long double does.
	value, err = s.IncrByFloat("decimal", "0.1")
	if err != nil {
		t.Fatal(err)
	}
	value, err = s.IncrByFloat("decimal", "0.2")
	if err != nil || value != "0.3" {
		t.Fatalf("0.1+0.2 = %q, %v", value, err)
	}

	if _, err := s.Set("text", []byte("abc"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IncrByFloat("text", "1"); !errors.Is(err, ErrNotFloat) {
		t.Fatalf("want ErrNotFloat, got %v", err)
	}

	if _, err := s.Set("inf", []byte("inf"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IncrByFloat("inf", "1"); !errors.Is(err, ErrFloatResult) {
		t.Fatalf("want ErrFloatResult, got %v", err)
	}
}
