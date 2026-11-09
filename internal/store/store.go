// Package store implements the in-memory keyspace, expiration, and the
// append-only file.
//
// The keyspace is protected by a single reader-writer mutex. That is the
// simplest thing that is correct; sharding is tracked in ROADMAP.md as a
// performance task rather than a correctness one.
package store

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Errors returned by value-changing operations. Command handlers turn these
// into Redis error replies.
var (
	ErrNotInteger = errors.New("value is not an integer or out of range")
	ErrOverflow   = errors.New("increment or decrement would overflow")
)

type entry struct {
	value    []byte
	expireAt int64 // unix milliseconds; 0 means no expiry
}

// Stats are counters that INFO reports.
type Stats struct {
	Hits     uint64
	Misses   uint64
	Expired  uint64
	SetCount uint64
}

// Store is a concurrent keyspace.
type Store struct {
	mu   sync.RWMutex
	data map[string]*entry

	hits    atomic.Uint64
	misses  atomic.Uint64
	expired atomic.Uint64
	sets    atomic.Uint64

	now      func() int64
	stop     chan struct{}
	stopOnce sync.Once
}

// New returns an empty store using the wall clock.
func New() *Store {
	return NewWithClock(func() int64 { return time.Now().UnixMilli() })
}

// NewWithClock returns a store driven by an injectable clock, for tests.
func NewWithClock(now func() int64) *Store {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Store{
		data: make(map[string]*entry),
		now:  now,
		stop: make(chan struct{}),
	}
}

// StartExpiry runs the background expiration sampler. Redis samples a few keys
// per tick instead of scanning the whole keyspace; so do we.
func (s *Store) StartExpiry(interval time.Duration, sample int) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	if sample <= 0 {
		sample = 20
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.expireSample(sample)
			case <-s.stop:
				return
			}
		}
	}()
}

// Close stops the background expiration sampler. It is safe to call twice.
func (s *Store) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
}

func (s *Store) expireSample(limit int) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := 0
	for key, e := range s.data {
		if seen >= limit {
			return
		}
		seen++
		if e.expireAt != 0 && e.expireAt <= now {
			delete(s.data, key)
			s.expired.Add(1)
		}
	}
}

// Stats returns a snapshot of the counters.
func (s *Store) Stats() Stats {
	return Stats{
		Hits:     s.hits.Load(),
		Misses:   s.misses.Load(),
		Expired:  s.expired.Load(),
		SetCount: s.sets.Load(),
	}
}

// ResetStats zeroes the counters reported by INFO.
func (s *Store) ResetStats() {
	s.hits.Store(0)
	s.misses.Store(0)
	s.expired.Store(0)
	s.sets.Store(0)
}

// Get returns a copy of the value stored at key.
func (s *Store) Get(key string) ([]byte, bool) {
	now := s.now()
	s.mu.RLock()
	e, ok := s.data[key]
	if !ok || isExpired(e, now) {
		s.mu.RUnlock()
		s.misses.Add(1)
		return nil, false
	}
	value := clone(e.value)
	s.mu.RUnlock()
	s.hits.Add(1)
	return value, true
}

// SetOptions controls the behaviour of Set.
type SetOptions struct {
	NX        bool
	XX        bool
	KeepTTL   bool
	HasExpire bool
	ExpireAt  int64 // unix milliseconds
}

// SetResult reports what Set did and what it replaced.
type SetResult struct {
	Written bool
	Old     []byte
	HadOld  bool
}

// Set stores value at key and applies the expiry described by opts.
func (s *Store) Set(key string, value []byte, opts SetOptions) SetResult {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.data[key]
	if exists && isExpired(current, now) {
		delete(s.data, key)
		s.expired.Add(1)
		exists = false
		current = nil
	}

	var res SetResult
	if exists {
		res.Old = clone(current.value)
		res.HadOld = true
	}
	if (opts.NX && exists) || (opts.XX && !exists) {
		return res
	}

	expireAt := int64(0)
	switch {
	case opts.KeepTTL && exists:
		expireAt = current.expireAt
	case opts.HasExpire:
		expireAt = opts.ExpireAt
	}

	res.Written = true
	s.sets.Add(1)

	if expireAt != 0 && expireAt <= now {
		delete(s.data, key)
		return res
	}
	s.data[key] = &entry{value: clone(value), expireAt: expireAt}
	return res
}

// KeyValue is a key/value pair used by MSET-style operations.
type KeyValue struct {
	Key   string
	Value []byte
}

