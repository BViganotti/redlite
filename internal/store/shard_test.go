package store

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

func TestShardDistribution(t *testing.T) {
	s := New()
	seen := make(map[*shard]bool)
	for i := 0; i < 256; i++ {
		seen[s.shardFor("key:"+strconv.Itoa(i))] = true
	}
	if len(seen) < defaultShardCount/2 {
		t.Fatalf("256 keys landed in only %d of %d shards", len(seen), defaultShardCount)
	}
}

func TestShardedKeyspaceOperations(t *testing.T) {
	s := New()
	for i := 0; i < 500; i++ {
		if _, err := s.Set(fmt.Sprintf("key:%d", i), []byte("v"), SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.DBSize(); n != 500 {
		t.Fatalf("DBSize = %d, want 500", n)
	}
	if keys := s.Keys("key:1*"); len(keys) != 111 {
		// key:1, key:10-19, key:100-199
		t.Fatalf("KEYS returned %d keys, want 111", len(keys))
	}

	// Rename across shards.
	if _, err := s.Set("source", []byte("v"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if !s.Rename("source", "destination") {
		t.Fatal("Rename failed")
	}
	if _, ok, _ := s.Get("destination"); !ok {
		t.Fatal("renamed key missing")
	}

	// Multi-key operations span shards.
	pairs := []KeyValue{
		{Key: "multi:a", Value: []byte("1")},
		{Key: "multi:b", Value: []byte("2")},
		{Key: "multi:c", Value: []byte("3")},
	}
	if !s.SetManyNX(pairs) {
		t.Fatal("SetManyNX failed")
	}
	if n := s.Exists([]string{"multi:a", "multi:b", "multi:c", "multi:missing"}); n != 3 {
		t.Fatalf("Exists = %d, want 3", n)
	}
	if n := s.Del([]string{"multi:a", "multi:b"}); n != 2 {
		t.Fatalf("Del = %d, want 2", n)
	}

	s.Flush()
	if n := s.DBSize(); n != 0 {
		t.Fatalf("DBSize after Flush = %d", n)
	}
	if keys := s.Keys("*"); len(keys) != 0 {
		t.Fatalf("KEYS after Flush = %v", keys)
	}
}

func TestConcurrentDistinctKeys(t *testing.T) {
	s := New()
	const workers = 8
	const perWorker = 200

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				key := fmt.Sprintf("worker:%d:%d", id, i)
				if _, err := s.Set(key, []byte("v"), SetOptions{}); err != nil {
					errs <- err
					return
				}
				if _, ok, err := s.Get(key); err != nil || !ok {
					errs <- fmt.Errorf("Get(%s) = %v, %v", key, ok, err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	if n := s.DBSize(); n != workers*perWorker {
		t.Fatalf("DBSize = %d, want %d", n, workers*perWorker)
	}
}

// TestGlobalOpsDuringWrites exercises the global gate against concurrent
// single-key writers. Run with -race.
func TestGlobalOpsDuringWrites(t *testing.T) {
	s := New()
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.Keys("*")
			s.DBSize()
			s.Scan(0, "*", 10, "")
			s.KeyCounts()
			s.EmitSnapshot(func(args [][]byte) {})
		}
	}()

	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("k:%d", i)
		if _, err := s.Set(key, []byte("v"), SetOptions{}); err != nil {
			t.Fatal(err)
		}
		if i%10 == 0 {
			s.Del([]string{key})
		}
	}
	close(stop)
	wg.Wait()
}
