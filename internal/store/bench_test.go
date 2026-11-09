package store

import (
	"strconv"
	"sync"
	"testing"
)

func BenchmarkGet(b *testing.B) {
	s := New()
	if _, err := s.Set("key", []byte("value"), SetOptions{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok, err := s.Get("key"); err != nil || !ok {
			b.Fatal("missing key")
		}
	}
}

func BenchmarkSet(b *testing.B) {
	s := New()
	value := []byte("value")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Set("key", value, SetOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSetParallel(b *testing.B) {
	s := New()
	value := []byte("value")
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := s.Set("key", value, SetOptions{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkIncr(b *testing.B) {
	s := New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.IncrBy("counter", 1); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHSet(b *testing.B) {
	s := New()
	pair := []KeyValue{{Key: "field", Value: []byte("value")}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.HSet("hash", pair); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRPush(b *testing.B) {
	s := New()
	value := [][]byte{[]byte("value")}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.RPush("list", value); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSAdd(b *testing.B) {
	s := New()
	member := [][]byte{[]byte("member")}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.SAdd("set", member); err != nil {
			b.Fatal(err)
		}
	}
}

// TestConcurrentMixedTypes exercises the single keyspace lock from many
// goroutines. Run with -race to make it meaningful.
func TestConcurrentMixedTypes(t *testing.T) {
	s := New()
	const workers = 8
	const iterations = 200

	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			suffix := strconv.Itoa(id)
			for i := 0; i < iterations; i++ {
				if _, err := s.Set("str:"+suffix, []byte("v"), SetOptions{}); err != nil {
					errs <- err
					return
				}
				if _, _, err := s.Get("str:" + suffix); err != nil {
					errs <- err
					return
				}
				if _, err := s.HSet("hash:"+suffix, []KeyValue{{Key: "f", Value: []byte("v")}}); err != nil {
					errs <- err
					return
				}
				if _, err := s.RPush("list:"+suffix, [][]byte{[]byte("v")}); err != nil {
					errs <- err
					return
				}
				if _, err := s.LPop("list:"+suffix, 1); err != nil {
					errs <- err
					return
				}
				if _, err := s.SAdd("set:"+suffix, [][]byte{[]byte("m")}); err != nil {
					errs <- err
					return
				}
				if _, err := s.SPop("set:"+suffix, 1); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