// SetMany writes every pair under one lock so the batch is atomic to readers.
func (s *Store) SetMany(pairs []KeyValue) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pair := range pairs {
		if current, ok := s.data[pair.Key]; ok && isExpired(current, now) {
			delete(s.data, pair.Key)
			s.expired.Add(1)
		}
		s.data[pair.Key] = &entry{value: clone(pair.Value)}
	}
	s.sets.Add(uint64(len(pairs)))
}

// SetManyNX writes all pairs only if none of the keys exists.
func (s *Store) SetManyNX(pairs []KeyValue) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, pair := range pairs {
		if current, ok := s.data[pair.Key]; ok {
			if isExpired(current, now) {
				delete(s.data, pair.Key)
				s.expired.Add(1)
				continue
			}
			return false
		}
	}
	for _, pair := range pairs {
		s.data[pair.Key] = &entry{value: clone(pair.Value)}
	}
	s.sets.Add(uint64(len(pairs)))
	return true
}

// Del removes keys and reports how many existed and were not already expired.
func (s *Store) Del(keys []string) int64 {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed int64
	for _, key := range keys {
		e, ok := s.data[key]
		if !ok {
			continue
		}
		delete(s.data, key)
		if isExpired(e, now) {
			s.expired.Add(1)
			continue
		}
		removed++
	}
	return removed
}

// Exists counts how many of keys exist, once per occurrence.
func (s *Store) Exists(keys []string) int64 {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	var n int64
	for _, key := range keys {
		if e, ok := s.data[key]; ok && !isExpired(e, now) {
			n++
		}
	}
	return n
}

// Type reports the value type of key, or "none".
func (s *Store) Type(key string) string {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	if !ok || isExpired(e, now) {
		return "none"
	}
	return "string"
}

// Append appends suffix to the value at key and returns the new length.
func (s *Store) Append(key string, suffix []byte) int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.data[key]
	if ok && isExpired(e, now) {
		delete(s.data, key)
		s.expired.Add(1)
		ok = false
	}
	if !ok {
		s.data[key] = &entry{value: clone(suffix)}
		s.sets.Add(1)
		return len(suffix)
	}
	e.value = append(e.value, suffix...)
	return len(e.value)
}

// StrLen returns the length of the value at key.
func (s *Store) StrLen(key string) int64 {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	if !ok || isExpired(e, now) {
		return 0
	}
	return int64(len(e.value))
}

// IncrBy adds delta to the integer stored at key, initializing missing keys to
// zero.
func (s *Store) IncrBy(key string, delta int64) (int64, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.data[key]
	if ok && isExpired(e, now) {
		delete(s.data, key)
		s.expired.Add(1)
		ok = false
	}

	var current int64
	if ok {
		parsed, err := strconv.ParseInt(string(e.value), 10, 64)
		if err != nil {
			return 0, ErrNotInteger
		}
		current = parsed
	}
	if (delta > 0 && current > math.MaxInt64-delta) || (delta < 0 && current < math.MinInt64-delta) {
		return 0, ErrOverflow
	}

	next := current + delta
	if ok {
		e.value = strconv.AppendInt(e.value[:0], next, 10)
	} else {
		s.data[key] = &entry{value: strconv.AppendInt(nil, next, 10)}
		s.sets.Add(1)
	}
	return next, nil
}

// ExpireMode selects the conditional behaviour of Expire.
type ExpireMode int

const (
	ExpireAlways ExpireMode = iota
	ExpireNX
	ExpireXX
	ExpireGT
	ExpireLT
)

// Expire sets the absolute expiry of key to expireAt (unix milliseconds).
func (s *Store) Expire(key string, expireAt int64, mode ExpireMode) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.data[key]
	if !ok || isExpired(e, now) {
		if ok {
			delete(s.data, key)
			s.expired.Add(1)
		}
		return false
	}

	switch mode {
	case ExpireNX:
		if e.expireAt != 0 {
			return false
		}
	case ExpireXX:
		if e.expireAt == 0 {
			return false
		}
	case ExpireGT:
		if e.expireAt == 0 || expireAt <= e.expireAt {
			return false
		}
	case ExpireLT:
		if e.expireAt != 0 && expireAt >= e.expireAt {
			return false
		}
	}

	if expireAt <= now {
		delete(s.data, key)
		s.expired.Add(1)
		return true
	}
	e.expireAt = expireAt
	return true
}

// Expiry retry codes returned alongside TTLMillis.
const (
	ExpiryMissing  = -2
	ExpiryNoExpiry = -1
)

// TTLMillis returns the remaining lifetime of key in milliseconds, or one of
// the Expiry constants.
func (s *Store) TTLMillis(key string) int64 {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	if !ok || isExpired(e, now) {
		return ExpiryMissing
	}
	if e.expireAt == 0 {
		return ExpiryNoExpiry
	}
	return e.expireAt - now
}

// ExpireTimeMillis returns the absolute expiry of key in milliseconds, or one
// of the Expiry constants.
func (s *Store) ExpireTimeMillis(key string) int64 {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.data[key]
	if !ok || isExpired(e, now) {
		return ExpiryMissing
	}
	if e.expireAt == 0 {
		return ExpiryNoExpiry
	}
	return e.expireAt
}

// Persist removes the expiry from key.
func (s *Store) Persist(key string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok || isExpired(e, now) {
		if ok {
			delete(s.data, key)
			s.expired.Add(1)
		}
		return false
	}
	if e.expireAt == 0 {
		return false
	}
	e.expireAt = 0
	return true
}

// Keys returns every live key matching pattern.
func (s *Store) Keys(pattern string) []string {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(s.data))
	for key, e := range s.data {
		if isExpired(e, now) {
			continue
		}
		if pattern == "*" || Match(pattern, key) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Scan walks the keyspace from cursor, returning the next cursor and up to
// count matching keys.
//
// The cursor is an index into a sorted snapshot of the keyspace. That keeps
// pagination correct and deterministic, at the cost of sorting on every call;
// a bucket-based cursor is tracked in ROADMAP.md.
func (s *Store) Scan(cursor uint64, pattern string, count int, typeName string) (uint64, []string) {
	if count <= 0 {
		count = 10
	}
	now := s.now()
	s.mu.RLock()
	keys := make([]string, 0, len(s.data))
	for key, e := range s.data {
		if !isExpired(e, now) {
			keys = append(keys, key)
		}
	}
	s.mu.RUnlock()
	sort.Strings(keys)

	if cursor >= uint64(len(keys)) {
		return 0, nil
	}

	var (
		out      []string
		examined int
	)
	for examined < count && cursor < uint64(len(keys)) {
		key := keys[cursor]
		cursor++
		examined++
		if pattern != "" && pattern != "*" && !Match(pattern, key) {
			continue
		}
		if typeName != "" && typeName != "string" {
			continue
		}
		out = append(out, key)
	}
	if cursor >= uint64(len(keys)) {
		cursor = 0
	}
	return cursor, out
}

// RandomKey returns an arbitrary live key, if one exists.
func (s *Store) RandomKey() (string, bool) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	for key, e := range s.data {
		if !isExpired(e, now) {
			return key, true
		}
	}
	return "", false
}

// Rename moves src to dst. It reports false if src does not exist.
func (s *Store) Rename(src, dst string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.data[src]
	if !ok || isExpired(e, now) {
		if ok {
			delete(s.data, src)
			s.expired.Add(1)
		}
		return false
	}
	delete(s.data, src)
	s.data[dst] = e
	return true
}

// RenameNX moves src to dst only if dst does not exist.
func (s *Store) RenameNX(src, dst string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.data[src]
	if !ok || isExpired(e, now) {
		if ok {
			delete(s.data, src)
			s.expired.Add(1)
		}
		return false
	}
	if existing, ok := s.data[dst]; ok && !isExpired(existing, now) {
		return false
	}
	delete(s.data, src)
	s.data[dst] = e
	return true
}

// DBSize counts keys that are not expired.
func (s *Store) DBSize() int64 {
	keys, _ := s.KeyCounts()
	return keys
}

// KeyCounts reports the number of live keys and how many of them expire.
func (s *Store) KeyCounts() (keys, expiring int64) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, e := range s.data {
		if isExpired(e, now) {
			continue
		}
		keys++
		if e.expireAt != 0 {
			expiring++
		}
	}
	return keys, expiring
}

// Flush removes every key.
func (s *Store) Flush() {
	s.mu.Lock()
	s.data = make(map[string]*entry)
	s.mu.Unlock()
}

// Snapshot calls fn for every live key. The value slice is only valid for the
// duration of the call.
func (s *Store) Snapshot(fn func(key string, value []byte, expireAt int64)) {
	now := s.now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for key, e := range s.data {
		if isExpired(e, now) {
			continue
		}
		fn(key, e.value, e.expireAt)
	}
}

func isExpired(e *entry, now int64) bool {
	return e.expireAt != 0 && e.expireAt <= now
}

func clone(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
